package clickhouse

import (
	"time"

	"github.com/blang/semver/v4"
)

const (
	RequeueOnRefreshTimeout = time.Second * 1
	RequeueOnErrorTimeout   = time.Second * 5

	PortManagement   = 9001
	PortNative       = 9000
	PortNativeSecure = 9440
	PortHTTP         = 8123
	PortHTTPSecure   = 8443

	PortPrometheusScrape = 9363
	PortInterserver      = 9009

	ConfigPath           = "/etc/clickhouse-server/"
	ConfigDPath          = "config.d"
	ConfigFileName       = "config.yaml"
	UsersFileName        = "users.yaml"
	ExtraConfigFileName  = "99-extra-config.yaml"
	ClientConfigPath     = "/etc/clickhouse-client/"
	ClientConfigFileName = "config.yaml"

	PersistentVolumeName = "clickhouse-storage-volume"

	TLSConfigPath       = "/etc/clickhouse-server/tls/"
	CABundleFilename    = "ca-bundle.crt"
	CertificateFilename = "clickhouse-server.crt"
	KeyFilename         = "clickhouse-server.key"
	TLSVolumeName       = "clickhouse-server-tls-volume"
	CustomCAFilename    = "custom-ca.crt"
	CustomCAVolumeName  = "clickhouse-server-custom-ca-volume"

	LogPath      = "/var/log/clickhouse-server/"
	BaseDataPath = "/var/lib/clickhouse/"

	DefaultClusterName       = "default"
	KeeperPathUsers          = "/clickhouse/access"
	KeeperPathDiscovery      = "/clickhouse/discovery/default"
	KeeperPathUDF            = "/clickhouse/user_defined"
	KeeperPathDistributedDDL = "/clickhouse/task_queue/ddl"

	ContainerName          = "clickhouse-server"
	DefaultRevisionHistory = 10

	InterserverUserName        = "interserver"
	OperatorManagementUsername = "operator"
	DefaultProfileName         = "default"

	EnvInterserverPassword = "CLICKHOUSE_INTERSERVER_PASSWORD"
	EnvDefaultUserPassword = "CLICKHOUSE_DEFAULT_USER_PASSWORD"
	EnvKeeperIdentity      = "CLICKHOUSE_KEEPER_IDENTITY"

	SecretKeyInterserverPassword = "interserver-password"
	SecretKeyManagementPassword  = "management-password"
	SecretKeyKeeperIdentity      = "keeper-identity"
)

var (
	BreakingStatefulSetVersion, _       = semver.Parse("0.0.1")
	TLSFileMode                   int32 = 0444
	SecretsToGenerate                   = map[string]string{
		SecretKeyInterserverPassword: "%s",
		SecretKeyManagementPassword:  "%s",
		SecretKeyKeeperIdentity:      "clickhouse:%s",
	}
	ReservedVolumeNames = []string{
		PersistentVolumeName,
		TLSVolumeName,
		CustomCAVolumeName,
	}
)
