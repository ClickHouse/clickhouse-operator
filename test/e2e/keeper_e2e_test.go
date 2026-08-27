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
	"k8s.io/apimachinery/pkg/runtime"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/test/testutil"
)

var _ = Describe("Keeper controller", Label("keeper"), func() {
	DescribeTable("standalone keeper updates", func(ctx context.Context, specUpdate v1.KeeperClusterSpec) {
		ns := testutil.EnsureTestNamespace(ctx, env)

		name := fmt.Sprintf("test-%d", rand.Uint32()) //nolint:gosec
		cr := testutil.NewKeeperCluster(ns, name).
			WithStorage(defaultStorage).
			Cluster()
		checks := 0

		By("creating cluster CR")
		Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			By("deleting cluster CR")
			Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
		})
		env.WaitKeeperUpdatedAndReady(ctx, &cr, time.Minute, false)
		env.KeeperRWChecks(ctx, &cr, &checks)

		By("updating cluster CR")
		Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
		Expect(controllerutil.ApplyDefault(&specUpdate, cr.Spec)).To(Succeed())
		cr.Spec = specUpdate
		Expect(k8sClient.Update(ctx, &cr)).To(Succeed())

		env.WaitKeeperUpdatedAndReady(ctx, &cr, 5*time.Minute, true)
		Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
		Expect(cr.Status.Version).To(HavePrefix(cr.Spec.ContainerTemplate.Image.Tag))
		env.KeeperRWChecks(ctx, &cr, &checks)
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
		ns := testutil.EnsureTestNamespace(ctx, env)

		name := fmt.Sprintf("keeper-%d", rand.Uint32()) //nolint:gosec
		cr := testutil.NewKeeperCluster(ns, name).
			WithReplicas(int32(baseReplicas)).
			WithStorage(defaultStorage).
			Cluster()
		checks := 0

		By("creating cluster CR")
		Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			By("deleting cluster CR")
			Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
		})
		env.WaitKeeperUpdatedAndReady(ctx, &cr, 2*time.Minute, false)
		env.KeeperRWChecks(ctx, &cr, &checks)

		By("updating cluster CR")
		Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
		Expect(controllerutil.ApplyDefault(&specUpdate, cr.Spec)).To(Succeed())
		cr.Spec = specUpdate
		Expect(k8sClient.Update(ctx, &cr)).To(Succeed())

		env.WaitKeeperUpdatedAndReady(ctx, &cr, 5*time.Minute, true)
		env.KeeperRWChecks(ctx, &cr, &checks)
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
			ns := testutil.EnsureTestNamespace(ctx, env)

			suffix := rand.Uint32() //nolint:gosec
			certName := fmt.Sprintf("keeper-cert-%d", suffix)

			name := fmt.Sprintf("keeper-%d", rand.Uint32()) //nolint:gosec
			cr := testutil.NewKeeperCluster(ns, name).
				WithReplicas(3).
				WithStorage(defaultStorage).
				WithSettings(v1.KeeperSettings{
					TLS: v1.ClusterTLSSpec{
						Enabled:  true,
						Required: true,
						ServerCertSecret: &corev1.LocalObjectReference{
							Name: certName,
						},
					},
				}).
				Cluster()

			issuer := &certv1.Issuer{
				Namespace: ns,
				Name:      fmt.Sprintf("keeper-test-issuer-%d", suffix),
				Spec: certv1.IssuerSpec{
					IssuerConfig: certv1.IssuerConfig{
						SelfSigned: &certv1.SelfSignedIssuer{},
					},
				},
			}

			cert := &certv1.Certificate{
				Namespace: ns,
				Name:      fmt.Sprintf("keeper-cert-%d", suffix),
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
			env.WaitKeeperUpdatedAndReady(ctx, &cr, 2*time.Minute, false)
			env.KeeperRWChecks(ctx, &cr, new(0))
		})
	})

	It("should work with custom data folder mount", func(ctx context.Context) {
		ns := testutil.EnsureTestNamespace(ctx, env)

		// Diskless configuration: no storage, data on an emptyDir mount.
		name := fmt.Sprintf("custom-disk-%d", rand.Uint32()) //nolint:gosec
		cr := testutil.NewKeeperCluster(ns, name).
			WithContainerTemplate(v1.ContainerTemplateSpec{
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "custom-data",
					MountPath: "/var/lib/clickhouse",
				}},
			}).
			WithPodTemplate(v1.PodTemplateSpec{
				Volumes: []corev1.Volume{{
					Name:     "custom-data",
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				}},
			}).
			Cluster()

		By("creating diskless keeper cluster CR")
		Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
		DeferCleanup(func(ctx context.Context) {
			By("deleting diskless keeper cluster CR")
			Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
		})

		By("waiting for diskless keeper to be ready")
		env.WaitKeeperUpdatedAndReady(ctx, &cr, 2*time.Minute, false)

		By("verifying keeper is functional with basic read/write")
		env.KeeperRWChecks(ctx, &cr, new(0))
	})

	It("should recreate stuck pods", func(ctx context.Context) {
		ns := testutil.EnsureTestNamespace(ctx, env)

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
						Tag:        BaseVersion,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &cr)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, cr.NamespacedName(), &cr)).To(Succeed())
			cond := meta.FindStatusCondition(cr.Status.Conditions, v1.ConditionTypeReplicaStartupSucceeded)
			g.Expect(cond).ToNot(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(BeEquivalentTo(v1.ConditionReasonReplicaError))
		}).WithPolling(pollingInterval).WithTimeout(time.Minute).Should(Succeed())

		cr.Spec.ContainerTemplate.Image = v1.ContainerImage{
			Tag: BaseVersion,
		}
		Expect(k8sClient.Update(ctx, &cr)).To(Succeed())
		env.WaitKeeperUpdatedAndReady(ctx, &cr, 2*time.Minute, true)
		env.KeeperRWChecks(ctx, &cr, new(0))
	})
})
