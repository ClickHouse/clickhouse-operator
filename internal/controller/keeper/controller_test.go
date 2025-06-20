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

package keeper

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
)

var _ = Describe("KeeperCluster Controller", func() {
	Context("When reconciling standalone KeeperCluster resource", Ordered, func() {
		ctx := context.Background()
		cr := &v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "standalone",
				Namespace: "default",
			},
			Spec: v1.KeeperClusterSpec{
				Replicas: ptr.To[int32](1),
				Labels: map[string]string{
					"test-label": "test-val",
				},
				Annotations: map[string]string{
					"test-annotation": "test-val",
				},
			},
		}

		var services corev1.ServiceList
		var pdbs policyv1.PodDisruptionBudgetList
		var configs corev1.ConfigMapList
		var statefulsets appsv1.StatefulSetList

		It("should create standalone cluster", func() {
			By("by creating standalone resource CR")
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())
			Expect(k8sClient.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())
		})

		It("should successfully create all resources of the new cluster", func() {
			By("reconciling the created resource once")
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())

			appReq, err := labels.NewRequirement(util.LabelAppKey, selection.Equals, []string{cr.SpecificName()})
			Expect(err).ToNot(HaveOccurred())
			listOpts := &runtimeclient.ListOptions{
				Namespace:     cr.Namespace,
				LabelSelector: labels.NewSelector().Add(*appReq),
			}

			Expect(k8sClient.List(ctx, &services, listOpts)).To(Succeed())
			Expect(services.Items).To(HaveLen(1))

			Expect(k8sClient.List(ctx, &pdbs, listOpts)).To(Succeed())
			Expect(pdbs.Items).To(HaveLen(1))

			Expect(k8sClient.List(ctx, &configs, listOpts)).To(Succeed())
			Expect(configs.Items).To(HaveLen(2))

			Expect(k8sClient.List(ctx, &statefulsets, listOpts)).To(Succeed())
			Expect(statefulsets.Items).To(HaveLen(1))
		})

		It("should propagate meta attributes for every resource", func() {
			expectedOwnerRef := metav1.OwnerReference{
				Kind:               "KeeperCluster",
				APIVersion:         "clickhouse.com/v1alpha1",
				UID:                cr.UID,
				Name:               cr.Name,
				Controller:         ptr.To(true),
				BlockOwnerDeletion: ptr.To(true),
			}

			By("setting meta attributes for service")
			for _, service := range services.Items {
				Expect(service.ObjectMeta.OwnerReferences).To(ContainElement(expectedOwnerRef))
				for k, v := range cr.Spec.Labels {
					Expect(service.ObjectMeta.Labels).To(HaveKeyWithValue(k, v))
				}
				for k, v := range cr.Spec.Annotations {
					Expect(service.ObjectMeta.Annotations).To(HaveKeyWithValue(k, v))
				}
			}

			By("setting meta attributes for pod disruption budget")
			for _, pdb := range pdbs.Items {
				Expect(pdb.ObjectMeta.OwnerReferences).To(ContainElement(expectedOwnerRef))
				for k, v := range cr.Spec.Labels {
					Expect(pdb.ObjectMeta.Labels).To(HaveKeyWithValue(k, v))
				}
				for k, v := range cr.Spec.Annotations {
					Expect(pdb.ObjectMeta.Annotations).To(HaveKeyWithValue(k, v))
				}
			}

			By("setting meta attributes for configs")
			for _, config := range configs.Items {
				Expect(config.ObjectMeta.OwnerReferences).To(ContainElement(expectedOwnerRef))
				for k, v := range cr.Spec.Labels {
					Expect(config.ObjectMeta.Labels).To(HaveKeyWithValue(k, v))
				}
				for k, v := range cr.Spec.Annotations {
					Expect(config.ObjectMeta.Annotations).To(HaveKeyWithValue(k, v))
				}
			}

			By("setting meta attributes for statefulsets")
			for _, sts := range statefulsets.Items {
				Expect(sts.ObjectMeta.OwnerReferences).To(ContainElement(expectedOwnerRef))
				for k, v := range cr.Spec.Labels {
					Expect(sts.ObjectMeta.Labels).To(HaveKeyWithValue(k, v))
					Expect(sts.Spec.Template.ObjectMeta.Labels).To(HaveKeyWithValue(k, v))
				}
				for k, v := range cr.Spec.Annotations {
					Expect(sts.ObjectMeta.Annotations).To(HaveKeyWithValue(k, v))
					Expect(sts.Spec.Template.ObjectMeta.Annotations).To(HaveKeyWithValue(k, v))
				}
			}
		})

		It("should reflect configuration changes in revisions", func() {
			updatedCR := cr.DeepCopy()
			updatedCR.Spec.Settings.Logger.Level = "warning"
			Expect(k8sClient.Update(ctx, updatedCR)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())

			Expect(updatedCR.Status.ObservedGeneration).To(Equal(updatedCR.Generation))
			Expect(updatedCR.Status.UpdateRevision).NotTo(Equal(updatedCR.Status.CurrentRevision))
			Expect(updatedCR.Status.ConfigurationRevision).NotTo(Equal(cr.Status.ConfigurationRevision))
			Expect(updatedCR.Status.StatefulSetRevision).To(Equal(cr.Status.StatefulSetRevision))
		})

		It("should merge extra config in configmap", func() {
			updatedCR := cr.DeepCopy()
			Expect(k8sClient.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())
			reconcileStatefulSets(updatedCR)
			updatedCR.Spec.Settings.ExtraConfig = runtime.RawExtension{Raw: []byte(`{"keeper_server": {
				"coordination_settings":{"quorum_reads": true}}}`)}
			Expect(k8sClient.Update(ctx, updatedCR)).To(Succeed())
			_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())

			Expect(updatedCR.Status.ObservedGeneration).To(Equal(updatedCR.Generation))
			Expect(updatedCR.Status.UpdateRevision).NotTo(Equal(updatedCR.Status.CurrentRevision))
			Expect(updatedCR.Status.ConfigurationRevision).NotTo(Equal(cr.Status.ConfigurationRevision))
			Expect(updatedCR.Status.StatefulSetRevision).To(Equal(cr.Status.StatefulSetRevision))

			var configmap corev1.ConfigMap
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: cr.Namespace,
				Name:      cr.ConfigMapNameByReplicaID("1")}, &configmap)).To(Succeed())

			Expect(configmap.Data).To(HaveKey(ConfigFileName))
			var config confMap
			Expect(yaml.Unmarshal([]byte(configmap.Data[ConfigFileName]), &config)).To(Succeed())
			Expect(config["keeper_server"].(confMap)["coordination_settings"].(confMap)["quorum_reads"]).To(BeTrue())
		})
	})
})

func reconcileStatefulSets(cr *v1.KeeperCluster) {
	for _, replicaID := range cr.Status.Replicas {
		var sts appsv1.StatefulSet
		ExpectWithOffset(1, k8sClient.Get(ctx, types.NamespacedName{
			Namespace: cr.Namespace,
			Name:      cr.StatefulSetNameByReplicaID(replicaID),
		}, &sts)).To(Succeed())

		sts.Status.ObservedGeneration = cr.Generation
		sts.Status.UpdateRevision = cr.Status.CurrentRevision

		ExpectWithOffset(1, k8sClient.Status().Update(ctx, &sts)).To(Succeed())
	}
}
