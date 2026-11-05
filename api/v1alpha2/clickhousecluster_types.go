package v1alpha2

import (
	"cmp"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// ClickHouseNetworkSpec defines the network configuration of the ClickHouse cluster.
type ClickHouseNetworkSpec struct {
	NetworkSpec `json:",inline"`

	// AdditionalPorts declares extra TCP ports to expose on the ClickHouse Pod and the public headless Service.
	// The operator only adds the ports to the Kubernetes resources, it does not configure the ClickHouse server to listen on them.
	// +listType=map
	// +listMapKey=name
	// +optional
	AdditionalPorts []AdditionalPort `json:"additionalPorts,omitempty"`
}

// ClickHouseClusterSpec defines the desired state of ClickHouseCluster.
// +kubebuilder:validation:XValidation:rule="has(self.keeperClusterRef) != has(self.externalKeeper)",message="exactly one of keeperClusterRef or externalKeeper must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.podTemplateName) || (has(self.podTemplates) && self.podTemplates.exists(t, t.name == self.podTemplateName))",message="podTemplateName must reference an entry in podTemplates"
// +kubebuilder:validation:XValidation:rule="!has(self.shardSpecs) || (!has(self.shards) && !has(self.replicas))",message="shardSpecs is mutually exclusive with shards and replicas"
// +kubebuilder:validation:XValidation:rule="!has(self.shardSpecs) || self.shardSpecs.all(s, !has(s.podTemplateName) || (has(self.podTemplates) && self.podTemplates.exists(t, t.name == s.podTemplateName)))",message="shardSpecs podTemplateName must reference an entry in podTemplates"
// +kubebuilder:validation:XValidation:rule="!has(self.shardSpecs) || self.shardSpecs.all(s, !has(s.replicaSpecs) || s.replicaSpecs.all(r, !has(r.podTemplateName) || (has(self.podTemplates) && self.podTemplates.exists(t, t.name == r.podTemplateName))))",message="replicaSpecs podTemplateName must reference an entry in podTemplates"
// +kubebuilder:validation:XValidation:rule="!has(self.dataVolumeClaimTemplateName) || (has(self.dataVolumeClaimTemplates) && self.dataVolumeClaimTemplates.exists(t, t.name == self.dataVolumeClaimTemplateName))",message="dataVolumeClaimTemplateName must reference an entry in dataVolumeClaimTemplates"
// +kubebuilder:validation:XValidation:rule="!has(self.shardSpecs) || self.shardSpecs.all(s, !has(s.dataVolumeClaimTemplateName) || (has(self.dataVolumeClaimTemplates) && self.dataVolumeClaimTemplates.exists(t, t.name == s.dataVolumeClaimTemplateName)))",message="shardSpecs dataVolumeClaimTemplateName must reference an entry in dataVolumeClaimTemplates"
// +kubebuilder:validation:XValidation:rule="!has(self.shardSpecs) || self.shardSpecs.all(s, !has(s.replicaSpecs) || s.replicaSpecs.all(r, !has(r.dataVolumeClaimTemplateName) || (has(self.dataVolumeClaimTemplates) && self.dataVolumeClaimTemplates.exists(t, t.name == r.dataVolumeClaimTemplateName))))",message="replicaSpecs dataVolumeClaimTemplateName must reference an entry in dataVolumeClaimTemplates"
// +kubebuilder:validation:XValidation:rule="!has(self.additionalVolumeClaimTemplateSetName) || (has(self.additionalVolumeClaimTemplateSets) && self.additionalVolumeClaimTemplateSets.exists(t, t.name == self.additionalVolumeClaimTemplateSetName))",message="additionalVolumeClaimTemplateSetName must reference an entry in additionalVolumeClaimTemplateSets"
// +kubebuilder:validation:XValidation:rule="!has(self.shardSpecs) || self.shardSpecs.all(s, !has(s.additionalVolumeClaimTemplateSetName) || (has(self.additionalVolumeClaimTemplateSets) && self.additionalVolumeClaimTemplateSets.exists(t, t.name == s.additionalVolumeClaimTemplateSetName)))",message="shardSpecs additionalVolumeClaimTemplateSetName must reference an entry in additionalVolumeClaimTemplateSets"
// +kubebuilder:validation:XValidation:rule="!has(self.shardSpecs) || self.shardSpecs.all(s, !has(s.replicaSpecs) || s.replicaSpecs.all(r, !has(r.additionalVolumeClaimTemplateSetName) || (has(self.additionalVolumeClaimTemplateSets) && self.additionalVolumeClaimTemplateSets.exists(t, t.name == r.additionalVolumeClaimTemplateSetName))))",message="replicaSpecs additionalVolumeClaimTemplateSetName must reference an entry in additionalVolumeClaimTemplateSets"
type ClickHouseClusterSpec struct {
	// Number of replicas in every shard.
	// Mutually exclusive with `shardSpecs`. Defaults to 3 when `shardSpecs` is unset.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Replica count in shard"
	Replicas *int32 `json:"replicas,omitempty"`

	// Number of shards in the cluster. Shards are named "0" to "N-1".
	// Mutually exclusive with `shardSpecs`. Defaults to 1 when `shardSpecs` is unset.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Shards *int32 `json:"shards,omitempty"`

	// ShardSpecs describes every shard of the cluster individually.
	// Mutually exclusive with `shards` and `replicas`.
	// The shard name is the stable identity of the shard: it is used in the names of the shard resources and
	// in the replication metadata. Removing an entry removes the shard, renaming an entry replaces the shard.
	// List order is not significant: shards are ordered by name, numeric names by value first.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	ShardSpecs []ClickHouseShardSpec `json:"shardSpecs,omitempty"`

	// Reference to the KeeperCluster that is used for ClickHouse coordination.
	// When namespace is omitted, the ClickHouseCluster namespace is used.
	// Mutually exclusive with `externalKeeper`, exactly one of them must be set.
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Keeper Cluster Reference"
	KeeperClusterRef *KeeperClusterReference `json:"keeperClusterRef,omitempty"`

	// Keeper ensemble that is not managed by this operator, used for ClickHouse coordination.
	// The operator does not own the lifecycle of these servers and only points ClickHouse at them.
	// Mutually exclusive with `keeperClusterRef`, exactly one of them must be set.
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="External Keeper"
	ExternalKeeper *ExternalKeeperSpec `json:"externalKeeper,omitempty"`

	// Scheduling configures how the ClickHouse Pods are spread across nodes and topology domains.
	// The generated rules apply to every Pod, regardless of the pod template it uses.
	// +optional
	Scheduling *SchedulingSpec `json:"scheduling,omitempty"`

	// PodTemplates is the set of named templates for the ClickHouse Pods.
	// A template is used only when referenced; the referenced template is merged onto the operator-generated Pod.
	// Each Pod uses the most specific reference: `shardSpecs[].replicaSpecs[].podTemplateName`, then
	// `shardSpecs[].podTemplateName`, then `podTemplateName`. Templates are not merged with each other.
	// The ClickHouse server container is addressed by name "clickhouse" in `spec.containers` of a template.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	PodTemplates []NamedPodTemplate `json:"podTemplates,omitempty"`

	// PodTemplateName references the entry of `podTemplates` used for the ClickHouse Pods.
	// Overridden per shard and per replica in `shardSpecs`.
	// When no template is referenced, Pods use the operator defaults only.
	// +optional
	PodTemplateName string `json:"podTemplateName,omitempty"`

	// DataVolumeClaimTemplates is the set of named templates for the ClickHouse data persistent volume claims.
	// A template is used only when referenced.
	// Each replica uses the most specific reference: `shardSpecs[].replicaSpecs[].dataVolumeClaimTemplateName`, then
	// `shardSpecs[].dataVolumeClaimTemplateName`, then `dataVolumeClaimTemplateName`.
	// Volume claims are not updated in place: changes apply to new replicas only.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Data Volume Claim Templates"
	DataVolumeClaimTemplates []NamedDataVolumeClaimTemplate `json:"dataVolumeClaimTemplates,omitempty"`

	// DataVolumeClaimTemplateName references the entry of `dataVolumeClaimTemplates` used for the ClickHouse data
	// volume claims. Overridden per shard and per replica in `shardSpecs`.
	// +optional
	DataVolumeClaimTemplateName string `json:"dataVolumeClaimTemplateName,omitempty"`

	// AdditionalVolumeClaimTemplateSets is the set of named groups of additional per-pod PVC templates for
	// JBOD / multi-disk storage. A set is used only when referenced.
	// Each replica uses the most specific reference: `shardSpecs[].replicaSpecs[].additionalVolumeClaimTemplateSetName`,
	// then `shardSpecs[].additionalVolumeClaimTemplateSetName`, then `additionalVolumeClaimTemplateSetName`.
	// Sets are not merged with each other.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	AdditionalVolumeClaimTemplateSets []NamedVolumeClaimTemplateSet `json:"additionalVolumeClaimTemplateSets,omitempty"`

	// AdditionalVolumeClaimTemplateSetName references the entry of `additionalVolumeClaimTemplateSets` used for the
	// ClickHouse replicas. Overridden per shard and per replica in `shardSpecs`.
	// When no set is referenced, replicas have no additional disks.
	// +optional
	AdditionalVolumeClaimTemplateSetName string `json:"additionalVolumeClaimTemplateSetName,omitempty"`

	// Additional labels that are added to resources.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Additional annotations that are added to resources.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// PodDisruptionBudget configures the PDB created for each shard.
	// When unset, the operator defaults to maxUnavailable=1 for single-replica
	// shards and minAvailable=1 for multi-replica shards.
	// +optional
	PodDisruptionBudget *PodDisruptionBudgetTemplate `json:"podDisruptionBudget,omitempty"`

	// Network configures the cluster networking: DNS domain, TLS, managed NetworkPolicy and extra ports.
	// +optional
	// +kubebuilder:default:={}
	Network ClickHouseNetworkSpec `json:"network,omitempty"`

	// Configuration parameters for ClickHouse server.
	// +optional
	// +kubebuilder:default:={}
	Settings ClickHouseSettings `json:"settings,omitempty"`

	// UpgradeChannel specifies the release channel for major version upgrade checks.
	// When empty, only minor updates will be proposed. Allowed values are: stable, lts or specific major.minor version (e.g. 25.8).
	// +optional
	// +kubebuilder:validation:Pattern=`^(lts|stable|\d+\.\d+)?$`
	UpgradeChannel string `json:"upgradeChannel,omitempty"`

	// ImageVersionCatalog maps ClickHouse server container images to the ClickHouse version they contain.
	// It replaces the version probe Job of v1alpha1: the operator reads the version of every ClickHouse
	// server image used by the cluster from this catalog, so the version is known before any Pod starts.
	// The version drives version-dependent server configuration and upgrade checks.
	// The default operator image is covered by a built-in entry; every custom image set on the "clickhouse"
	// container in `podTemplates` must have an entry here (enforced by ValidateImageVersionCatalog, the check
	// is too expensive for CRD CEL rules). Images are matched by exact string, so
	// "clickhouse/clickhouse-server:25.8" and "docker.io/clickhouse/clickhouse-server:25.8" are different entries.
	// +optional
	// +listType=map
	// +listMapKey=image
	// +kubebuilder:validation:MaxItems=64
	ImageVersionCatalog []ImageVersionEntry `json:"imageVersionCatalog,omitempty"`

	// ExternalSecret is an optional reference to an externally-managed Secret containing cluster secrets.
	// The secret must reside in the same namespace as the cluster.
	// +optional
	ExternalSecret *ExternalSecret `json:"externalSecret,omitempty"`
}

// AdditionalPort declares one extra TCP port to expose on the ClickHouse Pod and the public headless Service.
type AdditionalPort struct {
	// Name uniquely identifies the port within the list. Used as both the container port name and the Service port name.
	// This must be a DNS_LABEL.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Port is the TCP port number to expose.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
}

// ClickHouseShardSpec describes a single shard of the cluster.
// +kubebuilder:validation:XValidation:rule="has(self.replicas) != has(self.replicaSpecs)",message="exactly one of replicas or replicaSpecs must be set"
type ClickHouseShardSpec struct {
	// Name of the shard, its stable identity.
	// Lowercase alphanumerics and '-', kept short to fit Kubernetes resource names.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=15
	Name string `json:"name"`

	// Number of replicas in the shard.
	// Exactly one of `replicas` or `replicaSpecs` must be set.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Replicas *int32 `json:"replicas,omitempty"`

	// ReplicaSpecs describes every replica of the shard individually, the replica index is the list index.
	// Exactly one of `replicas` or `replicaSpecs` must be set.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	ReplicaSpecs []ClickHouseReplicaSpec `json:"replicaSpecs,omitempty"`

	// PodTemplateName references the entry of `podTemplates` used for the Pods of this shard.
	// Overrides `spec.podTemplateName`.
	// +optional
	PodTemplateName string `json:"podTemplateName,omitempty"`

	// DataVolumeClaimTemplateName references the entry of `dataVolumeClaimTemplates` used for the data volume
	// claims of this shard. Overrides `spec.dataVolumeClaimTemplateName`.
	// +optional
	DataVolumeClaimTemplateName string `json:"dataVolumeClaimTemplateName,omitempty"`

	// AdditionalVolumeClaimTemplateSetName references the entry of `additionalVolumeClaimTemplateSets` used for the
	// replicas of this shard. Overrides `spec.additionalVolumeClaimTemplateSetName`.
	// +optional
	AdditionalVolumeClaimTemplateSetName string `json:"additionalVolumeClaimTemplateSetName,omitempty"`
}

// ClickHouseReplicaSpec describes a single replica of a shard.
type ClickHouseReplicaSpec struct {
	// PodTemplateName references the entry of `podTemplates` used for the Pod of this replica.
	// Overrides the shard and cluster `podTemplateName`.
	// +optional
	PodTemplateName string `json:"podTemplateName,omitempty"`

	// DataVolumeClaimTemplateName references the entry of `dataVolumeClaimTemplates` used for the data volume
	// claim of this replica. Overrides the shard and cluster `dataVolumeClaimTemplateName`.
	// +optional
	DataVolumeClaimTemplateName string `json:"dataVolumeClaimTemplateName,omitempty"`

	// AdditionalVolumeClaimTemplateSetName references the entry of `additionalVolumeClaimTemplateSets` used for this
	// replica. Overrides the shard and cluster `additionalVolumeClaimTemplateSetName`.
	// +optional
	AdditionalVolumeClaimTemplateSetName string `json:"additionalVolumeClaimTemplateSetName,omitempty"`
}

// PodTemplate returns the pod template referenced by PodTemplateName, or nil when none is referenced.
func (s *ClickHouseClusterSpec) PodTemplate() *PodTemplateSpec {
	return findPodTemplate(s.PodTemplates, s.PodTemplateName)
}

// ResolveImageVersion returns the ClickHouse version of the given server image.
// The user-provided imageVersionCatalog takes precedence over the operator built-in catalog.
func (s *ClickHouseClusterSpec) ResolveImageVersion(image string) (string, bool) {
	for _, catalog := range [][]ImageVersionEntry{s.ImageVersionCatalog, BuiltinImageVersionCatalog()} {
		for _, entry := range catalog {
			if entry.Image == image {
				return entry.Version, true
			}
		}
	}

	return "", false
}

// ValidateImageVersionCatalog checks that the image of every "clickhouse" container in podTemplates
// resolves to a version through imageVersionCatalog or the built-in catalog.
// Every template is checked, referenced or not. Other containers (sidecars, init containers) are not checked.
func (s *ClickHouseClusterSpec) ValidateImageVersionCatalog() error {
	var errs []error

	for i, tmpl := range s.PodTemplates {
		for j, c := range tmpl.Spec.Containers {
			if c.Name != ClickHouseServerContainerName || c.Image == "" {
				continue
			}

			if _, ok := s.ResolveImageVersion(c.Image); !ok {
				errs = append(errs, fmt.Errorf(
					"spec.podTemplates[%d].spec.containers[%d].image: image %q has no entry in spec.imageVersionCatalog",
					i, j, c.Image))
			}
		}
	}

	return errors.Join(errs...)
}

// WithDefaults sets default values for ClickHouseClusterSpec fields.
// Pod and container defaults (image, resources, probes, ...) are not stored in the spec:
// the operator generates them and merges the referenced pod template on top.
func (s *ClickHouseClusterSpec) WithDefaults() {
	if len(s.ShardSpecs) == 0 {
		if s.Replicas == nil {
			s.Replicas = new(int32(DefaultClickHouseReplicaCount))
		}

		if s.Shards == nil {
			s.Shards = new(int32(DefaultClickHouseShardCount))
		}
	}

	defaultSpec := ClickHouseClusterSpec{
		Settings: ClickHouseSettings{
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

	for i, t := range s.DataVolumeClaimTemplates {
		if len(t.Spec.AccessModes) == 0 {
			s.DataVolumeClaimTemplates[i].Spec.AccessModes = []corev1.PersistentVolumeAccessMode{DefaultAccessMode}
		}
	}

	for i := range s.AdditionalVolumeClaimTemplateSets {
		templates := s.AdditionalVolumeClaimTemplateSets[i].Templates
		for j := range templates {
			if len(templates[j].Spec.AccessModes) == 0 {
				templates[j].Spec.AccessModes = []corev1.PersistentVolumeAccessMode{DefaultAccessMode}
			}
		}
	}
}

// ClickHouseSettings defines ClickHouse server settings options.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.encryption) || has(self.encryption)",message="encryption cannot be disabled after it has been enabled"
type ClickHouseSettings struct {
	// Specifies source and type of the password for `default` ClickHouse user.
	// +optional
	DefaultUserPassword *DefaultPasswordSelector `json:"defaultUserPassword,omitempty"`

	// Configuration of ClickHouse server logging.
	// +optional
	Logger LoggerConfig `json:"logger,omitempty"`

	// Retention in days for the system log tables (`query_log`, `part_log`, `text_log`, `metric_log`,
	// `asynchronous_metric_log`), applied as a table TTL on the next server restart. Defaults to 30 days
	// on new clusters only; clusters created before this field existed keep unbounded tables until it is set.
	// 0 disables the TTL.
	// +optional
	// +kubebuilder:validation:Minimum=0
	SystemLogsTTLDays *int32 `json:"systemLogsTTLDays,omitempty"`

	// Encryption, when set, enables at-rest encryption of table data through a dedicated storage policy.
	// Opt in per table with `SETTINGS storage_policy = '<policyName>'`.
	// +optional
	Encryption *EncryptionSettings `json:"encryption,omitempty"`

	// DatabaseSync controls how the operator keeps database replicas consistent across the cluster:
	// seeding newly created replicas and dropping replicas left behind in Keeper after scale down.
	// Supports only Replicated and integration databases.
	// +optional
	// +kubebuilder:default:={}
	DatabaseSync DatabaseSyncSettings `json:"databaseSync,omitempty"`

	// Additional ClickHouse configuration that will be merged with the default one.
	// +nullable
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	ExtraConfig runtime.RawExtension `json:"extraConfig,omitempty"`

	// Additional ClickHouse configuration that will be merged with the default one and applied through a
	// configuration reload, without restarting pods. Placing a section here asserts it is reload-safe:
	// a restart-only setting placed here takes effect only at the next restart, without any error.
	// +nullable
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	ExtraReloadableConfig runtime.RawExtension `json:"extraReloadableConfig,omitempty"`

	// Additional ClickHouse users configuration that will be merged with the default one.
	// +nullable
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	ExtraUsersConfig runtime.RawExtension `json:"extraUsersConfig,omitempty"`
}

// NewReplicasPolicy controls whether the operator creates existing databases on newly added replicas.
// +kubebuilder:validation:Enum=Sync;Skip
type NewReplicasPolicy string

const (
	// NewReplicasSync makes the operator create Replicated and integration databases on new replicas.
	NewReplicasSync NewReplicasPolicy = "Sync"
	// NewReplicasSkip leaves new replicas without databases; they must be created manually.
	NewReplicasSkip NewReplicasPolicy = "Skip"
)

// StaleReplicasPolicy controls what the operator does with database replicas that remain registered in Keeper
// after their shard or replica was removed from the cluster.
// +kubebuilder:validation:Enum=Drop;Ignore
type StaleReplicasPolicy string

const (
	// StaleReplicasDrop makes the operator drop inactive stale database replicas from Keeper.
	StaleReplicasDrop StaleReplicasPolicy = "Drop"
	// StaleReplicasIgnore leaves stale database replicas in Keeper untouched.
	StaleReplicasIgnore StaleReplicasPolicy = "Ignore"
)

// DatabaseSyncSettings configures database replica synchronization.
type DatabaseSyncSettings struct {
	// NewReplicas controls whether databases are created on newly added replicas.
	// +optional
	// +kubebuilder:default=Sync
	NewReplicas NewReplicasPolicy `json:"newReplicas,omitempty"`

	// StaleReplicas controls whether database replicas left in Keeper after scale down are dropped.
	// +optional
	// +kubebuilder:default=Drop
	StaleReplicas StaleReplicasPolicy `json:"staleReplicas,omitempty"`
}

// SyncNewReplicas reports whether databases should be created on newly added replicas.
func (s *DatabaseSyncSettings) SyncNewReplicas() bool {
	return s.NewReplicas != NewReplicasSkip
}

// DropStaleReplicas reports whether stale database replicas should be dropped from Keeper.
func (s *DatabaseSyncSettings) DropStaleReplicas() bool {
	return s.StaleReplicas != StaleReplicasIgnore
}

// EncryptionSettings enables at-rest encryption of table data. When present, the operator generates an
// encrypted ClickHouse storage policy wrapping every data disk; tables opt in via `SETTINGS storage_policy`.
// Encryption cannot be disabled once enabled.
type EncryptionSettings struct {
	// PolicyName is the name of the generated encrypted storage policy.
	// +kubebuilder:validation:Pattern=`^[a-z]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="policyName is immutable"
	// +kubebuilder:validation:XValidation:rule="self != 'default'",message="policyName 'default' is reserved; the encrypted policy must not replace the default storage policy"
	// +kubebuilder:default=encrypted
	// +optional
	PolicyName string `json:"policyName,omitempty"`
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
	// Replicas is the total number of replicas requested across all shards.
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=status
	Replicas int32 `json:"replicas"`
	// ReadyReplicas Total number of replicas ready to serve requests.
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=status
	ReadyReplicas int32 `json:"readyReplicas"`

	// CurrentRevision indicates latest applied ClickHouseCluster spec revision.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	CurrentRevision string `json:"currentRevision,omitempty"`
	// UpdateRevision indicates latest requested ClickHouseCluster spec revision.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	UpdateRevision string `json:"updateRevision,omitempty"`
	// ObservedGeneration indicates latest generation observed by controller.
	// +operator-sdk:csv:customresourcedefinitions:type=status
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// ImageVersions lists the version resolved from imageVersionCatalog for every distinct ClickHouse image
	// referenced by the cluster Pods. Entries are removed once no Pod references the image anymore.
	// +optional
	// +listType=map
	// +listMapKey=image
	// +operator-sdk:csv:customresourcedefinitions:type=status
	ImageVersions []ImageVersion `json:"imageVersions,omitempty"`
}

// ImageVersion is the ClickHouse version detected for a container image.
type ImageVersion struct {
	// Image is the resolved image reference of the ClickHouse server container.
	Image string `json:"image"`
	// Version is the version reported by the image.
	Version string `json:"version"`
}

// KeeperClusterReference identifies the KeeperCluster used by a ClickHouseCluster.
type KeeperClusterReference struct {
	// Name of the KeeperCluster resource.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
	// Namespace of the KeeperCluster resource.
	// When omitted, the ClickHouseCluster namespace is used.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// NamespacedName resolves the reference to a fully-qualified Kubernetes object key.
func (r *KeeperClusterReference) NamespacedName(defaultNamespace string) types.NamespacedName {
	if r == nil {
		return types.NamespacedName{}
	}

	namespace := r.Namespace
	if namespace == "" {
		namespace = defaultNamespace
	}

	return types.NamespacedName{
		Namespace: namespace,
		Name:      r.Name,
	}
}

// ExternalKeeperTLSPolicy controls how ClickHouse connects to an external Keeper ensemble.
// +kubebuilder:validation:Enum=Enabled;Disabled
type ExternalKeeperTLSPolicy string

const (
	// ExternalKeeperTLSEnabled makes ClickHouse connect to the ensemble over TLS.
	ExternalKeeperTLSEnabled ExternalKeeperTLSPolicy = "Enabled"
	// ExternalKeeperTLSDisabled is the default policy: connections are made in plain text.
	ExternalKeeperTLSDisabled ExternalKeeperTLSPolicy = "Disabled"
)

// ExternalKeeperSpec describes a Keeper ensemble managed outside of this operator.
type ExternalKeeperSpec struct {
	// Servers of the Keeper ensemble ClickHouse connects to for coordination.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=15
	// +kubebuilder:validation:XValidation:rule="self.all(n, self.exists_one(m, m.host.lowerAscii() == n.host.lowerAscii() && m.port == n.port))",message="nodes must not repeat the same host and port"
	// +listType=atomic
	Nodes []ExternalKeeperNode `json:"nodes"`

	// TLS controls whether ClickHouse connects to the ensemble over TLS.
	// The ports in nodes must match the chosen policy.
	// Server certificates are verified against the system trust store and `spec.network.tls.caBundle`.
	// +optional
	// +kubebuilder:default:=Disabled
	TLS ExternalKeeperTLSPolicy `json:"tls,omitempty"`
}

// ExternalKeeperNode addresses a single server of an external Keeper ensemble.
type ExternalKeeperNode struct {
	// Host name or IP address of the Keeper server. IPv6 addresses must be enclosed in square brackets.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Host string `json:"host"`
	// Port the Keeper server accepts client connections on.
	// +kubebuilder:default:=9181
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`
}

// ClickHouseCluster is the Schema for the `clickhouseclusters` API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=chc;clickhouse
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].message"
// +kubebuilder:printcolumn:name="ReadyReplicas",type="number",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Replicas",type="number",JSONPath=".status.replicas"
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".status.imageVersions[*].version"
// +kubebuilder:printcolumn:name="ExternalSecret",type="string",priority=1,JSONPath=".status.conditions[?(@.type==\"ExternalSecretValid\")].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:unservedversion
// +operator-sdk:csv:customresourcedefinitions:displayName="ClickHouse Cluster"
// +operator-sdk:csv:customresourcedefinitions:resources={{Pod,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{PersistentVolumeClaim,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{StatefulSet,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{ConfigMap,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{Secret,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{Service,v1}}
// +operator-sdk:csv:customresourcedefinitions:resources={{PodDisruptionBudget,v1}}
// +kubebuilder:validation:XValidation:rule="has(self.spec)",message="spec is required"
type ClickHouseCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClickHouseClusterSpec   `json:"spec,omitempty"`
	Status ClickHouseClusterStatus `json:"status,omitempty"`
}

// ClickHouseReplicaID identifies a ClickHouse replica within the cluster.
// +kubebuilder:object:generate=false
type ClickHouseReplicaID struct {
	// Shard is the shard name: "0" to "N-1" with `shards`, the entry name with `shardSpecs`.
	Shard string
	Index int32
}

// ClickHouseIDFromLabels extracts ClickHouseReplicaID from given labels map.
func ClickHouseIDFromLabels(labels map[string]string) (ClickHouseReplicaID, error) {
	shard, ok := labels[controllerutil.LabelClickHouseShardID]
	if !ok || shard == "" {
		return ClickHouseReplicaID{}, errors.New("missing shard ID label")
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
		Shard: shard,
		Index: int32(index),
	}, nil
}

// Labels returns labels that should be set for every resource related to the specified replica.
func (id ClickHouseReplicaID) Labels() map[string]string {
	return map[string]string{
		controllerutil.LabelClickHouseShardID:   id.Shard,
		controllerutil.LabelClickHouseReplicaID: strconv.Itoa(int(id.Index)),
	}
}

var _ logr.Marshaler = ClickHouseReplicaID{}

// MarshalLog implements logr.Marshaler interface for pretty printing in logs.
func (id ClickHouseReplicaID) MarshalLog() any {
	return id.String()
}

func (id ClickHouseReplicaID) String() string {
	return fmt.Sprintf("(%s:%d)", id.Shard, id.Index)
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

// GetConditions returns pointer to the conditions slice.
func (v *ClickHouseClusterStatus) GetConditions() *[]metav1.Condition {
	if v.Conditions == nil {
		v.Conditions = []metav1.Condition{}
	}

	return &v.Conditions
}

// SpecificName returns cluster name with role suffix.
func (v *ClickHouseCluster) SpecificName() string {
	return v.Name + "-clickhouse"
}

// SpecificResourceName returns owned resource name with suffix.
// Base name is truncated to fit in length limits.
func (v *ClickHouseCluster) SpecificResourceName(suffix string) string {
	return resourceName(v.SpecificName(), suffix)
}

// Shards returns requested number of shards in the ClickHouseCluster.
func (v *ClickHouseCluster) Shards() int32 {
	if len(v.Spec.ShardSpecs) > 0 {
		return int32(len(v.Spec.ShardSpecs)) //nolint:gosec // Bounded by the CRD MaxItems.
	}

	if v.Spec.Shards == nil {
		return DefaultClickHouseShardCount
	}

	return *v.Spec.Shards
}

// Replicas returns requested number of replicas in each shard when the shards are defined by `spec.shards`.
// Use ShardReplicas for the replica count of a specific shard.
func (v *ClickHouseCluster) Replicas() int32 {
	if v.Spec.Replicas == nil {
		return DefaultClickHouseReplicaCount
	}

	return *v.Spec.Replicas
}

// ShardNames returns the names of all shards in a deterministic order independent of the `shardSpecs` order:
// numeric names by value first, then the other names lexicographically.
// With `spec.shards` the names are "0" to "N-1".
func (v *ClickHouseCluster) ShardNames() []string {
	if len(v.Spec.ShardSpecs) == 0 {
		names := make([]string, 0, v.Shards())
		for shard := range v.Shards() {
			names = append(names, strconv.Itoa(int(shard)))
		}

		return names
	}

	names := make([]string, 0, len(v.Spec.ShardSpecs))
	for _, shard := range v.Spec.ShardSpecs {
		names = append(names, shard.Name)
	}

	slices.SortFunc(names, compareShardNames)

	return names
}

// compareShardNames orders numeric names by value before the other names, which are ordered lexicographically.
func compareShardNames(a, b string) int {
	an, aErr := strconv.ParseUint(a, 10, 64)
	bn, bErr := strconv.ParseUint(b, 10, 64)

	switch {
	case aErr == nil && bErr == nil:
		if res := cmp.Compare(an, bn); res != 0 {
			return res
		}

		return strings.Compare(a, b)

	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// ShardSpec returns the `shardSpecs` entry for the given shard, or nil when the shards are defined by count.
func (v *ClickHouseCluster) ShardSpec(shard string) *ClickHouseShardSpec {
	for i := range v.Spec.ShardSpecs {
		if v.Spec.ShardSpecs[i].Name == shard {
			return &v.Spec.ShardSpecs[i]
		}
	}

	return nil
}

// ShardReplicas returns requested number of replicas in the given shard.
func (v *ClickHouseCluster) ShardReplicas(shard string) int32 {
	if len(v.Spec.ShardSpecs) == 0 {
		return v.Replicas()
	}

	spec := v.ShardSpec(shard)
	switch {
	case spec == nil:
		return 0
	case spec.Replicas != nil:
		return *spec.Replicas
	default:
		return int32(len(spec.ReplicaSpecs)) //nolint:gosec // Bounded by the CRD MaxItems.
	}
}

// TotalReplicas returns requested number of replicas across all shards.
func (v *ClickHouseCluster) TotalReplicas() int32 {
	var total int32
	for _, shard := range v.ShardNames() {
		total += v.ShardReplicas(shard)
	}

	return total
}

// ReplicaIDs returns sequence of ClickHouseReplicaID for every replica in the ClickHouseCluster.
func (v *ClickHouseCluster) ReplicaIDs() iter.Seq[ClickHouseReplicaID] {
	return func(yield func(ClickHouseReplicaID) bool) {
		for _, shard := range v.ShardNames() {
			for index := range v.ShardReplicas(shard) {
				if !yield(ClickHouseReplicaID{Shard: shard, Index: index}) {
					return
				}
			}
		}
	}
}

// PodTemplateNameFor returns the most specific pod template name referenced for the replica:
// the replica spec, then the shard spec, then the cluster `podTemplateName`. Empty when none is referenced.
func (v *ClickHouseCluster) PodTemplateNameFor(id ClickHouseReplicaID) string {
	return v.templateNameFor(id, v.Spec.PodTemplateName,
		func(s *ClickHouseShardSpec) string { return s.PodTemplateName },
		func(r *ClickHouseReplicaSpec) string { return r.PodTemplateName },
	)
}

// PodTemplateFor returns the most specific pod template referenced for the replica, or nil when none is referenced.
// The returned template is merged onto the operator-generated Pod, it is not merged with less specific templates.
func (v *ClickHouseCluster) PodTemplateFor(id ClickHouseReplicaID) *PodTemplateSpec {
	return findPodTemplate(v.Spec.PodTemplates, v.PodTemplateNameFor(id))
}

// DataVolumeClaimTemplateNameFor returns the most specific data volume claim template name referenced for the
// replica: the replica spec, then the shard spec, then the cluster `dataVolumeClaimTemplateName`.
// Empty when none is referenced.
func (v *ClickHouseCluster) DataVolumeClaimTemplateNameFor(id ClickHouseReplicaID) string {
	return v.templateNameFor(id, v.Spec.DataVolumeClaimTemplateName,
		func(s *ClickHouseShardSpec) string { return s.DataVolumeClaimTemplateName },
		func(r *ClickHouseReplicaSpec) string { return r.DataVolumeClaimTemplateName },
	)
}

// DataVolumeClaimTemplateFor returns the most specific data volume claim template referenced for the replica,
// or nil when none is referenced.
func (v *ClickHouseCluster) DataVolumeClaimTemplateFor(id ClickHouseReplicaID) *DataVolumeClaimTemplate {
	return findDataVolumeClaimTemplate(v.Spec.DataVolumeClaimTemplates, v.DataVolumeClaimTemplateNameFor(id))
}

// AdditionalVolumeClaimTemplateSetNameFor returns the most specific additional volume claim template set name
// referenced for the replica: the replica spec, then the shard spec, then the cluster
// `additionalVolumeClaimTemplateSetName`. Empty when none is referenced.
func (v *ClickHouseCluster) AdditionalVolumeClaimTemplateSetNameFor(id ClickHouseReplicaID) string {
	return v.templateNameFor(id, v.Spec.AdditionalVolumeClaimTemplateSetName,
		func(s *ClickHouseShardSpec) string { return s.AdditionalVolumeClaimTemplateSetName },
		func(r *ClickHouseReplicaSpec) string { return r.AdditionalVolumeClaimTemplateSetName },
	)
}

// AdditionalVolumeClaimTemplatesFor returns the additional volume claim templates of the most specific set
// referenced for the replica, or nil when none is referenced.
func (v *ClickHouseCluster) AdditionalVolumeClaimTemplatesFor(id ClickHouseReplicaID) []PersistentVolumeClaimTemplate {
	return findVolumeClaimTemplateSet(v.Spec.AdditionalVolumeClaimTemplateSets, v.AdditionalVolumeClaimTemplateSetNameFor(id))
}

// HeadlessServiceName returns name of the headless service for the ClickHouseCluster.
func (v *ClickHouseCluster) HeadlessServiceName() string {
	return v.SpecificResourceName("headless")
}

// InternalServiceNameByReplicaID returns name of the internal headless Service for a replica.
func (v *ClickHouseCluster) InternalServiceNameByReplicaID(id ClickHouseReplicaID) string {
	return v.SpecificResourceName(fmt.Sprintf("internal-%s-%d", id.Shard, id.Index))
}

// PodDisruptionBudgetNameByShard returns name of the PodDisruptionBudget for the specific shard.
func (v *ClickHouseCluster) PodDisruptionBudgetNameByShard(shard string) string {
	return v.SpecificResourceName(shard)
}

// SecretName returns name of the Secret with cluster secret values.
// When ExternalSecret is configured, returns the external secret name instead of the operator-generated one.
func (v *ClickHouseCluster) SecretName() string {
	if v.Spec.ExternalSecret != nil {
		return v.Spec.ExternalSecret.Name
	}

	return v.SpecificResourceName("")
}

// ConfigMapNameByReplicaID returns name of the ConfigMap for the specific replica.
func (v *ClickHouseCluster) ConfigMapNameByReplicaID(id ClickHouseReplicaID) string {
	return v.SpecificResourceName(fmt.Sprintf("%s-%d", id.Shard, id.Index))
}

// StatefulSetNameByReplicaID returns name of the StatefulSet for the specific replica.
func (v *ClickHouseCluster) StatefulSetNameByReplicaID(id ClickHouseReplicaID) string {
	return v.SpecificResourceName(fmt.Sprintf("%s-%d", id.Shard, id.Index))
}

// KeeperClusterNamespacedName returns the fully-qualified KeeperCluster reference.
func (v *ClickHouseCluster) KeeperClusterNamespacedName() types.NamespacedName {
	return v.Spec.KeeperClusterRef.NamespacedName(v.Namespace)
}

// HostnameByID returns domain name for the specific replica to access within Kubernetes cluster.
func (v *ClickHouseCluster) HostnameByID(id ClickHouseReplicaID) string {
	return formatPodHostname(v.StatefulSetNameByReplicaID(id), v.HeadlessServiceName(), v.Namespace, v.Spec.Network.ClusterDomain)
}

// InternalHostnameByID returns the internal domain name for the specific replica.
func (v *ClickHouseCluster) InternalHostnameByID(id ClickHouseReplicaID) string {
	return formatServiceHostname(v.InternalServiceNameByReplicaID(id), v.Namespace, v.Spec.Network.ClusterDomain)
}

// templateNameFor resolves the most specific non-empty template name for the replica:
// the replica spec, then the shard spec, then the cluster-level name.
func (v *ClickHouseCluster) templateNameFor(
	id ClickHouseReplicaID,
	clusterName string,
	shardName func(*ClickHouseShardSpec) string,
	replicaName func(*ClickHouseReplicaSpec) string,
) string {
	if shard := v.ShardSpec(id.Shard); shard != nil {
		if id.Index >= 0 && int(id.Index) < len(shard.ReplicaSpecs) {
			if name := replicaName(&shard.ReplicaSpecs[id.Index]); name != "" {
				return name
			}
		}

		if name := shardName(shard); name != "" {
			return name
		}
	}

	return clusterName
}

// +kubebuilder:object:root=true

// ClickHouseClusterList contains a list of ClickHouseCluster.
type ClickHouseClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ClickHouseCluster `json:"items"`
}
