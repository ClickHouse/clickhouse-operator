package keeper

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/controller/testutil"
	"github.com/clickhouse-operator/internal/util"
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Keeper Controller Suite")
}

var _ = When("reconciling standalone KeeperCluster resource", Ordered, func() {
	var (
		suite        testutil.TestSuit
		reconciler   *ClusterReconciler
		services     corev1.ServiceList
		pdbs         policyv1.PodDisruptionBudgetList
		configs      corev1.ConfigMapList
		statefulsets appsv1.StatefulSetList
		cr           = &v1.KeeperCluster{
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
	)

	BeforeAll(func() {
		suite = testutil.SetupEnvironment(v1.AddToScheme)
		reconciler = &ClusterReconciler{
			Client:   suite.Client,
			Scheme:   scheme.Scheme,
			Logger:   suite.Log.Named("keeper"),
			Recorder: record.NewFakeRecorder(128),
		}
	})

	AfterAll(func() {
		By("tearing down the test environment")
		suite.Cancel()
		err := suite.TestEnv.Stop()
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		testutil.EnsureNoEvents(reconciler.Recorder.(*record.FakeRecorder).Events)
	})

	It("should create standalone cluster", func() {
		By("by creating standalone resource CR")
		Expect(suite.Client.Create(suite.Context, cr)).To(Succeed())
		Expect(suite.Client.Get(suite.Context, cr.NamespacedName(), cr)).To(Succeed())
	})

	It("should successfully create all resources of the new cluster", func() {
		By("reconciling the created resource once")
		_, err := reconciler.Reconcile(suite.Context, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(suite.Context, cr.NamespacedName(), cr)).To(Succeed())

		listOpts := util.AppRequirements(cr.Namespace, cr.SpecificName())

		Expect(suite.Client.List(suite.Context, &services, listOpts)).To(Succeed())
		Expect(services.Items).To(HaveLen(1))

		Expect(suite.Client.List(suite.Context, &pdbs, listOpts)).To(Succeed())
		Expect(pdbs.Items).To(HaveLen(1))

		Expect(suite.Client.List(suite.Context, &configs, listOpts)).To(Succeed())
		Expect(configs.Items).To(HaveLen(2))

		Expect(suite.Client.List(suite.Context, &statefulsets, listOpts)).To(Succeed())
		Expect(statefulsets.Items).To(HaveLen(1))

		testutil.AssertEvents(reconciler.Recorder.(*record.FakeRecorder).Events, map[string]int{
			"ReplicaCreated":  1,
			"ClusterNotReady": 1,
		})
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
		Expect(suite.Client.Update(suite.Context, updatedCR)).To(Succeed())
		_, err := reconciler.Reconcile(suite.Context, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(suite.Context, cr.NamespacedName(), updatedCR)).To(Succeed())

		Expect(updatedCR.Status.ObservedGeneration).To(Equal(updatedCR.Generation))
		Expect(updatedCR.Status.UpdateRevision).NotTo(Equal(updatedCR.Status.CurrentRevision))
		Expect(updatedCR.Status.ConfigurationRevision).NotTo(Equal(cr.Status.ConfigurationRevision))
		Expect(updatedCR.Status.StatefulSetRevision).To(Equal(cr.Status.StatefulSetRevision))

		testutil.AssertEvents(reconciler.Recorder.(*record.FakeRecorder).Events, map[string]int{
			"HorizontalScaleBlocked": 1,
		})
	})

	It("should merge extra config in configmap", func() {
		updatedCR := cr.DeepCopy()
		Expect(suite.Client.Get(suite.Context, cr.NamespacedName(), updatedCR)).To(Succeed())
		testutil.ReconcileStatefulSets(updatedCR, suite)
		updatedCR.Spec.Settings.ExtraConfig = runtime.RawExtension{Raw: []byte(`{"keeper_server": {
				"coordination_settings":{"quorum_reads": true}}}`)}
		Expect(suite.Client.Update(suite.Context, updatedCR)).To(Succeed())
		_, err := reconciler.Reconcile(suite.Context, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(suite.Context, cr.NamespacedName(), updatedCR)).To(Succeed())

		Expect(updatedCR.Status.ObservedGeneration).To(Equal(updatedCR.Generation))
		Expect(updatedCR.Status.UpdateRevision).NotTo(Equal(updatedCR.Status.CurrentRevision))
		Expect(updatedCR.Status.ConfigurationRevision).NotTo(Equal(cr.Status.ConfigurationRevision))
		Expect(updatedCR.Status.StatefulSetRevision).To(Equal(cr.Status.StatefulSetRevision))

		var configmap corev1.ConfigMap
		Expect(suite.Client.Get(suite.Context, types.NamespacedName{
			Namespace: cr.Namespace,
			Name:      cr.ConfigMapNameByReplicaID(0)}, &configmap)).To(Succeed())

		Expect(configmap.Data).To(HaveKey(ConfigFileName))
		var config confMap
		Expect(yaml.Unmarshal([]byte(configmap.Data[ConfigFileName]), &config)).To(Succeed())
		Expect(config["keeper_server"].(confMap)["coordination_settings"].(confMap)["quorum_reads"]).To(BeTrue())
	})
})
