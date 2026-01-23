package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
	gcmp "github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ClusterObject[Status any] interface {
	client.Object
	GetGeneration() int64
	Conditions() *[]metav1.Condition
	NamespacedName() types.NamespacedName
	GetStatus() *Status
}

type ReconcileContextBase[S any, T ClusterObject[S], ReplicaKey comparable, ReplicaState any] struct {
	Cluster T
	Context context.Context

	// Should be populated by reconcileActiveReplicaStatus.
	ReplicaState map[ReplicaKey]ReplicaState
}

func (c *ReconcileContextBase[Status, T, K, S]) Replica(key K) S {
	return c.ReplicaState[key]
}

func (c *ReconcileContextBase[Status, T, K, S]) SetReplica(key K, state S) bool {
	_, exists := c.ReplicaState[key]
	c.ReplicaState[key] = state
	return exists
}

func (c *ReconcileContextBase[Status, T, K, S]) NewCondition(
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

func (c *ReconcileContextBase[Status, T, K, S]) SetConditions(
	log util.Logger,
	conditions []metav1.Condition,
) bool {
	clusterCond := c.Cluster.Conditions()
	if *clusterCond == nil {
		*clusterCond = make([]metav1.Condition, 0, len(conditions))
	}

	hasChanges := false
	for _, condition := range conditions {
		if setStatusCondition(clusterCond, condition) {
			log.Debug("condition changed", "condition", condition.Type, "condition_value", condition.Status)
			hasChanges = true
		}
	}

	return hasChanges
}

func (c *ReconcileContextBase[Status, T, K, S]) SetCondition(
	log util.Logger,
	condType v1.ConditionType,
	status metav1.ConditionStatus,
	reason v1.ConditionReason,
	message string,
) bool {
	return c.SetConditions(log, []metav1.Condition{c.NewCondition(condType, status, reason, message)})
}

// UpsertCondition upserts the given condition into the CRD status conditions.
// Returns true if the condition was changed. Useful to precise detect if condition transition happened.
func (c *ReconcileContextBase[Status, T, K, S]) UpsertCondition(
	controller Controller,
	log util.Logger,
	condition metav1.Condition,
) (bool, error) {
	changed := false
	crdInstance := c.Cluster.DeepCopyObject().(ClusterObject[Status])
	setStatusCondition(c.Cluster.Conditions(), condition)

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli := controller.GetClient()
		if err := cli.Get(c.Context, c.Cluster.NamespacedName(), crdInstance); err != nil {
			return err
		}

		if changed = setStatusCondition(crdInstance.Conditions(), condition); !changed {
			log.Debug("condition is up to date", "condition", condition.Type, "condition_value", condition.Status)
			return nil
		}

		return cli.Status().Update(c.Context, crdInstance)
	})

	return changed, err
}

// UpsertConditionAndSendEvent upserts the given condition into the CRD status conditions.
// Sends an event if the condition was changed.
func (c *ReconcileContextBase[Status, T, K, S]) UpsertConditionAndSendEvent(
	controller Controller,
	log util.Logger,
	condition metav1.Condition,
	eventType string,
	eventReason v1.EventReason,
	eventMessageFormat string,
	eventMessageArgs ...any,
) (bool, error) {
	changed, err := c.UpsertCondition(controller, log, condition)
	if err != nil {
		return false, err
	}

	if changed {
		controller.GetRecorder().Eventf(c.Cluster, eventType, eventReason, eventMessageFormat, eventMessageArgs...)
		return true, nil
	}

	return false, nil
}

func (c *ReconcileContextBase[Status, T, K, S]) UpsertStatus(controller Controller, log util.Logger) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli := controller.GetClient()
		crdInstance := c.Cluster.DeepCopyObject().(ClusterObject[Status])
		if err := cli.Get(c.Context, c.Cluster.NamespacedName(), crdInstance); err != nil {
			return err
		}
		preStatus := crdInstance.GetStatus()

		if reflect.DeepEqual(*preStatus, *c.Cluster.GetStatus()) {
			log.Info("statuses are equal, nothing to do")
			return nil
		}
		log.Debug(fmt.Sprintf("status difference:\n%s", gcmp.Diff(*preStatus, *c.Cluster.GetStatus())))
		*crdInstance.GetStatus() = *c.Cluster.GetStatus()
		return cli.Status().Update(c.Context, crdInstance)
	})
}

// SetStatusCondition sets the given condition in conditions and returns true if the condition was changed.
// Differs from meta.SetStatusCondition as it checks only Status changes.
func setStatusCondition(conditions *[]metav1.Condition, newCondition metav1.Condition) bool {
	if conditions == nil {
		return false
	}
	existingCondition := meta.FindStatusCondition(*conditions, newCondition.Type)
	if existingCondition == nil {
		newCondition.LastTransitionTime = metav1.NewTime(time.Now())
		*conditions = append(*conditions, newCondition)
		return true
	}

	changed := existingCondition.Status != newCondition.Status
	if changed {
		newCondition.LastTransitionTime = metav1.NewTime(time.Now())
	} else {
		newCondition.LastTransitionTime = existingCondition.LastTransitionTime
	}

	*existingCondition = newCondition
	return changed
}
