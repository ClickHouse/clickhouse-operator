package keeper

import (
	"time"

	"github.com/blang/semver/v4"
)

const (
	RequeueOnRefreshTimeout = time.Second * 1
	RequeueOnErrorTimeout   = time.Second * 5
	StatusRequestTimeout    = time.Second * 10

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

	BaseDataPath        = "/var/lib/clickhouse/"
	StorageLogPath      = BaseDataPath + "coordination/log/"
	StorageSnapshotPath = BaseDataPath + "coordination/snapshots/"

	ContainerName          = "clickhouse-keeper"
	DefaultRevisionHistory = 10
)

var (
	BreakingStatefulSetVersion, _       = semver.Parse("0.0.1")
	TLSFileMode                   int32 = 0444
)
