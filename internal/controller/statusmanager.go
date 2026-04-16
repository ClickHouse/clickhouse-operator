package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	gcmp "github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	util "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// clusterStatus constrains pointer-to-status types (*ClickHouseClusterStatus, *KeeperClusterStatus).
type clusterStatus[S any] interface {
	*S
	DeepCopy() *S
	GetConditions() *[]metav1.Condition
}

type clusterObject[S any, SP clusterStatus[S]] interface {
	client.Object
	GetGeneration() int64
	NamespacedName() types.NamespacedName
	SpecificName() string
	GetStatus() SP
}

// StatusManager provides generic status/condition management for cluster reconcilers.
// S is the concrete status struct (e.g., ClickHouseClusterStatus).
// SP is the pointer-to-status type satisfying clusterStatus[S].
type StatusManager[S any, SP clusterStatus[S], C clusterObject[S, SP]] struct {
	ctrl    Controller
	cluster C
	// pendingEvents records events to emit during UpsertStatus when conditions transition.
	pendingEvents map[v1.ConditionType]EventSpec
}

// NewStatusManager creates a new StatusManager instance.
func NewStatusManager[S any, SP clusterStatus[S], C clusterObject[S, SP]](
	ctrl Controller,
	cluster C,
) StatusManager[S, SP, C] {
	return StatusManager[S, SP, C]{
		ctrl:          ctrl,
		cluster:       cluster,
		pendingEvents: make(map[v1.ConditionType]EventSpec),
	}
}

// EventSpec defines the specification for an event to be emitted when a condition transitions.
type EventSpec struct {
	Type    string
	Reason  v1.EventReason
	Action  v1.EventAction
	Message string
}

// SetConditions sets the given conditions in the CRD status conditions.
// ObservedGeneration is filled automatically from the cluster object.
func (r *StatusManager[S, SP, C]) SetConditions(conditions []metav1.Condition) {
	gen := r.cluster.GetGeneration()
	clusterCond := r.cluster.GetStatus().GetConditions()

	for _, condition := range conditions {
		condition.ObservedGeneration = gen
		SetStatusCondition(clusterCond, condition)
	}
}

// SetCondition sets a single condition in the CRD status conditions.
// If event is set it is emitted during UpsertStatus only if the condition actually transitions.
func (r *StatusManager[S, SP, C]) SetCondition(cond metav1.Condition, event ...EventSpec) {
	r.SetConditions([]metav1.Condition{cond})

	if len(event) > 0 {
		r.pendingEvents[cond.Type] = event[0]
	}
}

// SetUnknownConditions sets the given condition types to Unknown.
func (r *StatusManager[S, SP, C]) SetUnknownConditions(cr v1.ConditionReason, m string, cts []v1.ConditionType) {
	conditions := make([]metav1.Condition, 0, len(cts))
	for _, c := range cts {
		conditions = append(conditions, metav1.Condition{
			Type:    c,
			Status:  metav1.ConditionUnknown,
			Reason:  cr,
			Message: m,
		})
	}

	r.SetConditions(conditions)
}

