package keeper

import (
	"github.com/blang/semver/v4"

	"github.com/ClickHouse/clickhouse-operator/internal"
)

const (
	PortNative           = 2181
	PortNativeSecure     = 2281
	PortPrometheusScrape = 9090
	PortInterserver      = 9234
	PortHTTPControl      = 9123

	QuorumConfigPath     = "/etc/clickhouse-keeper/"
	QuorumConfigFileName = "config.yaml"

	ConfigPath     = QuorumConfigPath + "config.d/"
	ConfigFileName = "00-config.yaml"

	TLSConfigPath       = "/etc/clickhouse-keeper/tls/"
	CABundleFilename    = "ca-bundle.crt"
	CertificateFilename = "clickhouse-keeper.crt"
	KeyFilename         = "clickhouse-keeper.key"

	LogPath = "/var/log/clickhouse-keeper/"

	StorageLogPath      = internal.KeeperDataPath + "/coordination/log/"
	StorageSnapshotPath = internal.KeeperDataPath + "/coordination/snapshots/"

	ContainerName          = "clickhouse-keeper"
	DefaultRevisionHistory = 10
)

var breakingStatefulSetVersion, _ = semver.Parse("0.0.1")
