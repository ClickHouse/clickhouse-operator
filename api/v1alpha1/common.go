package v1alpha1

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

type ContainerImage struct {
	// Container image registry name
	// Example: docker.io/clickhouse/clickhouse
	// +optional
	Repository string `json:"repository,omitempty"`

	// Container image tag
	// Example: 25.3
	// +optional
	Tag string `json:"tag,omitempty"`

	// Container image hash, mutually exclusive with 'tag'.
	// +optional
	Hash string `json:"hash,omitempty"`
}

func (c *ContainerImage) String() string {
	if c.Tag != "" {
		return fmt.Sprintf("%s:%s", c.Repository, c.Tag)
	}
	if c.Hash != "" {
		return fmt.Sprintf("%s:%s", c.Repository, c.Hash)
	}

	return c.Repository
}

type LoggerConfig struct {
	// If false then disable all logging to file.
	// +optional
	// +kubebuilder:default:=true
	LogToFile bool `json:"logToFile,omitempty"`

	// If true, then log in JSON format.
	// +optional
	// +kubebuilder:default:=false
	JSONLogs bool `json:"jsonLogs,omitempty"`

	// +optional
	// +kubebuilder:validation:Enum:=test;trace;debug;information;error;warning
	// +kubebuilder:default:=trace
	Level string `json:"level,omitempty"`
}

type PodTemplateSpec struct {
	// Optional duration in seconds the pod needs to terminate gracefully. May be decreased in delete request.
	// Value must be non-negative integer. The value zero indicates stop immediately via
	// the kill signal (no opportunity to shut down).
	// If this value is nil, the default grace period will be used instead.
	// The grace period is the duration in seconds after the processes running in the pod are sent
	// a termination signal and the time when the processes are forcibly halted with a kill signal.
	// Set this value longer than the expected cleanup time for your process.
	// Defaults to 30 seconds.
	// +optional
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`

	// TopologySpreadConstraints describes how a group of pods ought to spread across topology
	// domains. Scheduler will schedule pods in a way which abides by the constraints.
	// All topologySpreadConstraints are ANDed.
	// +optional
	// +patchMergeKey=topologyKey
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=topologyKey
	// +listMapKey=whenUnsatisfiable
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty" patchStrategy:"merge" patchMergeKey:"topologyKey"`

	// ImagePullSecrets is an optional list of references to secrets in the same namespace to use for pulling any of the images used by this PodSpec.
	// If specified, these secrets will be passed to individual puller implementations for them to use.
	// More info: https://kubernetes.io/docs/concepts/containers/images#specifying-imagepullsecrets-on-a-pod
	// +optional
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty" patchStrategy:"merge" patchMergeKey:"name"`

	// NodeSelector is a selector which must be true for the pod to fit on a node.
	// Selector which must match a node's labels for the pod to be scheduled on that node.
	// More info: https://kubernetes.io/docs/concepts/configuration/assign-pod-node/
	// +optional
	// +mapType=atomic
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// If specified, the pod's scheduling constraints
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// If specified, the pod's tolerations.
	// +optional
	// +listType=atomic
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// If specified, the pod will be dispatched by specified scheduler.
	// If not specified, the pod will be dispatched by default scheduler.
	// +optional
	SchedulerName string `json:"schedulerName,omitempty"`

	// ServiceAccountName is the name of the ServiceAccount to use to run this pod.
	// More info: https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

type ContainerTemplateSpec struct {
	// Image is the container image to be deployed.
	Image ContainerImage `json:"image,omitempty"`

	// ImagePullPolicy for the image, which defaults to IfNotPresent.
	// +optional
	// +kubebuilder:validation:Enum="Always";"Never";"IfNotPresent"
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// Resources is the resource requirements for the container.
	// This field cannot be updated once the cluster is created.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

type StatefulSetTemplateSpec struct {
}
