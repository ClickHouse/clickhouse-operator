package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-logr/zapr"
	. "github.com/onsi/ginkgo/v2" //nolint:staticcheck
	. "github.com/onsi/gomega"    //nolint:staticcheck
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"

	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
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

	suite.Client, err = client.New(suite.Cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(suite.Client).NotTo(BeNil())

	return suite
}

// ReconcileStatefulSets updates the status of all StatefulSets associated with the given Cluster.
func ReconcileStatefulSets[T interface {
	SpecificName() string
}](ctx context.Context, cr T, suite TestSuit) {
	listOpts := controllerutil.AppRequirements("", cr.SpecificName())

	var stsList appsv1.StatefulSetList
	ExpectWithOffset(1, suite.Client.List(ctx, &stsList, listOpts)).To(Succeed())

	for _, sts := range stsList.Items {
		sts.Status.ObservedGeneration = sts.Generation
		sts.Status.UpdateRevision = sts.Status.CurrentRevision

		ExpectWithOffset(1, suite.Client.Status().Update(ctx, &sts)).To(Succeed())
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
