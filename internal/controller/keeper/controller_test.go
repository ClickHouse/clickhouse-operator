package keeper

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/testutil"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	webhookv1 "github.com/ClickHouse/clickhouse-operator/internal/webhook/v1alpha1"
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Keeper Controller Suite")
}

var _ = AfterSuite(testutil.AssertNoLeakedGoroutines)

var _ = When("reconciling standalone KeeperCluster resource", Ordered, func() {
	var (
		suite        testutil.TestSuit
		recorder     *events.FakeRecorder
		controller   *ClusterController
		services     corev1.ServiceList
		pdbs         policyv1.PodDisruptionBudgetList
		configs      corev1.ConfigMapList
		statefulsets appsv1.StatefulSetList
		cr           = &v1.KeeperCluster{
			Name:      "standalone",
			Namespace: "default",
			Spec: v1.KeeperClusterSpec{
				Replicas: new(int32(1)),
				Labels: map[string]string{
					"test-label": "test-val",
				},
				Annotations: map[string]string{
					"test-annotation": "test-val",
				},
			},
		}
	)

	BeforeAll(func() {
		suite = testutil.SetupEnvironment(v1.AddToScheme)
		recorder = events.NewFakeRecorder(128)
		controller = &ClusterController{
			Client:   suite.Client,
			Scheme:   scheme.Scheme,
			Logger:   suite.Log.Named("keeper"),
			Recorder: recorder,
			Webhook: webhookv1.KeeperClusterWebhook{
				Log: suite.Log.Named("keeper-webhook"),
			},
			Dialer: func(context.Context, string) (net.Conn, error) {
				return nil, errors.New("disabled")
			},
			EnablePDB:    true,
			ResyncPeriod: 30 * time.Second,
		}
	})

	AfterAll(func() {
		By("tearing down the test environment")

		err := suite.TestEnv.Stop()
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		testutil.EnsureNoEvents(recorder.Events)
	})

	It("should create standalone cluster", func(ctx context.Context) {
		By("by creating standalone resource CR")
		Expect(suite.Client.Create(ctx, cr)).To(Succeed())
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())
	})

	It("should successfully create all resources of the new cluster", func(ctx context.Context) {
		By("reconciling the created resource once")

		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())

		listOpts := controllerutil.AppRequirements(cr.Namespace, cr.SpecificName())

		Expect(suite.Client.List(ctx, &services, listOpts)).To(Succeed())
		Expect(services.Items).To(HaveLen(1))

		Expect(suite.Client.List(ctx, &pdbs, listOpts)).To(Succeed())
		Expect(pdbs.Items).To(HaveLen(1))

		Expect(suite.Client.List(ctx, &configs, listOpts)).To(Succeed())
		Expect(configs.Items).To(HaveLen(2))

		Expect(suite.Client.List(ctx, &statefulsets, listOpts)).To(Succeed())
		Expect(statefulsets.Items).To(HaveLen(1))

		var jobs batchv1.JobList
		Expect(suite.Client.List(ctx, &jobs, listOpts)).To(Succeed())
		Expect(jobs.Items).To(BeEmpty())
		Expect(meta.FindStatusCondition(cr.Status.Conditions, v1.ConditionTypeVersionUpgraded)).To(BeNil())

		testutil.AssertEvents(recorder.Events, map[string]int{
			"ReplicaCreated":  1,
			"ClusterNotReady": 1,
		})
	})

	It("should not write anything on repeated reconciles of a settled cluster", func(ctx context.Context) {
		listOpts := controllerutil.AppRequirements(cr.Namespace, cr.SpecificName())

		collect := func() map[string]string {
			rvs := map[string]string{}

			var cluster v1.KeeperCluster
			Expect(suite.Client.Get(ctx, cr.NamespacedName(), &cluster)).To(Succeed())
			rvs["KeeperCluster/"+cluster.Name] = cluster.ResourceVersion

			lists := []client.ObjectList{
				&corev1.PodList{},
				&corev1.PersistentVolumeClaimList{},
				&corev1.ServiceList{},
				&corev1.ConfigMapList{},
				&appsv1.StatefulSetList{},
				&policyv1.PodDisruptionBudgetList{},
				&networkingv1.NetworkPolicyList{},
			}
			for _, list := range lists {
				Expect(suite.Client.List(ctx, list, listOpts)).To(Succeed())
				Expect(meta.EachListItem(list, func(obj runtime.Object) error {
					o := obj.(client.Object) //nolint:forcetypeassert
					rvs[fmt.Sprintf("%T/%s", o, o.GetName())] = o.GetResourceVersion()

					return nil
				})).To(Succeed())
			}

			return rvs
		}

		By("settling the cluster with one extra reconcile")

		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())

		testutil.AssertEvents(recorder.Events, map[string]int{
			"HorizontalScaleBlocked": 1,
		})

		before := collect()

		By("reconciling the unchanged cluster repeatedly")

		for range 5 {
			result, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0), "with a resync period set, every reconcile must schedule a wake-up")
		}

		Expect(collect()).To(Equal(before), "repeated reconciles must not update any resource")
	})

	It("should propagate meta attributes for every resource", func() {
		expectedOwnerRef := metav1.OwnerReference{
			Kind:               "KeeperCluster",
			APIVersion:         "clickhouse.com/v1alpha1",
			UID:                cr.UID,
			Name:               cr.Name,
			Controller:         new(true),
			BlockOwnerDeletion: new(true),
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

	It("should reflect configuration changes in revisions", func(ctx context.Context) {
		updatedCR := cr.DeepCopy()
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())
		updatedCR.Spec.Settings.Logger.Level = "warning"
		Expect(suite.Client.Update(ctx, updatedCR)).To(Succeed())
		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())

		Expect(updatedCR.Status.ObservedGeneration).To(Equal(updatedCR.Generation))
		Expect(updatedCR.Status.UpdateRevision).NotTo(Equal(updatedCR.Status.CurrentRevision))
		Expect(updatedCR.Status.ConfigurationRevision).NotTo(Equal(cr.Status.ConfigurationRevision))
		Expect(updatedCR.Status.StatefulSetRevision).NotTo(Equal(cr.Status.StatefulSetRevision))
	})

	It("should add extra config in configmap", func(ctx context.Context) {
		updatedCR := cr.DeepCopy()
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())
		testutil.ReconcileStatefulSets(ctx, updatedCR, suite)
		updatedCR.Spec.Settings.ExtraConfig = runtime.RawExtension{Raw: []byte(`{"keeper_server": {
				"coordination_settings":{"quorum_reads": true}}}`)}
		Expect(suite.Client.Update(ctx, updatedCR)).To(Succeed())
		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())

		Expect(updatedCR.Status.ObservedGeneration).To(Equal(updatedCR.Generation))
		Expect(updatedCR.Status.UpdateRevision).NotTo(Equal(updatedCR.Status.CurrentRevision))
		Expect(updatedCR.Status.ConfigurationRevision).NotTo(Equal(cr.Status.ConfigurationRevision))
		Expect(updatedCR.Status.StatefulSetRevision).NotTo(Equal(cr.Status.StatefulSetRevision))

		var configmap corev1.ConfigMap
		Expect(suite.Client.Get(ctx, types.NamespacedName{
			Namespace: cr.Namespace,
			Name:      cr.ConfigMapNameByReplicaID(0),
		}, &configmap)).To(Succeed())

		Expect(configmap.Data).To(HaveKey(ConfigFileName))
		Expect(configmap.Data).To(HaveKey(ExtraConfigFileName))
		Expect(configmap.Data[ExtraConfigFileName]).To(ContainSubstring("quorum_reads"))
	})

	It("should use security context overrides from spec", func(ctx context.Context) {
		updatedCR := cr.DeepCopy()
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())
		updatedCR.Spec.PodTemplate.SecurityContext = &corev1.PodSecurityContext{
			RunAsUser: new(int64(7)),
		}
		updatedCR.Spec.ContainerTemplate.SecurityContext = &corev1.SecurityContext{
			AllowPrivilegeEscalation: new(true),
		}
		testutil.ReconcileStatefulSets(ctx, updatedCR, suite)
		Expect(suite.Client.Update(ctx, updatedCR)).To(Succeed())
		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())

		var sts appsv1.StatefulSet
		Expect(suite.Client.Get(ctx, types.NamespacedName{
			Namespace: cr.Namespace,
			Name:      cr.StatefulSetNameByReplicaID(0),
		}, &sts)).To(Succeed())

		Expect(*sts.Spec.Template.Spec.SecurityContext.RunAsUser).To(BeEquivalentTo(7))
		Expect(*sts.Spec.Template.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation).To(BeEquivalentTo(true))
	})

	It("should ignore PDBs if Ignored", func(ctx context.Context) {
		var updatedCR v1.KeeperCluster
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), &updatedCR)).To(Succeed())

		updatedCR.Spec.PodDisruptionBudget = &v1.PodDisruptionBudgetSpec{
			Policy: v1.PDBPolicyIgnored,
		}

		Expect(suite.Client.Update(ctx, &updatedCR)).To(Succeed())
		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())

		listOpts := controllerutil.AppRequirements(cr.Namespace, cr.SpecificName())
		Expect(suite.Client.List(ctx, &pdbs, listOpts)).To(Succeed())
		Expect(pdbs.Items).To(HaveLen(1))
	})

	It("should delete PDBs if Disabled", func(ctx context.Context) {
		var updatedCR v1.KeeperCluster
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), &updatedCR)).To(Succeed())

		updatedCR.Spec.PodDisruptionBudget = &v1.PodDisruptionBudgetSpec{
			Policy: v1.PDBPolicyDisabled,
		}

		Expect(suite.Client.Update(ctx, &updatedCR)).To(Succeed())
		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())

		listOpts := controllerutil.AppRequirements(cr.Namespace, cr.SpecificName())
		Expect(suite.Client.List(ctx, &pdbs, listOpts)).To(Succeed())
		Expect(pdbs.Items).To(BeEmpty())
	})

	It("should not recreate resources while the cluster is being deleted", func(ctx context.Context) {
		By("creating a cluster to delete")

		deletingCR := &v1.KeeperCluster{
			Name:      "deleting",
			Namespace: "default",
			Spec: v1.KeeperClusterSpec{
				Replicas: new(int32(1)),
			},
		}
		Expect(suite.Client.Create(ctx, deletingCR)).To(Succeed())

		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: deletingCR.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())

		testutil.AssertEvents(recorder.Events, map[string]int{
			"ReplicaCreated":  1,
			"ClusterNotReady": 1,
		})

		stsKey := types.NamespacedName{
			Namespace: deletingCR.Namespace,
			Name:      deletingCR.StatefulSetNameByReplicaID(0),
		}

		var sts appsv1.StatefulSet
		Expect(suite.Client.Get(ctx, stsKey, &sts)).To(Succeed())

		By("foreground-deleting the cluster so it stays pending deletion")
		Expect(suite.Client.Delete(ctx, deletingCR, client.PropagationPolicy(metav1.DeletePropagationForeground))).To(Succeed())
		Expect(suite.Client.Get(ctx, deletingCR.NamespacedName(), deletingCR)).To(Succeed())
		Expect(deletingCR.DeletionTimestamp.IsZero()).To(BeFalse())

		By("removing a dependent as foreground garbage collection would")
		Expect(suite.Client.Delete(ctx, &sts)).To(Succeed())

		By("reconciling the deleting cluster")

		result, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: deletingCR.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(BeZero())

		By("checking the dependent was not recreated")
		Expect(k8serrors.IsNotFound(suite.Client.Get(ctx, stsKey, &sts))).To(BeTrue())
	})
})

