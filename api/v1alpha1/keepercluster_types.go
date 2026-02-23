package v1alpha1

import (
	"errors"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// KeeperClusterSpec defines the desired state of KeeperCluster.
type KeeperClusterSpec struct {
	// Number of replicas in the cluster
	// This is a pointer to distinguish between explicit zero and unspecified.
	// +optional
	// +kubebuilder:default:=3
	// +kubebuilder:validation:Enum=0;1;3;5;7;9;11;13;15
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Replica count"
	Replicas *int32 `json:"replicas"`

	// Parameters passed to the Keeper pod spec.
	// +optional
	PodTemplate PodTemplateSpec `json:"podTemplate,omitempty"`

	// Parameters passed to the Keeper container spec.
	// +optional
	ContainerTemplate ContainerTemplateSpec `json:"containerTemplate,omitempty"`

	// Settings for the replicas storage.
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Data Volume Claim Spec"
	DataVolumeClaimSpec *corev1.PersistentVolumeClaimSpec `json:"dataVolumeClaimSpec,omitempty"`

	// Additional labels that are added to resources.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Additional annotations that are added to resources.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Configuration parameters for ClickHouse Keeper server.
	// +optional
	Settings KeeperSettings `json:"settings,omitempty"`

	// ClusterDomain is the Kubernetes cluster domain suffix used for DNS resolution.
	// Defaults to "cluster.local" if not specified.
	// +optional
	// +kubebuilder:default:="cluster.local"
	ClusterDomain string `json:"clusterDomain,omitempty"`
}

// WithDefaults sets default values for KeeperClusterSpec fields.
func (s *KeeperClusterSpec) WithDefaults() {
	defaultSpec := KeeperClusterSpec{
		Replicas: ptr.To[int32](DefaultKeeperReplicaCount),
		ContainerTemplate: ContainerTemplateSpec{
			Image: ContainerImage{
				Repository: DefaultKeeperContainerRepository,
				Tag:        DefaultKeeperContainerTag,
			},
			ImagePullPolicy: DefaultKeeperContainerPolicy,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(DefaultKeeperCPURequest),
					corev1.ResourceMemory: resource.MustParse(DefaultKeeperMemoryRequest),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(DefaultKeeperCPULimit),
					corev1.ResourceMemory: resource.MustParse(DefaultKeeperMemoryLimit),
				},
			},
		},
		Settings: KeeperSettings{
			Logger: LoggerConfig{
				LogToFile: new(true),
				Level:     "trace",
				Size:      "1000M",
				Count:     DefaultMaxLogFiles,
			},
		},
	}

	if err := controllerutil.ApplyDefault(s, defaultSpec); err != nil {
		panic(fmt.Sprintf("unable to apply defaults: %v", err))
	}

	if s.DataVolumeClaimSpec != nil && len(s.DataVolumeClaimSpec.AccessModes) == 0 {
		s.DataVolumeClaimSpec.AccessModes = []corev1.PersistentVolumeAccessMode{DefaultAccessMode}
	}
}

// KeeperSettings defines ClickHouse Keeper server configuration.
type KeeperSettings struct {
	// Optionally you can lower the logger level or disable logging to file at all.
	// +optional
	Logger LoggerConfig `json:"logger,omitempty"`

	// TLS settings, allows to enable TLS settings for Keeper.
	// +optional
	TLS ClusterTLSSpec `json:"tls,omitempty"`

	// Additional ClickHouse Keeper configuration that will be merged with the default one.
	// +nullable
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	ExtraConfig runtime.RawExtension `json:"extraConfig,omitempty"`
}

// KeeperClusterStatus defines the observed state of KeeperCluster.
type KeeperClusterStatus struct {
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ReadyReplicas Total number of replicas ready to server requests.
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=status
	ReadyReplicas int32 `json:"readyReplicas"`
	// ConfigurationRevision indicates target configuration revision for every replica.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	ConfigurationRevision string `json:"configurationRevision,omitempty"`
	// StatefulSetRevision indicates target StatefulSet revision for every replica.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	StatefulSetRevision string `json:"statefulSetRevision,omitempty"`

	// CurrentRevision indicates latest applied KeeperCluster spec revision.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	CurrentRevision string `json:"currentRevision,omitempty"`
	// CurrentRevision indicates latest requested KeeperCluster spec revision.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	UpdateRevision string `json:"updateRevision,omitempty"`
	// ObservedGeneration indicates latest generation observed by controller.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// KeeperCluster is the Schema for the keeperclusters API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=chk;keeper
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].message"
// +kubebuilder:printcolumn:name="ReadyReplicas",type="number",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Replicas",type="number",JSONPath=".spec.replicas"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +operator-sdk:csv:customresourcedefinitions:resources={{Pod,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{PersistentVolumeClaim,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{StatefulSet,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{ConfigMap,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{Service,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{PodDisruptionBudget,v1}}
type KeeperCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KeeperClusterSpec   `json:"spec,omitempty"`
	Status KeeperClusterStatus `json:"status,omitempty"`

	specificName string `json:"-"`
}

