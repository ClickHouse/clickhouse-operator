package v1alpha1

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BackupStorage selects the storage that holds backups.
// +kubebuilder:validation:XValidation:rule="has(self.s3)",message="s3 storage must be set"
type BackupStorage struct {
	// S3 stores backups in an S3-compatible object storage.
	// +optional
	S3 *S3BackupStorage `json:"s3,omitempty"`
}

// S3BackupStorage describes an S3-compatible backup storage.
type S3BackupStorage struct {
	// Endpoint is the URL of the bucket and the prefix that holds backups, for example
	// `https://my-bucket.s3.eu-west-1.amazonaws.com/clickhouse/my-cluster`.
	// Every backup is written to its own directory under this URL.
	// +kubebuilder:validation:Pattern=`^https?://[^\s'\\]+$`
	// +kubebuilder:validation:MaxLength=2048
	Endpoint string `json:"endpoint"`

	// Credentials used to access the storage.
	// When omitted, ClickHouse uses the credentials available to the server itself,
	// such as an IAM role attached to the Pod or the node.
	// +optional
	Credentials *S3Credentials `json:"credentials,omitempty"`
}

// S3Credentials references the access key used to access an S3-compatible storage.
type S3Credentials struct {
	// AccessKeyID selects the Secret key that holds the access key ID.
	AccessKeyID SecretKeySelector `json:"accessKeyID"`
	// SecretAccessKey selects the Secret key that holds the secret access key.
	SecretAccessKey SecretKeySelector `json:"secretAccessKey"`
}

// Location returns the URL of the backup with the given name in the storage.
func (s *S3BackupStorage) Location(name string) string {
	return strings.TrimRight(s.Endpoint, "/") + "/" + name
}

// BackupScope limits a backup or a restore to a subset of the cluster data.
// When no databases and no tables are listed, everything except the `system` database is included.
type BackupScope struct {
	// Databases lists the databases to include.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:items:MinLength=1
	Databases []string `json:"databases,omitempty"`

	// Tables lists the individual tables to include.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=1024
	Tables []BackupTable `json:"tables,omitempty"`
}

// BackupTable identifies a table.
type BackupTable struct {
	// Database is the name of the database that holds the table.
	// +kubebuilder:validation:MinLength=1
	Database string `json:"database"`
	// Table is the name of the table.
	// +kubebuilder:validation:MinLength=1
	Table string `json:"table"`
}

// BackupPhase is the lifecycle phase of a backup or a restore.
// +kubebuilder:validation:Enum=Pending;Running;Completed;Failed
type BackupPhase string

const (
	// BackupPhasePending means that the operation is not started yet.
	BackupPhasePending BackupPhase = "Pending"
	// BackupPhaseRunning means that ClickHouse is executing the operation.
	BackupPhaseRunning BackupPhase = "Running"
	// BackupPhaseCompleted means that the operation finished successfully.
	BackupPhaseCompleted BackupPhase = "Completed"
	// BackupPhaseFailed means that the operation failed. Failed operations are not retried.
	BackupPhaseFailed BackupPhase = "Failed"
)

// Finished returns true if the phase is terminal.
func (p BackupPhase) Finished() bool {
	return p == BackupPhaseCompleted || p == BackupPhaseFailed
}

// BackupReplica identifies the cluster replica that executes a backup or a restore.
type BackupReplica struct {
	// ShardID is the shard of the replica.
	ShardID int32 `json:"shardID"`
	// Index is the index of the replica within the shard.
	Index int32 `json:"index"`
}

// BackupOperationStatus is the observed state shared by backups and restores.
type BackupOperationStatus struct {
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Phase is the lifecycle phase of the operation.
	// +optional
	Phase BackupPhase `json:"phase,omitempty"`

	// ID identifies the operation in the `system.backups` table of the replica that executes it.
	// +optional
	ID string `json:"id,omitempty"`

	// Replica is the cluster replica that executes the operation.
	// +optional
	Replica *BackupReplica `json:"replica,omitempty"`

	// Location is the URL of the backup in the storage.
	// +optional
	Location string `json:"location,omitempty"`

	// StartTime is the time the operation was submitted to ClickHouse.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is the time the operation finished.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
}

// ClickHouseBackupSpec defines the desired state of ClickHouseBackup.
// +kubebuilder:validation:XValidation:rule="!has(self.settings) || self.settings.all(k, !(k in ['id', 'async', 'base_backup']))",message="settings id, async and base_backup are managed by the operator"
type ClickHouseBackupSpec struct {
	// ClusterRef references the ClickHouseCluster to back up. The cluster must be in the namespace of the backup.
	ClusterRef corev1.LocalObjectReference `json:"clusterRef"`

	// Storage is the storage the backup is written to.
	// The backup is stored in the directory named after this resource.
	Storage BackupStorage `json:"storage"`

	// Scope limits the backup to the listed databases and tables.
	// When omitted, everything except the `system` database is backed up.
	// +optional
	Scope *BackupScope `json:"scope,omitempty"`

	// BaseBackupRef makes the backup incremental: only the data that is missing from the referenced
	// ClickHouseBackup is stored. The base backup must be completed and must stay in the storage
	// for as long as this backup is needed.
	// +optional
	BaseBackupRef *corev1.LocalObjectReference `json:"baseBackupRef,omitempty"`

	// Settings are additional settings passed to the BACKUP statement.
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.matches('^[A-Za-z_][A-Za-z0-9_]*$'))",message="setting names must be valid identifiers"
	Settings map[string]string `json:"settings,omitempty"`
}

// ClickHouseBackupStatus defines the observed state of ClickHouseBackup.
type ClickHouseBackupStatus struct {
	BackupOperationStatus `json:",inline"`

	// BaseLocation is the URL of the base backup of an incremental backup.
	// +optional
	BaseLocation string `json:"baseLocation,omitempty"`

	// NumFiles is the number of files stored in the backup.
	// +optional
	NumFiles int64 `json:"numFiles,omitempty"`

	// TotalSize is the size in bytes of the data covered by the backup, including the data stored in base backups.
	// +optional
	TotalSize int64 `json:"totalSize,omitempty"`

	// CompressedSize is the size in bytes the backup takes in the storage.
	// +optional
	CompressedSize int64 `json:"compressedSize,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ClickHouseBackup is the Schema for the `clickhousebackups` API.
// It runs a single backup of a ClickHouseCluster using the ClickHouse BACKUP statement.
// +kubebuilder:resource:shortName=chbackup
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Base",type="string",JSONPath=".spec.baseBackupRef.name"
// +kubebuilder:printcolumn:name="Size",type="integer",JSONPath=".status.compressedSize"
// +kubebuilder:printcolumn:name="Completed",type="date",JSONPath=".status.completionTime"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +operator-sdk:csv:customresourcedefinitions:displayName="ClickHouse Backup"
// +kubebuilder:validation:XValidation:rule="has(self.spec)",message="spec is required"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec == oldSelf.spec",message="spec is immutable"
type ClickHouseBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClickHouseBackupSpec   `json:"spec,omitempty"`
	Status ClickHouseBackupStatus `json:"status,omitempty"`
}

// IsIncremental returns true if the backup stores only the difference from a base backup.
func (v *ClickHouseBackup) IsIncremental() bool {
	return v.Spec.BaseBackupRef != nil
}

// +kubebuilder:object:root=true

// ClickHouseBackupList contains a list of ClickHouseBackup.
type ClickHouseBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ClickHouseBackup `json:"items"`
}
