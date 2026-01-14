package keeper

import (
	"context"
	"reflect"
	"testing"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
	. "github.com/onsi/gomega"
	"go.uber.org/zap/zaptest"
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
)

func TestUpdateReplica(t *testing.T) {
	r, ctx := setupReconciler(t)

	var replicaID v1.KeeperReplicaID = 1
	configMapName := ctx.Cluster.ConfigMapNameByReplicaID(replicaID)
	stsName := ctx.Cluster.StatefulSetNameByReplicaID(replicaID)

	// Create resources
	ctx.SetReplica(1, replicaState{})
	result, err := r.reconcileReplicaResources(r.Logger, &ctx)
	Expect(err).ToNot(HaveOccurred())
	Expect(result.IsZero()).To(BeFalse())

	configMap := mustGet[*corev1.ConfigMap](r.Client, types.NamespacedName{Namespace: ctx.Cluster.Namespace, Name: configMapName})
	sts := mustGet[*appsv1.StatefulSet](r.Client, types.NamespacedName{Namespace: ctx.Cluster.Namespace, Name: stsName})
	Expect(configMap).ToNot(BeNil())
	Expect(sts).ToNot(BeNil())
	Expect(util.GetConfigHashFromObject(sts)).To(BeEquivalentTo(ctx.Cluster.Status.ConfigurationRevision))
	Expect(util.GetSpecHashFromObject(sts)).To(BeEquivalentTo(ctx.Cluster.Status.StatefulSetRevision))

	// Nothing to update
	sts.Status.ObservedGeneration = sts.Generation
	sts.Status.ReadyReplicas = 1
	ctx.ReplicaState[replicaID] = replicaState{
		Error:       false,
		StatefulSet: sts,
		Status: ServerStatus{
			ServerState: ModeStandalone,
		},
	}
	result, err = r.reconcileReplicaResources(r.Logger, &ctx)
	Expect(err).ToNot(HaveOccurred())
	Expect(result.IsZero()).To(BeTrue())

	// Apply changes
	ctx.Cluster.Spec.ContainerTemplate.Image.Repository = "custom-keeper"
	ctx.Cluster.Spec.ContainerTemplate.Image.Tag = "latest"
	ctx.Cluster.Status.StatefulSetRevision = "sts-v2"
	result, err = r.reconcileReplicaResources(r.Logger, &ctx)
	Expect(err).ToNot(HaveOccurred())
	Expect(result.IsZero()).To(BeFalse())
	Expect(sts.Spec.Template.Spec.Containers[0].Image).To(Equal("custom-keeper:latest"))

	// Config changes trigger restart
	Expect(sts.Spec.Template.Annotations[util.AnnotationRestartedAt]).To(BeEmpty())
	ctx.Cluster.Spec.Settings.Logger.Level = "info"
	ctx.Cluster.Status.ConfigurationRevision = "cfg-v2"
	result, err = r.reconcileReplicaResources(r.Logger, &ctx)
	Expect(err).ToNot(HaveOccurred())
	Expect(result.IsZero()).To(BeFalse())
	Expect(sts.Spec.Template.Annotations[util.AnnotationRestartedAt]).ToNot(BeEmpty())
}

func mustGet[T client.Object](c client.Client, key types.NamespacedName) T {
	var result T
	result = reflect.New(reflect.TypeOf(result).Elem()).Interface().(T)

	Expect(c.Get(context.TODO(), key, result)).To(Succeed())
	return result
}

func setupReconciler(t *testing.T) (*ClusterReconciler, reconcileContext) {
	Default = NewWithT(t)
	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(v1.AddToScheme(scheme)).To(Succeed())

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	reconciler := &ClusterReconciler{
		Scheme:   scheme,
		Client:   fakeClient,
		Logger:   util.NewZapLogger(zaptest.NewLogger(t)),
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
	ctx.Context = t.Context()
	ctx.ReplicaState = map[v1.KeeperReplicaID]replicaState{}

	// Drain events
	go func() {
		for {
			select {
			case <-reconciler.Recorder.(*record.FakeRecorder).Events:
			case <-ctx.Context.Done():
				return
			}
		}
	}()

	return reconciler, ctx
}
