package keeper

import (
	"context"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/ClickHouse/clickhouse-operator/internal/controller"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	util "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
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

	BeforeAll(func() {
		log, rec, cancelEvents = setupReconciler()
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
		rec.SetReplica(1, replicaState{})
		result, err := rec.reconcileReplicaResources(ctx, log)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeFalse())

		configMap := mustGet[*corev1.ConfigMap](ctx, rec.GetClient(), cfgKey)
		sts := mustGet[*appsv1.StatefulSet](ctx, rec.GetClient(), stsKey)
		Expect(configMap).ToNot(BeNil())
		Expect(sts).ToNot(BeNil())
		Expect(util.GetConfigHashFromObject(sts)).To(BeEquivalentTo(rec.Cluster.Status.ConfigurationRevision))
		Expect(util.GetSpecHashFromObject(sts)).To(BeEquivalentTo(rec.Cluster.Status.StatefulSetRevision))
	})

	It("should do nothing if no changes", func(ctx context.Context) {
		sts := mustGet[*appsv1.StatefulSet](ctx, rec.GetClient(), stsKey)
		sts.Status.ObservedGeneration = sts.Generation
		sts.Status.ReadyReplicas = 1
		rec.ReplicaState[replicaID] = replicaState{
			Error:       false,
			StatefulSet: sts,
			Status: serverStatus{
				ServerState: ModeStandalone,
			},
		}
		result, err := rec.reconcileReplicaResources(ctx, log)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeTrue())
	})

	It("should update resources on spec changes", func(ctx context.Context) {
		rec.Cluster.Spec.ContainerTemplate.Image.Repository = "custom-keeper"
		rec.Cluster.Spec.ContainerTemplate.Image.Tag = "latest"
		rec.Cluster.Status.StatefulSetRevision = "sts-v2"
		result, err := rec.reconcileReplicaResources(ctx, log)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeFalse())
		sts := mustGet[*appsv1.StatefulSet](ctx, rec.GetClient(), stsKey)
		Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal("custom-keeper:latest"))
	})

	It("should restart server on config changes", func(ctx context.Context) {
		sts := mustGet[*appsv1.StatefulSet](ctx, rec.GetClient(), stsKey)
		Expect(sts.Spec.Template.Annotations[util.AnnotationRestartedAt]).To(BeEmpty())
		rec.Cluster.Spec.Settings.Logger.Level = "info"
		rec.Cluster.Status.ConfigurationRevision = "cfg-v2"
		result, err := rec.reconcileReplicaResources(ctx, log)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeFalse())
		sts = mustGet[*appsv1.StatefulSet](ctx, rec.GetClient(), stsKey)
		Expect(sts.Spec.Template.Annotations[util.AnnotationRestartedAt]).ToNot(BeEmpty())
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
	eventRecorder := record.NewFakeRecorder(32)
	logger := util.NewLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	reconciler := &keeperReconciler{
		reconcilerBase: controller.NewReconcilerBase[
			v1.KeeperClusterStatus,
			*v1.KeeperCluster,
			v1.KeeperReplicaID,
			replicaState,
		](&ClusterController{
			Scheme:   scheme,
			Client:   fakeClient,
			Logger:   logger,
			Recorder: eventRecorder,
		},
			&v1.KeeperCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "default",
					Name:      "test",
				},
				Spec: v1.KeeperClusterSpec{
					Replicas: ptr.To[int32](1),
				},
				Status: v1.KeeperClusterStatus{
					ConfigurationRevision: "config-v1",
					StatefulSetRevision:   "sts-v1",
				},
			}),
		ExtraConfig: map[string]any{},
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
