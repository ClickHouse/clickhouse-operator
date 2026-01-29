package e2e

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	mcertv1 "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/test/testutil"
)

const (
	KeeperBaseVersion   = "25.3"
	KeeperUpdateVersion = "25.5"
)

var _ = Describe("Keeper controller", Label("keeper"), func() {
	DescribeTable("standalone keeper updates", func(ctx context.Context, specUpdate v1.KeeperClusterSpec) {
		cr := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("test-%d", rand.Uint32()), //nolint:gosec
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: ptr.To[int32](1),
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: KeeperBaseVersion,
					},
				},
				DataVolumeClaimSpec: defaultStorage,
			},
		}
		checks := 0

		By("creating cluster CR")
		Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			By("deleting cluster CR")
			Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
		})
		WaitKeeperUpdatedAndReady(ctx, &cr, time.Minute, false)
		KeeperRWChecks(ctx, &cr, &checks)

		By("updating cluster CR")
		Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
		Expect(controllerutil.ApplyDefault(&specUpdate, cr.Spec)).To(Succeed())
		cr.Spec = specUpdate
		Expect(k8sClient.Update(ctx, &cr)).To(Succeed())

		WaitKeeperUpdatedAndReady(ctx, &cr, 3*time.Minute, true)
		KeeperRWChecks(ctx, &cr, &checks)
	},
		Entry("update log level", v1.KeeperClusterSpec{Settings: v1.KeeperSettings{
			Logger: v1.LoggerConfig{Level: "warning"},
		}}),
		Entry("update coordination settings", v1.KeeperClusterSpec{Settings: v1.KeeperSettings{
			ExtraConfig: runtime.RawExtension{Raw: []byte(`{"keeper_server": {
				"coordination_settings":{"quorum_reads": true}}}`,
			)},
		}}),
		Entry("upgrade version", v1.KeeperClusterSpec{ContainerTemplate: v1.ContainerTemplateSpec{
			Image: v1.ContainerImage{Tag: KeeperUpdateVersion},
		}}),
		Entry("scale up to 3 replicas", v1.KeeperClusterSpec{Replicas: ptr.To[int32](3)}),
	)

	DescribeTable("keeper cluster updates", func(ctx context.Context, baseReplicas int, specUpdate v1.KeeperClusterSpec) {
		cr := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("keeper-%d", rand.Uint32()), //nolint:gosec
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: ptr.To(int32(baseReplicas)),
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: KeeperBaseVersion,
					},
				},
				DataVolumeClaimSpec: defaultStorage,
			},
		}
		checks := 0

		By("creating cluster CR")
		Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			By("deleting cluster CR")
			Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
		})
		WaitKeeperUpdatedAndReady(ctx, &cr, 2*time.Minute, false)
		KeeperRWChecks(ctx, &cr, &checks)

		By("updating cluster CR")
		Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
		Expect(controllerutil.ApplyDefault(&specUpdate, cr.Spec)).To(Succeed())
		cr.Spec = specUpdate
		Expect(k8sClient.Update(ctx, &cr)).To(Succeed())

		WaitKeeperUpdatedAndReady(ctx, &cr, 5*time.Minute, true)
		KeeperRWChecks(ctx, &cr, &checks)
	},
		Entry("update log level", 3, v1.KeeperClusterSpec{Settings: v1.KeeperSettings{
			Logger: v1.LoggerConfig{Level: "warning"},
		}}),
		Entry("update coordination settings", 3, v1.KeeperClusterSpec{Settings: v1.KeeperSettings{
			ExtraConfig: runtime.RawExtension{Raw: []byte(`{"keeper_server": {
				"coordination_settings":{"quorum_reads": true}}}`,
			)},
		}}),
		Entry("upgrade version", 3, v1.KeeperClusterSpec{ContainerTemplate: v1.ContainerTemplateSpec{
			Image: v1.ContainerImage{Tag: KeeperUpdateVersion},
		}}),
		Entry("scale up to 5 replicas", 3, v1.KeeperClusterSpec{Replicas: ptr.To[int32](5)}),
		Entry("scale down to 3 replicas", 5, v1.KeeperClusterSpec{Replicas: ptr.To[int32](3)}),
	)

	Describe("secure keeper cluster", func() {
		suffix := rand.Uint32() //nolint:gosec
		certName := fmt.Sprintf("keeper-cert-%d", suffix)

		cr := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("keeper-%d", rand.Uint32()), //nolint:gosec
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: ptr.To[int32](3),
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: KeeperBaseVersion,
					},
				},
				DataVolumeClaimSpec: defaultStorage,
				Settings: v1.KeeperSettings{
					TLS: v1.ClusterTLSSpec{
						Enabled:  true,
						Required: true,
						ServerCertSecret: &corev1.LocalObjectReference{
							Name: certName,
						},
					},
				},
			},
		}

		issuer := &certv1.Issuer{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("keeper-test-issuer-%d", suffix),
			},
			Spec: certv1.IssuerSpec{
				IssuerConfig: certv1.IssuerConfig{
					SelfSigned: &certv1.SelfSignedIssuer{},
				},
			},
		}

		cert := &certv1.Certificate{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("keeper-cert-%d", suffix),
			},
			Spec: certv1.CertificateSpec{
				IssuerRef: mcertv1.IssuerReference{
					Kind: "Issuer",
					Name: issuer.Name,
				},
				DNSNames: []string{
					fmt.Sprintf("*.%s.%s.svc", cr.HeadlessServiceName(), cr.Namespace),
					fmt.Sprintf("*.%s.%s.svc.cluster.local", cr.HeadlessServiceName(), cr.Namespace),
				},
				SecretName: certName,
			},
		}

		It("should create secure cluster", func(ctx context.Context) {
			DeferCleanup(func(ctx context.Context) {
				By("deleting all resources")
				Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
				Expect(k8sClient.Delete(ctx, cert)).To(Succeed())
				Expect(k8sClient.Delete(ctx, issuer)).To(Succeed())
			})

			By("creating certificate")
			Expect(k8sClient.Create(ctx, issuer)).To(Succeed())
			Expect(k8sClient.Create(ctx, cert)).To(Succeed())
			By("creating secure keeper cluster CR")
			Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
			By("ensuring secure port is working")
			WaitKeeperUpdatedAndReady(ctx, &cr, 2*time.Minute, false)
			KeeperRWChecks(ctx, &cr, ptr.To(0))
		})
	})
})

