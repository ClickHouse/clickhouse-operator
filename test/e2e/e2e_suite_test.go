package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"github.com/go-logr/zapr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sethvargo/go-envconfig"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	clickhousecomv1alpha1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/clickhouse"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/keeper"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/internal/upgrade"
	"github.com/ClickHouse/clickhouse-operator/test/testutil"
)

const (
	pollingInterval = testutil.PollInterval

	BaseVersion   = testutil.BaseVersion
	UpdateVersion = testutil.UpdateVersion
)

var releases = map[string][]upgrade.ClickHouseVersion{
	upgrade.ChannelStable: {
		{Major: 26, Minor: 7, Patch: 5, Build: 10},
		{Major: 26, Minor: 6, Patch: 3, Build: 62},
	},
	upgrade.ChannelLTS: {
		{Major: 26, Minor: 3, Patch: 21, Build: 7},
		{Major: 25, Minor: 8, Patch: 32, Build: 4},
	},
}

type shardingConfig struct {
	Index    int    `env:"E2E_SHARD_INDEX, default=0"`
	Total    int    `env:"E2E_SHARD_TOTAL, default=0"`
	PlanPath string `env:"E2E_SHARD_PLAN"`

	shardAssignments map[string]int
}

func (c *shardingConfig) Load() error {
	if err := envconfig.Process(context.Background(), c); err != nil {
		return fmt.Errorf("load sharding config from env: %w", err)
	}

	if c.Total <= 1 {
		return nil
	}

	if c.PlanPath == "" {
		return errors.New("sharding plan path must be set if more than 1 shard")
	}

	if c.Index < 1 || c.Index > c.Total {
		return fmt.Errorf("invalid shard index %d, should be between 1 and %d", c.Index, c.Total)
	}

	data, err := os.ReadFile(filepath.Clean(c.PlanPath))
	if err != nil {
		return fmt.Errorf("read e2e shard plan: %w", err)
	}

	if err = json.Unmarshal(data, &c.shardAssignments); err != nil {
		return fmt.Errorf("decode e2e shard plan: %w", err)
	}

	return nil
}

func (c *shardingConfig) Enabled(spec string) (bool, error) {
	if c.Total <= 1 {
		return true, nil
	}

	shard, ok := c.shardAssignments[spec]
	if !ok {
		return false, fmt.Errorf("test %q is not assigned in sharding plan", spec)
	}

	if shard < 1 || shard > c.Total {
		return false, fmt.Errorf("invalid assignment for %q", spec)
	}

	return shard == c.Index, nil
}

var (
	sharding       shardingConfig
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

	kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
	config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred())

	Expect(clickhousecomv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(certv1.AddToScheme(scheme.Scheme)).To(Succeed())

	// +kubebuilder:scaffold:scheme

	baseClient, err := client.NewWithWatch(config, client.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).NotTo(HaveOccurred())

	k8sClient = interceptor.NewClient(baseClient, interceptor.Funcs{
		Create: requireExplicitImageTag,
	})

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
	podDialer = testutil.NewPortForwardDialer(config)
	env = &testutil.Env{Client: k8sClient, Config: config, Dialer: podDialer}
	Expect(keeper.SetupWithManager(mgr, zapLogger, upgradeChecker, podDialer, true, true)).To(Succeed())
	Expect(clickhouse.SetupWithManager(mgr, zapLogger, upgradeChecker, podDialer, true, true)).To(Succeed())
	// +kubebuilder:scaffold:builder

	mgrCtx, cancel := context.WithCancel(context.Background())

	go func() {
		defer GinkgoRecover()

		Expect(mgr.Start(mgrCtx)).To(Succeed())
	}()

	DeferCleanup(func() {
		cancel()
	})

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

var _ = JustAfterEach(func(ctx context.Context) {
	if !CurrentSpecReport().Failed() {
		return
	}

	testutil.DumpNamespaceDiagnostics(ctx, env, testutil.TestNamespace(), "report")
})

func requireExplicitImageTag(
	ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption,
) error {
	switch cr := obj.(type) {
	case *clickhousecomv1alpha1.ClickHouseCluster:
		if cr.Spec.ContainerTemplate.Image.Tag == "" {
			return fmt.Errorf("refusing to create ClickHouseCluster %q without an explicit image tag", cr.Name)
		}
	case *clickhousecomv1alpha1.KeeperCluster:
		if cr.Spec.ContainerTemplate.Image.Tag == "" {
			return fmt.Errorf("refusing to create KeeperCluster %q without an explicit image tag", cr.Name)
		}
	}

	if err := cli.Create(ctx, obj, opts...); err != nil {
		return fmt.Errorf("create %T: %w", obj, err)
	}

	return nil
}
