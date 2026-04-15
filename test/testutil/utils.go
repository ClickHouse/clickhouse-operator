package testutil

import (
	"cmp"
	"context"
	"crypto/md5" //nolint:gosec
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	certmanagerVersion = "v1.19.2"
	certmanagerURLTmpl = "https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml"

	logTailLines = 10
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

// CurrentSpecHash returns a stable hash for the currently running Ginkgo spec.
func CurrentSpecHash() string {
	hash := md5.Sum([]byte(CurrentSpecReport().FullText())) //nolint:gosec
	return hex.EncodeToString(hash[:8])
}

// Run executes the provided command within this context.
func Run(cmd *exec.Cmd) ([]byte, error) {
	dir, _ := GetProjectDir()
	cmd.Dir = dir

	if err := os.Chdir(cmd.Dir); err != nil {
		GinkgoWriter.Printf("chdir dir: %s\n", err)
	}

	cmd.Env = append(os.Environ(), "GO111MODULE=on")
	command := strings.Join(cmd.Args, " ")
	GinkgoWriter.Printf("running: %s\n", command)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s failed with error: (%w) %s", command, err, string(output))
	}

	return output, nil
}

// InstallCRDs installs the CRDs into the cluster using make install.
func InstallCRDs(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "make", "install")
	_, err := Run(cmd)

	return err
}

// UninstallCRDs removes the CRDs from the cluster using make uninstall.
func UninstallCRDs(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "make", "uninstall", "ignore-not-found=true")
	_, err := Run(cmd)

	return err
}

// InstallCertManager installs the cert manager bundle.
func InstallCertManager(ctx context.Context) error {
	url := fmt.Sprintf(certmanagerURLTmpl, certmanagerVersion)

	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", url)
	if _, err := Run(cmd); err != nil {
		return err
	}
	// Wait for cert-manager-webhook to be ready, which can take time if cert-manager
	// was re-installed after uninstalling on a cluster.
	cmd = exec.CommandContext(ctx, "kubectl", "wait", "deployment.apps/cert-manager-webhook",
		"--for", "condition=Available",
		"--namespace", "cert-manager",
		"--timeout", "5m",
	)

	_, err := Run(cmd)

	return err
}

// GetProjectDir will return the directory where the project is by walking
// up from the current working directory until it finds a go.mod file.
func GetProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return wd, fmt.Errorf("get project dir: %w", err)
	}

	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return wd, fmt.Errorf("could not find project root (go.mod) from %s", wd)
		}

		dir = parent
	}
}

// DumpNamespaceDiagnostics dumps resources, pod logs and events from namespace.
func DumpNamespaceDiagnostics(ctx context.Context, config *rest.Config, cli client.Client, ns, dir string) {
	By("collecting diagnostics report")

	report := CurrentSpecReport()

	resDump, err := DumpNamespaceResources(ctx, cli, ns)
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

	logsDump, err := DumpNamespacePodLogs(ctx, config, ns)
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

	events, err := DumpNamespaceEvents(ctx, cli, ns, report.StartTime)
	if err != nil {
		GinkgoWriter.Printf("failed to dump namespace events: %v\n", err)
	}

	if strings.TrimSpace(events) != "" {
		GinkgoWriter.Printf("\n=== Namespace Events (since test start) ===\n%s\n", events)
	}
}

// DumpNamespaceResources collects all resources in the namespace.
// The full dump contains the complete JSON representation of each resource,
// while the short dump contains only the resource type and object names.
func DumpNamespaceResources(ctx context.Context, cli client.Client, namespace string) (DumpResult, error) {
	resources := []client.ObjectList{
		&corev1.ConfigMapList{},
		&corev1.SecretList{},
		&corev1.ServiceList{},
		&corev1.PodList{},
		&batchv1.JobList{},
		&appsv1.StatefulSetList{},
		&policyv1.PodDisruptionBudgetList{},
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

		// Short dump: resource type + object names only.
		names := extractObjectNames(resource)
		if len(names) > 0 {
			_, _ = fmt.Fprintf(&short, "%T: %s\n", resource, strings.Join(names, ", "))
		}
	}

	return DumpResult{Short: short.String(), Full: full.String()}, errors.Join(errs...)
}

func extractObjectNames(list client.ObjectList) []string {
	items := reflect.ValueOf(list).Elem().FieldByName("Items")

	names := make([]string, 0, items.Len())
	for i := range items.Len() {
		names = append(names, items.Index(i).Addr().Interface().(client.Object).GetName()) //nolint:forcetypeassert
	}

	return names
}

