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

package clickhouse

import (
	"context"
	"time"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// ClusterReconciler reconciles a ClickHouseCluster object.
type ClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Recorder record.EventRecorder
	Reader   client.Reader // Used to bypass the cache.
	Logger   util.Logger
}

// +kubebuilder:rbac:groups=clickhouse.com,resources=clickhouseclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=clickhouse.com,resources=clickhouseclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clickhouse.com,resources=clickhouseclusters/finalizers,verbs=update

// +kubebuilder:rbac:groups="",resources=secrets;persistentvolumeclaims,verbs=get;list;watch;create;update;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cluster := &v1.ClickHouseCluster{}

	err := r.Get(ctx, req.NamespacedName, cluster)
	if err != nil {
		if errors.IsNotFound(err) {
			r.Logger.Info("clickhouse cluster not found")
			return ctrl.Result{Requeue: false}, nil
		}

		r.Logger.Error(err, "failed to Get ClickHouse cluster")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	logger := r.Logger.WithContext(ctx, cluster)
	return r.Sync(ctx, logger, cluster)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	controllerBuilder := ctrl.NewControllerManagedBy(mgr).
		For(&v1.ClickHouseCluster{}, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{}, predicate.AnnotationChangedPredicate{}))).
		Watches(
			&v1.KeeperCluster{},
			handler.EnqueueRequestsFromMapFunc(r.clickHouseClustersForKeeper),
		).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.Service{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		WithEventFilter(predicate.ResourceVersionChangedPredicate{})

	return controllerBuilder.Complete(r)
}

func (r *ClusterReconciler) clickHouseClustersForKeeper(ctx context.Context, obj client.Object) []reconcile.Request {
	zk := obj.(*v1.KeeperCluster)

	// List all ClickHouseClusters that reference this KeeperCluster
	var chList v1.ClickHouseClusterList
	if err := r.List(ctx, &chList, client.InNamespace(zk.Namespace)); err != nil {
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
