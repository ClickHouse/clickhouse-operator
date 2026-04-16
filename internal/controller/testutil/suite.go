package testutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-logr/zapr"
	gcmp "github.com/google/go-cmp/cmp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/randfill"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// TestSuit encapsulates the testing environment and utilities.
type TestSuit struct {
	TestEnv *envtest.Environment
	Cfg     *rest.Config
	Client  client.Client
	Log     controllerutil.Logger
}

// SetupEnvironment initializes the test environment, including the Kubernetes API server and etcd.
func SetupEnvironment(addToScheme func(*k8sruntime.Scheme) error) TestSuit {
	var suite TestSuit

	logger := zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true))
	logf.SetLogger(zapr.NewLogger(logger))
	suite.Log = controllerutil.NewLogger(logger)

	var err error

	err = addToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	// +kubebuilder:scaffold:scheme

	By("bootstrapping test environment")

	suite.TestEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,

		// The BinaryAssetsDirectory is only required if you want to run the tests directly
		// without call the makefile target test.
		BinaryAssetsDirectory: filepath.Join("..", "..", "..", "bin", "k8s",
			fmt.Sprintf("1.31.0-%s-%s", runtime.GOOS, runtime.GOARCH)),
	}

	// Retrieve the first found binary directory to allow running tests from IDEs
	if getFirstFoundEnvTestBinaryDir() != "" {
		suite.TestEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	// cfg is defined in this file globally.
	suite.Cfg, err = suite.TestEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(suite.Cfg).NotTo(BeNil())

	rawClient, err := client.NewWithWatch(suite.Cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(rawClient).NotTo(BeNil())

	suite.Client = interceptor.NewClient(rawClient, interceptor.Funcs{
		Update: validateUpdate,
	})

	return suite
}

// validateUpdate rejects resource updates to mimic real API server behavior.
func validateUpdate(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
	if updatedPVC, ok := obj.(*corev1.PersistentVolumeClaim); ok {
		var pvc corev1.PersistentVolumeClaim

		if err := c.Get(ctx, client.ObjectKeyFromObject(updatedPVC), &pvc); err != nil {
			return fmt.Errorf("get existing PVC: %w", err)
		}

		if !gcmp.Equal(updatedPVC.Spec, pvc.Spec, gcmp.FilterPath(func(p gcmp.Path) bool {
			return p.Last().String() == "Resources"
		}, gcmp.Ignore())) {
			return k8serrors.NewForbidden(schema.GroupResource{}, updatedPVC.GetName(), errors.New("immutable field changed"))
		}
	}

	return c.Update(ctx, obj, opts...) //nolint:wrapcheck
}

// ReconcileStatefulSets updates the status of all StatefulSets associated with the given Cluster.
func ReconcileStatefulSets[T interface {
	SpecificName() string
}](ctx context.Context, cr T, suite TestSuit) {
	listOpts := controllerutil.AppRequirements("", cr.SpecificName())

	var stsList appsv1.StatefulSetList
	ExpectWithOffset(1, suite.Client.List(ctx, &stsList, listOpts)).To(Succeed())

	var podList corev1.PodList
	ExpectWithOffset(1, suite.Client.List(ctx, &podList, listOpts)).To(Succeed())

	foundPods := map[string]struct{}{}
	for _, pod := range podList.Items {
		foundPods[pod.Name] = struct{}{}
	}

	for _, sts := range stsList.Items {
		sts.Status.ObservedGeneration = sts.Generation
		sts.Status.UpdateRevision = sts.Status.CurrentRevision

		ExpectWithOffset(1, suite.Client.Status().Update(ctx, &sts)).To(Succeed())

		podName := sts.Name + "-0"
		if _, ok := foundPods[podName]; !ok {
			pod := corev1.Pod{
				ObjectMeta: sts.Spec.Template.ObjectMeta,
				Spec:       sts.Spec.Template.Spec,
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
				},
			}
			pod.Name = podName

			pod.Namespace = sts.Namespace
			for _, pvcTemplate := range sts.Spec.VolumeClaimTemplates {
				pvc := pvcTemplate.DeepCopy()
				pvcName := fmt.Sprintf("%s-%s", pvc.Name, pod.Name)
				pvc.Name = pvcName

				pvc.Namespace = sts.Namespace
				if err := suite.Client.Create(ctx, pvc); err != nil && !k8serrors.IsAlreadyExists(err) {
					Fail(err.Error())
				}

				pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
					Name: pvcTemplate.Name,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: pvcName,
						},
					},
				})
			}

			ExpectWithOffset(1, suite.Client.Create(ctx, &pod)).To(Succeed())
		}
	}
}

