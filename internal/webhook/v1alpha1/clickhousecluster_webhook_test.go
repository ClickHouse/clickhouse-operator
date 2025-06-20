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

var _ = Describe("ClickHouseCluster Webhook", func() {
	Context("When creating ClickHouseCluster under Defaulting Webhook", func() {
		It("Should fill in the default value if a required field is empty", func() {
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

		It("Should check TLS enabled if required", func() {
			By("Rejecting wrong settings")
			chCluster.Spec.Settings.TLS = chv1.ClusterTLSSpec{
				Enabled:  false,
				Required: true,
			}

			err := k8sClient.Create(ctx, chCluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("TLS cannot be required"))
		})

		It("Should check certificate passed if TLS enabled", func() {
			By("Rejecting wrong settings")
			chCluster.Spec.Settings.TLS = chv1.ClusterTLSSpec{
				Enabled: true,
			}

			err := k8sClient.Create(ctx, chCluster)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serverCertSecret must be specified"))
		})
	})

})