// UpsertStatus upserts the current status of the cluster into the CRD status.
// Emits scheduled events of changed conditions.
func (r *StatusManager[S, SP, C]) UpsertStatus(ctx context.Context, log util.Logger) error {
	cli := r.ctrl.GetClient()
	pendingEvents := r.pendingEvents
	r.pendingEvents = map[v1.ConditionType]EventSpec{}

	var changed map[v1.ConditionType]bool

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		apiCluster := r.cluster.DeepCopyObject().(C) //nolint:forcetypeassert // safe cast
		if err := cli.Get(ctx, r.cluster.NamespacedName(), apiCluster); err != nil {
			return fmt.Errorf("get resource %s: %w", r.cluster.GetName(), err)
		}

		changed = map[v1.ConditionType]bool{}

		apiStatus := apiCluster.GetStatus()
		localStatus := r.cluster.GetStatus()

		mergedConditions := SP(apiStatus.DeepCopy()).GetConditions()
		for _, cond := range *localStatus.GetConditions() {
			if SetStatusCondition(mergedConditions, cond) {
				changed[cond.Type] = true
			}
		}

		*mergedConditions = slices.DeleteFunc(*mergedConditions, func(c metav1.Condition) bool {
			return meta.FindStatusCondition(*localStatus.GetConditions(), c.Type) == nil
		})

		preSnapshot := apiStatus.DeepCopy()
		*apiStatus = *localStatus
		*apiStatus.GetConditions() = *mergedConditions

		diff := gcmp.Diff(preSnapshot, apiStatus)
		if diff == "" {
			log.Info("statuses are equal, nothing to do")
			return nil
		}

		if err := cli.Status().Update(ctx, apiCluster); err != nil {
			return fmt.Errorf("update resource %s: %w", r.cluster.GetName(), err)
		}

		log.Debug("status difference:\n" + diff)

		return nil
	})
	if err != nil {
		return fmt.Errorf("upsert status: %w", err)
	}

	// Emit events for changed conditions.
	for cond, e := range pendingEvents {
		if changed[cond] {
			r.ctrl.GetRecorder().Eventf(r.cluster, nil, e.Type, e.Reason, e.Action, e.Message)
		}
	}

	return nil
}

// SetStatusCondition sets the given condition in conditions and returns true if the condition was changed.
// LastTransitionTime is updated only when Status changes, per Kubernetes conventions.
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

	changed := existingCondition.Status != newCondition.Status
	if changed {
		newCondition.LastTransitionTime = metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	} else {
		newCondition.LastTransitionTime = existingCondition.LastTransitionTime
	}

	*existingCondition = newCondition

	return changed
}

// Condition helper functions — pure functions returning metav1.Condition for shared use across controllers.

func replicaCondition(condType v1.ConditionType, ids []string, falseReason, trueReason v1.ConditionReason, msgPrefix string) metav1.Condition {
	if len(ids) > 0 {
		slices.Sort(ids)

		return metav1.Condition{
			Type: condType, Status: metav1.ConditionFalse,
			Reason: falseReason, Message: fmt.Sprintf("%s: %v", msgPrefix, ids),
		}
	}

	return metav1.Condition{
		Type: condType, Status: metav1.ConditionTrue,
		Reason: trueReason,
	}
}

// ReplicaStartupCondition evaluates ReplicaStartupSucceeded.
func ReplicaStartupCondition(errorIDs []string) metav1.Condition {
	return replicaCondition(v1.ConditionTypeReplicaStartupSucceeded, errorIDs,
		v1.ConditionReasonReplicaError, v1.ConditionReasonReplicasRunning, "Replicas have startup errors")
}

// HealthyCondition evaluates Healthy.
func HealthyCondition(notReadyIDs []string) metav1.Condition {
	return replicaCondition(v1.ConditionTypeHealthy, notReadyIDs,
		v1.ConditionReasonReplicasNotReady, v1.ConditionReasonReplicasReady, "Not ready replicas")
}

// ConfigSyncCondition evaluates ConfigurationInSync.
func ConfigSyncCondition(notUpdatedIDs []string) metav1.Condition {
	return replicaCondition(v1.ConditionTypeConfigurationInSync, notUpdatedIDs,
		v1.ConditionReasonConfigurationChanged, v1.ConditionReasonUpToDate, "Replicas have pending updates")
}

// ClusterSizeCondition evaluates ClusterSizeAligned.
func ClusterSizeCondition(existing, expected int) metav1.Condition {
	switch {
	case existing < expected:
		return metav1.Condition{Type: v1.ConditionTypeClusterSizeAligned, Status: metav1.ConditionFalse, Reason: v1.ConditionReasonScalingUp, Message: "Cluster has less replicas than requested"}
	case existing > expected:
		return metav1.Condition{Type: v1.ConditionTypeClusterSizeAligned, Status: metav1.ConditionFalse, Reason: v1.ConditionReasonScalingDown, Message: "Cluster has more replicas than requested"}
	default:
		return metav1.Condition{Type: v1.ConditionTypeClusterSizeAligned, Status: metav1.ConditionTrue, Reason: v1.ConditionReasonUpToDate}
	}
}
