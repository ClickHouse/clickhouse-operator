package clickhouse

import (
	"github.com/clickhouse-operator/internal/controller"
)

// Config is a root server ClickHouse server configuration.
type Config struct {
	Path                       string                       `yaml:"path"`
	ListenHost                 string                       `yaml:"listen_host"`
	Logger                     controller.LoggerConfig      `yaml:"logger"`
	Protocols                  map[string]Protocol          `yaml:"protocols"`
	OpenSSL                    controller.OpenSSLConfig     `yaml:"openSSL"`
	UserDirectories            map[string]map[string]string `yaml:"user_directories,omitempty"`
	Macros                     map[string]string            `yaml:"macros,omitempty"`
	RemoteServers              map[string]RemoteCluster     `yaml:"remote_servers"`
	DistributedDDL             map[string]string            `yaml:"distributed_ddl"`
	ZooKeeper                  ZooKeeper                    `yaml:"zookeeper,omitempty"`
	UserDefinedZookeeperPath   string                       `yaml:"user_defined_zookeeper_path"`
	InterserverHTTPCredentials map[string]any               `yaml:"interserver_http_credentials"`
	// TODO log tables
	// TODO merge tree settings, named collections, engines (kafka/rocksdb/etc)

	// Special settings, needed for base cluster
	AllowExperimentalClusterDiscovery bool `yaml:"allow_experimental_cluster_discovery"`
}

type Protocol struct {
	Type        string `yaml:"type"`
	Port        uint16 `yaml:"port,omitempty"`
	Impl        string `yaml:"impl,omitempty"`
	Description string `yaml:"description,omitempty"`
}

type RemoteCluster struct {
	Discovery ClusterDiscovery `yaml:"discovery"`
}

type ClusterDiscovery struct {
	Path  string `yaml:"path"`
	Shard int32  `yaml:"shard"`
}

type KeeperNode struct {
	Host   string `yaml:"host"`
	Port   int32  `yaml:"port"`
	Secure int32  `yaml:"secure,omitempty"` // 0 for insecure, 1 for secure
}

type ZooKeeper struct {
	Nodes    []KeeperNode `yaml:"node"`
	Identity EnvVal
}
type EnvVal struct {
	FromEnv string `yaml:"@from_env"`
}

type querySpec struct {
	Query string `yaml:"query"`
}

type User struct {
	PasswordSha256 string      `yaml:"password_sha256_hex,omitempty"`
	Password       EnvVal      `yaml:"password,omitempty"`
	NoPassword     *struct{}   `yaml:"no_password,omitempty"`
	Profile        string      `yaml:"profile,omitempty"`
	Quota          string      `yaml:"quota,omitempty"`
	Grants         []querySpec `yaml:"grants,omitempty"`
	// TODO add user settings
}

type Profile struct {
	// TODO add profile settings
}

type Quota struct {
	// TODO add quota settings
}

type UserConfig struct {
	Users    map[string]User    `yaml:"users"`
	Profiles map[string]Profile `yaml:"profiles"`
	Quotas   map[string]Quota   `yaml:"quotas"`
}
