package controller

import (
	"context"
	"fmt"
	"slices"

	v1 "github.com/clickhouse-operator/api/v1alpha1"
	"github.com/clickhouse-operator/internal/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ReplicaUpdateStage int

const (
	StageUpToDate ReplicaUpdateStage = iota
	StageStsAndConfigDiff
	StageConfigDiff
	StageStsDiff
	StageNotReadyUpToDate
	StageNotReadyWithDiff
	StageUpdating
	StageError
	StageNotExists
)

var (
	mapStatusText = map[ReplicaUpdateStage]string{
		StageUpToDate:         "UpToDate",
		StageStsAndConfigDiff: "StatefulSetAndConfigDiff",
		StageConfigDiff:       "ConfigDiff",
		StageStsDiff:          "StatefulSetDiff",
		StageNotReadyUpToDate: "NotReadyUpToDate",
		StageNotReadyWithDiff: "NotReadyWithDiff",
		StageUpdating:         "Updating",
		StageError:            "Error",
		StageNotExists:        "NotExists",
	}
)

func (s ReplicaUpdateStage) String() string {
	return mapStatusText[s]
}

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

var podErrorStatuses = []string{"ImagePullBackOff", "ErrImagePull", "CrashLoopBackOff"}

func CheckPodError(ctx context.Context, log util.Logger, client client.Client, sts *appsv1.StatefulSet) (bool, error) {
	var pod corev1.Pod
	podName := fmt.Sprintf("%s-0", sts.Name)

	if err := client.Get(ctx, types.NamespacedName{
		Namespace: sts.Namespace,
		Name:      podName,
	}, &pod); err != nil {
		if !k8serrors.IsNotFound(err) {
			return false, fmt.Errorf("get clickhouse pod %q: %w", podName, err)
		}

		log.Info("pod is not exists", "pod", podName, "stateful_set", sts.Name)
		return false, nil
	}

	isError := false
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting != nil && slices.Contains(podErrorStatuses, status.State.Waiting.Reason) {
			log.Info("pod in error state", "pod", podName, "reason", status.State.Waiting.Reason)
			isError = true
			break
		}
	}

	return isError, nil
}
