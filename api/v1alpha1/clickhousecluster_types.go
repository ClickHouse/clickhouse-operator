package v1alpha1

import (
	"errors"
	"fmt"
	"iter"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// ClickHouseClusterSpec defines the desired state of ClickHouseCluster.
type ClickHouseClusterSpec struct {
	// Number of replicas in the single shard
	// This is a pointer to distinguish between explicit zero and unspecified.
	// +optional
	// +kubebuilder:default:=3
	// +kubebuilder:validation:Minimum=0
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Replica count in shard"
	Replicas *int32 `json:"replicas"`

	// Number of shards in the cluster
	// This is a pointer to distinguish between explicit zero and unspecified.
	// +optional
	// +kubebuilder:default:=1
	// +kubebuilder:validation:Minimum=0
	Shards *int32 `json:"shards"`

	// Reference to the KeeperCluster that is used for ClickHouse coordination.
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Keeper Cluster Reference"
	KeeperClusterRef *corev1.LocalObjectReference `json:"keeperClusterRef"`

	// Parameters passed to the Keeper pod spec.
	// +optional
	PodTemplate PodTemplateSpec `json:"podTemplate,omitempty"`

	// Parameters passed to the ClickHouse container spec.
	// +optional
	ContainerTemplate ContainerTemplateSpec `json:"containerTemplate,omitempty"`

	// Settings for the replicas storage.
	// +required
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Data Volume Claim Spec"
	DataVolumeClaimSpec corev1.PersistentVolumeClaimSpec `json:"dataVolumeClaimSpec,omitempty"`

	// Additional labels that are added to resources.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Additional annotations that are added to resources.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Configuration parameters for ClickHouse server.
	// +optional
	Settings ClickHouseSettings `json:"settings,omitempty"`
}

// WithDefaults sets default values for ClickHouseClusterSpec fields.
func (s *ClickHouseClusterSpec) WithDefaults() {
	defaultSpec := ClickHouseClusterSpec{
		Replicas: ptr.To[int32](DefaultClickHouseReplicaCount),
		Shards:   ptr.To[int32](DefaulClickHouseShardCount),
		ContainerTemplate: ContainerTemplateSpec{
			Image: ContainerImage{
				Repository: DefaultClickHouseContainerRepository,
				Tag:        DefaultClickHouseContainerTag,
			},
			ImagePullPolicy: DefaultClickHouseContainerPolicy,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(DefaultClickHouseCPURequest),
					corev1.ResourceMemory: resource.MustParse(DefaultClickHouseMemoryRequest),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(DefaultClickHouseCPULimit),
					corev1.ResourceMemory: resource.MustParse(DefaultClickHouseMemoryLimit),
				},
			},
		},
		Settings: ClickHouseSettings{
			Logger: LoggerConfig{
				LogToFile: true,
				Level:     "trace",
				Size:      "1000M",
				Count:     DefaultMaxLogFiles,
			},
		},
	}

	if err := controllerutil.ApplyDefault(s, defaultSpec); err != nil {
		panic(fmt.Sprintf("unable to apply defaults: %v", err))
	}

	if s.Settings.TLS.CABundle != nil && s.Settings.TLS.CABundle.Key == "" {
		s.Settings.TLS.CABundle.Key = "ca.crt"
	}
}

// ClickHouseSettings defines ClickHouse server settings options.
type ClickHouseSettings struct {
	// Reference to the Secret key, which contains password for the user 'default'.
	// +optional
	DefaultUserPassword *DefaultPasswordSelector `json:"defaultUserPassword,omitempty"`

	// Optionally you can lower the logger level or disable logging to file at all.
	// +optional
	Logger LoggerConfig `json:"logger,omitempty"`

	// TLS settings, allows to enable TLS settings for ClickHouse.
	// +optional
	TLS ClusterTLSSpec `json:"tls,omitempty"`

	// Enables synchronization of ClickHouse databases to the newly created replicas by the operator.
	// Supports only Replicated and integration tables.
	// +optional
	// +kubebuilder:default:=true
	EnableDatabaseSync bool `json:"enableDatabaseSync,omitempty"`

	// Additional ClickHouse configuration that will be merged with the default one.
	// +nullable
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	ExtraConfig runtime.RawExtension `json:"extraConfig,omitempty"`

	// Additional ClickHouse users configuration that will be merged with the default one.
	// +nullable
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	ExtraUsersConfig runtime.RawExtension `json:"extraUsersConfig,omitempty"`
}

