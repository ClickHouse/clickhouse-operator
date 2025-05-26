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

package e2e

import (
	"fmt"
	"time"

	"github.com/clickhouse-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/exp/rand"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

var _ = Describe("Keeper controller", func() {
	When("operate standalone cluster", Ordered, func() {
		cr := v1alpha1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testNamespace,
				Name:      fmt.Sprintf("standalone-%d", rand.Uint32()),
			},
			Spec: v1alpha1.KeeperClusterSpec{
				Replicas: ptr.To[int32](1),
			},
		}

		AfterAll(func() {
			Expect(k8sClient.Delete(ctx, &cr)).To(Succeed())
		})

		It("should successfully create standalone cluster", func() {
			By("creating cluster CR")
			Expect(k8sClient.Create(ctx, &cr)).To(Succeed())

			By("waiting for cluster to be ready")
			Eventually(func() bool {
				var cluster v1alpha1.KeeperCluster
				Expect(k8sClient.Get(ctx, cr.GetNamespacedName(), &cluster)).To(Succeed())
				return cluster.Status.ReadyReplicas == 1
			}, time.Minute).Should(BeTrue())
		})
	})
})
