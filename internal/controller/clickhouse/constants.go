package clickhouse

import (
	"fmt"

	"github.com/blang/semver/v4"

	v1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/internal/upgrade"
)

// MinVersionNamedCollections is the minimum ClickHouse version that supports keeper_encrypted for named collections.
var MinVersionNamedCollections = upgrade.ClickHouseVersion{Major: 25, Minor: 12} //nolint:mnd

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

// versionAtLeast returns true if the actual version string is >= min.
// Returns false for empty, unparsable, or unknown version strings.
func versionAtLeast(actual string, minVersion upgrade.ClickHouseVersion) bool {
	v, err := upgrade.ParseBareVersion(actual)
	if err != nil {
		return false
	}

	return v.Compare(minVersion) >= 0
}

type secretSpec struct {
	Key      string
	Env      string
	Format   string
	Generate func() any
	Enabled  func(status *v1.ClickHouseCluster) bool
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
	breakingStatefulSetVersion, _ = semver.Parse("0.0.1")
	clusterSecrets                = []secretSpec{
		{Key: SecretKeyInterserverPassword, Env: EnvInterserverPassword, Format: "%s"},
		{Key: SecretKeyManagementPassword, Format: "%s"},
		{Key: SecretKeyKeeperIdentity, Env: EnvKeeperIdentity, Format: "clickhouse:%s"},
		{Key: SecretKeyClusterSecret, Env: EnvClusterSecret, Format: "%s"},
		{Key: SecretKeyNamedCollectionsKey, Env: EnvNamedCollectionsKey, Format: "%x",
			Generate: func() any { return controllerutil.GenerateRandomBytes(NamedCollectionsKeyByteLen) },
			Enabled: func(status *v1.ClickHouseCluster) bool {
				return versionAtLeast(status.Spec.ClusterDomain, MinVersionNamedCollections)
			},
		},
	}
)
