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

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	util "github.com/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("UpdateReplica", Ordered, func() {
	var (
		cancelEvents context.CancelFunc
		r            *ClusterReconciler
		recCtx       reconcileContext
		replicaID    v1.KeeperReplicaID = 1
		cfgKey       types.NamespacedName
		stsKey       types.NamespacedName
	)

	BeforeAll(func() {
		r, recCtx, cancelEvents = setupReconciler()
		cfgKey = types.NamespacedName{
			Namespace: recCtx.Cluster.Namespace,
			Name:      recCtx.Cluster.ConfigMapNameByReplicaID(replicaID),
		}
		stsKey = types.NamespacedName{
			Namespace: recCtx.Cluster.Namespace,
			Name:      recCtx.Cluster.StatefulSetNameByReplicaID(replicaID),
		}
	})

	AfterAll(func() {
		cancelEvents()
	})

	It("should create replica resources", func(ctx context.Context) {
		recCtx.SetReplica(1, replicaState{})
		result, err := r.reconcileReplicaResources(ctx, r.Logger, &recCtx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeFalse())

		configMap := mustGet[*corev1.ConfigMap](ctx, r.Client, cfgKey)
		sts := mustGet[*appsv1.StatefulSet](ctx, r.Client, stsKey)
		Expect(configMap).ToNot(BeNil())
		Expect(sts).ToNot(BeNil())
		Expect(util.GetConfigHashFromObject(sts)).To(BeEquivalentTo(recCtx.Cluster.Status.ConfigurationRevision))
		Expect(util.GetSpecHashFromObject(sts)).To(BeEquivalentTo(recCtx.Cluster.Status.StatefulSetRevision))
	})

	It("should do nothing if no changes", func(ctx context.Context) {
		sts := mustGet[*appsv1.StatefulSet](ctx, r.Client, stsKey)
		sts.Status.ObservedGeneration = sts.Generation
		sts.Status.ReadyReplicas = 1
		recCtx.ReplicaState[replicaID] = replicaState{
			Error:       false,
			StatefulSet: sts,
			Status: serverStatus{
				ServerState: ModeStandalone,
			},
		}
		result, err := r.reconcileReplicaResources(ctx, r.Logger, &recCtx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeTrue())
	})

	It("should update resources on spec changes", func(ctx context.Context) {
		recCtx.Cluster.Spec.ContainerTemplate.Image.Repository = "custom-keeper"
		recCtx.Cluster.Spec.ContainerTemplate.Image.Tag = "latest"
		recCtx.Cluster.Status.StatefulSetRevision = "sts-v2"
		result, err := r.reconcileReplicaResources(ctx, r.Logger, &recCtx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeFalse())
		sts := mustGet[*appsv1.StatefulSet](ctx, r.Client, stsKey)
		Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal("custom-keeper:latest"))
	})

	It("should restart server on config changes", func(ctx context.Context) {
		sts := mustGet[*appsv1.StatefulSet](ctx, r.Client, stsKey)
		Expect(sts.Spec.Template.Annotations[util.AnnotationRestartedAt]).To(BeEmpty())
		recCtx.Cluster.Spec.Settings.Logger.Level = "info"
		recCtx.Cluster.Status.ConfigurationRevision = "cfg-v2"
		result, err := r.reconcileReplicaResources(ctx, r.Logger, &recCtx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeFalse())
		sts = mustGet[*appsv1.StatefulSet](ctx, r.Client, stsKey)
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

func setupReconciler() (*ClusterReconciler, reconcileContext, context.CancelFunc) {
	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(v1.AddToScheme(scheme)).To(Succeed())

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	eventRecorder := record.NewFakeRecorder(32)
	clusterReconciler := &ClusterReconciler{
		Scheme:   scheme,
		Client:   fakeClient,
		Logger:   util.NewLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true))),
		Recorder: eventRecorder,
	}

	recCtx := reconcileContext{}
	recCtx.Cluster = &v1.KeeperCluster{
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
	}
	eventContext, cancel := context.WithCancel(context.Background())
	recCtx.ReplicaState = map[v1.KeeperReplicaID]replicaState{}

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

	return clusterReconciler, recCtx, cancel
}
