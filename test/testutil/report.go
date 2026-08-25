package testutil

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

var (
	unsafeFileNameChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
)

// DumpResult holds a short summary and the full dump content.
type DumpResult struct {
	Short string
	Full  string
}

// Empty returns true if no data dumped.
func (d *DumpResult) Empty() bool {
	return len(d.Full) == 0
}

// WriteFull writes the full dump to a file inside dir and returns the file path.
func (d *DumpResult) WriteFull(dir, filename string) (string, error) {
	if d.Full == "" {
		return "", nil
	}

	filename = unsafeFileNameChars.ReplaceAllString(filename, "")

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create dump dir %s: %w", dir, err)
	}

	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(d.Full), 0o600); err != nil {
		return "", fmt.Errorf("write dump file %s: %w", path, err)
	}

	return path, nil
}

// DumpNamespaceDiagnostics dumps resources, pod logs and events from namespace.
func DumpNamespaceDiagnostics(ctx context.Context, env *Env, ns, dir string) {
	By("collecting diagnostics report")

	report := CurrentSpecReport()

	resDump, err := dumpNamespaceResources(ctx, env.Client, ns)
	if err != nil {
		GinkgoWriter.Printf("failed to dump namespace resources: %v\n", err)
	}

	if !resDump.Empty() {
		path, writeErr := resDump.WriteFull(dir, fmt.Sprintf("resources-%s.log", report.FullText()))
		if writeErr != nil {
			GinkgoWriter.Printf("failed to write resources dump: %v\n", writeErr)
		}

		GinkgoWriter.Printf("\n=== Namespace Resources ===\n%s", resDump.Short)

		if path != "" {
			GinkgoWriter.Printf("Full resources dump: %s\n", path)
		}
	}

	logsDump, err := dumpNamespacePodLogs(ctx, env.Config, ns)
	if err != nil {
		GinkgoWriter.Printf("failed to dump pod logs: %v\n", err)
	}

	if !logsDump.Empty() {
		path, writeErr := logsDump.WriteFull(dir, fmt.Sprintf("pod-logs-%s.log", report.FullText()))
		if writeErr != nil {
			GinkgoWriter.Printf("failed to write pod logs dump: %v\n", writeErr)
		}

		GinkgoWriter.Printf("\n=== Pod Logs (last 10 lines per container) ===\n%s", logsDump.Short)

		if path != "" {
			GinkgoWriter.Printf("Full pod logs dump: %s\n", path)
		}
	}

	events, err := dumpNamespaceEvents(ctx, env.Client, ns, report.StartTime)
	if err != nil {
		GinkgoWriter.Printf("failed to dump namespace events: %v\n", err)
	}

	if strings.TrimSpace(events) != "" {
		path, writeErr := (&DumpResult{Full: events}).WriteFull(dir, fmt.Sprintf("events-%s.log", report.FullText()))
		if writeErr != nil {
			GinkgoWriter.Printf("failed to write events dump: %v\n", writeErr)
		}

		GinkgoWriter.Printf("\n=== Namespace Events (since test start) ===\n%s\n", events)

		if path != "" {
			GinkgoWriter.Printf("Full events dump: %s\n", path)
		}
	}
}

func formatPodState(pod *corev1.Pod) string {
	containers := slices.Concat(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses)

	parts := make([]string, 0, 1+len(containers))
	parts = append(parts, "phase="+string(pod.Status.Phase))

	for _, cs := range containers {
		state := fmt.Sprintf("ready=%t", cs.Ready)

		switch {
		case cs.State.Waiting != nil:
			state = "waiting=" + cs.State.Waiting.Reason
		case cs.State.Terminated != nil:
			state = "terminated=" + cs.State.Terminated.Reason
		}

		parts = append(parts, fmt.Sprintf("%s[%s restarts=%d]", cs.Name, state, cs.RestartCount))
	}

	return strings.Join(parts, " ")
}

// FormatConditions renders conditions as "Type=Status(Reason): message" entries.
func formatConditions(conditions []metav1.Condition) string {
	if len(conditions) == 0 {
		return "<no conditions>"
	}

	parts := make([]string, 0, len(conditions))

	for _, cond := range conditions {
		part := fmt.Sprintf("%s=%s(%s)", cond.Type, cond.Status, cond.Reason)
		if cond.Status != metav1.ConditionTrue && cond.Message != "" {
			part += ": " + cond.Message
		}

		parts = append(parts, part)
	}

	return strings.Join(parts, "; ")
}

// DumpNamespaceResources collects all resources in the namespace.
// The full dump contains the complete JSON representation of each resource,
// while the short dump contains only the resource type and object names.
func dumpNamespaceResources(ctx context.Context, cli client.Client, namespace string) (DumpResult, error) {
	resources := []client.ObjectList{
		&v1.ClickHouseClusterList{},
		&v1.KeeperClusterList{},
		&corev1.ConfigMapList{},
		&corev1.SecretList{},
		&corev1.ServiceList{},
		&corev1.PodList{},
		&batchv1.JobList{},
		&appsv1.StatefulSetList{},
		&policyv1.PodDisruptionBudgetList{},
		&networkingv1.NetworkPolicyList{},
	}

	var errs []error

	full := strings.Builder{}
	short := strings.Builder{}

	for _, resource := range resources {
		if err := cli.List(ctx, resource, &client.ListOptions{
			Namespace: namespace,
		}); err != nil {
			errs = append(errs, fmt.Errorf("list %T: %w", resource, err))
			continue
		}

		marshalled, err := json.MarshalIndent(resource, "", "  ")
		if err != nil {
			errs = append(errs, fmt.Errorf("marshal %T: %w", resource, err))
			continue
		}

		_, _ = fmt.Fprintf(&full, "Dump %T:\n", resource)
		full.Write(marshalled)
		full.WriteString("\n\n")

		// Short dump: one state line per object where a summary exists,
		// resource type + object names otherwise.
		states, names := summarizeObjects(resource)
		for _, state := range states {
			short.WriteString(state)
			short.WriteString("\n")
		}

		if len(names) > 0 {
			_, _ = fmt.Fprintf(&short, "%T: %s\n", resource, strings.Join(names, ", "))
		}
	}

	return DumpResult{Short: short.String(), Full: full.String()}, errors.Join(errs...)
}