func WaitKeeperUpdatedAndReady(ctx context.Context, cr *v1.KeeperCluster, timeout time.Duration, isUpdate bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	By(fmt.Sprintf("waiting for cluster %s to be ready", cr.Name))
	EventuallyWithOffset(1, func() bool {
		var cluster v1.KeeperCluster
		Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cluster)).To(Succeed())

		if cluster.Generation != cluster.Status.ObservedGeneration ||
			cluster.Status.CurrentRevision != cluster.Status.UpdateRevision ||
			cluster.Status.ReadyReplicas != cluster.Replicas() {
			return false
		}

		for _, cond := range cluster.Status.Conditions {
			if cond.Status != metav1.ConditionTrue {
				return false
			}
		}

		if isUpdate {
			CheckKeeperUpdateOrder(ctx, cluster)
		}

		return true
	}, timeout).Should(BeTrue())
	// Needed for replica deletion to not forward deleting pods.
	By(fmt.Sprintf("waiting for cluster %s replicas count match", cr.Name))
	count := int(cr.Replicas())
	ExpectWithOffset(1, testutil.WaitReplicaCount(ctx, k8sClient, cr.Namespace, cr.SpecificName(), count)).To(Succeed())
}

func KeeperRWChecks(ctx context.Context, cr *v1.KeeperCluster, checksDone *int) {
	ExpectWithOffset(1, k8sClient.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())

	By("connecting to cluster")

	client, err := testutil.NewKeeperClient(ctx, config, cr)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	defer client.Close()

	By("writing new test data")
	ExpectWithOffset(1, client.CheckWrite(*checksDone)).To(Succeed())
	*checksDone++

	By("reading all test data")

	for i := range *checksDone {
		ExpectWithOffset(1, client.CheckRead(i)).To(Succeed(), "check read %d failed", i)
	}
}

// Validates that updates are applied in the correct order and for single replica at a time.
// Allows to the single replica to be in updating state (not ready but updated).
// Which id must be between the latest not updated and earliest updated replicas.
func CheckKeeperUpdateOrder(ctx context.Context, cluster v1.KeeperCluster) {
	var pods corev1.PodList

	err := k8sClient.List(ctx, &pods, controllerutil.AppRequirements(cluster.Namespace, cluster.SpecificName()))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	maxNotUpdated := v1.KeeperReplicaID(-1)
	minUpdated := v1.KeeperReplicaID(math.MaxInt32)

	updatingReplica := v1.KeeperReplicaID(-1)
	for _, pod := range pods.Items {
		replicaID, err := v1.KeeperReplicaIDFromLabels(pod.Labels)
		Expect(err).NotTo(HaveOccurred())

		ready := CheckPodReady(&pod)
		updated := CheckReplicaUpdated(
			ctx,
			cluster.ConfigMapNameByReplicaID(replicaID),
			cluster.Status.ConfigurationRevision,
			cluster.StatefulSetNameByReplicaID(replicaID),
			cluster.Status.StatefulSetRevision,
		)

		switch {
		// Replica waiting for update
		case !updated && ready:
			maxNotUpdated = max(maxNotUpdated, replicaID)
		// Broken before update
		case !updated:
			Fail(fmt.Sprintf("pod %q is broken before update", pod.Name))
		// Not ready after update, allow one
		case !ready:
			Expect(updatingReplica).To(Equal(v1.KeeperReplicaID(-1)),
				"more than one replica is updating: %d and %d", updatingReplica, replicaID)
			updatingReplica = replicaID
		// Successfully updated replica
		default:
			minUpdated = min(minUpdated, replicaID)
		}
	}

	formatErr := func(r1 v1.KeeperReplicaID, r2 v1.KeeperReplicaID) string {
		return fmt.Sprintf("replica %d updated before replica %d", r1, r2)
	}

	Expect(maxNotUpdated < minUpdated).To(BeTrue(), formatErr(minUpdated, maxNotUpdated))

	if updatingReplica != -1 {
		Expect(maxNotUpdated < updatingReplica).To(BeTrue(), formatErr(updatingReplica, maxNotUpdated))
		Expect(updatingReplica < minUpdated).To(BeTrue(), formatErr(minUpdated, updatingReplica))
	}
}
