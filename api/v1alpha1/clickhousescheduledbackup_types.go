package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClickHouseBackupTemplate describes the ClickHouseBackup resources created by a ClickHouseScheduledBackup.
type ClickHouseBackupTemplate struct {
	// Spec of the created backups. `baseBackupRef` is managed by the schedule and must not be set.
	// +kubebuilder:validation:XValidation:rule="!has(self.baseBackupRef)",message="baseBackupRef is managed by the schedule"
	Spec ClickHouseBackupSpec `json:"spec"`
}

// ClickHouseScheduledBackupSpec defines the desired state of ClickHouseScheduledBackup.
type ClickHouseScheduledBackupSpec struct {
	// Schedule in the cron format, see https://en.wikipedia.org/wiki/Cron.
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`

	// TimeZone is the IANA time zone name the schedule is evaluated in. Defaults to UTC.
	// +optional
	TimeZone *string `json:"timeZone,omitempty"`

	// Suspend stops the creation of new backups. Backups that are already running are not affected.
	// +optional
	Suspend *bool `json:"suspend,omitempty"`

	// FullBackupInterval makes the schedule take incremental backups: every full backup is followed by
	// `fullBackupInterval - 1` incremental backups based on it.
	// When unset or 1, every backup is a full backup.
	// +optional
	// +kubebuilder:validation:Minimum=1
	FullBackupInterval *int32 `json:"fullBackupInterval,omitempty"`

	// SuccessfulBackupsHistoryLimit is the number of completed ClickHouseBackup resources to keep.
	// The most recent full backup and the base backups of the kept incremental backups are always kept.
	// Deleting a ClickHouseBackup resource does not delete the backup data from the storage.
	// +optional
	// +kubebuilder:default:=10
	// +kubebuilder:validation:Minimum=1
	SuccessfulBackupsHistoryLimit *int32 `json:"successfulBackupsHistoryLimit,omitempty"`

	// FailedBackupsHistoryLimit is the number of failed ClickHouseBackup resources to keep.
	// +optional
	// +kubebuilder:default:=3
	// +kubebuilder:validation:Minimum=0
	FailedBackupsHistoryLimit *int32 `json:"failedBackupsHistoryLimit,omitempty"`

	// Template describes the backups created by the schedule.
	Template ClickHouseBackupTemplate `json:"template"`
}

// ClickHouseScheduledBackupStatus defines the observed state of ClickHouseScheduledBackup.
type ClickHouseScheduledBackupStatus struct {
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastScheduleTime is the schedule time of the most recent backup created by the schedule.
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`

	// LastBackupName is the name of the most recent ClickHouseBackup created by the schedule.
	// +optional
	LastBackupName string `json:"lastBackupName,omitempty"`

	// LastSuccessfulTime is the completion time of the most recent successful backup.
	// +optional
	LastSuccessfulTime *metav1.Time `json:"lastSuccessfulTime,omitempty"`

	// LastSuccessfulBackupName is the name of the most recent successful ClickHouseBackup.
	// +optional
	LastSuccessfulBackupName string `json:"lastSuccessfulBackupName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ClickHouseScheduledBackup is the Schema for the `clickhousescheduledbackups` API.
// It creates ClickHouseBackup resources on a schedule.
// +kubebuilder:resource:shortName=chscheduledbackup
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.template.spec.clusterRef.name"
// +kubebuilder:printcolumn:name="Schedule",type="string",JSONPath=".spec.schedule"
// +kubebuilder:printcolumn:name="Suspend",type="boolean",JSONPath=".spec.suspend"
// +kubebuilder:printcolumn:name="LastSchedule",type="date",JSONPath=".status.lastScheduleTime"
// +kubebuilder:printcolumn:name="LastSuccessful",type="date",JSONPath=".status.lastSuccessfulTime"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +operator-sdk:csv:customresourcedefinitions:displayName="ClickHouse Scheduled Backup"
// +operator-sdk:csv:customresourcedefinitions:resources={{ClickHouseBackup,v1alpha1}}
// +kubebuilder:validation:XValidation:rule="has(self.spec)",message="spec is required"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 48",message="name must be no more than 48 characters"
type ClickHouseScheduledBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClickHouseScheduledBackupSpec   `json:"spec,omitempty"`
	Status ClickHouseScheduledBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClickHouseScheduledBackupList contains a list of ClickHouseScheduledBackup.
type ClickHouseScheduledBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ClickHouseScheduledBackup `json:"items"`
}
