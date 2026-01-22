package keeper

import (
	"context"
	"reflect"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
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
)

var _ = Describe("UpdateReplica", Ordered, func() {
	var (
		cancelEvents context.CancelFunc
		r            *ClusterReconciler
		ctx          reconcileContext
		replicaID    v1.KeeperReplicaID = 1
		cfgKey       types.NamespacedName
		stsKey       types.NamespacedName
	)

	BeforeAll(func() {
		r, ctx, cancelEvents = setupReconciler()
		cfgKey = types.NamespacedName{
			Namespace: ctx.Cluster.Namespace,
			Name:      ctx.Cluster.ConfigMapNameByReplicaID(replicaID),
		}
		stsKey = types.NamespacedName{
			Namespace: ctx.Cluster.Namespace,
			Name:      ctx.Cluster.StatefulSetNameByReplicaID(replicaID),
		}
	})

	AfterAll(func() {
		cancelEvents()
	})

	It("should create replica resources", func() {
		ctx.SetReplica(1, replicaState{})
		result, err := r.reconcileReplicaResources(r.Logger, &ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeFalse())

		configMap := mustGet[*corev1.ConfigMap](r.Client, cfgKey)
		sts := mustGet[*appsv1.StatefulSet](r.Client, stsKey)
		Expect(configMap).ToNot(BeNil())
		Expect(sts).ToNot(BeNil())
		Expect(util.GetConfigHashFromObject(sts)).To(BeEquivalentTo(ctx.Cluster.Status.ConfigurationRevision))
		Expect(util.GetSpecHashFromObject(sts)).To(BeEquivalentTo(ctx.Cluster.Status.StatefulSetRevision))
	})

	It("should do nothing if no changes", func() {
		sts := mustGet[*appsv1.StatefulSet](r.Client, stsKey)
		sts.Status.ObservedGeneration = sts.Generation
		sts.Status.ReadyReplicas = 1
		ctx.ReplicaState[replicaID] = replicaState{
			Error:       false,
			StatefulSet: sts,
			Status: ServerStatus{
				ServerState: ModeStandalone,
			},
		}
		result, err := r.reconcileReplicaResources(r.Logger, &ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeTrue())
	})

	It("should update resources on spec changes", func() {
		ctx.Cluster.Spec.ContainerTemplate.Image.Repository = "custom-keeper"
		ctx.Cluster.Spec.ContainerTemplate.Image.Tag = "latest"
		ctx.Cluster.Status.StatefulSetRevision = "sts-v2"
		result, err := r.reconcileReplicaResources(r.Logger, &ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeFalse())
		sts := mustGet[*appsv1.StatefulSet](r.Client, stsKey)
		Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal("custom-keeper:latest"))
	})

	It("should restart server on config changes", func() {
		sts := mustGet[*appsv1.StatefulSet](r.Client, stsKey)
		Expect(sts.Spec.Template.Annotations[util.AnnotationRestartedAt]).To(BeEmpty())
		ctx.Cluster.Spec.Settings.Logger.Level = "info"
		ctx.Cluster.Status.ConfigurationRevision = "cfg-v2"
		result, err := r.reconcileReplicaResources(r.Logger, &ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.IsZero()).To(BeFalse())
		sts = mustGet[*appsv1.StatefulSet](r.Client, stsKey)
		Expect(sts.Spec.Template.Annotations[util.AnnotationRestartedAt]).ToNot(BeEmpty())
	})
})

func mustGet[T client.Object](c client.Client, key types.NamespacedName) T {
	var result T
	result = reflect.New(reflect.TypeOf(result).Elem()).Interface().(T)

	ExpectWithOffset(1, c.Get(context.TODO(), key, result)).To(Succeed())
	return result
}

func setupReconciler() (*ClusterReconciler, reconcileContext, context.CancelFunc) {
	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(v1.AddToScheme(scheme)).To(Succeed())

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	reconciler := &ClusterReconciler{
		Scheme:   scheme,
		Client:   fakeClient,
		Logger:   util.NewZapLogger(zap.NewRaw(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true))),
		Recorder: record.NewFakeRecorder(32),
	}

	ctx := reconcileContext{}
	ctx.Cluster = &v1.KeeperCluster{
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
	ctx.ReplicaState = map[v1.KeeperReplicaID]replicaState{}

	// Drain events
	go func() {
		for {
			select {
			case <-reconciler.Recorder.(*record.FakeRecorder).Events:
			case <-eventContext.Done():
				return
			}
		}
	}()

	return reconciler, ctx, cancel
}
