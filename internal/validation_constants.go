package internal

const (
	PersistentVolumeName = "clickhouse-storage-volume"
	TLSVolumeName        = "clickhouse-server-tls-volume"
	CustomCAVolumeName   = "clickhouse-server-custom-ca-volume"

	QuorumConfigVolumeName = "clickhouse-keeper-quorum-config-volume"
	ConfigVolumeName       = "clickhouse-keeper-config-volume"
)

var (
	ReservedClickHouseVolumeNames = []string{
		PersistentVolumeName,
		TLSVolumeName,
		CustomCAVolumeName,
	}

	ReservedKeeperVolumeNames = []string{
		QuorumConfigVolumeName,
		PersistentVolumeName,
		ConfigVolumeName,
		TLSVolumeName,
	}
)