func dumpPodLogs(ctx context.Context, clientset kubernetes.Clientset, ns, name, container string) (string, error) {
	stream, err := clientset.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{
		Container: container,
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
func DumpNamespacePodLogs(ctx context.Context, config *rest.Config, namespace string) (DumpResult, error) {
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

	processContainer := func(name, container string) {
		logs, err := dumpPodLogs(ctx, *clientset, namespace, name, container)
		if err != nil {
			errs = append(errs, err)
			return
		}

		if len(logs) == 0 {
			return
		}

		header := fmt.Sprintf("Container logs %s/%s/%s:\n", namespace, name, container)
		full.WriteString(header)
		full.WriteString(logs)
		full.WriteString("\n\n")

		short.WriteString(header)
		short.WriteString(tailLines(logs, logTailLines))
		short.WriteString("\n\n")
	}

	for _, pod := range pods.Items {
		for _, container := range pod.Spec.InitContainers {
			processContainer(pod.Name, container.Name)
		}

		for _, container := range pod.Spec.Containers {
			processContainer(pod.Name, container.Name)
		}

		for _, container := range pod.Spec.EphemeralContainers {
			processContainer(pod.Name, container.Name)
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
func DumpNamespaceEvents(ctx context.Context, cli client.Client, namespace string, since time.Time) (string, error) {
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

// SetupCA sets up a self-signed CA issuer and a CA certificate in the given namespace.
func SetupCA(ctx context.Context, k8sClient client.Client, namespace string, suffix uint32) {
	ssIssuer := certv1.ClusterIssuer{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      fmt.Sprintf("issuer-%d", suffix),
		},
		Spec: certv1.IssuerSpec{
			IssuerConfig: certv1.IssuerConfig{
				SelfSigned: &certv1.SelfSignedIssuer{},
			},
		},
	}

	By("creating self-signed issuer")
	Expect(k8sClient.Create(ctx, &ssIssuer)).To(Succeed())
	DeferCleanup(func(ctx context.Context) {
		if err := k8sClient.Delete(ctx, &ssIssuer); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "failed to delete self-signed issuer: %v\n", err)
		}
	})

	caCert := certv1.Certificate{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      fmt.Sprintf("ca-cert-%d", suffix),
		},
		Spec: certv1.CertificateSpec{
			IssuerRef: cmmeta.IssuerReference{
				Kind: "ClusterIssuer",
				Name: ssIssuer.Name,
			},
			IsCA:       true,
			CommonName: fmt.Sprintf("ca-cert-%d", suffix),
			SecretName: fmt.Sprintf("ca-cert-%d", suffix),
		},
	}

	By("creating CA cert")
	Expect(k8sClient.Create(ctx, &caCert)).To(Succeed())
	DeferCleanup(func(ctx context.Context) {
		if err := k8sClient.Delete(ctx, &caCert); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "failed to delete CA certificate: %v\n", err)
		}
	})

	issuer := certv1.Issuer{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      fmt.Sprintf("issuer-%d", suffix),
		},
		Spec: certv1.IssuerSpec{
			IssuerConfig: certv1.IssuerConfig{
				CA: &certv1.CAIssuer{
					SecretName: caCert.Spec.SecretName,
				},
			},
		},
	}

	By("creating Issuer")
	Expect(k8sClient.Create(ctx, &issuer)).To(Succeed())
	DeferCleanup(func(ctx context.Context) {
		if err := k8sClient.Delete(ctx, &issuer); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter, "failed to delete CA issuer: %v\n", err)
		}
	})
}

// PreloadImages pulls each image from the registry and loads it into the kind cluster.
// All images are processed in parallel.
func PreloadImages(ctx context.Context, images []string) *errgroup.Group {
	g, ctx := errgroup.WithContext(ctx)

	for _, image := range images {
		g.Go(func() error {
			// Remove any cached manifest-index
			_ = exec.CommandContext(ctx, "docker", "image", "rm", image).Run()

			By("pulling image:" + image)

			pull := exec.CommandContext(ctx, "docker", "pull", "--platform", "linux/"+runtime.GOARCH, image)
			if out, err := pull.CombinedOutput(); err != nil {
				return fmt.Errorf("docker pull %s: %w\n%s", image, err, out)
			}

			By("loading image into kind: " + image)

			load := exec.CommandContext(ctx, "kind", "load", "docker-image", image)
			if out, err := load.CombinedOutput(); err != nil {
				return fmt.Errorf("kind load %s: %w\n%s", image, err, out)
			}

			return nil
		})
	}

	return g
}

// EnsureNamespace ensures the test namespace is created and active.
func EnsureNamespace(ctx context.Context, k8sClient client.Client, name string) {
	DeferCleanup(func(ctx context.Context) {
		ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}

		err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, &ns)
		if err != nil {
			return
		}

		if err := k8sClient.Delete(ctx, &ns); err != nil {
			GinkgoWriter.Printf("failed to delete namespace %s: %v\n", name, err)
		}
	})

	ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, &ns)

	if k8serrors.IsNotFound(err) {
		ExpectWithOffset(1, k8sClient.Create(ctx, &ns)).To(Succeed())
		return
	}

	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	if ns.Status.Phase != corev1.NamespaceTerminating {
		return
	}

	Eventually(func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, &ns)
		return k8serrors.IsNotFound(err)
	}, "5s", "100ms").Should(BeTrue())

	ExpectWithOffset(1, k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})).To(Succeed())
}
