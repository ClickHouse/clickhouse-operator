package clickhouse

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controller/testutil"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	webhookv1 "github.com/ClickHouse/clickhouse-operator/internal/webhook/v1alpha1"
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "ClickHouse Controller Suite")
}

var _ = When("reconciling ClickHouseCluster", Ordered, func() {
	var (
		suite        testutil.TestSuit
		recorder     *record.FakeRecorder
		controller   *ClusterController
		keeperName   = "keeper"
		services     corev1.ServiceList
		pdbs         policyv1.PodDisruptionBudgetList
		secrets      corev1.SecretList
		configs      corev1.ConfigMapList
		statefulsets appsv1.StatefulSetList
		cr           = &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "standalone",
				Namespace: "default",
			},
			Spec: v1.ClickHouseClusterSpec{
				Replicas:         ptr.To[int32](2),
				Shards:           ptr.To[int32](2),
				KeeperClusterRef: &corev1.LocalObjectReference{Name: keeperName},
				Labels: map[string]string{
					"test-label": "test-val",
				},
				Annotations: map[string]string{
					"test-annotation": "test-val",
				},
			},
		}
	)

	BeforeAll(func(ctx context.Context) {
		suite = testutil.SetupEnvironment(v1.AddToScheme)
		recorder = record.NewFakeRecorder(128)
		controller = &ClusterController{
			Client:   suite.Client,
			Scheme:   scheme.Scheme,
			Logger:   suite.Log.Named("clickhouse"),
			Recorder: recorder,
			Webhook: webhookv1.ClickHouseClusterWebhook{
				Log: suite.Log.Named("clickhouse-webhook"),
			},
		}

		keeper := &v1.KeeperCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      keeperName,
				Namespace: "default",
			},
		}
		Expect(suite.Client.Create(ctx, keeper)).To(Succeed())
		Expect(suite.Client.Get(ctx, keeper.NamespacedName(), keeper)).To(Succeed())
		meta.SetStatusCondition(&keeper.Status.Conditions, metav1.Condition{
			Type:   string(v1.ConditionTypeReady),
			Status: metav1.ConditionTrue,
			Reason: string(v1.KeeperConditionReasonStandaloneReady),
		})
		Expect(suite.Client.Status().Update(ctx, keeper)).To(Succeed())
	})

	AfterAll(func() {
		By("tearing down the test environment")
		err := suite.TestEnv.Stop()
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		testutil.EnsureNoEvents(recorder.Events)
	})

	It("should create ClickHouse cluster", func(ctx context.Context) {
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
		Expect(pdbs.Items).To(HaveLen(2))

		Expect(suite.Client.List(ctx, &secrets, listOpts)).To(Succeed())
		Expect(secrets.Items).To(HaveLen(1))

		Expect(suite.Client.List(ctx, &configs, listOpts)).To(Succeed())
		Expect(configs.Items).To(HaveLen(4))

		Expect(suite.Client.List(ctx, &statefulsets, listOpts)).To(Succeed())
		Expect(statefulsets.Items).To(HaveLen(4))

		testutil.AssertEvents(recorder.Events, map[string]int{
			"ClusterNotReady": 1,
		})
	})

	It("should propagate meta attributes for every resource", func() {
		expectedOwnerRef := metav1.OwnerReference{
			Kind:               "ClickHouseCluster",
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

		By("setting meta attributes for secrets")
		for _, secret := range secrets.Items {
			Expect(secret.ObjectMeta.OwnerReferences).To(ContainElement(expectedOwnerRef))
			for k, v := range cr.Spec.Labels {
				Expect(secret.ObjectMeta.Labels).To(HaveKeyWithValue(k, v))
			}
			for k, v := range cr.Spec.Annotations {
				Expect(secret.ObjectMeta.Annotations).To(HaveKeyWithValue(k, v))
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

	It("should generate all secret values", func() {
		for key := range secretsToGenerate {
			Expect(secrets.Items[0].Data).To(HaveKey(key))
			Expect(secrets.Items[0].Data[key]).To(Not(BeEmpty()))
		}
	})

	It("should generate a valid YAML objects as config files", func() {
		var unused map[string]any
		for _, config := range configs.Items {
			for filename, data := range config.Data {
				Expect(yaml.Unmarshal([]byte(data), &unused)).To(Succeed(), filename, data)
			}
		}
	})

	It("should delete unneeded secrets and generate missing", func(ctx context.Context) {
		secret := secrets.Items[0]
		secret.Data["invalid-key"] = []byte("invalid-value")
		delete(secrets.Items[0].Data, SecretKeyManagementPassword)
		By("Changing secret data")
		Expect(suite.Client.Update(ctx, &secret)).To(Succeed())

		By("reconciling the cluster")
		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())

		By("checking that secret is updated")
		Expect(suite.Client.Get(ctx, types.NamespacedName{
			Name:      secret.Name,
			Namespace: secret.Namespace,
		}, &secret)).To(Succeed())

		Expect(secret.Data).NotTo(HaveKey("invalid-key"))
		Expect(secret.Data).To(HaveKey(SecretKeyManagementPassword))
		Expect(secret.Data[SecretKeyManagementPassword]).NotTo(BeEmpty())
	})

	It("should reflect configuration changes in revisions", func(ctx context.Context) {
		updatedCR := cr.DeepCopy()
		updatedCR.Spec.Settings.Logger.Level = "warning"
		Expect(suite.Client.Update(ctx, updatedCR)).To(Succeed())
		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())

		Expect(updatedCR.Status.ObservedGeneration).To(Equal(updatedCR.Generation))
		Expect(updatedCR.Status.UpdateRevision).NotTo(Equal(updatedCR.Status.CurrentRevision))
		Expect(updatedCR.Status.ConfigurationRevision).NotTo(Equal(cr.Status.ConfigurationRevision))
		Expect(updatedCR.Status.StatefulSetRevision).To(Equal(cr.Status.StatefulSetRevision))
	})

	It("should use security context overrides from spec", func(ctx context.Context) {
		updatedCR := cr.DeepCopy()
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())
		updatedCR.Spec.PodTemplate.SecurityContext = &corev1.PodSecurityContext{
			RunAsUser: ptr.To[int64](7),
		}
		updatedCR.Spec.ContainerTemplate.SecurityContext = &corev1.SecurityContext{
			Privileged: ptr.To(true),
		}
		testutil.ReconcileStatefulSets(ctx, updatedCR, suite)
		Expect(suite.Client.Update(ctx, updatedCR)).To(Succeed())
		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())

		var sts appsv1.StatefulSet
		Expect(suite.Client.Get(ctx, types.NamespacedName{
			Namespace: cr.Namespace,
			Name: cr.StatefulSetNameByReplicaID(v1.ClickHouseReplicaID{
				ShardID: 1,
				Index:   1,
			})}, &sts)).To(Succeed())

		Expect(*sts.Spec.Template.Spec.SecurityContext.RunAsUser).To(BeEquivalentTo(7))
		Expect(*sts.Spec.Template.Spec.Containers[0].SecurityContext.Privileged).To(BeEquivalentTo(true))
	})
})
