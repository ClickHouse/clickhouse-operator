package controller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type clusterObject[Status any] interface {
	client.Object
	GetGeneration() int64
	Conditions() *[]metav1.Condition
	NamespacedName() types.NamespacedName
	GetStatus() *Status
	SpecificName() string
}

type replicaID interface {
	comparable
	Labels() map[string]string
}

type controller interface {
	GetClient() client.Client
	GetScheme() *k8sruntime.Scheme
	GetRecorder() events.EventRecorder
}

// ResourceReconcilerBase provides a base class for cluster reconcilers.
// It is expected to be embedded in specific reconcilers that hold reconciliation logic and state.
// It contains common methods for managing Cluster status and resources.
type ResourceReconcilerBase[
	ClusterStatus any,
	Cluster clusterObject[ClusterStatus],
	ReplicaID replicaID,
	ReplicaState any,
] struct {
	controller

	Cluster Cluster
	// Should be populated by reconcileActiveReplicaStatus.
	ReplicaState map[ReplicaID]ReplicaState
}

// NewReconcilerBase creates a new ResourceReconcilerBase instance.
func NewReconcilerBase[
	ClusterStatus any,
	Cluster clusterObject[ClusterStatus],
	ReplicaID replicaID,
	ReplicaState any,
](ctrl controller, cluster Cluster) ResourceReconcilerBase[ClusterStatus, Cluster, ReplicaID, ReplicaState] {
	return ResourceReconcilerBase[ClusterStatus, Cluster, ReplicaID, ReplicaState]{
		controller:   ctrl,
		Cluster:      cluster,
		ReplicaState: map[ReplicaID]ReplicaState{},
	}
}

// Replica returns the state of the replica with the given ID.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) Replica(id ReplicaID) S {
	return r.ReplicaState[id]
}

// SetReplica sets the state of the replica with the given ID.
func (r *ResourceReconcilerBase[Status, T, ReplicaID, S]) SetReplica(id ReplicaID, state S) bool {
	_, exists := r.ReplicaState[id]
	r.ReplicaState[id] = state
	return exists
}
