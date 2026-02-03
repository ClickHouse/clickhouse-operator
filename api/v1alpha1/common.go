package v1alpha1

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// ContainerImage defines a container image with repository, tag, and hash.
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
		return fmt.Sprintf("%s@%s", c.Repository, c.Hash)
	}

	return c.Repository
}

// LoggerConfig defines logging configuration.
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
	// +kubebuilder:validation:Enum:=test;trace;debug;information;notice;warning;error;critical;fatal
	// +kubebuilder:default:=trace
	Level string `json:"level,omitempty"`

	// Maximum log file size.
	// +optional
	// +kubebuilder:default:="1000M"
	Size string `json:"size,omitempty"`

	// Maximum number of log files to keep.
	// +optional
	// +kubebuilder:default:=50
	Count int64 `json:"count,omitempty"`
}

// PodTemplateSpec describes the pod configuration overrides for the cluster's pods.
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
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty" patchMergeKey:"topologyKey" patchStrategy:"merge"`

	// ImagePullSecrets is an optional list of references to secrets in the same namespace to use for pulling any of the images used by this PodSpec.
	// If specified, these secrets will be passed to individual puller implementations for them to use.
	// More info: https://kubernetes.io/docs/concepts/containers/images#specifying-imagepullsecrets-on-a-pod
	// +optional
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty" patchMergeKey:"name" patchStrategy:"merge"`

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

	// Volumes defines the list of volumes that can be mounted by containers belonging to the pod.
	// More info: https://kubernetes.io/docs/concepts/storage/volumes
	// +optional
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	Volumes []corev1.Volume `json:"volumes,omitempty" patchMergeKey:"name" patchStrategy:"merge"`

	// SecurityContext holds pod-level security attributes and common container settings.
	// +optional
	SecurityContext *corev1.PodSecurityContext `json:"securityContext,omitempty"`

	// TopologyZoneKey is the key of node labels.
	// Nodes that have a label with this key and identical values are considered to be in the same topology zone.
	// Set it to enable default TopologySpreadConstraints and Affinity rules to spread pods across zones.
	// Recommended to be set to "topology.kubernetes.io/zone"
	// +optional
	TopologyZoneKey *string `json:"topologyZoneKey,omitempty"`

	// NodeHostnameKey is the key of node labels.
	// Nodes that have a label with this key and identical values are considered to be on the same node.
	// Set it to enable default AntiAffinity rules to spread replicas from the different shards across nodes.
	// Recommended to be set to "kubernetes.io/hostname"
	// +optional
	NodeHostnameKey *string `json:"nodeHostnameKey,omitempty"`
}

// ContainerTemplateSpec describes the container configuration overrides for the cluster's containers.
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

	// VolumeMounts is the list of volume mounts for the container.
	// +optional
	// +patchMergeKey=mountPath
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=mountPath
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty" patchMergeKey:"mountPath" patchStrategy:"merge"`

	// Env is the list of environment variables to set in the container.
	// +optional
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	Env []corev1.EnvVar `json:"env,omitempty" patchMergeKey:"name" patchStrategy:"merge"`

	// SecurityContext defines the security options the container should be run with.
	// If set, the fields of SecurityContext override the equivalent fields of PodSecurityContext.
	// More info: https://kubernetes.io/docs/tasks/configure-pod-container/security-context/
	// +optional
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`
}

// ClusterTLSSpec defines cluster TLS configuration.
type ClusterTLSSpec struct {
	// Enabled indicates whether TLS is enabled, determining if secure ports should be opened.
	// +kubebuilder:default:=false
	// +optional
	Enabled bool `json:"enabled"`
	// Required specifies whether TLS must be enforced for all connections. Disables not secure ports.
	// +kubebuilder:default:=false
	// +optional
	Required bool `json:"required,omitempty"`
	// ServerCertSecretRef is a reference to a TLS Secret containing the server certificate.
	// +optional
	ServerCertSecret *corev1.LocalObjectReference `json:"serverCertSecret,omitempty"`
	// CABundle is a reference to a TLS Secret containing the CA bundle.
	// If empty and ServerCertSecret is specified, the CA bundle from certificate will be used.
	// Otherwise, system trusted CA bundle will be used.
	// Key is defaulted to "ca.crt" if not specified.
	// +optional
	CABundle *SecretKeySelector `json:"caBundle,omitempty"`
}

// Validate validates the ClusterTLSSpec configuration.
func (s *ClusterTLSSpec) Validate() error {
	if !s.Enabled {
		if s.Required {
			return errors.New("TLS cannot be required if it is not enabled")
		}

		return nil
	}

	if s.ServerCertSecret == nil || s.ServerCertSecret.Name == "" {
		return errors.New("serverCertSecret must be specified when TLS is enabled")
	}

	return nil
}

// SecretKeySelector selects a key of a Secret.
type SecretKeySelector struct {
	// The name of the secret in the cluster's namespace to select from.
	// +kubebuilder:validation:Required
	Name string `json:"name,omitempty"`
	// The key of the secret to select from.  Must be a valid secret key.
	// +kubebuilder:validation:Required
	Key string `json:"key,omitempty"`
}

// ConfigMapKeySelector selects a key of a ConfigMap.
type ConfigMapKeySelector struct {
	// The name of the configMap in the cluster's namespace to select from.
	// +kubebuilder:validation:Required
	Name string `json:"name,omitempty"`
	// The key of the configMap to select from. Must be a valid key.
	// +kubebuilder:validation:Required
	Key string `json:"key,omitempty"`
}

// DefaultPasswordSelector selects the source for the default user's password.
type DefaultPasswordSelector struct {
	// Type of the provided password. Consider documentation for possible values https://clickhouse.com/docs/operations/settings/settings-users#user-namepassword
	// +kubebuilder:default:=password
	PasswordType string `json:"passwordType,omitempty"`
	// Select password value from a Secret key
	// +optional
	Secret *SecretKeySelector `json:"secret,omitempty"`
	// Select password value from a ConfigMap key
	// +optional
	ConfigMap *ConfigMapKeySelector `json:"configMap,omitempty"`
}

// Validate validates the DefaultPasswordSelector configuration.
func (s *DefaultPasswordSelector) Validate() error {
	if s == nil {
		return nil
	}

	// Ensure exactly one source is specified
	hasSecret := s.Secret != nil

	hasConfigMap := s.ConfigMap != nil
	if hasSecret == hasConfigMap { // both set or both nil
		return errors.New("exactly one of secret or configMap must be specified")
	}

	if hasSecret {
		if s.Secret.Name == "" || s.Secret.Key == "" {
			return errors.New("default user secret name and key must be specified when using secret")
		}
	}

	if hasConfigMap {
		if s.ConfigMap.Name == "" || s.ConfigMap.Key == "" {
			return errors.New("default user configMap name and key must be specified when using configMap")
		}
	}

	return nil
}
