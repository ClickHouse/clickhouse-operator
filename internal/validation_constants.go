package internal

const (
	PersistentVolumeName = "clickhouse-storage-volume"
	TLSVolumeName        = "clickhouse-server-tls-volume"
	CustomCAVolumeName   = "clickhouse-server-custom-ca-volume"

	QuorumConfigVolumeName = "clickhouse-keeper-quorum-config-volume"
	ConfigVolumeName       = "clickhouse-keeper-config-volume"

	KeeperDataPath     = "/var/lib/clickhouse"
	ClickHouseDataPath = "/var/lib/clickhouse"
)

// The volume names below are also hard-coded in the v1alpha2 CEL rules on KeeperClusterSpec (Keeper reserved
// names) and NamedVolumeClaimTemplateSet.Templates (PersistentVolumeName); keep them in sync.
var (
	// ReservedClickHouseVolumeNames list of reserved volume names for ClickHouse pods.
	ReservedClickHouseVolumeNames = []string{
		PersistentVolumeName,
		TLSVolumeName,
		CustomCAVolumeName,
	}

	// ReservedKeeperVolumeNames list of reserved volume names for ClickHouse Keeper pods.
	ReservedKeeperVolumeNames = []string{
		QuorumConfigVolumeName,
		PersistentVolumeName,
		ConfigVolumeName,
		TLSVolumeName,
	}
)
