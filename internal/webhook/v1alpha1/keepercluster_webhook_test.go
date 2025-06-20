/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	chv1 "github.com/clickhouse-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("KeeperCluster Webhook", func() {

	Context("When creating KeeperCluster under Defaulting Webhook", func() {
		It("Should fill in the default value if a required field is empty", func() {
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

		It("Should check TLS enabled if required", func() {
			By("Rejecting wrong settings")
			keeperCluster.Spec.Settings.TLS = chv1.ClusterTLSSpec{
				Enabled:  false,
				Required: true,
			}

			err := k8sClient.Create(ctx, keeperCluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("TLS cannot be required"))
		})

		It("Should check certificate passed if TLS enabled", func() {
			By("Rejecting wrong settings")
			keeperCluster.Spec.Settings.TLS = chv1.ClusterTLSSpec{
				Enabled: true,
			}

			err := k8sClient.Create(ctx, keeperCluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverCertSecret must be specified"))
		})
	})
})
