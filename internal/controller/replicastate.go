package controller

import (
	"context"
	"fmt"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"

	util "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// ReplicaUpdateStage represents the stage of updating a ClickHouse replica. Used in reconciliation process.
type ReplicaUpdateStage int

const (
	StageUpToDate ReplicaUpdateStage = iota
	StageHasDiff
	StageNotReadyUpToDate
	StageUpdating
	StageError
	StageNotExists
)

var mapStatusText = map[ReplicaUpdateStage]string{
	StageUpToDate:         "UpToDate",
	StageHasDiff:          "HasDiff",
	StageNotReadyUpToDate: "NotReadyUpToDate",
	StageUpdating:         "Updating",
	StageError:            "Error",
	StageNotExists:        "NotExists",
}

func (s ReplicaUpdateStage) String() string {
	return mapStatusText[s]
}

var podErrorStatuses = []string{"ImagePullBackOff", "ErrImagePull", "CrashLoopBackOff", "CreateContainerError", "CreateContainerConfigError", "InvalidImageName"}

// CheckPodError checks if the pod of the given StatefulSet have permanent errors preventing it from starting.
func CheckPodError(ctx context.Context, log util.Logger, client client.Client, sts *appsv1.StatefulSet) (bool, error) {
	var pod corev1.Pod

	podName := sts.Name + "-0"

	if err := client.Get(ctx, types.NamespacedName{
		Namespace: sts.Namespace,
		Name:      podName,
	}, &pod); err != nil {
		if !k8serrors.IsNotFound(err) {
			return false, fmt.Errorf("get clickhouse pod %q: %w", podName, err)
		}

		log.Info("pod does not exist", "pod", podName, "statefulset", sts.Name)

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
