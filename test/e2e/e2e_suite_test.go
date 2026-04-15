package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"github.com/go-logr/zapr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	pollingInterval = time.Millisecond * 100

	BaseVersion   = "26.2"
	UpdateVersion = "26.3"
)

var releases = map[string][]upgrade.ClickHouseVersion{
	upgrade.ChannelStable: {
		{Major: 26, Minor: 2, Patch: 1, Build: 1},
		{Major: 26, Minor: 1, Patch: 1, Build: 1},
	},
	upgrade.ChannelLTS: {
		{Major: 26, Minor: 3, Patch: 1, Build: 1},
		{Major: 25, Minor: 8, Patch: 1, Build: 1},
	},
}

var (
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

	_, _ = fmt.Fprintf(GinkgoWriter, "Starting clickhouse-operator suite\n")

	RunSpecs(t, "e2e suite")
}

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

	k8sClient, err = client.New(config, client.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).NotTo(HaveOccurred())

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
	Expect(keeper.SetupWithManager(mgr, zapLogger, upgradeChecker, podDialer, true)).To(Succeed())
	Expect(clickhouse.SetupWithManager(mgr, zapLogger, upgradeChecker, podDialer, true)).To(Succeed())
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

var _ = JustAfterEach(func(ctx context.Context) {
	if !CurrentSpecReport().Failed() {
		return
	}

	testutil.DumpNamespaceDiagnostics(ctx, config, k8sClient, testNamespace(ctx), "report")
})

func WaitReplicaCount(ctx context.Context, k8sClient client.Client, namespace, app string, replicas int) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(time.Minute)
	}

	Eventually(func() int {
		var pods corev1.PodList
		Expect(k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
			controllerutil.LabelAppKey: app,
		})).To(Succeed())

		return len(pods.Items)
	}).WithTimeout(time.Until(deadline)).WithPolling(pollingInterval).Should(Equal(replicas))
}

func CheckPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}

	return false
}

// CheckUpdateOrder lists StatefulSets for the given app and validates rolling update invariants:
// 1. Updated StatefulSets form a contiguous group from the highest replica ID
// 2. At most one StatefulSet has zero ready replicas (the one currently being updated).
func CheckUpdateOrder(ctx context.Context, selector *client.ListOptions, replicaLabel, stsRev, cfgRev string) error {
	var stsList appsv1.StatefulSetList
	Expect(k8sClient.List(ctx, &stsList, selector)).To(Succeed())

	if len(stsList.Items) < 2 {
		return nil
	}

	notReadyCount := 0
	updated := make([]bool, len(stsList.Items))

	for _, sts := range stsList.Items {
		index, err := strconv.Atoi(sts.Labels[replicaLabel])
		Expect(err).NotTo(HaveOccurred())

		if sts.Status.ReadyReplicas != 1 {
			notReadyCount++
		}

		updated[index] = controllerutil.GetSpecHashFromObject(&sts) == stsRev &&
			controllerutil.GetConfigHashFromObject(&sts) == cfgRev
	}

	if notReadyCount > 1 {
		return fmt.Errorf("%d replicas not ready, expected at most 1", notReadyCount)
	}

	// The controller updates the highest-index replica first.
	// If it doesn't match the target revisions, either the rollout hasn't started
	// or the revisions are stale (cluster status read before the STS list) — skip.
	if !updated[len(updated)-1] {
		return nil
	}

	// find the first updated replica (lowest index that matches target)
	updatedID := 0
	for i, isUpdated := range updated {
		if isUpdated {
			updatedID = i
			break
		}
	}

	// all replicas above the first updated one must also be updated
	for i := updatedID + 1; i < len(updated); i++ {
		if !updated[i] {
			return fmt.Errorf("replica %d updated before %d", updatedID, i)
		}
	}

	return nil
}

func testNamespace(ctx context.Context) string {
	ns := "e2e-" + testutil.CurrentSpecHash()
	testutil.EnsureNamespace(ctx, k8sClient, ns)
	return ns
}
