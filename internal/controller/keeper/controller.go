/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package keeper

import (
	"context"
	"fmt"
	"time"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
	webhookv1 "github.com/clickhouse-operator/internal/webhook/v1alpha1"
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
)

// ClusterReconciler reconciles a KeeperCluster object.
type ClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Recorder record.EventRecorder
	Reader   client.Reader // Used to bypass the cache.
	Logger   util.Logger
}

// +kubebuilder:rbac:groups=clickhouse.com,resources=keeperclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=clickhouse.com,resources=keeperclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clickhouse.com,resources=keeperclusters/finalizers,verbs=update

// +kubebuilder:rbac:groups="",resources=configmaps;services;pods,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=apps,resources=statefulsets/status,verbs=get
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;delete

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

// SetupWithManager sets up the controller with the Manager.
func SetupWithManager(mgr ctrl.Manager, log util.Logger) error {
	namedLogger := log.Named("keeper")

	controller := &ClusterReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("clickhouse.KeeperCluster"),
		Reader:   mgr.GetAPIReader(),
		Logger:   namedLogger,
	}

	controllerBuilder := ctrl.NewControllerManagedBy(mgr).
		For(&v1.KeeperCluster{}, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{}, predicate.AnnotationChangedPredicate{}))).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		WithEventFilter(predicate.ResourceVersionChangedPredicate{})

	return controllerBuilder.Complete(controller)
}
