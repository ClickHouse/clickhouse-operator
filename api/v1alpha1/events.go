package v1alpha1

type EventReason = string

// Event reasons for owned resources lifecycle events.
const (
	EventReasonFailedCreate     EventReason = "FailedCreate"
	EventReasonFailedUpdate     EventReason = "FailedUpdate"
	EventReasonSuccessfulDelete EventReason = "SuccessfulDelete"
	EventReasonFailedDelete     EventReason = "FailedDelete"
)
