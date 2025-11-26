package v1alpha1

const (
	// KeeperConditionTypeReconcileSucceeded indicates that latest reconciliation was successful.
	KeeperConditionTypeReconcileSucceeded ConditionType = "ReconcileSucceeded"
	// KeeperConditionTypeReplicaStartupSucceeded indicates that all replicas of the KeeperCluster are able to start.
	KeeperConditionTypeReplicaStartupSucceeded ConditionType = "ReplicaStartupSucceeded"
	// KeeperConditionTypeHealthy indicates that all replicas of the KeeperCluster are ready to accept connections.
	KeeperConditionTypeHealthy = "Healthy"
	// KeeperConditionTypeClusterSizeAligned indicates that KeeperCluster replica amount matches the requested value.
	KeeperConditionTypeClusterSizeAligned ConditionType = "ClusterSizeAligned"
	// KeeperConditionTypeConfigurationInSync indicates that KeeperCluster configuration is in desired state.
	KeeperConditionTypeConfigurationInSync ConditionType = "ConfigurationInSync"
	// KeeperConditionTypeReady indicates that KeeperCluster is ready to serve client requests.
	KeeperConditionTypeReady ConditionType = "Ready"
)

var (
	AllKeeperConditionTypes = []ConditionType{
		ConditionTypeSpecValid,
		KeeperConditionTypeReconcileSucceeded,
		KeeperConditionTypeReplicaStartupSucceeded,
		KeeperConditionTypeHealthy,
		KeeperConditionTypeClusterSizeAligned,
		KeeperConditionTypeConfigurationInSync,
		KeeperConditionTypeReady,
	}
)

const (
	KeeperConditionReasonStepFailed        ConditionReason = "ReconcileStepFailed"
	KeeperConditionReasonReconcileFinished ConditionReason = "ReconcileFinished"

	KeeperConditionReasonReplicasRunning ConditionReason = "ReplicasRunning"
	KeeperConditionReasonReplicaError    ConditionReason = "ReplicaError"

	KeeperConditionReasonReplicasReady    ConditionReason = "ReplicasReady"
	KeeperConditionReasonReplicasNotReady ConditionReason = "ReplicasNotReady"

	KeeperConditionReasonUpToDate             ConditionReason = "UpToDate"
	KeeperConditionReasonScalingDown          ConditionReason = "ScalingDown"
	KeeperConditionReasonScalingUp            ConditionReason = "ScalingUp"
	KeeperConditionReasonConfigurationChanged ConditionReason = "ConfigurationChanged"

	KeeperConditionReasonStandaloneReady    ConditionReason = "StandaloneReady"
	KeeperConditionReasonClusterReady       ConditionReason = "ClusterReady"
	KeeperConditionReasonNoLeader           ConditionReason = "NoLeader"
	KeeperConditionReasonInconsistentState  ConditionReason = "InconsistentState"
	KeeperConditionReasonNotEnoughFollowers ConditionReason = "NotEnoughFollowers"
)
