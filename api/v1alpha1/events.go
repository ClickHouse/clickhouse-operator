package v1alpha1

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
