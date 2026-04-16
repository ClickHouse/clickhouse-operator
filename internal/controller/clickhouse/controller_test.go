package clickhouse

import (
	"context"
	"errors"
	"net"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
		recorder     *events.FakeRecorder
		controller   *ClusterController
		keeperName   = "keeper"
		services     corev1.ServiceList
		pdbs         policyv1.PodDisruptionBudgetList
		secrets      corev1.SecretList
		configs      corev1.ConfigMapList
		statefulsets appsv1.StatefulSetList
		jobs         batchv1.JobList
		cr           = &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "standalone",
				Namespace: "default",
			},
			Spec: v1.ClickHouseClusterSpec{
				Replicas:         new(int32(2)),
				Shards:           new(int32(2)),
				KeeperClusterRef: &corev1.LocalObjectReference{Name: keeperName},
				Labels: map[string]string{
					"test-label": "test-val",
				},
				Annotations: map[string]string{
					"test-annotation": "test-val",
				},
			},
			Status: v1.ClickHouseClusterStatus{
				Version: "26.1.1.1",
			},
		}
		listOpts = controllerutil.AppRequirements(cr.Namespace, cr.SpecificName())
	)

	BeforeAll(func(ctx context.Context) {
		suite = testutil.SetupEnvironment(v1.AddToScheme)
		recorder = events.NewFakeRecorder(128)
		controller = &ClusterController{
			Client:   suite.Client,
			Scheme:   scheme.Scheme,
			Logger:   suite.Log.Named("clickhouse"),
			Recorder: recorder,
			Webhook: webhookv1.ClickHouseClusterWebhook{
				Log: suite.Log.Named("clickhouse-webhook"),
			},
			Dialer: func(context.Context, string) (net.Conn, error) {
				return nil, errors.New("disabled")
			},
			EnablePDB: true,
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
			Type:   v1.ConditionTypeReady,
			Status: metav1.ConditionTrue,
			Reason: v1.KeeperConditionReasonStandaloneReady,
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

	It("should create version probe job and wait for completion", func(ctx context.Context) {
		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())

		Expect(suite.Client.List(ctx, &jobs, listOpts)).To(Succeed())
		Expect(jobs.Items).To(HaveLen(1))
		Expect(jobs.Items[0].Labels[controllerutil.LabelRoleKey]).To(Equal(controllerutil.LabelVersionProbe))

		testutil.AssertEvents(recorder.Events, map[string]int{
			"ClusterNotReady": 1,
		})

		By("completing the version probe job")
		testutil.CompleteVersionProbeJob(ctx, suite, cr.Namespace, cr.SpecificName(), "26.1.1.1")
	})

	It("should successfully create all resources of the new cluster", func(ctx context.Context) {
		By("reconciling the cluster after version probe completion")

		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), cr)).To(Succeed())

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
	})

	It("should propagate version probe overrides to the job", func(ctx context.Context) {
		By("updating the CR with version probe overrides")

		updatedCR := cr.DeepCopy()
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())
		updatedCR.Spec.VersionProbeTemplate = &v1.VersionProbeTemplate{
			Spec: v1.VersionProbeJobSpec{
				Template: v1.VersionProbePodTemplate{
					Metadata: v1.TemplateMeta{
						Annotations: map[string]string{
							"sidecar.istio.io/inject": "false",
						},
						Labels: map[string]string{
							"probe-label": "probe-value",
						},
					},
				},
			},
		}
		updatedCR.Spec.PodTemplate.Tolerations = []corev1.Toleration{
			{Key: "workload", Operator: corev1.TolerationOpEqual, Value: "system", Effect: corev1.TaintEffectNoSchedule},
		}
		Expect(suite.Client.Update(ctx, updatedCR)).To(Succeed())

		// Delete old job so new one is created with overrides.
		for _, j := range jobs.Items {
			Expect(suite.Client.Delete(ctx, &j, client.PropagationPolicy(metav1.DeletePropagationBackground))).To(Succeed())
		}

		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())

		listOpts := controllerutil.AppRequirements(cr.Namespace, cr.SpecificName())
		Expect(suite.Client.List(ctx, &jobs, listOpts)).To(Succeed())
		Expect(jobs.Items).To(HaveLen(1))

		By("verifying annotations on Pod template only")
		Expect(jobs.Items[0].Spec.Template.Annotations).To(HaveKeyWithValue("sidecar.istio.io/inject", "false"))

		By("verifying probe-specific labels on Pod template only")
		Expect(jobs.Items[0].Spec.Template.Labels).To(HaveKeyWithValue("probe-label", "probe-value"))

		By("verifying operator-reserved labels are preserved")
		Expect(jobs.Items[0].Labels[controllerutil.LabelRoleKey]).To(Equal(controllerutil.LabelVersionProbe))
		Expect(jobs.Items[0].Labels[controllerutil.LabelAppKey]).To(Equal(cr.SpecificName()))

		By("verifying scheduling fields inherited from PodTemplate")
		Expect(jobs.Items[0].Spec.Template.Spec.Tolerations).To(ContainElement(corev1.Toleration{
			Key: "workload", Operator: corev1.TolerationOpEqual, Value: "system", Effect: corev1.TaintEffectNoSchedule,
		}))

		By("completing the new version probe job for subsequent tests")
		testutil.CompleteVersionProbeJob(ctx, suite, cr.Namespace, cr.SpecificName(), "26.1.1.1")

		cr = updatedCR.DeepCopy()
	})

	It("should propagate meta attributes for every resource", func() {
		expectedOwnerRef := metav1.OwnerReference{
			Kind:               "ClickHouseCluster",
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
		for _, spec := range clusterSecrets {
			if !spec.enabled(cr) {
				continue
			}

			Expect(secrets.Items[0].Data).To(HaveKey(spec.Key))
			Expect(secrets.Items[0].Data[spec.Key]).To(Not(BeEmpty()))
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

	It("should preserve unknown secret keys and generate missing", func(ctx context.Context) {
		secret := secrets.Items[0]
		secret.Data["unknown-key"] = []byte("unknown-value")
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

		Expect(secret.Data).To(HaveKey("unknown-key"))
		Expect(secret.Data).To(HaveKey(SecretKeyManagementPassword))
		Expect(secret.Data[SecretKeyManagementPassword]).NotTo(BeEmpty())
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
		Expect(updatedCR.Status.StatefulSetRevision).To(Equal(cr.Status.StatefulSetRevision))
	})

	It("should use security context overrides from spec", func(ctx context.Context) {
		updatedCR := cr.DeepCopy()
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), updatedCR)).To(Succeed())
		updatedCR.Spec.PodTemplate.SecurityContext = &corev1.PodSecurityContext{
			RunAsUser: new(int64(7)),
		}
		updatedCR.Spec.ContainerTemplate.SecurityContext = &corev1.SecurityContext{
			Privileged: new(true),
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
			}),
		}, &sts)).To(Succeed())

		Expect(*sts.Spec.Template.Spec.SecurityContext.RunAsUser).To(BeEquivalentTo(7))
		Expect(*sts.Spec.Template.Spec.Containers[0].SecurityContext.Privileged).To(BeEquivalentTo(true))
	})

	It("should ignore PDBs if Ignored", func(ctx context.Context) {
		var updatedCR v1.ClickHouseCluster
		Expect(suite.Client.Get(ctx, cr.NamespacedName(), &updatedCR)).To(Succeed())

		updatedCR.Spec.PodDisruptionBudget = &v1.PodDisruptionBudgetSpec{
			Policy: v1.PDBPolicyIgnored,
		}

		Expect(suite.Client.Update(ctx, &updatedCR)).To(Succeed())
		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: cr.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())

		listOpts := controllerutil.AppRequirements(cr.Namespace, cr.SpecificName())
		Expect(suite.Client.List(ctx, &pdbs, listOpts)).To(Succeed())
		Expect(pdbs.Items).To(HaveLen(2))
	})

	It("should delete PDBs if Disabled", func(ctx context.Context) {
		var updatedCR v1.ClickHouseCluster
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

	It("should update all replica resources, but not proceed to the next if failed", func(ctx context.Context) {
		By("creating a new cluster with DataVolumeClaimSpec")

		pvcCR := &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pvc-test",
				Namespace: "default",
			},
			Spec: v1.ClickHouseClusterSpec{
				Replicas:         new(int32(2)),
				Shards:           new(int32(1)),
				KeeperClusterRef: &corev1.LocalObjectReference{Name: keeperName},
				DataVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("10Gi"),
						},
					},
				},
			},
		}
		Expect(suite.Client.Create(ctx, pvcCR)).To(Succeed())

		By("reconcile to create version probe job")

		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: pvcCR.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())

		By("completing the version probe job")
		testutil.CompleteVersionProbeJob(ctx, suite, pvcCR.Namespace, pvcCR.SpecificName(), "26.1.1.1")

		By("reconcile to create all resources including STS with VolumeClaimTemplates")

		_, err = controller.Reconcile(ctx, ctrl.Request{NamespacedName: pvcCR.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(ctx, pvcCR.NamespacedName(), pvcCR)).To(Succeed())
		testutil.AssertEvents(recorder.Events, map[string]int{
			"ClusterNotReady": 1,
		})

		By("marking StatefulSets as ready")
		testutil.ReconcileStatefulSets(ctx, pvcCR, suite)

		By("recording STS state before PVC change")

		replicaID := v1.ClickHouseReplicaID{ShardID: 0, Index: 1}
		stsName := pvcCR.StatefulSetNameByReplicaID(replicaID)

		var sts appsv1.StatefulSet
		Expect(suite.Client.Get(ctx, types.NamespacedName{Namespace: pvcCR.Namespace, Name: stsName}, &sts)).To(Succeed())

		By("make STS and PVC changes")

		pvcCR.Spec.DataVolumeClaimSpec.StorageClassName = new("changed")
		pvcCR.Spec.ContainerTemplate.Image = v1.ContainerImage{Tag: "changed"}
		Expect(suite.Client.Update(ctx, pvcCR)).To(Succeed())

		By("reconcile to create new version probe job for changed image")

		_, err = controller.Reconcile(ctx, ctrl.Request{NamespacedName: pvcCR.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())

		By("completing the new version probe job")
		testutil.CompleteVersionProbeJob(ctx, suite, pvcCR.Namespace, pvcCR.SpecificName(), "26.1.1.1")

		By("reconcile updated CR")

		_, err = controller.Reconcile(ctx, ctrl.Request{NamespacedName: pvcCR.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		testutil.AssertEvents(recorder.Events, map[string]int{
			"FailedUpdate": 1,
		})

		By("ensuring STS updated")
		Expect(suite.Client.Get(ctx, pvcCR.NamespacedName(), pvcCR)).To(Succeed())
		Expect(suite.Client.Get(ctx, client.ObjectKeyFromObject(&sts), &sts)).To(Succeed())
		Expect(sts.Annotations[controllerutil.AnnotationSpecHash]).To(Equal(pvcCR.Status.StatefulSetRevision))

		By("retry reconcile")

		_, err = controller.Reconcile(ctx, ctrl.Request{NamespacedName: pvcCR.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		testutil.AssertEvents(recorder.Events, map[string]int{
			"FailedUpdate": 1,
		})

		By("ensuring next replica not changed")
		Expect(suite.Client.Get(ctx, pvcCR.NamespacedName(), pvcCR)).To(Succeed())
		Expect(suite.Client.Get(ctx, types.NamespacedName{
			Namespace: pvcCR.Namespace,
			Name:      pvcCR.StatefulSetNameByReplicaID(v1.ClickHouseReplicaID{ShardID: 0, Index: 0}),
		}, &sts)).To(Succeed())
		Expect(sts.Annotations[controllerutil.AnnotationSpecHash]).ToNot(Equal(pvcCR.Status.StatefulSetRevision))
	})

	It("should correctly work with ExternalSecret", func(ctx context.Context) {
		By("creating a new cluster with ExternalSecret")

		secret := corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "eso-managed-secret",
			},
			Data: map[string][]byte{
				SecretKeyInterserverPassword: []byte("interserver-pass"),
			},
		}

		esoCR := &v1.ClickHouseCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ext-secret",
				Namespace: "default",
			},
			Spec: v1.ClickHouseClusterSpec{
				KeeperClusterRef: &corev1.LocalObjectReference{Name: keeperName},
				ExternalSecret: &v1.ExternalSecret{
					Name: secret.Name,
				},
			},
		}
		Expect(suite.Client.Create(ctx, esoCR)).To(Succeed())

		By("reconciling without secret")

		_, err := controller.Reconcile(ctx, ctrl.Request{NamespacedName: esoCR.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(ctx, esoCR.NamespacedName(), esoCR)).To(Succeed())

		cond := meta.FindStatusCondition(esoCR.Status.Conditions, v1.ClickHouseConditionTypeExternalSecretValid)
		Expect(cond).ToNot(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(BeEquivalentTo(v1.ClickHouseConditionReasonExternalSecretNotFound))

		testutil.AssertEvents(recorder.Events, map[string]int{
			"ExternalSecretNotFound": 1,
			"ClusterNotReady":        1,
		})

		testutil.CompleteVersionProbeJob(ctx, suite, esoCR.Namespace, esoCR.SpecificName(), "26.1.1.1")

		By("creating partial secret and reconciling")
		Expect(suite.Client.Create(ctx, &secret)).To(Succeed())
		_, err = controller.Reconcile(ctx, ctrl.Request{NamespacedName: esoCR.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())
		Expect(suite.Client.Get(ctx, esoCR.NamespacedName(), esoCR)).To(Succeed())

		cond = meta.FindStatusCondition(esoCR.Status.Conditions, v1.ClickHouseConditionTypeExternalSecretValid)
		Expect(cond).ToNot(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(BeEquivalentTo(v1.ClickHouseConditionReasonExternalSecretInvalid))
		Expect(cond.Message).To(ContainSubstring("cluster-secret"))
		Expect(cond.Message).To(ContainSubstring("keeper-identity"))
		Expect(cond.Message).To(ContainSubstring("management-password"))
		Expect(cond.Message).To(ContainSubstring("plaintext password"))
		Expect(cond.Message).NotTo(ContainSubstring("interserver-password"))

		By("reconciling with external secret manage policy")

		esoCR.Spec.ExternalSecret.Policy = v1.ExternalSecretPolicyManage
		Expect(suite.Client.Update(ctx, esoCR)).To(Succeed())
		_, err = controller.Reconcile(ctx, ctrl.Request{NamespacedName: esoCR.NamespacedName()})
		Expect(err).NotTo(HaveOccurred())

		Expect(suite.Client.Get(ctx, esoCR.NamespacedName(), esoCR)).To(Succeed())
		cond = meta.FindStatusCondition(esoCR.Status.Conditions, v1.ClickHouseConditionTypeExternalSecretValid)
		Expect(cond).ToNot(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(BeEquivalentTo(v1.ClickHouseConditionReasonExternalSecretValid))

		Expect(suite.Client.Get(ctx, client.ObjectKeyFromObject(&secret), &secret)).To(Succeed())
		Expect(secret.Data).To(HaveKey(SecretKeyManagementPassword))
		Expect(secret.Data).To(HaveKey(SecretKeyClusterSecret))
	})
})
