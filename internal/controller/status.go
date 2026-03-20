package controller

import (
	"context"
	"fmt"
	"time"

	gcmp "github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	util "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// NewCondition creates a new condition with the given parameters.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) NewCondition(
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
		ObservedGeneration: r.Cluster.GetGeneration(),
	}
}

// SetConditions sets the given conditions in the CRD status conditions.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) SetConditions(
	log util.Logger,
	conditions []metav1.Condition,
) bool {
	clusterCond := r.Cluster.Conditions()

	hasChanges := false
	for _, condition := range conditions {
		if SetStatusCondition(clusterCond, condition) {
			log.Debug("condition changed", "condition", condition.Type, "condition_value", condition.Status)

			hasChanges = true
		}
	}

	return hasChanges
}

// SetCondition sets a single condition in the CRD status conditions.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) SetCondition(log util.Logger, cond metav1.Condition) bool {
	return r.SetConditions(log, []metav1.Condition{cond})
}

// UpsertCondition upserts the given condition into the CRD status conditions.
// Returns true if the condition was changed. Useful to precise detect if condition transition happened.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) UpsertCondition(
	ctx context.Context,
	log util.Logger,
	condition metav1.Condition,
) (bool, error) {
	first := true
	changed := false
	cli := r.GetClient()
	crdInstance := r.Cluster.DeepCopyObject().(clusterObject[Status]) //nolint:forcetypeassert // safe cast
	SetStatusCondition(r.Cluster.Conditions(), condition)

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if !first {
			if err := cli.Get(ctx, r.Cluster.NamespacedName(), crdInstance); err != nil {
				return fmt.Errorf("upsert condition %s: get resource %s: %w", condition.Type, r.Cluster.GetName(), err)
			}
		}

		first = false

		if changed = SetStatusCondition(crdInstance.Conditions(), condition); !changed {
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
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) UpsertConditionAndSendEvent(
	ctx context.Context,
	log util.Logger,
	condition metav1.Condition,
	eventType string,
	eventReason v1.EventReason,
	eventAction v1.EventAction,
	eventMessageFormat string,
	eventMessageArgs ...any,
) (bool, error) {
	changed, err := r.UpsertCondition(ctx, log, condition)
	if err != nil {
		return false, err
	}

	if changed {
		r.GetRecorder().Eventf(r.Cluster, nil, eventType, eventReason, eventAction, eventMessageFormat, eventMessageArgs...)
		return true, nil
	}

	return false, nil
}

// UpsertStatus upserts the current status of the Cluster into the CRD status.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) UpsertStatus(
	ctx context.Context,
	log util.Logger,
) error {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli := r.GetClient()

		crdInstance := r.Cluster.DeepCopyObject().(clusterObject[Status]) //nolint:forcetypeassert // safe cast
		if err := cli.Get(ctx, r.Cluster.NamespacedName(), crdInstance); err != nil {
			return fmt.Errorf("get resource %s: %w", r.Cluster.GetName(), err)
		}

		preStatus := crdInstance.GetStatus()

		diff := gcmp.Diff(*preStatus, *r.Cluster.GetStatus())
		if diff == "" {
			log.Info("statuses are equal, nothing to do")
			return nil
		}

		*crdInstance.GetStatus() = *r.Cluster.GetStatus()
		if err := cli.Status().Update(ctx, crdInstance); err != nil {
			return fmt.Errorf("update resource %s: %w", r.Cluster.GetName(), err)
		}

		log.Debug("status difference:\n" + diff)

		return nil
	})
	if err != nil {
		return fmt.Errorf("upsert status: %w", err)
	}

	return nil
}

// SetStatusCondition sets the given condition in conditions and returns true if the condition was changed.
// Differs from meta.SetStatusCondition as it checks only Status changes.
func SetStatusCondition(conditions *[]metav1.Condition, newCondition metav1.Condition) bool {
	if conditions == nil {
		return false
	}

	existingCondition := meta.FindStatusCondition(*conditions, newCondition.Type)
	if existingCondition == nil {
		newCondition.LastTransitionTime = metav1.NewTime(time.Now().UTC().Truncate(time.Second))
		*conditions = append(*conditions, newCondition)
		return true
	}

	changed := existingCondition.Status != newCondition.Status || existingCondition.Reason != newCondition.Reason
	if changed {
		newCondition.LastTransitionTime = metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	} else {
		newCondition.LastTransitionTime = existingCondition.LastTransitionTime
	}

	*existingCondition = newCondition

	return changed
}
