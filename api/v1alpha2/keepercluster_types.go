package v1alpha2

import (
	"errors"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// KeeperClusterSpec defines the desired state of KeeperCluster.
type KeeperClusterSpec struct {
	// Number of replicas in the cluster
	// +optional
	// +kubebuilder:default:=3
	// +kubebuilder:validation:Enum=0;1;3;5;7;9;11;13;15
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Replica count"
	Replicas *int32 `json:"replicas"`

	// Scheduling configures how the Keeper Pods are spread across nodes and topology domains.
	// The generated rules apply to every Pod, regardless of the pod template it uses.
	// +optional
	Scheduling *SchedulingSpec `json:"scheduling,omitempty"`

	// PodTemplate is merged onto the operator-generated Keeper Pod.
	// The Keeper server container is addressed by name "keeper" in `spec.containers`.
	// When unset, Pods use the operator defaults only.
	// +optional
	PodTemplate *PodTemplateSpec `json:"podTemplate,omitempty"`

	// Template of the persistent volume claim for ClickHouse Keeper data.
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Data Volume Claim Template"
	DataVolumeClaimTemplate *DataVolumeClaimTemplate `json:"dataVolumeClaimTemplate,omitempty"`

	// Additional labels that are added to resources.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Additional annotations that are added to resources.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// PodDisruptionBudget configures the PDB created for the Keeper cluster.
	// When unset, the operator defaults to maxUnavailable=replicas/2
	// (preserving quorum for a 2F+1 cluster); single-replica clusters use maxUnavailable=1.
	// +optional
	PodDisruptionBudget *PodDisruptionBudgetTemplate `json:"podDisruptionBudget,omitempty"`

	// Network configures the cluster networking: DNS domain, TLS and managed NetworkPolicy.
	// +optional
	// +kubebuilder:default:={}
	Network NetworkSpec `json:"network,omitempty"`

	// Configuration parameters for ClickHouse Keeper server.
	// +optional
	Settings KeeperSettings `json:"settings,omitempty"`

	// UpgradeChannel specifies the release channel for major version upgrade checks.
	// When empty, only minor updates will be proposed. Allowed values are: stable, lts or specific major.minor version (e.g. 25.8).
	// +optional
	// +kubebuilder:validation:Pattern=`^(lts|stable|\d+\.\d+)?$`
	UpgradeChannel string `json:"upgradeChannel,omitempty"`
}

// WithDefaults sets default values for KeeperClusterSpec fields.
// Pod and container defaults (image, resources, ...) are not stored in the spec:
// the operator generates them and merges the referenced pod template on top.
func (s *KeeperClusterSpec) WithDefaults() {
	defaultSpec := KeeperClusterSpec{
		Replicas: new(int32(DefaultKeeperReplicaCount)),
		Settings: KeeperSettings{
			Logger: LoggerConfig{
				LogToFile: new(true),
				Level:     DefaultLogLevel,
				Size:      DefaultMaxLogFileSize,
				Count:     DefaultMaxLogFiles,
			},
		},
	}

	if err := controllerutil.ApplyDefault(s, defaultSpec); err != nil {
		panic(fmt.Sprintf("unable to apply defaults: %v", err))
	}

	if s.DataVolumeClaimTemplate != nil && len(s.DataVolumeClaimTemplate.Spec.AccessModes) == 0 {
		s.DataVolumeClaimTemplate.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{DefaultAccessMode}
	}
}

// KeeperSettings defines ClickHouse Keeper server configuration.
type KeeperSettings struct {
	// Configuration of ClickHouse Keeper server logging.
	// +optional
	Logger LoggerConfig `json:"logger,omitempty"`

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
	// ReadyReplicas Total number of replicas ready to serve requests.
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
	// UpdateRevision indicates latest requested KeeperCluster spec revision.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	UpdateRevision string `json:"updateRevision,omitempty"`
	// ObservedGeneration indicates latest generation observed by controller.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Version indicates the version reported by the Keeper server.
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=status
	Version string `json:"version,omitempty"`
}

// KeeperCluster is the Schema for the `keeperclusters` API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=chk;keeper
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].message"
// +kubebuilder:printcolumn:name="ReadyReplicas",type="number",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Replicas",type="number",JSONPath=".spec.replicas"
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".status.version"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:unservedversion
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

// GetConditions returns pointer to the conditions slice.
func (v *KeeperClusterStatus) GetConditions() *[]metav1.Condition {
	if v.Conditions == nil {
		v.Conditions = []metav1.Condition{}
	}

	return &v.Conditions
}

// SpecificName returns cluster name with role suffix.
func (v *KeeperCluster) SpecificName() string {
	return v.Name + "-keeper"
}

// SpecificResourceName returns owned resource name with suffix.
// Base name is truncated to fit in length limits.
func (v *KeeperCluster) SpecificResourceName(suffix string) string {
	return resourceName(v.SpecificName(), suffix)
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
	return v.SpecificResourceName("headless")
}

// PodDisruptionBudgetName returns PodDisruptionBudget name for the Keeper cluster.
func (v *KeeperCluster) PodDisruptionBudgetName() string {
	return v.SpecificResourceName("")
}

// QuorumConfigMapName returns ConfigMap name mounted in every replica.
func (v *KeeperCluster) QuorumConfigMapName() string {
	return v.SpecificResourceName(fmt.Sprintf("quorum-%s-%d", KeeperConfigMapNameSuffix, latestKeeperQuorumConfigMapVersion))
}

// ConfigMapNameByReplicaID returns ConfigMap name for given replica ID.
func (v *KeeperCluster) ConfigMapNameByReplicaID(replicaID KeeperReplicaID) string {
	return v.SpecificResourceName(fmt.Sprintf("%d-%s-v%d", replicaID, KeeperConfigMapNameSuffix, latestKeeperConfigMapVersion))
}

// StatefulSetNameByReplicaID returns StatefulSet name for given replica ID.
func (v *KeeperCluster) StatefulSetNameByReplicaID(replicaID KeeperReplicaID) string {
	return v.SpecificResourceName(fmt.Sprintf("%d", replicaID))
}

// HostnameByID returns domain name for the specific replica to access within Kubernetes cluster.
func (v *KeeperCluster) HostnameByID(id KeeperReplicaID) string {
	return formatPodHostname(v.StatefulSetNameByReplicaID(id), v.HeadlessServiceName(), v.Namespace, v.Spec.Network.ClusterDomain)
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