var _ = Describe("KeeperClustersForClickHouse", func() {
	It("should map a ClickHouse cluster to its referenced keeper", func(ctx context.Context) {
		cluster := &v1.ClickHouseCluster{
			Name: "test", Namespace: "ch-ns",
			Spec: v1.ClickHouseClusterSpec{
				KeeperClusterRef: v1.KeeperClusterReference{Name: "quorum", Namespace: "keeper-ns"},
			},
		}

		Expect(keeperClustersForClickHouse(ctx, cluster)).To(Equal([]reconcile.Request{
			{Namespace: "keeper-ns", Name: "quorum"},
		}))
	})

	It("should default the keeper namespace to the cluster namespace", func(ctx context.Context) {
		cluster := &v1.ClickHouseCluster{
			Name: "test", Namespace: "ch-ns",
			Spec: v1.ClickHouseClusterSpec{
				KeeperClusterRef: v1.KeeperClusterReference{Name: "quorum"},
			},
		}

		Expect(keeperClustersForClickHouse(ctx, cluster)).To(Equal([]reconcile.Request{
			{Namespace: "ch-ns", Name: "quorum"},
		}))
	})

	It("should skip clusters without a keeper reference", func(ctx context.Context) {
		Expect(keeperClustersForClickHouse(ctx, &v1.ClickHouseCluster{})).To(BeNil())
	})
})
