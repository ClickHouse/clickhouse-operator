package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RestoreSource locates a backup that is not described by a ClickHouseBackup resource,
// for example a backup taken in another Kubernetes cluster.
type RestoreSource struct {
	// Storage is the storage that holds the backup.
	Storage BackupStorage `json:"storage"`

	// Name is the name of the backup directory in the storage.
	// It is the name of the ClickHouseBackup resource that created the backup.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[^\s'\\]+$`
	Name string `json:"name"`
}

// ClickHouseRestoreSpec defines the desired state of ClickHouseRestore.
// +kubebuilder:validation:XValidation:rule="has(self.backupRef) != has(self.source)",message="exactly one of backupRef or source must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.settings) || self.settings.all(k, !(k in ['id', 'async', 'base_backup']))",message="settings id, async and base_backup are managed by the operator"
type ClickHouseRestoreSpec struct {
	// ClusterRef references the ClickHouseCluster to restore into. The cluster must be in the namespace of the restore.
	ClusterRef corev1.LocalObjectReference `json:"clusterRef"`

	// BackupRef references the completed ClickHouseBackup to restore from.
	// Mutually exclusive with `source`, exactly one of them must be set.
	// +optional
	BackupRef *corev1.LocalObjectReference `json:"backupRef,omitempty"`

	// Source locates the backup to restore from directly in the storage.
	// Mutually exclusive with `backupRef`, exactly one of them must be set.
	// +optional
	Source *RestoreSource `json:"source,omitempty"`

	// Scope limits the restore to the listed databases and tables.
	// When omitted, everything except the `system` database is restored.
	// +optional
	Scope *BackupScope `json:"scope,omitempty"`

	// Settings are additional settings passed to the RESTORE statement,
	// for example `allow_non_empty_tables` or `structure_only`.
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	// +kubebuilder:validation:XValidation:rule="self.all(k, k.matches('^[A-Za-z_][A-Za-z0-9_]*$'))",message="setting names must be valid identifiers"
	Settings map[string]string `json:"settings,omitempty"`
}

// ClickHouseRestoreStatus defines the observed state of ClickHouseRestore.
type ClickHouseRestoreStatus struct {
	BackupOperationStatus `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ClickHouseRestore is the Schema for the `clickhouserestores` API.
// It restores a backup into a ClickHouseCluster once using the ClickHouse RESTORE statement.
// +kubebuilder:resource:shortName=chrestore
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Backup",type="string",JSONPath=".spec.backupRef.name"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Completed",type="date",JSONPath=".status.completionTime"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +operator-sdk:csv:customresourcedefinitions:displayName="ClickHouse Restore"
// +kubebuilder:validation:XValidation:rule="has(self.spec)",message="spec is required"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.spec) || self.spec == oldSelf.spec",message="spec is immutable"
type ClickHouseRestore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClickHouseRestoreSpec   `json:"spec,omitempty"`
	Status ClickHouseRestoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClickHouseRestoreList contains a list of ClickHouseRestore.
type ClickHouseRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ClickHouseRestore `json:"items"`
}
