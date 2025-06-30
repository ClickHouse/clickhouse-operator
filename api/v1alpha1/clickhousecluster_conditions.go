package v1alpha1

const (
	// ClickHouseConditionTypeReconcileSucceeded indicates that latest reconciliation was successful.
	ClickHouseConditionTypeReconcileSucceeded ConditionType = "ReconcileSucceeded"
	// ClickHouseConditionTypeReplicaStartupSucceeded indicates that all replicas of the ClickHouseCluster are able to start.
	ClickHouseConditionTypeReplicaStartupSucceeded ConditionType = "ReplicaStartupSucceeded"
	// ClickHouseConditionTypeHealthy indicates that all replicas of the ClickHouseCluster are ready to accept connections.
	ClickHouseConditionTypeHealthy = "Healthy"
	// ClickHouseConditionTypeClusterSizeAligned indicates that ClickHouseCluster replica amount matches the requested value.
	ClickHouseConditionTypeClusterSizeAligned ConditionType = "ClusterSizeAligned"
	// ClickHouseConditionTypeConfigurationInSync indicates that ClickHouseCluster configuration is in desired state.
	ClickHouseConditionTypeConfigurationInSync ConditionType = "ConfigurationInSync"
	// ClickHouseConditionTypeReady indicates that ClickHouseCluster is ready to serve client requests.
	ClickHouseConditionTypeReady ConditionType = "Ready"
)

var (
	AllClickHouseConditionTypes = []ConditionType{
		ClickHouseConditionTypeReconcileSucceeded,
		ClickHouseConditionTypeReplicaStartupSucceeded,
		ClickHouseConditionTypeHealthy,
		ClickHouseConditionTypeClusterSizeAligned,
		ClickHouseConditionTypeConfigurationInSync,
		ClickHouseConditionTypeReady,
	}
)

const (
	ClickHouseConditionReasonStepFailed        ConditionReason = "ReconcileStepFailed"
	ClickHouseConditionReasonReconcileFinished ConditionReason = "ReconcileFinished"

	ClickHouseConditionReasonReplicasRunning ConditionReason = "ReplicasRunning"
	ClickHouseConditionReasonReplicaError    ConditionReason = "ReplicaError"

	ClickHouseConditionReasonReplicasReady    ConditionReason = "ReplicasReady"
	ClickHouseConditionReasonReplicasNotReady ConditionReason = "ReplicasNotReady"

	ClickHouseConditionReasonUpToDate             ConditionReason = "UpToDate"
	ClickHouseConditionReasonScalingDown          ConditionReason = "ScalingDown"
	ClickHouseConditionReasonScalingUp            ConditionReason = "ScalingUp"
	ClickHouseConditionReasonConfigurationChanged ConditionReason = "ConfigurationChanged"

	ClickHouseConditionAllShardsReady     ConditionReason = "AllShardsReady"
	ClickHouseConditionSomeShardsNotReady ConditionReason = "SomeShardsNotReady"
)
