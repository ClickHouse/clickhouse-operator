package keeper

import (
	"context"
	"reflect"
	"testing"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestUpdateReplica(t *testing.T) {
	r, ctx := setupReconciler(t)

	replicaID := "1"
	configMapName := ctx.KeeperCluster.ConfigMapNameByReplicaID(replicaID)
	stsName := ctx.KeeperCluster.StatefulSetNameByReplicaID(replicaID)

	// Create resources
	result, err := r.reconcileReplicaResources(ctx)
	require.NoError(t, err)
	require.False(t, result.IsZero())

	configMap := mustGet[*corev1.ConfigMap](t, r.Client, types.NamespacedName{Namespace: ctx.KeeperCluster.Namespace, Name: configMapName})
	sts := mustGet[*appsv1.StatefulSet](t, r.Client, types.NamespacedName{Namespace: ctx.KeeperCluster.Namespace, Name: stsName})
	require.NotEmpty(t, configMap)
	require.NotEmpty(t, sts)
	require.Equal(t, ctx.KeeperCluster.Status.ConfigurationRevision, util.GetConfigHashFromObject(sts))
	require.Equal(t, ctx.KeeperCluster.Status.StatefulSetRevision, util.GetSpecHashFromObject(sts))

	// Nothing to update
	sts.Status.ObservedGeneration = sts.Generation
	sts.Status.ReadyReplicas = 1
	ctx.stateByID[replicaID] = replicaState{
		Error:       false,
		StatefulSet: sts,
		Status: ServerStatus{
			ServerState: ModeStandalone,
		},
	}
	result, err = r.reconcileReplicaResources(ctx)
	require.NoError(t, err)
	require.True(t, result.IsZero())

	// Apply changes
	ctx.KeeperCluster.Spec.Image.Repository = "custom-keeper"
	ctx.KeeperCluster.Spec.Image.Tag = "latest"
	ctx.KeeperCluster.Status.StatefulSetRevision = "sts-v2"
	result, err = r.reconcileReplicaResources(ctx)
	require.NoError(t, err)
	require.False(t, result.IsZero())
	require.Equal(t, "custom-keeper:latest", sts.Spec.Template.Spec.Containers[0].Image)

	// Config changes trigger restart
	require.Empty(t, sts.Spec.Template.Annotations[util.AnnotationRestartedAt])
	ctx.KeeperCluster.Spec.LoggerConfig.LoggerLevel = "info"
	ctx.KeeperCluster.Status.ConfigurationRevision = "cfg-v2"
	result, err = r.reconcileReplicaResources(ctx)
	require.NoError(t, err)
	require.False(t, result.IsZero())
	require.NotEmpty(t, sts.Spec.Template.Annotations[util.AnnotationRestartedAt])
}

func mustGet[T client.Object](t *testing.T, c client.Client, key types.NamespacedName) T {
	var result T
	result = reflect.New(reflect.TypeOf(result).Elem()).Interface().(T)

	err := c.Get(context.TODO(), key, result)
	require.NoError(t, err)
	return result
}

func setupReconciler(t *testing.T) (*ClusterReconciler, reconcileContext) {
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, v1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	reconciler := &ClusterReconciler{
		Scheme: scheme,
		Client: fakeClient,
		Logger: util.NewZapLogger(zaptest.NewLogger(t)),
	}

	return reconciler, reconcileContext{
		KeeperCluster: &v1.KeeperCluster{
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
				Replicas:              []string{"1"},
			},
		},
		Context:   t.Context(),
		Log:       reconciler.Logger,
		stateByID: map[string]replicaState{},
	}
}
