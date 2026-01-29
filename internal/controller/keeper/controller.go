package keeper

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
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	webhookv1 "github.com/ClickHouse/clickhouse-operator/internal/webhook/v1alpha1"
)

// ClusterController reconciles a KeeperCluster object.
type ClusterController struct {
	client.Client

	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Logger   controllerutil.Logger
	Webhook  webhookv1.KeeperClusterWebhook
}

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
func (cc *ClusterController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cluster := &v1.KeeperCluster{}

	err := cc.Get(ctx, req.NamespacedName, cluster)
	if err != nil {
		if errors.IsNotFound(err) {
			cc.Logger.Info("keeper cluster not found")
			return ctrl.Result{}, nil
		}

		cc.Logger.Error(err, "failed to Get keeper cluster")

		return ctrl.Result{}, fmt.Errorf("get KeeperCluster %s: %w", req.String(), err)
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
			return ctrl.Result{}, fmt.Errorf("update keeper cluster status: %w", err)
		}

		return ctrl.Result{}, nil
	}

	meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
		Type:               string(v1.ConditionTypeSpecValid),
		Status:             metav1.ConditionTrue,
		Reason:             string(v1.ConditionReasonSpecValid),
		ObservedGeneration: cluster.GetGeneration(),
	})

	reconciler := keeperReconciler{
		reconcilerBase: chctrl.NewReconcilerBase[
			v1.KeeperClusterStatus,
			*v1.KeeperCluster,
			v1.KeeperReplicaID,
			replicaState,
		](cc, cluster),
		ExtraConfig: map[string]any{},
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
	namedLogger := log.Named("keeper")

	keeperController := &ClusterController{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("keeper-controller"),
		Logger:   namedLogger,
		Webhook:  webhookv1.KeeperClusterWebhook{Log: namedLogger},
	}

	err := ctrl.NewControllerManagedBy(mgr).
		For(&v1.KeeperCluster{}, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{}, predicate.AnnotationChangedPredicate{}))).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Owns(&corev1.Pod{}).
		WithEventFilter(predicate.ResourceVersionChangedPredicate{}).
		Complete(keeperController)
	if err != nil {
		return fmt.Errorf("setup Keeper controller: %w", err)
	}

	return nil
}