func summarizeObjects(list client.ObjectList) (states, names []string) {
	items := reflect.ValueOf(list).Elem().FieldByName("Items")

	for i := range items.Len() {
		obj := items.Index(i).Addr().Interface().(client.Object) //nolint:forcetypeassert

		if state := summarizeObject(obj); state != "" {
			states = append(states, state)
		} else {
			names = append(names, obj.GetName())
		}
	}

	return states, names
}

func summarizeObject(obj client.Object) string {
	switch o := obj.(type) {
	case *v1.ClickHouseCluster:
		return fmt.Sprintf("ClickHouseCluster %s: %s", o.Name, formatConditions(o.Status.Conditions))
	case *v1.KeeperCluster:
		return fmt.Sprintf("KeeperCluster %s: %s", o.Name, formatConditions(o.Status.Conditions))
	case *batchv1.Job:
		return fmt.Sprintf("Job %s: active=%d succeeded=%d failed=%d",
			o.Name, o.Status.Active, o.Status.Succeeded, o.Status.Failed)
	case *corev1.Pod:
		return fmt.Sprintf("Pod %s: %s", o.Name, formatPodState(o))
	}

	return ""
}

func dumpPodLogs(
	ctx context.Context, clientset kubernetes.Clientset, ns, name, container string, previous bool,
) (string, error) {
	stream, err := clientset.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{
		Container: container,
		Previous:  previous,
	}).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("get %s/%s/%s logs: %w", ns, name, container, err)
	}

	defer func() {
		_ = stream.Close()
	}()

	logs, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("read %s/%s/%s logs: %w", ns, name, container, err)
	}

	return string(logs), nil
}

// DumpNamespacePodLogs collects logs for all pod containers in the given namespace.
// The short dump contains only the last logTailLines lines per container.
func dumpNamespacePodLogs(ctx context.Context, config *rest.Config, namespace string) (DumpResult, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return DumpResult{}, fmt.Errorf("unable to create k8s client: %w", err)
	}

	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return DumpResult{}, fmt.Errorf("list pods in namespace %s: %w", namespace, err)
	}

	var errs []error

	full := strings.Builder{}
	short := strings.Builder{}

	processContainer := func(name, container string, previous bool) {
		logs, err := dumpPodLogs(ctx, *clientset, namespace, name, container, previous)
		if err != nil {
			errs = append(errs, err)
			return
		}

		suffix := ""
		if previous {
			suffix = " (previous)"
		}

		header := fmt.Sprintf("Container logs %s/%s/%s%s:\n", namespace, name, container, suffix)
		full.WriteString(header)
		short.WriteString(header)

		if len(logs) == 0 {
			full.WriteString("<empty>\n\n")
			short.WriteString("<empty>\n\n")
			return
		}

		full.WriteString(logs)
		full.WriteString("\n\n")

		short.WriteString(tailLines(logs, logTailLines))
		short.WriteString("\n\n")
	}

	for _, pod := range pods.Items {
		restarted := map[string]bool{}
		for _, cs := range slices.Concat(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses) {
			restarted[cs.Name] = cs.RestartCount > 0
		}

		processPodContainer := func(name, container string) {
			processContainer(name, container, false)

			if restarted[container] {
				processContainer(name, container, true)
			}
		}

		for _, container := range pod.Spec.InitContainers {
			processPodContainer(pod.Name, container.Name)
		}

		for _, container := range pod.Spec.Containers {
			processPodContainer(pod.Name, container.Name)
		}

		for _, container := range pod.Spec.EphemeralContainers {
			processPodContainer(pod.Name, container.Name)
		}
	}

	return DumpResult{Short: short.String(), Full: full.String()}, errors.Join(errs...)
}

// tailLines returns the last n lines from s.
func tailLines(s string, n int) string {
	if len(s) == 0 {
		return ""
	}

	truncatePos := len(s) - 1
	for ; truncatePos > 0 && n > 0; truncatePos-- {
		if s[truncatePos] == '\n' {
			n--
		}
	}

	if truncatePos == 0 {
		return s
	}

	return "TRUNCATED" + s[truncatePos+1:]
}

// DumpNamespaceEvents fetches all events in the namespace that occurred since sinceTime.
func dumpNamespaceEvents(ctx context.Context, cli client.Client, namespace string, since time.Time) (string, error) {
	var events corev1.EventList
	if err := cli.List(ctx, &events, client.InNamespace(namespace)); err != nil {
		return "", fmt.Errorf("list events: %w", err)
	}

	slices.SortFunc(events.Items, func(a, b corev1.Event) int {
		return cmp.Compare(a.CreationTimestamp.UnixNano(), b.CreationTimestamp.UnixNano())
	})

	var buf strings.Builder
	for _, event := range events.Items {
		if event.CreationTimestamp.After(since) {
			_, _ = fmt.Fprintf(&buf, "%s\t%s\t%s/%s\t%s\t%s\n",
				event.CreationTimestamp.Format(time.RFC3339),
				event.Type,
				event.InvolvedObject.Kind,
				event.InvolvedObject.Name,
				event.Reason,
				event.Message,
			)
		}
	}

	return buf.String(), nil
}
