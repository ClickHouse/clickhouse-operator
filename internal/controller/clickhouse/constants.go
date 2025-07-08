package clickhouse

import (
	"time"

	"github.com/blang/semver/v4"
)

const (
	RequeueOnRefreshTimeout = time.Second * 1
	RequeueOnErrorTimeout   = time.Second * 5
	StatusRequestTimeout    = time.Second * 10

	PortNative       = 9000
	PortNativeSecure = 9440
	PortHTTP         = 8123
	PortHTTPSecure   = 8443

	PortPrometheusScrape = 9363
	PortInterserver      = 9009

	ConfigPath       = "/etc/clickhouse-server/"
	ConfigFileName   = "config.yaml"
	UsersFileName    = "users.yaml"
	ConfigVolumeName = "clickhouse-config-volume"

	PersistentVolumeName = "clickhouse-storage-volume"

	TLSConfigPath       = "/etc/clickhouse-server/tls/"
	CABundleFilename    = "ca-bundle.crt"
	CertificateFilename = "clickhouse-server.crt"
	KeyFilename         = "clickhouse-server.key"
	TLSVolumeName       = "clickhouse-server-tls-volume"

	LogPath      = "/var/log/clickhouse-server/"
	BaseDataPath = "/var/lib/clickhouse/"

	DefaultClusterName       = "default"
	KeeperPathUsers          = "/clickhouse/access"
	KeeperPathDiscovery      = "/clickhouse/discovery/default"
	KeeperPathUDF            = "/clickhouse/user_defined"
	KeeperPathDistributedDDL = "/clickhouse/task_queue/ddl"

	ContainerName          = "clickhouse-server"
	DefaultRevisionHistory = 10

	DefaultProfileName = "default"
)

var (
	BreakingStatefulSetVersion, _       = semver.Parse("0.0.1")
	TLSFileMode                   int32 = 0444
)
