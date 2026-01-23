package v1alpha1

type ConditionType string
type ConditionReason string

// Common condition types and reasons for all resources.
const (
	// ConditionTypeSpecValid indicates that the Custom Resource passes validation.
	ConditionTypeSpecValid     ConditionType   = "SpecValid"
	ConditionReasonSpecInvalid ConditionReason = "SpecInvalid"
	ConditionReasonSpecValid   ConditionReason = "SpecValid"

	// ConditionTypeReconcileSucceeded indicates that latest reconciliation was successful.
	ConditionTypeReconcileSucceeded  ConditionType   = "ReconcileSucceeded"
	ConditionReasonStepFailed        ConditionReason = "ReconcileStepFailed"
	ConditionReasonReconcileFinished ConditionReason = "ReconcileFinished"

	// ConditionTypeReplicaStartupSucceeded indicates that all replicas of the cluster are able to start.
	ConditionTypeReplicaStartupSucceeded ConditionType   = "ReplicaStartupSucceeded"
	ConditionReasonReplicasRunning       ConditionReason = "ReplicasRunning"
	ConditionReasonReplicaError          ConditionReason = "ReplicaError"

	// ConditionTypeHealthy indicates that all replicas of the cluster are ready to accept connections.
	ConditionTypeHealthy            ConditionType   = "Healthy"
	ConditionReasonReplicasReady    ConditionReason = "ReplicasReady"
	ConditionReasonReplicasNotReady ConditionReason = "ReplicasNotReady"

	// ConditionTypeClusterSizeAligned indicates that cluster replica amount matches the requested value.
	ConditionTypeClusterSizeAligned ConditionType   = "ClusterSizeAligned"
	ConditionReasonUpToDate         ConditionReason = "UpToDate"
	ConditionReasonScalingDown      ConditionReason = "ScalingDown"
	ConditionReasonScalingUp        ConditionReason = "ScalingUp"

	// ConditionTypeConfigurationInSync indicates that cluster configuration is in desired state.
	ConditionTypeConfigurationInSync    ConditionType   = "ConfigurationInSync"
	ConditionReasonConfigurationChanged ConditionReason = "ConfigurationChanged"

	// ConditionTypeReady indicates that cluster is ready to serve client requests.
	ConditionTypeReady                      ConditionType   = "Ready"
	ClickHouseConditionAllShardsReady       ConditionReason = "AllShardsReady"
	ClickHouseConditionSomeShardsNotReady   ConditionReason = "SomeShardsNotReady"
	KeeperConditionReasonStandaloneReady    ConditionReason = "StandaloneReady"
	KeeperConditionReasonClusterReady       ConditionReason = "ClusterReady"
	KeeperConditionReasonNoLeader           ConditionReason = "NoLeader"
	KeeperConditionReasonInconsistentState  ConditionReason = "InconsistentState"
	KeeperConditionReasonNotEnoughFollowers ConditionReason = "NotEnoughFollowers"
)

// ClickHouseCluster specific condition types and reasons.
const (
	// ClickHouseConditionTypeSchemaInSync indicates that databases were created on all new replicas and deleted
	// replicas metadata was removed. This condition indicates that newly created replicas are ready to use or cluster
	// should operate normally after scale down, but it does not mean that all replicas have the same schema.
	ClickHouseConditionTypeSchemaInSync ConditionType = "SchemaInSync"

	ClickHouseConditionSchemaSyncDisabled   ConditionReason = "SchemaSyncDisabled"
	ClickHouseConditionReplicasInSync       ConditionReason = "ReplicasInSync"
	ClickHouseConditionDatabasesNotCreated  ConditionReason = "DatabasesNotCreated"
	ClickHouseConditionReplicasNotCleanedUp ConditionReason = "ReplicasNotCleanedUp"
)

// KeeperCluster specific condition types and reasons.
const (
	// KeeperConditionTypeScaleAllowed indicates that cluster is ready to change quorum size.
	KeeperConditionTypeScaleAllowed ConditionType = "ScaleAllowed"

	KeeperConditionReasonReplicaHasPendingChanges ConditionReason = "ReplicaHasPendingChanges"
	KeeperConditionReasonReplicaNotReady          ConditionReason = "ReplicaNotReady"
	KeeperConditionReasonNoQuorum                 ConditionReason = "NoQuorum"
	KeeperConditionReasonWaitingFollowers         ConditionReason = "WaitingFollowers"
	KeeperConditionReasonReadyToScale             ConditionReason = "ReadyToScale"
)

var (
	AllClickHouseConditionTypes = []ConditionType{
		ConditionTypeSpecValid,
		ConditionTypeReconcileSucceeded,
		ConditionTypeReplicaStartupSucceeded,
		ConditionTypeHealthy,
		ConditionTypeClusterSizeAligned,
		ConditionTypeConfigurationInSync,
		ConditionTypeReady,
		ClickHouseConditionTypeSchemaInSync,
	}

	AllKeeperConditionTypes = []ConditionType{
		ConditionTypeSpecValid,
		ConditionTypeReconcileSucceeded,
		ConditionTypeReplicaStartupSucceeded,
		ConditionTypeHealthy,
		ConditionTypeClusterSizeAligned,
		ConditionTypeConfigurationInSync,
		ConditionTypeReady,
	}
)
