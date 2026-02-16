package e2e

import (
	"context"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/utils/ptr"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

var _ = Context("Compatibility", Label("compatibility"), func() {
	versionTag := os.Getenv("CLICKHOUSE_VERSION")
	if versionTag == "" {
		It("should have CLICKHOUSE_VERSION env var set", func() {
			Fail("CLICKHOUSE_VERSION env var not set")
		})

		return
	}

	body := func(ctx context.Context, version string) {
		dc, err := discovery.NewDiscoveryClientForConfig(config)
		Expect(err).NotTo(HaveOccurred())
		serverVersion, err := dc.ServerVersion()
		Expect(err).NotTo(HaveOccurred())
		By("running on Kubernetes " + serverVersion.GitVersion)

		keeper := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      "compat-keeper-" + version,
			},
			Spec: v1.KeeperClusterSpec{
				Replicas:            new(int32(3)),
				DataVolumeClaimSpec: &defaultStorage,
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: version,
					},
				},
			},
		}
		clickhouse := v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      "compat-ch-" + version,
			},
			Spec: v1.ClickHouseClusterSpec{
				Replicas:            new(int32(3)),
				DataVolumeClaimSpec: &defaultStorage,
				KeeperClusterRef: &corev1.LocalObjectReference{
					Name: keeper.Name,
				},
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: version,
					},
				},
			},
		}

		By("creating KeeperCluster")
		Expect(k8sClient.Create(ctx, &keeper)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			Expect(k8sClient.Delete(ctx, &keeper)).To(Succeed())
		})

		By("creating ClickHouseCluster")
		Expect(k8sClient.Create(ctx, &clickhouse)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			Expect(k8sClient.Delete(ctx, &clickhouse)).To(Succeed())
		})

		By("ensuring KeeperCluster is healthy")
		WaitKeeperUpdatedAndReady(ctx, &keeper, 2*time.Minute, false)
		KeeperRWChecks(ctx, &keeper, ptr.To(0))

		By("ensuring ClickHouseCluster is healthy")
		WaitClickHouseUpdatedAndReady(ctx, &clickhouse, 2*time.Minute, false)
		ClickHouseRWChecks(ctx, &clickhouse, ptr.To(0))
	}

	tableArgs := []any{body}
	for version := range strings.SplitSeq(versionTag, ",") {
		tableArgs = append(tableArgs, Entry("version: "+version, version))
	}

	DescribeTable("should successfully work", tableArgs...)
})
