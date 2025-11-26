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
	// ClickHouseConditionTypeSchemaInSync indicates that databases were created on all new replicas and deleted
	// replicas metadata was removed. This condition indicates that newly created replicas are ready to use or cluster
	// should operate normally after scale down, but it does not mean that all replicas have the same schema.
	ClickHouseConditionTypeSchemaInSync ConditionType = "SchemaInSync"
)

var (
	AllClickHouseConditionTypes = []ConditionType{
		ConditionTypeSpecValid,
		ClickHouseConditionTypeReconcileSucceeded,
		ClickHouseConditionTypeReplicaStartupSucceeded,
		ClickHouseConditionTypeHealthy,
		ClickHouseConditionTypeClusterSizeAligned,
		ClickHouseConditionTypeConfigurationInSync,
		ClickHouseConditionTypeReady,
		ClickHouseConditionTypeSchemaInSync,
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

	ClickHouseConditionSchemaSyncDisabled   ConditionReason = "SchemaSyncDisabled"
	ClickHouseConditionReplicasInSync       ConditionReason = "ReplicasInSync"
	ClickHouseConditionDatabasesNotCreated  ConditionReason = "DatabasesNotCreated"
	ClickHouseConditionReplicasNotCleanedUp ConditionReason = "ReplicasNotCleanedUp"
)
