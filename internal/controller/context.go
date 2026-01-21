package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	gcmp "github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/controllerutil"
)

type clusterObject[Status any] interface {
	client.Object
	GetGeneration() int64
	Conditions() *[]metav1.Condition
	NamespacedName() types.NamespacedName
	GetStatus() *Status
}

// ReconcileContextBase provides a base context for reconciling a cluster object.
// It provides common methods for reconciling.
type ReconcileContextBase[S any, T clusterObject[S], ReplicaID comparable, ReplicaState any] struct {
	Cluster T

	// Should be populated by reconcileActiveReplicaStatus.
	ReplicaState map[ReplicaID]ReplicaState
}

// Replica returns the state of the replica with the given ID.
func (c *ReconcileContextBase[Status, T, ReplicaID, S]) Replica(id ReplicaID) S {
	return c.ReplicaState[id]
}

// SetReplica sets the state of the replica with the given ID.
func (c *ReconcileContextBase[Status, T, ReplicaID, S]) SetReplica(id ReplicaID, state S) bool {
	_, exists := c.ReplicaState[id]
	c.ReplicaState[id] = state
	return exists
}

// NewCondition creates a new condition with the given parameters.
func (c *ReconcileContextBase[Status, T, ReplicaID, S]) NewCondition(
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

// SetConditions sets the given conditions in the CRD status conditions.
func (c *ReconcileContextBase[Status, T, ReplicaID, S]) SetConditions(
	log controllerutil.Logger,
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

// SetCondition sets a single condition in the CRD status conditions.
func (c *ReconcileContextBase[Status, T, ReplicaID, S]) SetCondition(
	log controllerutil.Logger,
	condType v1.ConditionType,
	status metav1.ConditionStatus,
	reason v1.ConditionReason,
	message string,
) bool {
	return c.SetConditions(log, []metav1.Condition{c.NewCondition(condType, status, reason, message)})
}

// UpsertCondition upserts the given condition into the CRD status conditions.
// Returns true if the condition was changed. Useful to precise detect if condition transition happened.
func (c *ReconcileContextBase[Status, T, ReplicaID, S]) UpsertCondition(
	ctx context.Context,
	log controllerutil.Logger,
	controller controller,
	condition metav1.Condition,
) (bool, error) {
	changed := false
	crdInstance := c.Cluster.DeepCopyObject().(clusterObject[Status]) //nolint:forcetypeassert // safe cast
	setStatusCondition(c.Cluster.Conditions(), condition)

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli := controller.GetClient()
		if err := cli.Get(ctx, c.Cluster.NamespacedName(), crdInstance); err != nil {
			return fmt.Errorf("upsert condition %s: get resource %s: %w", condition.Type, c.Cluster.GetName(), err)
		}

		if changed = setStatusCondition(crdInstance.Conditions(), condition); !changed {
			log.Debug("condition is up to date", "condition", condition.Type, "condition_value", condition.Status)
			return nil
		}

		return cli.Status().Update(ctx, crdInstance)
	})
	if err != nil {
		return false, fmt.Errorf("upsert condition %s: %w", condition.Type, err)
	}

	return changed, nil
}

// UpsertConditionAndSendEvent upserts the given condition into the CRD status conditions.
// Sends an event if the condition was changed.
func (c *ReconcileContextBase[Status, T, ReplicaID, S]) UpsertConditionAndSendEvent(
	ctx context.Context,
	log controllerutil.Logger,
	controller controller,
	condition metav1.Condition,
	eventType string,
	eventReason v1.EventReason,
	eventMessageFormat string,
	eventMessageArgs ...any,
) (bool, error) {
	changed, err := c.UpsertCondition(ctx, log, controller, condition)
	if err != nil {
		return false, err
	}

	if changed {
		controller.GetRecorder().Eventf(c.Cluster, eventType, eventReason, eventMessageFormat, eventMessageArgs...)
		return true, nil
	}

	return false, nil
}

// UpsertStatus upserts the current status of the cluster into the CRD status.
func (c *ReconcileContextBase[Status, T, ReplicaID, S]) UpsertStatus(
	ctx context.Context,
	log controllerutil.Logger,
	controller controller,
) error {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli := controller.GetClient()

		crdInstance := c.Cluster.DeepCopyObject().(clusterObject[Status]) //nolint:forcetypeassert // safe cast
		if err := cli.Get(ctx, c.Cluster.NamespacedName(), crdInstance); err != nil {
			return fmt.Errorf("upsert status: get resource %s: %w", c.Cluster.GetName(), err)
		}

		preStatus := crdInstance.GetStatus()

		if reflect.DeepEqual(*preStatus, *c.Cluster.GetStatus()) {
			log.Info("statuses are equal, nothing to do")
			return nil
		}

		log.Debug("status difference:\n" + gcmp.Diff(*preStatus, *c.Cluster.GetStatus()))
		*crdInstance.GetStatus() = *c.Cluster.GetStatus()

		return cli.Status().Update(ctx, crdInstance)
	})
	if err != nil {
		return fmt.Errorf("upsert status: %w", err)
	}

	return nil
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