// KeeperReplicaID represents ClickHouse Keeper replica ID. Used for naming resources and RAFT configuration.
type KeeperReplicaID int32

// KeeperReplicaIDFromLabels extracts KeeperReplicaID from given labels map.
func KeeperReplicaIDFromLabels(labels map[string]string) (KeeperReplicaID, error) {
	idStr, ok := labels[controllerutil.LabelKeeperReplicaID]
	if !ok {
		return 0, errors.New("missing replica ID label")
	}

	id, err := strconv.ParseInt(idStr, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid replica ID %q: %w", idStr, err)
	}

	return KeeperReplicaID(id), nil
}

// Labels returns labels that should be set for every resource related to the specified replica.
func (id KeeperReplicaID) Labels() map[string]string {
	return map[string]string{
		controllerutil.LabelKeeperReplicaID: strconv.FormatInt(int64(id), 10),
	}
}

// NamespacedName returns NamespacedName for the KeeperCluster.
func (v *KeeperCluster) NamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: v.Namespace,
		Name:      v.Name,
	}
}

// GetStatus returns pointer to the KeeperClusterStatus.
func (v *KeeperCluster) GetStatus() *KeeperClusterStatus {
	return &v.Status
}

// Conditions returns pointer to the conditions slice.
func (v *KeeperCluster) Conditions() *[]metav1.Condition {
	return &v.Status.Conditions
}

// SpecificName returns cluster name with resource suffix. Used to generate resource names.
func (v *KeeperCluster) SpecificName() string {
	if v.specificName == "" {
		v.specificName = normalizeName(v.Name) + "-keeper"
	}

	return v.specificName
}

// Replicas returns requested number of replicas in the cluster.
func (v *KeeperCluster) Replicas() int32 {
	if v.Spec.Replicas == nil {
		return DefaultKeeperReplicaCount
	}

	return *v.Spec.Replicas
}

const (
	KeeperConfigMapNameSuffix          = "configmap"
	latestKeeperConfigMapVersion       = 1
	latestKeeperQuorumConfigMapVersion = 1
)

// HeadlessServiceName returns headless service name for the Keeper cluster.
func (v *KeeperCluster) HeadlessServiceName() string {
	return v.SpecificName() + "-headless"
}

// PodDisruptionBudgetName returns PodDisruptionBudget name for the Keeper cluster.
func (v *KeeperCluster) PodDisruptionBudgetName() string {
	return v.SpecificName()
}

// QuorumConfigMapName returns ConfigMap name mounted in every replica.
func (v *KeeperCluster) QuorumConfigMapName() string {
	return fmt.Sprintf("%s-quorum-%s-%d", v.SpecificName(), KeeperConfigMapNameSuffix, latestKeeperQuorumConfigMapVersion)
}

// ConfigMapNameByReplicaID returns ConfigMap name for given replica ID.
func (v *KeeperCluster) ConfigMapNameByReplicaID(replicaID KeeperReplicaID) string {
	return fmt.Sprintf("%s-%d-%s-v%d", v.SpecificName(), replicaID, KeeperConfigMapNameSuffix, latestKeeperConfigMapVersion)
}

// StatefulSetNameByReplicaID returns StatefulSet name for given replica ID.
func (v *KeeperCluster) StatefulSetNameByReplicaID(replicaID KeeperReplicaID) string {
	return fmt.Sprintf("%s-%d", v.SpecificName(), replicaID)
}

// HostnameByID returns domain name for the specific replica to access within Kubernetes cluster.
func (v *KeeperCluster) HostnameByID(id KeeperReplicaID) string {
	return formatPodHostname(v.StatefulSetNameByReplicaID(id), v.HeadlessServiceName(), v.Namespace, v.Spec.ClusterDomain)
}

// Hostnames returns list of domain names for all replicas to access within Kubernetes cluster.
func (v *KeeperCluster) Hostnames() []string {
	hostnames := make([]string, 0, v.Replicas())
	for id := range KeeperReplicaID(v.Replicas()) {
		hostnames = append(hostnames, v.HostnameByID(id))
	}

	return hostnames
}

// +kubebuilder:object:root=true

// KeeperClusterList contains a list of KeeperCluster.
type KeeperClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []KeeperCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KeeperCluster{}, &KeeperClusterList{})
}
