package clickhouse

import (
	"fmt"

	"github.com/blang/semver/v4"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/internal/upgrade"
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

	DefaultClusterName         = "default"
	KeeperPathUsers            = "/clickhouse/access"
	KeeperPathUDF              = "/clickhouse/user_defined"
	KeeperPathDistributedDDL   = "/clickhouse/task_queue/ddl"
	KeeperPathNamedCollections = "/clickhouse/named_collections"

	ContainerName          = "clickhouse-server"
	DefaultRevisionHistory = 10
	MaximalAffinityWeight  = 100

	InterserverUserName        = "interserver"
	OperatorManagementUsername = "operator"
	DefaultProfileName         = "default"

	EnvInterserverPassword = "CLICKHOUSE_INTERSERVER_PASSWORD"
	EnvDefaultUserPassword = "CLICKHOUSE_DEFAULT_USER_PASSWORD"
	EnvKeeperIdentity      = "CLICKHOUSE_KEEPER_IDENTITY"
	EnvClusterSecret       = "CLICKHOUSE_CLUSTER_SECRET"
	EnvNamedCollectionsKey = "CLICKHOUSE_NAMED_COLLECTIONS_KEY"

	SecretKeyInterserverPassword = "interserver-password"
	SecretKeyManagementPassword  = "management-password"
	SecretKeyKeeperIdentity      = "keeper-identity"
	SecretKeyClusterSecret       = "cluster-secret"
	SecretKeyNamedCollectionsKey = "named-collections-key"

	// NamedCollectionsKeyByteLen is the AES-128 key size in bytes (16 bytes = 32 hex chars).
	NamedCollectionsKeyByteLen = 16
)

type secretSpec struct {
	Key    string
	Env    string
	Format string
	// Hint is a human-readable description of the expected value.
	Hint     string
	Generate func() any
	Enabled  func(cluster *v1.ClickHouseCluster) bool
}

func (s *secretSpec) generate() []byte {
	var arg any
	if s.Generate != nil {
		arg = s.Generate()
	} else {
		arg = controllerutil.GeneratePassword()
	}

	return fmt.Appendf(nil, s.Format, arg)
}

func (s *secretSpec) enabled(cluster *v1.ClickHouseCluster) bool {
	return s.Enabled == nil || s.Enabled(cluster)
}

var (
	// minVersionNamedCollections is the minimum ClickHouse version that supports keeper_encrypted for named collections.
	minVersionNamedCollections    = upgrade.ClickHouseVersion{Major: 25, Minor: 12} //nolint:mnd
	breakingStatefulSetVersion, _ = semver.Parse("0.0.1")
	clusterSecrets                = []secretSpec{
		{Key: SecretKeyInterserverPassword, Env: EnvInterserverPassword, Format: "%s", Hint: "plaintext password"},
		{Key: SecretKeyManagementPassword, Format: "%s", Hint: "plaintext password"},
		{Key: SecretKeyKeeperIdentity, Env: EnvKeeperIdentity, Format: "clickhouse:%s", Hint: `"clickhouse:<password>"`},
		{Key: SecretKeyClusterSecret, Env: EnvClusterSecret, Format: "%s", Hint: "plaintext password"},
		{Key: SecretKeyNamedCollectionsKey, Env: EnvNamedCollectionsKey, Format: "%x",
			Hint:     fmt.Sprintf("hex-encoded %d-byte AES key", NamedCollectionsKeyByteLen),
			Generate: func() any { return controllerutil.GenerateRandomBytes(NamedCollectionsKeyByteLen) },
			Enabled: func(cluster *v1.ClickHouseCluster) bool {
				return upgrade.VersionAtLeast(cluster.Status.Version, minVersionNamedCollections)
			},
		},
	}
)
