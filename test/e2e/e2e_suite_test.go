package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/zapr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/ClickHouse/clickhouse-operator/internal/controller/clickhouse"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/keeper"
	ctrltestutil "github.com/ClickHouse/clickhouse-operator/internal/controller/testutil"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/internal/upgrade"
	"github.com/ClickHouse/clickhouse-operator/test/testutil"
)

const (
	pollingInterval = testutil.PollInterval

	BaseVersion   = testutil.BaseVersion
	UpdateVersion = testutil.UpdateVersion

	managerStopTimeout = 30 * time.Second
)

var releases = map[string][]upgrade.ClickHouseVersion{
	upgrade.ChannelStable: {
		{Major: 26, Minor: 7, Patch: 5, Build: 10},
		{Major: 26, Minor: 6, Patch: 3, Build: 62},
	},
	upgrade.ChannelLTS: {
		{Major: 26, Minor: 3, Patch: 22, Build: 7},
		{Major: 25, Minor: 8, Patch: 32, Build: 4},
	},
}

var (
	sharding       testutil.ShardingConfig
	env            *testutil.Env
	k8sClient      client.Client
	config         *rest.Config
	podDialer      controllerutil.DialContextFunc
	defaultStorage = corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			},
		},
	}
)

// Run e2e tests using the Ginkgo runner.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)

	if err := sharding.Load(); err != nil {
		t.Fatalf("failed to load sharding config: %v", err)
	}

	_, _ = fmt.Fprintf(GinkgoWriter, "Starting clickhouse-operator suite\n")

	RunSpecs(t, "e2e suite")
}

// Forbid Ordered containers at the suite level so the sharding contract holds.
var _ = ReportBeforeSuite(func(report Report) {
	var offenders []string
	for _, s := range report.SpecReports {
		if s.IsInOrderedContainer {
			offenders = append(offenders, fmt.Sprintf("  %s @ %s", s.FullText(), s.LeafNodeLocation))
		}
	}

	if len(offenders) > 0 {
		Fail("Ordered containers are not allowed (sharding-incompatible):\n" + strings.Join(offenders, "\n"))
	}
})

var _ = BeforeSuite(func(ctx context.Context) {
	var (
		err       error
		logger    = zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true))
		zapLogger = controllerutil.NewLogger(logger)
	)

	ctrl.SetLogger(zapr.NewLogger(logger))

	env = testutil.SetupEnv(testutil.SetupOptions{CertManagerScheme: true, RequireExplicitImageTag: true})
	k8sClient, config, podDialer = env.Client, env.Config, env.Dialer

	By("pre-loading clickhouse images into kind")

	imagePuller := testutil.PreloadImages(ctx, []string{
		"docker.io/clickhouse/clickhouse-server:" + BaseVersion,
		"docker.io/clickhouse/clickhouse-server:" + UpdateVersion,
		"docker.io/clickhouse/clickhouse-keeper:" + BaseVersion,
		"docker.io/clickhouse/clickhouse-keeper:" + UpdateVersion,
	})

	By("installing CRDs")
	Expect(testutil.InstallCRDs(ctx)).To(Succeed())
	DeferCleanup(func(ctx context.Context) {
		By("removing CRDs")
		Expect(testutil.UninstallCRDs(ctx)).To(Succeed())
	})

	By("installing the cert-manager")
	Expect(testutil.InstallCertManager(ctx)).To(Succeed())

	By("installing the NetworkPolicy controller")
	Expect(testutil.InstallNetworkPolicyController(ctx)).To(Succeed())

	By("setting up the manager")

	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Logger: zapr.NewLogger(logger),
		Scheme: scheme.Scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
		Cache: cache.Options{},
	})
	Expect(err).NotTo(HaveOccurred())

	updater := upgrade.NewReleaseUpdater(&upgrade.StaticFetcher{Releases: releases}, time.Minute, zapLogger)
	Expect(mgr.Add(updater)).To(Succeed())

	upgradeChecker := upgrade.NewChecker(updater)
	Expect(keeper.SetupWithManager(mgr, zapLogger, upgradeChecker, podDialer, true, true)).To(Succeed())
	Expect(clickhouse.SetupWithManager(mgr, zapLogger, upgradeChecker, podDialer, true, true)).To(Succeed())
	// +kubebuilder:scaffold:builder

	mgrCtx, cancel := context.WithCancel(context.Background())
	mgrDone := make(chan struct{})

	go func() {
		defer close(mgrDone)
		defer GinkgoRecover()

		Expect(mgr.Start(mgrCtx)).To(Succeed())
	}()

	DeferCleanup(func() {
		cancel()

		select {
		case <-mgrDone:
		case <-time.After(managerStopTimeout):
			Fail("manager did not stop within " + managerStopTimeout.String())
		}

		ctrltestutil.AssertNoLeakedGoroutines()
	})

	// Registered after the leak assertion so the cache stops before it runs.
	DeferCleanup(env.StopCache)

	if err = imagePuller.Wait(); err != nil {
		GinkgoWriter.Printf("failed to pre pull images: %s", err)
	}
})

var _ = BeforeEach(func() {
	enabled, err := sharding.Enabled(CurrentSpecReport().FullText())
	if err != nil {
		Fail(err.Error())
	}

	if !enabled {
		Skip(fmt.Sprintf("not in shard %d/%d", sharding.Index, sharding.Total))
	}
})

var _ = BeforeEach(func() {
	testutil.StartInvariants(env, testutil.TestNamespace())
})

var _ = JustAfterEach(func(ctx context.Context) {
	if !CurrentSpecReport().Failed() {
		return
	}

	testutil.DumpNamespaceDiagnostics(ctx, env, testutil.TestNamespace(), "report")
})
