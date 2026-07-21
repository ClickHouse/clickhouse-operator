package keeper

import (
	"context"
	"errors"
	"net"
	"reflect"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controller"
	util "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/internal/upgrade"
)

var _ = Describe("UpdateReplica", Ordered, func() {
	var (
		cancelEvents context.CancelFunc
		log          util.Logger
		rec          *keeperReconciler
		replicaID    v1.KeeperReplicaID = 1
		cfgKey       types.NamespacedName
		stsKey       types.NamespacedName
	)

	BeforeAll(func(ctx context.Context) {
		log, rec, cancelEvents = setupReconciler()

		// Populate revisions (normally done by reconcileClusterRevisions).
		_, err := rec.reconcileClusterRevisions(ctx, log)
		Expect(err).ToNot(HaveOccurred())

		cfgKey = types.NamespacedName{
			Namespace: rec.Cluster.Namespace,
			Name:      rec.Cluster.ConfigMapNameByReplicaID(replicaID),
		}
		stsKey = types.NamespacedName{
			Namespace: rec.Cluster.Namespace,
			Name:      rec.Cluster.StatefulSetNameByReplicaID(replicaID),
		}
	})

	AfterAll(func() {
		cancelEvents()
	})

	It("should create replica resources", func(ctx context.Context) {
		rec.ReplicaState[1] = replicaState{}
		result, err := rec.reconcileReplicaResources(ctx, log)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeFalse())

		configMap := mustGet[*corev1.ConfigMap](ctx, rec.GetClient(), cfgKey)
		sts := mustGet[*appsv1.StatefulSet](ctx, rec.GetClient(), stsKey)

		Expect(configMap).ToNot(BeNil())
		Expect(sts).ToNot(BeNil())
		Expect(sts.Spec.Template.Annotations[util.AnnotationConfigHash]).To(Equal(rec.revs.ConfigurationRevision))
		Expect(util.GetSpecHashFromObject(sts)).To(BeEquivalentTo(rec.revs.StatefulSetRevision))
	})

	It("should do nothing if no changes", func(ctx context.Context) {
		sts := mustGet[*appsv1.StatefulSet](ctx, rec.GetClient(), stsKey)
		sts.Status.ObservedGeneration = sts.Generation
		sts.Status.ReadyReplicas = 1
		rec.ReplicaState[replicaID] = replicaState{
			Status: serverStatus{
				ServerState: ModeStandalone,
			},
			ReplicaState: controller.ReplicaState{
				ReplicaResources: controller.ReplicaResources{
					STS: sts,
					CFG: mustGet[*corev1.ConfigMap](ctx, rec.GetClient(), cfgKey),
				},
			},
		}
		result, err := rec.reconcileReplicaResources(ctx, log)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeTrue())
	})

	It("should update resources on spec changes", func(ctx context.Context) {
		rec.Cluster.Spec.ContainerTemplate.Image.Repository = "custom-keeper"
		rec.Cluster.Spec.ContainerTemplate.Image.Tag = "latest"
		rec.revs.StatefulSetRevision = "sts-v2"
		result, err := rec.reconcileReplicaResources(ctx, log)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeFalse())

		sts := mustGet[*appsv1.StatefulSet](ctx, rec.GetClient(), stsKey)
		Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal("custom-keeper:latest"))
		Expect(sts.Annotations[util.AnnotationSpecHash]).To(Equal(rec.revs.StatefulSetRevision))
	})

	It("should restart server on config changes", func(ctx context.Context) {
		sts := mustGet[*appsv1.StatefulSet](ctx, rec.GetClient(), stsKey)
		previousHash := sts.Spec.Template.Annotations[util.AnnotationConfigHash]
		Expect(previousHash).ToNot(BeEmpty())

		rec.ReplicaState[replicaID] = replicaState{
			Status: serverStatus{
				ServerState: ModeStandalone,
			},
			ReplicaState: controller.ReplicaState{
				ReplicaResources: controller.ReplicaResources{STS: sts},
			},
		}
		rec.Cluster.Spec.Settings.Logger.Level = "info"
		rec.revs.ConfigurationRevision = "cfg-v2"
		rec.revs.RestartConfigRevision = "cfg-v2"
		newStsRevision, err := getStatefulSetRevision(rec.Cluster, "cfg-v2")
		Expect(err).ToNot(HaveOccurred())

		rec.revs.StatefulSetRevision = newStsRevision
		result, err := rec.reconcileReplicaResources(ctx, log)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeFalse())

		sts = mustGet[*appsv1.StatefulSet](ctx, rec.GetClient(), stsKey)
		Expect(sts.Spec.Template.Annotations[util.AnnotationConfigHash]).To(Equal(rec.revs.RestartConfigRevision))
	})

	It("should delete stuck pod in error state", func(ctx context.Context) {
		sts := mustGet[*appsv1.StatefulSet](ctx, rec.GetClient(), stsKey)

		podKey := types.NamespacedName{
			Namespace: rec.Cluster.Namespace,
			Name:      sts.Name + "-0",
		}

		By("creating a pod that simulates a stuck state")

		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: podKey.Namespace,
				Name:      podKey.Name,
				Labels: map[string]string{
					appsv1.ControllerRevisionHashLabelKey: "outdated",
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "clickhouse-keeper",
					Image: "custom-keeper:latest",
				}},
			},
		}
		Expect(rec.GetClient().Create(ctx, pod)).To(Succeed())

		By("setting replica state with error and a spec diff")

		rec.ReplicaState[replicaID] = replicaState{
			ReplicaState: controller.ReplicaState{
				ReplicaResources: controller.ReplicaResources{STS: sts},
				Pod:              pod,
				StartupError:     new("CreateContainerConfigError"),
			},
		}

		result, err := rec.reconcileReplicaResources(ctx, log)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeFalse())

		By("verifying the stuck pod was deleted")

		err = rec.GetClient().Get(ctx, podKey, &corev1.Pod{})
		Expect(k8serrors.IsNotFound(err)).To(BeTrue(), "stuck pod should have been deleted")
	})
})

