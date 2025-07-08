package util

// Contains common labels keys and helpers to work with.
const (
	LabelAppKey         = "app"
	LabelKindKey        = "kind"
	LabelRoleKey        = "role"
	LabelAppK8sKey      = "app.kubernetes.io/name"
	LabelInstanceK8sKey = "app.kubernetes.io/instance"

	LabelKeeperReplicaID = "clickhouse.com/keeper-replica-id"

	LabelClickHouseShardID   = "clickhouse.com/shard-id"
	LabelClickHouseReplicaID = "clickhouse.com/replica-id"
)

const (
	LabelKeeperValue       = "clickhouse-keeper"
	LabelKeeperAllReplicas = "all-replicas"

	LabelClickHouseValue = "clickhouse-server"
)
