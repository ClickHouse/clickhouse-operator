package v1alpha1

// EventReason represents the reason for an event.
type EventReason = string

// Event reasons for owned resources lifecycle events.
const (
	EventReasonFailedCreate EventReason = "FailedCreate"
	EventReasonFailedUpdate EventReason = "FailedUpdate"
	EventReasonFailedDelete EventReason = "FailedDelete"
)

// Event reasons for horizontal scaling events.
const (
	EventReasonReplicaCreated           EventReason = "ReplicaCreated"
	EventReasonReplicaDeleted           EventReason = "ReplicaDeleted"
	EventReasonHorizontalScaleBlocked   EventReason = "HorizontalScaleBlocked"
	EventReasonHorizontalScaleStarted   EventReason = "HorizontalScaleStarted"
	EventReasonHorizontalScaleCompleted EventReason = "HorizontalScaleCompleted"
)

// Event reasons for cluster health transitions.
const (
	EventReasonClusterReady    EventReason = "ClusterReady"
	EventReasonClusterNotReady EventReason = "ClusterNotReady"
)

// EventAction represents the action associated with an event.
type EventAction = string

const (
	EventActionReconciling    EventAction = "Reconciling"
	EventActionScaling        EventAction = "Scaling"
	EventActionBecameReady    EventAction = "BecameReady"
	EventActionBecameNotReady EventAction = "BecameNotReady"
)