var _ = Describe("Keeper version status", func() {
	It("should report matching live replica versions", func() {
		_, rec, cancelEvents := setupReconciler()
		defer cancelEvents()

		rec.ReplicaState = map[v1.KeeperReplicaID]replicaState{
			0: {Status: serverStatus{Version: "25.8.2.1"}},
			1: {Status: serverStatus{Version: "25.8.2.1"}},
		}

		rec.evaluateReplicaConditions()

		Expect(rec.Cluster.Status.Version).To(Equal("25.8.2.1"))
		cond := meta.FindStatusCondition(rec.Cluster.Status.Conditions, v1.ConditionTypeVersionInSync)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal(v1.ConditionReasonVersionMatch))
		Expect(meta.FindStatusCondition(rec.Cluster.Status.Conditions, v1.ConditionTypeVersionUpgraded)).To(BeNil())
	})

	It("should report the numerically newest replica version across a .9 to .10 boundary", func() {
		_, rec, cancelEvents := setupReconciler()
		defer cancelEvents()

		rec.ReplicaState = map[v1.KeeperReplicaID]replicaState{
			0: {Status: serverStatus{Version: "25.9.1.100"}},
			1: {Status: serverStatus{Version: "25.10.2.5"}},
		}

		rec.evaluateReplicaConditions()

		Expect(rec.Cluster.Status.Version).To(Equal("25.10.2.5"))
	})

	It("should keep last known version when no live replica version is observed", func() {
		_, rec, cancelEvents := setupReconciler()
		defer cancelEvents()

		rec.Cluster.Status.Version = "25.8.2.1"
		rec.ReplicaState = map[v1.KeeperReplicaID]replicaState{
			0: {Status: serverStatus{}},
		}

		rec.evaluateReplicaConditions()

		Expect(rec.Cluster.Status.Version).To(Equal("25.8.2.1"))
		cond := meta.FindStatusCondition(rec.Cluster.Status.Conditions, v1.ConditionTypeVersionInSync)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionUnknown))
		Expect(cond.Reason).To(Equal(v1.ConditionReasonVersionPending))
	})

	It("should use deterministic version for upgrade check when replicas diverge", func(ctx context.Context) {
		log, rec, cancelEvents := setupReconciler()
		defer cancelEvents()

		updater := upgrade.NewReleaseUpdater(&upgrade.StaticFetcher{Releases: map[string][]upgrade.ClickHouseVersion{
			upgrade.ChannelStable: {
				{Major: 25, Minor: 8, Patch: 2, Build: 1},
				{Major: 25, Minor: 8, Patch: 3, Build: 1},
			},
		}}, time.Hour, log)
		rec.Checker = upgrade.NewChecker(updater)
		rec.ReplicaState = map[v1.KeeperReplicaID]replicaState{
			0: {Status: serverStatus{Version: "25.7.2.1"}},
			1: {Status: serverStatus{Version: "25.8.2.1"}},
		}

		updCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		go func() {
			defer GinkgoRecover()

			Expect(updater.Start(updCtx)).To(Succeed())
		}()

		Eventually(updater.GetReleasesData).ShouldNot(BeNil())

		rec.evaluateReplicaConditions()

		Expect(rec.Cluster.Status.Version).To(Equal("25.8.2.1"))

		sync := meta.FindStatusCondition(rec.Cluster.Status.Conditions, v1.ConditionTypeVersionInSync)
		Expect(sync).NotTo(BeNil())
		Expect(sync.Status).To(Equal(metav1.ConditionFalse))
		Expect(sync.Reason).To(Equal(v1.ConditionReasonVersionMismatch))

		cond := meta.FindStatusCondition(rec.Cluster.Status.Conditions, v1.ConditionTypeVersionUpgraded)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(v1.ConditionReasonMinorUpdateAvailable))
	})
})

