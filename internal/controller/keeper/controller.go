package keeper

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/controller"
	"github.com/clickhouse-operator/internal/util"
	webhookv1 "github.com/clickhouse-operator/internal/webhook/v1alpha1"
)

// ClusterReconciler reconciles a KeeperCluster object.
type ClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Recorder record.EventRecorder
	Logger   util.Logger
}

var _ controller.Controller = &ClusterReconciler{}

// +kubebuilder:rbac:groups=clickhouse.com,resources=keeperclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=clickhouse.com,resources=keeperclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clickhouse.com,resources=keeperclusters/finalizers,verbs=update

// +kubebuilder:rbac:groups="",resources=configmaps;services;pods,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets/status,verbs=get
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cluster := &v1.KeeperCluster{}

	err := r.Get(ctx, req.NamespacedName, cluster)
	if err != nil {
		if errors.IsNotFound(err) {
			r.Logger.Info("keeper cluster not found")
			return ctrl.Result{}, nil
		}

		r.Logger.Error(err, "failed to Get keeper cluster")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	logger := r.Logger.WithContext(ctx, cluster)
	wh := webhookv1.KeeperClusterWebhook{Log: logger}

	if err := wh.Default(ctx, cluster); err != nil {
		return ctrl.Result{RequeueAfter: RequeueOnErrorTimeout}, fmt.Errorf("fill defaults before reconcile: %w", err)
	}
	if _, err := wh.ValidateCreate(ctx, cluster); err != nil {
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:               string(v1.ConditionTypeSpecValid),
			Status:             metav1.ConditionFalse,
			Reason:             string(v1.ConditionReasonSpecInvalid),
			Message:            err.Error(),
			ObservedGeneration: cluster.GetGeneration(),
		})
		if err := r.Status().Update(ctx, cluster); err != nil {
			return ctrl.Result{RequeueAfter: RequeueOnErrorTimeout}, fmt.Errorf("update keeper cluster status: %w", err)
		}
		return ctrl.Result{}, nil
	} else {
		meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
			Type:               string(v1.ConditionTypeSpecValid),
			Status:             metav1.ConditionTrue,
			Reason:             string(v1.ConditionReasonSpecValid),
			ObservedGeneration: cluster.GetGeneration(),
		})
	}

	return r.Sync(ctx, logger, cluster)
}

func (r *ClusterReconciler) GetClient() client.Client {
	return r.Client
}

func (r *ClusterReconciler) GetScheme() *runtime.Scheme {
	return r.Scheme
}

func (r *ClusterReconciler) GetRecorder() record.EventRecorder {
	return r.Recorder
}

// SetupWithManager sets up the controller with the Manager.
func SetupWithManager(mgr ctrl.Manager, log util.Logger) error {
	namedLogger := log.Named("keeper")

	keeperController := &ClusterReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("keeper-controller"),
		Logger:   namedLogger,
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.KeeperCluster{}, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{}, predicate.AnnotationChangedPredicate{}))).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Owns(&corev1.Pod{}).
		WithEventFilter(predicate.ResourceVersionChangedPredicate{}).
		Complete(keeperController)
}
