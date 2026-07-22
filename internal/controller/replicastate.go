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

// RevisionState holds the target revision hashes for comparing replica state against desired state.
// Constructed by the reconciler and passed to replicaState methods.
type RevisionState struct {
	StatefulSetRevision string

	ConfigurationRevision string
	// RestartConfigRevision is a partial revision of config that requires server restart.
	RestartConfigRevision string
	// ReloadConfigRevision is a partial revision of config that can be reloaded in runtime.
	ReloadConfigRevision string

	PVCRevisions map[string]string
}

// ReplicaResources holds resources owned by a single replica.
type ReplicaResources struct {
	STS  *appsv1.StatefulSet
	CFG  *corev1.ConfigMap
	PVCs map[string]*corev1.PersistentVolumeClaim
}

// StatefulSetUpdated checks whether StatefulSet controller applied updates.
func (s ReplicaResources) StatefulSetUpdated() bool {
	if s.STS == nil {
		return false
	}

	return s.STS.Generation == s.STS.Status.ObservedGeneration &&
		s.STS.Status.UpdateRevision == s.STS.Status.CurrentRevision
}

// ReplicaHasDiff checks whether any replica resources should be updated.
func (rev RevisionState) ReplicaHasDiff(state ReplicaResources) bool {
	if state.STS == nil {
		return true
	}

	if util.GetSpecHashFromObject(state.STS) != rev.StatefulSetRevision {
		return true
	}

	if state.STS.Spec.Template.Annotations[util.AnnotationConfigHash] != rev.RestartConfigRevision {
		return true
	}

	if state.CFG == nil {
		return true
	}

	if util.GetConfigHashFromObject(state.CFG) != rev.ConfigurationRevision {
		return true
	}

	for name, revision := range rev.PVCRevisions {
		pvc, ok := state.PVCs[name]
		if !ok || pvc == nil {
			return true
		}

		if util.GetSpecHashFromObject(pvc) != revision {
			return true
		}
	}

	return false
}

// ReplicaState holds the observed state of a single replica.
type ReplicaState struct {
	ReplicaResources

	Pod          *corev1.Pod
	StartupError *string
}

// GetReplicaPod loads the Pod of the given StatefulSet.
func GetReplicaPod(ctx context.Context, log util.Logger, client client.Client, sts *appsv1.StatefulSet) (*corev1.Pod, error) {
	pod := &corev1.Pod{}

	podName := sts.Name + "-0"

	if err := client.Get(ctx, types.NamespacedName{
		Namespace: sts.Namespace,
		Name:      podName,
	}, pod); err != nil {
		if !k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("get pod %q: %w", podName, err)
		}

		log.Info("pod does not exist", "pod", podName, "statefulset", sts.Name)

		return nil, nil
	}

	return pod, nil
}

var podErrorStatuses = []string{"ImagePullBackOff", "ErrImagePull", "CrashLoopBackOff", "CreateContainerError", "CreateContainerConfigError", "InvalidImageName"}

// PodStartupError reports a non-empty description if Pod experiences startup errors.
func PodStartupError(pod *corev1.Pod) *string {
	if pod == nil {
		return nil
	}

	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses} {
		for _, status := range statuses {
			waiting := status.State.Waiting
			if waiting != nil && slices.Contains(podErrorStatuses, waiting.Reason) {
				return new(fmt.Sprintf("container %q: %s: %s", status.Name, waiting.Reason, waiting.Message))
			}
		}
	}

	return nil
}
