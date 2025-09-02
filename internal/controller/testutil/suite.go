package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/clickhouse-operator/internal/util"
	"github.com/go-logr/zapr"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/kubernetes/scheme"

	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type TestSuit struct {
	Context context.Context
	Cancel  context.CancelFunc
	TestEnv *envtest.Environment
	Cfg     *rest.Config
	Client  client.Client
	Log     util.Logger
}

func SetupEnvironment(addToScheme func(*k8sruntime.Scheme) error) TestSuit {
	var suite TestSuit
	logger := zap.NewRaw(zap.WriteTo(ginkgo.GinkgoWriter), zap.UseDevMode(true))
	logf.SetLogger(zapr.NewLogger(logger))
	suite.Log = util.NewZapLogger(logger)

	suite.Context, suite.Cancel = context.WithCancel(context.TODO())

	var err error
	err = addToScheme(scheme.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	// +kubebuilder:scaffold:scheme

	ginkgo.By("bootstrapping test environment")
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
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(suite.Cfg).NotTo(gomega.BeNil())

	suite.Client, err = client.New(suite.Cfg, client.Options{Scheme: scheme.Scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(suite.Client).NotTo(gomega.BeNil())

	return suite
}

func ReconcileStatefulSets[T interface {
	SpecificName() string
}](cr T, suite TestSuit) {
	listOpts := util.AppRequirements("", cr.SpecificName())

	var stsList appsv1.StatefulSetList
	gomega.ExpectWithOffset(1, suite.Client.List(suite.Context, &stsList, listOpts)).To(gomega.Succeed())
	for _, sts := range stsList.Items {
		sts.Status.ObservedGeneration = sts.Generation
		sts.Status.UpdateRevision = sts.Status.CurrentRevision

		gomega.ExpectWithOffset(1, suite.Client.Status().Update(suite.Context, &sts)).To(gomega.Succeed())
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
