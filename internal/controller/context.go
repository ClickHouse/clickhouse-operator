package controller

import (
	"context"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type ClusterObject interface {
	runtime.Object
	GetGeneration() int64
	Conditions() *[]metav1.Condition
}

type ReconcileContextBase[T ClusterObject, ReplicaKey comparable, ReplicaState any] struct {
	Cluster T
	Context context.Context

	// Should be populated by reconcileActiveReplicaStatus.
	ReplicaState map[ReplicaKey]ReplicaState
}

func (c *ReconcileContextBase[T, K, S]) Replica(key K) S {
	return c.ReplicaState[key]
}

func (c *ReconcileContextBase[T, K, S]) SetReplica(key K, state S) bool {
	_, exists := c.ReplicaState[key]
	c.ReplicaState[key] = state
	return exists
}

func (c *ReconcileContextBase[T, K, S]) NewCondition(
	condType v1.ConditionType,
	status metav1.ConditionStatus,
	reason v1.ConditionReason,
	message string,
) metav1.Condition {
	return metav1.Condition{
		Type:               string(condType),
		Status:             status,
		Reason:             string(reason),
		Message:            message,
		ObservedGeneration: c.Cluster.GetGeneration(),
	}
}

func (c *ReconcileContextBase[T, K, S]) SetConditions(
	log util.Logger,
	conditions []metav1.Condition,
) {
	clusterCond := c.Cluster.Conditions()
	if *clusterCond == nil {
		*clusterCond = make([]metav1.Condition, 0, len(conditions))
	}

	for _, condition := range conditions {
		if meta.SetStatusCondition(clusterCond, condition) {
			log.Debug("condition changed", "condition", condition.Type, "condition_value", condition.Status)
		}
	}
}

func (c *ReconcileContextBase[T, K, S]) SetCondition(
	log util.Logger,
	condType v1.ConditionType,
	status metav1.ConditionStatus,
	reason v1.ConditionReason,
	message string,
) {
	c.SetConditions(log, []metav1.Condition{c.NewCondition(condType, status, reason, message)})
}
