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

// Shared with the webhook, which cannot import the clickhouse controller package.
// The v1alpha2 CEL rules on ClickHouseNetworkSpec.AdditionalPorts hard-code the ports and names; keep them in sync.
const (
	ClickHousePortManagement     = 9001
	ClickHousePortManagementHTTP = 9002
	ClickHousePortNative         = 9000
	ClickHousePortNativeSecure   = 9440
	ClickHousePortHTTP           = 8123
	ClickHousePortHTTPSecure     = 8443

	ClickHousePortPrometheusScrape = 9363
	ClickHousePortInterserver      = 9009

	ClickHousePortNameManagement     = "management"
	ClickHousePortNameManagementHTTP = "management-http"
	ClickHousePortNameTCP            = "tcp"
	ClickHousePortNameTCPSecure      = "tcp-secure"
	ClickHousePortNameHTTP           = "http"
	ClickHousePortNameHTTPSecure     = "http-secure"

	ClickHousePortNamePrometheus  = "prometheus"
	ClickHousePortNameInterserver = "interserver"

	ClickHouseDefaultDiskName = "default"
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
