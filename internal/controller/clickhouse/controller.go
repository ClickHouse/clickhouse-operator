package clickhouse

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	webhookv1 "github.com/ClickHouse/clickhouse-operator/internal/webhook/v1alpha1"
)

// ClusterController reconciles a ClickHouseCluster object.
type ClusterController struct {
	client.Client

	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Logger   controllerutil.Logger
	Webhook  webhookv1.ClickHouseClusterWebhook
}

// +kubebuilder:rbac:groups=clickhouse.com,resources=clickhouseclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=clickhouse.com,resources=clickhouseclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clickhouse.com,resources=clickhouseclusters/finalizers,verbs=update

// +kubebuilder:rbac:groups="",resources=secrets;persistentvolumeclaims,verbs=get;list;watch;create;update;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (cc *ClusterController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cluster := &v1.ClickHouseCluster{}

	err := cc.Get(ctx, req.NamespacedName, cluster)
	if err != nil {
		if errors.IsNotFound(err) {
			cc.Logger.Info("clickhouse cluster not found")
			return ctrl.Result{}, nil
		}

		cc.Logger.Error(err, "failed to Get ClickHouse cluster")

		return ctrl.Result{}, fmt.Errorf("get ClickHouseCluster %s: %w", req.String(), err)
	}

	logger := cc.Logger.WithContext(ctx, cluster)

	if err := cc.Webhook.Default(ctx, cluster); err != nil {
		return ctrl.Result{}, fmt.Errorf("fill defaults before reconcile: %w", err)
	}

	if _, err := cc.Webhook.ValidateCreate(ctx, cluster); err != nil {
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:               string(v1.ConditionTypeSpecValid),
			Status:             metav1.ConditionFalse,
			Reason:             string(v1.ConditionReasonSpecInvalid),
			Message:            err.Error(),
			ObservedGeneration: cluster.GetGeneration(),
		})

		if err := cc.Status().Update(ctx, cluster); err != nil {
			return ctrl.Result{}, fmt.Errorf("update clickhouse cluster status: %w", err)
		}

		return ctrl.Result{}, nil
	}

	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:               string(v1.ConditionTypeSpecValid),
		Status:             metav1.ConditionTrue,
		Reason:             string(v1.ConditionReasonSpecValid),
		ObservedGeneration: cluster.GetGeneration(),
	})

	reconciler := clickhouseReconciler{
		reconcilerBase: chctrl.NewReconcilerBase[
			v1.ClickHouseClusterStatus,
			*v1.ClickHouseCluster,
			v1.ClickHouseReplicaID,
			replicaState,
		](cc, cluster),
	}

	return reconciler.sync(ctx, logger)
}

// GetClient returns the K8S Client.
func (cc *ClusterController) GetClient() client.Client {
	return cc.Client
}

// GetScheme returns initialized with the Cluster Scheme.
func (cc *ClusterController) GetScheme() *runtime.Scheme {
	return cc.Scheme
}

// GetRecorder returns the KeeperCluster EventRecorder.
func (cc *ClusterController) GetRecorder() record.EventRecorder {
	return cc.Recorder
}

// SetupWithManager sets up the controller with the Manager.
func SetupWithManager(mgr ctrl.Manager, log controllerutil.Logger) error {
	namedLogger := log.Named("clickhouse")

	clickhouseController := &ClusterController{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("clickhouse-controller"),
		Logger:   namedLogger,
		Webhook:  webhookv1.ClickHouseClusterWebhook{Log: namedLogger},
	}

	err := ctrl.NewControllerManagedBy(mgr).
		For(&v1.ClickHouseCluster{}, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{}, predicate.AnnotationChangedPredicate{}))).
		Watches(
			&v1.KeeperCluster{},
			handler.EnqueueRequestsFromMapFunc(clickhouseController.clickHouseClustersForKeeper),
		).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.Service{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Owns(&corev1.Pod{}).
		WithEventFilter(predicate.ResourceVersionChangedPredicate{}).
		Complete(clickhouseController)
	if err != nil {
		return fmt.Errorf("setup ClickHouse controller: %w", err)
	}

	return nil
}

func (cc *ClusterController) clickHouseClustersForKeeper(ctx context.Context, obj client.Object) []reconcile.Request {
	zk, ok := obj.(*v1.KeeperCluster)
	if !ok {
		panic(fmt.Errorf("expected v1.KeeperCluster but got a %T", obj))
	}

	// List all ClickHouseClusters that reference this KeeperCluster
	var chList v1.ClickHouseClusterList
	if err := cc.List(ctx, &chList, client.InNamespace(zk.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, ch := range chList.Items {
		if ch.Spec.KeeperClusterRef.Name == zk.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      ch.Name,
					Namespace: ch.Namespace,
				},
			})
		}
	}

	return requests
}
