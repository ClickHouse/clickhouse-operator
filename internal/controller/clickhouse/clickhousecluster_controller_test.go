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

package clickhouse

import (
	v1 "github.com/clickhouse-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

var _ = Describe("ClickHouseCluster Controller", func() {
	Context("When reconciling a resource", func() {
		cr := &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "standalone",
				Namespace: "default",
			},
			Spec: v1.ClickHouseClusterSpec{
				Replicas: ptr.To[int32](1),
				KeeperClusterRef: &corev1.LocalObjectReference{
					Name: "keeper-cluster",
				},
				Labels: map[string]string{
					"test-label": "test-val",
				},
				Annotations: map[string]string{
					"test-annotation": "test-val",
				},
			},
		}

		It("should create standalone cluster", func() {
			By("by creating standalone resource CR")
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			Expect(k8sClient.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())
		})

	})
})
