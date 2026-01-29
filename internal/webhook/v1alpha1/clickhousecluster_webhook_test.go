package v1alpha1

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	chv1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

var _ = Describe("ClickHouseCluster Webhook", func() {
	Context("When creating ClickHouseCluster under Defaulting Webhook", func() {
		It("Should fill in the default value if a required field is empty", func(ctx context.Context) {
			By("Setting the default value")
			chCluster := &chv1.ClickHouseCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "test-default",
				},
				Spec: chv1.ClickHouseClusterSpec{
					KeeperClusterRef: &corev1.LocalObjectReference{
						Name: "some-keeper-cluster",
					},
				},
			}
			Expect(k8sClient.Create(ctx, chCluster)).Should(Succeed())
			Expect(k8sClient.Get(ctx, chCluster.NamespacedName(), chCluster)).Should(Succeed())

			Expect(chCluster.Spec.ContainerTemplate.Image.Repository).Should(Equal(chv1.DefaultClickHouseContainerRepository))
			Expect(chCluster.Spec.ContainerTemplate.Resources.Limits).Should(Equal(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(chv1.DefaultClickHouseCPULimit),
				corev1.ResourceMemory: resource.MustParse(chv1.DefaultClickHouseMemoryLimit),
			}))
		})
	})

	Context("When creating ClickHouseCluster under Validating Webhook", func() {
		chCluster := &chv1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "test-validate",
			},
			Spec: chv1.ClickHouseClusterSpec{
				KeeperClusterRef: &corev1.LocalObjectReference{
					Name: "some-keeper-cluster",
				},
			},
		}

		It("Should check TLS enabled if required", func(ctx context.Context) {
			By("Rejecting wrong settings")
			cluster := chCluster.DeepCopy()
			cluster.Spec.Settings.TLS = chv1.ClusterTLSSpec{
				Enabled:  false,
				Required: true,
			}

			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("TLS cannot be required"))
		})

		It("Should check certificate passed if TLS enabled", func(ctx context.Context) {
			By("Rejecting wrong settings")
			cluster := chCluster.DeepCopy()
			cluster.Spec.Settings.TLS = chv1.ClusterTLSSpec{
				Enabled: true,
			}

			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverCertSecret must be specified"))
		})

		It("Should check default password fields if set", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()

			By("Rejecting cr without source")
			cluster.Spec.Settings.DefaultUserPassword = &chv1.DefaultPasswordSelector{}
			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exactly one of secret or configMap must be specified"))

			By("Rejecting cr with empty secret name")
			cluster.Spec.Settings.DefaultUserPassword = &chv1.DefaultPasswordSelector{Secret: &chv1.SecretKeySelector{Key: "smth"}}
			err = k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.settings.defaultUserPassword.secret.name: Required value"))

			By("Rejecting cr with empty configmap key")
			cluster.Spec.Settings.DefaultUserPassword = &chv1.DefaultPasswordSelector{ConfigMap: &chv1.ConfigMapKeySelector{Name: "smth"}}
			err = k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.settings.defaultUserPassword.configMap.key: Required value"))

			By("Warning on empty field")
			cluster.Spec.Settings.DefaultUserPassword = nil
			err = k8sClient.Create(ctx, cluster)
			Expect(err).To(Succeed())
			Expect(warnings).To(HaveLen(1))
			Expect(warnings[0]).To(ContainSubstring("defaultUserPassword is empty"))
		})

		It("Should check that all volumes from volume mounts are exists", func(ctx context.Context) {
			cluster := chCluster.DeepCopy()

			cluster.Spec.ContainerTemplate.VolumeMounts = []corev1.VolumeMount{{
				Name: "non-existing-volume",
			}}
			By("Rejecting cr with non existing volume")
			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("the volume mount 'non-existing-volume' is invalid because the volume is not defined"))

			cluster.Spec.ContainerTemplate.VolumeMounts = nil
			cluster.Spec.PodTemplate.Volumes = []corev1.Volume{{
				Name: "clickhouse-storage-volume",
			}}
			err = k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("reserved and cannot be used"))
		})
	})

})
