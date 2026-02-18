package clickhouse

import (
	"github.com/blang/semver/v4"
)

const (
	PortManagement   = 9001
	PortNative       = 9000
	PortNativeSecure = 9440
	PortHTTP         = 8123
	PortHTTPSecure   = 8443

	PortPrometheusScrape = 9363
	PortInterserver      = 9009

	ConfigPath               = "/etc/clickhouse-server/"
	ConfigDPath              = "config.d"
	ConfigFileName           = "config.yaml"
	UsersDPath               = "users.d"
	UsersFileName            = "users.yaml"
	ExtraConfigFileName      = "99-extra-config.yaml"
	ExtraUsersConfigFileName = "99-extra-users-config.yaml"
	ClientConfigPath         = "/etc/clickhouse-client/"
	ClientConfigFileName     = "config.yaml"

	TLSConfigPath       = "/etc/clickhouse-server/tls/"
	CABundleFilename    = "ca-bundle.crt"
	CertificateFilename = "clickhouse-server.crt"
	KeyFilename         = "clickhouse-server.key"
	CustomCAFilename    = "custom-ca.crt"

	LogPath = "/var/log/clickhouse-server/"

	DefaultClusterName       = "default"
	KeeperPathUsers          = "/clickhouse/access"
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
	EnvClusterSecret       = "CLICKHOUSE_CLUSTER_SECRET"

	SecretKeyInterserverPassword = "interserver-password"
	SecretKeyManagementPassword  = "management-password"
	SecretKeyKeeperIdentity      = "keeper-identity"
	SecretKeyClusterSecret       = "cluster-secret"
)

var (
	breakingStatefulSetVersion, _ = semver.Parse("0.0.1")
	secretsToGenerate             = map[string]string{
		SecretKeyInterserverPassword: "%s",
		SecretKeyManagementPassword:  "%s",
		SecretKeyKeeperIdentity:      "clickhouse:%s",
		SecretKeyClusterSecret:       "%s",
	}
	secretsToEnvMapping = []struct {
		Key string
		Env string
	}{
		{Key: SecretKeyInterserverPassword, Env: EnvInterserverPassword},
		{Key: SecretKeyKeeperIdentity, Env: EnvKeeperIdentity},
		{Key: SecretKeyClusterSecret, Env: EnvClusterSecret},
	}
)
