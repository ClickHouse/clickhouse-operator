package internal

const (
	PersistentVolumeName = "clickhouse-storage-volume"
	TLSVolumeName        = "clickhouse-server-tls-volume"
	CustomCAVolumeName   = "clickhouse-server-custom-ca-volume"

	QuorumConfigVolumeName = "clickhouse-keeper-quorum-config-volume"
	ConfigVolumeName       = "clickhouse-keeper-config-volume"
)

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