// CompleteVersionProbeJob finds the version probe Job for the given cluster and simulates its successful completion
// by marking the Job as Complete and creating a Pod with the expected termination message.
func CompleteVersionProbeJob(ctx context.Context, suite TestSuit, namespace, specificName, version string) {
	listOpts := controllerutil.AppRequirements(namespace, specificName)

	var jobs batchv1.JobList
	ExpectWithOffset(1, suite.Client.List(ctx, &jobs, listOpts, client.MatchingLabels{
		controllerutil.LabelRoleKey: controllerutil.LabelVersionProbe,
	})).To(Succeed())
	ExpectWithOffset(1, jobs.Items).NotTo(BeEmpty(), "expected at least one version probe job")

	for _, job := range jobs.Items {
		job.Status.StartTime = &metav1.Time{Time: time.Now()}
		job.Status.CompletionTime = &metav1.Time{Time: time.Now()}
		job.Status.Conditions = []batchv1.JobCondition{{
			Type:   batchv1.JobComplete,
			Status: corev1.ConditionTrue,
		}, {
			Type:   batchv1.JobSuccessCriteriaMet,
			Status: corev1.ConditionTrue,
		}}

		By("Completing the version probe job: " + job.Name)
		ExpectWithOffset(1, suite.Client.Status().Update(ctx, &job)).To(Succeed())

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s-pod", job.Name, job.UID[:8]),
				Namespace: namespace,
				Labels: map[string]string{
					batchv1.ControllerUidLabel: string(job.UID),
					batchv1.JobNameLabel:       job.Name,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  v1.VersionProbeContainerName,
					Image: "stub",
				}},
			},
		}

		By("Creating version job pod: " + pod.Name)
		ExpectWithOffset(1, suite.Client.Create(ctx, pod)).To(Succeed())
		By("Setting version job pod status: " + pod.Name)
		pod.Status = corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: v1.VersionProbeContainerName,
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						Message: "ClickHouse server version " + version,
					},
				},
			}},
		}
		ExpectWithOffset(1, suite.Client.Status().Update(ctx, pod)).To(Succeed())
	}
}

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'make setup-envtest' beforehand.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "..", "bin", "k8s")

	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}

	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}

	return ""
}

// EnsureNoEvents ensures that all events are consumed from the events channel.
func EnsureNoEvents(events chan string) {
	By("ensure all events read")

	var allEvents []string
	for {
		var event string
		select {
		case event = <-events:
			allEvents = append(allEvents, event)
		default:
			if len(allEvents) > 0 {
				Fail("Expected no more events, but got:\n\t"+strings.Join(allEvents, "\n\t"), 1)
			}
			return
		}
	}
}

// AssertEvents asserts that the expected events were recorded in the events channel.
func AssertEvents(events chan string, expected map[string]int) {
	By("update events should be recorded")

	recordedEvents := map[string]int{}
	func() {
		for {
			var event string
			select {
			case event = <-events:
				var (
					eventType   string
					eventReason string
				)

				n, err := fmt.Sscanf(event, "%s %s ", &eventType, &eventReason)
				ExpectWithOffset(1, err).To(Succeed(), "Failed to parse event: %s", event)
				ExpectWithOffset(1, n).To(BeEquivalentTo(2))
				ExpectWithOffset(1, eventType).To(Or(Equal(corev1.EventTypeNormal), Equal(corev1.EventTypeWarning)))

				recordedEvents[eventReason]++

			default:
				return
			}
		}
	}()
	ExpectWithOffset(1, recordedEvents).To(BeEquivalentTo(expected))
}

// NewSpecFiller creates a new randfill.Filler from go-fuzz data,
// with custom functions to handle specific fields that require special handling.
func NewSpecFiller(data []byte) *randfill.Filler {
	return randfill.NewFromGoFuzz(data).NilChance(0.3).NumElements(0, 3).Funcs(
		// runtime.RawExtension contains a runtime.Object interface that randfill cannot fill.
		func(r *k8sruntime.RawExtension, c randfill.Continue) {
			if c.Bool() {
				r.Raw = []byte(`{"key":"value"}`)
			}
		},
		// DataSource contains a TypedObjectReference with interface-like fields.
		func(pvc *corev1.PersistentVolumeClaimSpec, c randfill.Continue) {
			c.FillNoCustom(pvc)
			pvc.DataSource = nil
			pvc.DataSourceRef = nil
		},
		// When TLS is enabled, ServerCertSecret must be non-nil.
		func(tls *v1.ClusterTLSSpec, c randfill.Continue) {
			c.FillNoCustom(tls)

			if tls.Enabled && tls.ServerCertSecret == nil {
				tls.ServerCertSecret = &corev1.LocalObjectReference{Name: "test-cert"}
			}
		},
	)
}