// ClickHouseClusterStatus defines the observed state of ClickHouseCluster.
type ClickHouseClusterStatus struct {
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

	// CurrentRevision indicates latest applied ClickHouseCluster spec revision.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	CurrentRevision string `json:"currentRevision,omitempty"`
	// UpdateRevision indicates latest requested ClickHouseCluster spec revision.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	UpdateRevision string `json:"updateRevision,omitempty"`
	// ObservedGeneration indicates latest generation observed by controller.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// ClickHouseCluster is the Schema for the clickhouseclusters API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=chc;clickhouse
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].message"
// +kubebuilder:printcolumn:name="ReadyReplicas",type="number",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Replicas",type="number",JSONPath=".spec.replicas"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +operator-sdk:csv:customresourcedefinitions:displayName="ClickHouse Cluster"
// +operator-sdk:csv:customresourcedefinitions:resources={{Pod,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{PersistentVolumeClaim,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{StatefulSet,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{ConfigMap,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{Secret,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{Service,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{PodDisruptionBudget,v1}}
type ClickHouseCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClickHouseClusterSpec   `json:"spec,omitempty"`
	Status ClickHouseClusterStatus `json:"status,omitempty"`
}

// ClickHouseReplicaID identifies a ClickHouse replica within the cluster.
// +kubebuilder:object:generate=false
type ClickHouseReplicaID struct {
	ShardID int32
	Index   int32
}

// ClickHouseIDFromLabels extracts ClickHouseReplicaID from given labels map.
func ClickHouseIDFromLabels(labels map[string]string) (ClickHouseReplicaID, error) {
	shardIDStr, ok := labels[controllerutil.LabelClickHouseShardID]
	if !ok {
		return ClickHouseReplicaID{}, errors.New("missing shard ID label")
	}

	shardID, err := strconv.ParseInt(shardIDStr, 10, 32)
	if err != nil {
		return ClickHouseReplicaID{}, fmt.Errorf("invalid shard ID %q: %w", shardIDStr, err)
	}

	replicaIDStr, ok := labels[controllerutil.LabelClickHouseReplicaID]
	if !ok {
		return ClickHouseReplicaID{}, errors.New("missing replica ID label")
	}

	index, err := strconv.ParseInt(replicaIDStr, 10, 32)
	if err != nil {
		return ClickHouseReplicaID{}, fmt.Errorf("invalid replica ID %q: %w", replicaIDStr, err)
	}

	return ClickHouseReplicaID{
		ShardID: int32(shardID),
		Index:   int32(index),
	}, nil
}

// Labels returns labels that should be set for every resource related to the specified replica.
func (id ClickHouseReplicaID) Labels() map[string]string {
	return map[string]string{
		controllerutil.LabelClickHouseShardID:   strconv.Itoa(int(id.ShardID)),
		controllerutil.LabelClickHouseReplicaID: strconv.Itoa(int(id.Index)),
	}
}

// IDFromHostname extracts ClickHouseReplicaID from given hostname.
func IDFromHostname(v *ClickHouseCluster, hostname string) (ClickHouseReplicaID, error) {
	if !strings.HasPrefix(hostname, v.SpecificName()+"-") || !strings.HasSuffix(hostname, "-0") {
		return ClickHouseReplicaID{}, fmt.Errorf("invalid hostname %q", hostname)
	}

	idParts := hostname[len(v.SpecificName())+1 : len(hostname)-2] // leave only {shard}-{index}

	parts := strings.Split(idParts, "-")
	if len(parts) != 2 {
		return ClickHouseReplicaID{}, fmt.Errorf("invalid hostname %q, expected format: <name>-<shard>-<index>-0", hostname)
	}

	shardID, err := strconv.ParseInt(parts[0], 10, 32)
	if err != nil {
		return ClickHouseReplicaID{}, fmt.Errorf("invalid shard ID %q in hostname %q: %w", parts[0], hostname, err)
	}

	index, err := strconv.ParseInt(parts[1], 10, 32)
	if err != nil {
		return ClickHouseReplicaID{}, fmt.Errorf("invalid index %q in hostname %q: %w", parts[1], hostname, err)
	}

	return ClickHouseReplicaID{
		ShardID: int32(shardID),
		Index:   int32(index),
	}, nil
}

