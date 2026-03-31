package e2e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	certv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	mcertv1 "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/test/testutil"
)

var _ = Describe("Keeper controller", Label("keeper"), func() {
	DescribeTable("standalone keeper updates", func(ctx context.Context, specUpdate v1.KeeperClusterSpec) {
		ns := testNamespace(ctx)

		cr := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      fmt.Sprintf("test-%d", rand.Uint32()), //nolint:gosec
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: new(int32(1)),
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: BaseVersion,
					},
				},
				DataVolumeClaimSpec: &defaultStorage,
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
		ExpectWithOffset(1, k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
		Expect(cr.Status.Version).To(HavePrefix(cr.Spec.ContainerTemplate.Image.Tag))
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
			Image: v1.ContainerImage{Tag: UpdateVersion},
		}}),
		Entry("scale up to 3 replicas", v1.KeeperClusterSpec{Replicas: new(int32(3))}),
	)

	DescribeTable("keeper cluster updates", func(ctx context.Context, baseReplicas int, specUpdate v1.KeeperClusterSpec) {
		ns := testNamespace(ctx)

		cr := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      fmt.Sprintf("keeper-%d", rand.Uint32()), //nolint:gosec
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: new(int32(baseReplicas)),
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: BaseVersion,
					},
				},
				DataVolumeClaimSpec: &defaultStorage,
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
			Image: v1.ContainerImage{Tag: UpdateVersion},
		}}),
		Entry("scale up to 5 replicas", 3, v1.KeeperClusterSpec{Replicas: new(int32(5))}),
		Entry("scale down to 3 replicas", 5, v1.KeeperClusterSpec{Replicas: new(int32(3))}),
	)

	Describe("secure keeper cluster", func() {
		It("should create secure cluster", func(ctx context.Context) {
			ns := testNamespace(ctx)

			suffix := rand.Uint32() //nolint:gosec
			certName := fmt.Sprintf("keeper-cert-%d", suffix)

			cr := v1.KeeperCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: ns,
					Name:      fmt.Sprintf("keeper-%d", rand.Uint32()), //nolint:gosec
				},
				Spec: v1.KeeperClusterSpec{
					Replicas: new(int32(3)),
					ContainerTemplate: v1.ContainerTemplateSpec{
						Image: v1.ContainerImage{
							Tag: BaseVersion,
						},
					},
					DataVolumeClaimSpec: &defaultStorage,
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
					Namespace: ns,
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
					Namespace: ns,
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
			KeeperRWChecks(ctx, &cr, new(0))
		})
	})

	It("should work with custom data folder mount", func(ctx context.Context) {
		ns := testNamespace(ctx)

		cr := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      fmt.Sprintf("custom-disk-%d", rand.Uint32()), //nolint:gosec
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: new(int32(1)),
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Tag: BaseVersion,
					},
					VolumeMounts: []corev1.VolumeMount{{
						Name:      "custom-data",
						MountPath: "/var/lib/clickhouse",
					}},
				},
				DataVolumeClaimSpec: nil, // Diskless configuration
				PodTemplate: v1.PodTemplateSpec{
					Volumes: []corev1.Volume{{
						Name: "custom-data",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					}},
				},
			},
		}

		By("creating diskless keeper cluster CR")
		Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			By("deleting diskless keeper cluster CR")
			Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
		})

		By("waiting for diskless keeper to be ready")
		WaitKeeperUpdatedAndReady(ctx, &cr, 2*time.Minute, false)

		By("verifying keeper is functional with basic read/write")
		KeeperRWChecks(ctx, &cr, new(0))
	})

	It("should recreate stuck pods", func(ctx context.Context) {
		ns := testNamespace(ctx)

		cr := v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      fmt.Sprintf("stuck-pod-%d", rand.Uint32()), //nolint:gosec
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: new(int32(1)),
				ContainerTemplate: v1.ContainerTemplateSpec{
					Image: v1.ContainerImage{
						Repository: "invalid",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
			cond := meta.FindStatusCondition(cr.Status.Conditions, string(v1.ConditionTypeReplicaStartupSucceeded))
			g.Expect(cond).ToNot(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(BeEquivalentTo(v1.ConditionReasonReplicaError))
		}).WithPolling(pollingInterval).WithTimeout(time.Minute).Should(Succeed())

		cr.Spec.ContainerTemplate.Image = v1.ContainerImage{
			Tag: BaseVersion,
		}
		Expect(k8sClient.Update(ctx, &cr)).To(Succeed())
		WaitKeeperUpdatedAndReady(ctx, &cr, 2*time.Minute, true)
		KeeperRWChecks(ctx, &cr, new(0))
	})
})

func WaitKeeperUpdatedAndReady(ctx context.Context, cr *v1.KeeperCluster, timeout time.Duration, isUpdate bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	By(fmt.Sprintf("waiting for cluster %s to be ready", cr.Name))
	EventuallyWithOffset(1, func(g Gomega) {
		var cluster v1.KeeperCluster
		g.Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cluster)).To(Succeed())
		g.Expect(cluster.Generation).To(Equal(cluster.Status.ObservedGeneration))

		if isUpdate {
			// Intentional global assertion to fail suite if update order is wrong.
			Expect(CheckUpdateOrder(ctx, &client.ListOptions{
				Namespace: cluster.Namespace,
				LabelSelector: labels.SelectorFromSet(map[string]string{
					controllerutil.LabelAppKey: cluster.SpecificName(),
				}),
			}, controllerutil.LabelKeeperReplicaID, cluster.Status.StatefulSetRevision,
				cluster.Status.ConfigurationRevision)).To(Succeed())
		}

		g.Expect(cluster.Status.CurrentRevision).To(Equal(cluster.Status.UpdateRevision))
		g.Expect(cluster.Status.ReadyReplicas).To(Equal(cluster.Replicas()))

		for _, cond := range cluster.Status.Conditions {
			g.Expect(cond.Status).To(
				Equal(metav1.ConditionTrue),
				fmt.Sprintf("condition %s is false: %s", cond.Type, cond.Message),
			)
		}
	}, timeout).WithPolling(pollingInterval).Should(Succeed())
	// Needed for replica deletion to not forward deleting pods.
	By(fmt.Sprintf("waiting for cluster %s replicas count match", cr.Name))
	count := int(cr.Replicas())
	WaitReplicaCount(ctx, k8sClient, cr.Namespace, cr.SpecificName(), count)
}

func KeeperRWChecks(ctx context.Context, cr *v1.KeeperCluster, checksDone *int) {
	ExpectWithOffset(1, k8sClient.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())

	By("connecting to cluster")

	cli, err := testutil.NewKeeperClient(ctx, podDialer, cr)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	defer cli.Close()

	By("writing new test data")
	ExpectWithOffset(1, cli.CheckWrite(*checksDone)).To(Succeed())
	*checksDone++

	By("reading all test data")

	for i := range *checksDone {
		ExpectWithOffset(1, cli.CheckRead(i)).To(Succeed(), "check read %d failed", i)
	}
}