func mustGet[T client.Object](ctx context.Context, c client.Client, key types.NamespacedName) T {
	var result T

	result, ok := reflect.New(reflect.TypeOf(result).Elem()).Interface().(T)
	if !ok {
		panic("unexpected type created")
	}

	ExpectWithOffset(1, c.Get(ctx, key, result)).To(Succeed())

	return result
}

func setupReconciler() (util.Logger, *keeperReconciler, context.CancelFunc) {
	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(v1.AddToScheme(scheme)).To(Succeed())

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	eventRecorder := events.NewFakeRecorder(32)
	logger := util.NewLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	cluster := &v1.KeeperCluster{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test",
		},
		Spec: v1.KeeperClusterSpec{
			Replicas: new(int32(1)),
		},
		Status: v1.KeeperClusterStatus{
			ConfigurationRevision: "config-v1",
			StatefulSetRevision:   "sts-v1",
		},
	}

	cc := &ClusterController{Client: fakeClient, Scheme: scheme, Recorder: eventRecorder, EnablePDB: true}

	reconciler := &keeperReconciler{
		Controller:      cc,
		statusManager:   controller.NewStatusManager(cc, cluster),
		ResourceManager: controller.NewResourceManager(cc, cluster),
		Dialer: func(context.Context, string) (net.Conn, error) {
			return nil, errors.New("disabled")
		},
		Cluster:      cluster,
		ReplicaState: map[v1.KeeperReplicaID]replicaState{},
	}

	eventContext, cancel := context.WithCancel(context.Background())

	// Drain events
	go func() {
		for {
			select {
			case <-eventRecorder.Events:
			case <-eventContext.Done():
				return
			}
		}
	}()

	return logger, reconciler, cancel
}
