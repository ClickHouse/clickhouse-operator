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

var _ = Describe("KeeperCluster Webhook", func() {

	Context("When creating KeeperCluster under Defaulting Webhook", func() {
		It("Should fill in the default value if a required field is empty", func(ctx context.Context) {
			By("Setting the default value")

			keeperCluster := &chv1.KeeperCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "test-keeper-default",
				},
				Spec: chv1.KeeperClusterSpec{},
			}
			Expect(k8sClient.Create(ctx, keeperCluster)).Should(Succeed())
			Expect(k8sClient.Get(ctx, keeperCluster.NamespacedName(), keeperCluster)).Should(Succeed())

			Expect(keeperCluster.Spec.ContainerTemplate.Image.Repository).Should(Equal(chv1.DefaultKeeperContainerRepository))
			Expect(keeperCluster.Spec.ContainerTemplate.Resources.Limits).Should(Equal(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(chv1.DefaultKeeperCPULimit),
				corev1.ResourceMemory: resource.MustParse(chv1.DefaultKeeperMemoryLimit),
			}))
		})
	})

	Context("When creating KeeperCluster under Validating Webhook", func() {
		keeperCluster := &chv1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "test-keeper-validate",
			},
		}

		It("Should check TLS enabled if required", func(ctx context.Context) {
			By("Rejecting wrong settings")
			keeperCluster.Spec.Settings.TLS = chv1.ClusterTLSSpec{
				Enabled:  false,
				Required: true,
			}

			err := k8sClient.Create(ctx, keeperCluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("TLS cannot be required"))
		})

		It("Should check certificate passed if TLS enabled", func(ctx context.Context) {
			By("Rejecting wrong settings")
			keeperCluster.Spec.Settings.TLS = chv1.ClusterTLSSpec{
				Enabled: true,
			}

			err := k8sClient.Create(ctx, keeperCluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverCertSecret must be specified"))
		})

		It("Should check that all volumes from volume mounts are exists", func(ctx context.Context) {
			cluster := keeperCluster.DeepCopy()

			cluster.Spec.ContainerTemplate.VolumeMounts = []corev1.VolumeMount{{
				Name: "non-existing-volume",
			}}
			By("Rejecting cr with non existing volume")
			err := k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("the volume mount 'non-existing-volume' is invalid because the volume is not defined"))

			cluster.Spec.ContainerTemplate.VolumeMounts = nil
			cluster.Spec.PodTemplate.Volumes = []corev1.Volume{{
				Name: "clickhouse-keeper-config-volume",
			}}
			err = k8sClient.Create(ctx, cluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("reserved and cannot be used"))
		})
	})
})