var _ logr.Marshaler = ClickHouseReplicaID{}

// MarshalLog implements logr.Marshaler interface for pretty printing in logs.
func (id ClickHouseReplicaID) MarshalLog() any {
	return id.String()
}

func (id ClickHouseReplicaID) String() string {
	return fmt.Sprintf("(%d:%d)", id.ShardID, id.Index)
}

// NamespacedName returns NamespacedName for the ClickHouseCluster.
func (v *ClickHouseCluster) NamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: v.Namespace,
		Name:      v.Name,
	}
}

// GetStatus returns pointer to ClickHouseClusterStatus.
func (v *ClickHouseCluster) GetStatus() *ClickHouseClusterStatus {
	return &v.Status
}

// Conditions returns pointer to the conditions slice.
func (v *ClickHouseCluster) Conditions() *[]metav1.Condition {
	return &v.Status.Conditions
}

// SpecificName returns cluster name with resource suffix. Used to generate resource names.
func (v *ClickHouseCluster) SpecificName() string {
	return v.GetName() + "-clickhouse"
}

// Shards returns requested number of shards in the ClickHouseCluster.
func (v *ClickHouseCluster) Shards() int32 {
	if v.Spec.Shards == nil {
		return DefaulClickHouseShardCount
	}

	return *v.Spec.Shards
}

// Replicas returns requested number of replicas in each shard of the ClickHouseCluster.
func (v *ClickHouseCluster) Replicas() int32 {
	if v.Spec.Replicas == nil {
		return DefaultClickHouseReplicaCount
	}

	return *v.Spec.Replicas
}

// ReplicaIDs returns sequence of ClickHouseReplicaID for every replica in the ClickHouseCluster.
func (v *ClickHouseCluster) ReplicaIDs() iter.Seq[ClickHouseReplicaID] {
	return func(yield func(ClickHouseReplicaID) bool) {
		for shard := range v.Shards() {
			for index := range v.Replicas() {
				if !yield(ClickHouseReplicaID{ShardID: shard, Index: index}) {
					return
				}
			}
		}
	}
}

// HeadlessServiceName returns name of the headless service for the ClickHouseCluster.
func (v *ClickHouseCluster) HeadlessServiceName() string {
	return v.SpecificName() + "-headless"
}

// PodDisruptionBudgetNameByShard returns name of the PodDisruptionBudget for the specific shard.
func (v *ClickHouseCluster) PodDisruptionBudgetNameByShard(shard int32) string {
	return fmt.Sprintf("%s-%d", v.SpecificName(), shard)
}

// SecretName returns name of the Secret with operator generated values.
func (v *ClickHouseCluster) SecretName() string {
	return v.SpecificName()
}

// ConfigMapNameByReplicaID returns name of the ConfigMap for the specific replica.
func (v *ClickHouseCluster) ConfigMapNameByReplicaID(id ClickHouseReplicaID) string {
	return fmt.Sprintf("%s-%d-%d", v.SpecificName(), id.ShardID, id.Index)
}

// StatefulSetNameByReplicaID returns name of the StatefulSet for the specific replica.
func (v *ClickHouseCluster) StatefulSetNameByReplicaID(id ClickHouseReplicaID) string {
	return fmt.Sprintf("%s-%d-%d", v.SpecificName(), id.ShardID, id.Index)
}

// HostnameByID returns domain name for the specific replica to access within Kubernetes cluster.
func (v *ClickHouseCluster) HostnameByID(id ClickHouseReplicaID) string {
	hostnameTemplate := "%s-0.%s.%s.svc.cluster.local"
	return fmt.Sprintf(hostnameTemplate, v.StatefulSetNameByReplicaID(id), v.HeadlessServiceName(), v.Namespace)
}

// +kubebuilder:object:root=true

// ClickHouseClusterList contains a list of ClickHouseCluster.
type ClickHouseClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ClickHouseCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClickHouseCluster{}, &ClickHouseClusterList{})
}
