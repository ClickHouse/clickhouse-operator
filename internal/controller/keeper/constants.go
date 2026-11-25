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

	PortNameNative           = "keeper"
	PortNameNativeSecure     = "keeper-secure"
	PortNamePrometheusScrape = "prometheus"
	PortNameInterserver      = "raft-ipc"
	PortNameHTTPControl      = "http-control"

	QuorumConfigPath     = "/etc/clickhouse-keeper/"
	QuorumConfigFileName = "config.yaml"

	ConfigPath          = QuorumConfigPath + "config.d/"
	ConfigFileName      = "00-config.yaml"
	ExtraConfigFileName = "99-extra-config.yaml"

	TLSConfigPath       = "/etc/clickhouse-keeper/tls/"
	CertificateFilename = "clickhouse-keeper.crt"
	KeyFilename         = "clickhouse-keeper.key"

	LogPath = "/var/log/clickhouse-keeper/"

	StorageLogPath      = internal.KeeperDataPath + "/coordination/log/"
	StorageSnapshotPath = internal.KeeperDataPath + "/coordination/snapshots/"

	ContainerName = "clickhouse-keeper"

	EnvConfig = "KEEPER_CONFIG"
)

// preStopPollAttempts and preStopPollInterval bound how long the PreStop hook waits for
// the outgoing leader to hand off Raft leadership, and for a new leader to be elected,
// before the kubelet is allowed to send SIGTERM. 10 * 0.5s = 5s, comfortably inside the
// pod's default 30s terminationGracePeriodSeconds. This cap only matters when http_control has stopped
// answering entirely (a hung server): a healthy handover finishes in well under a
// second even under heavy write load, and a server that answers with a non-leader role
// or an error exits the loop immediately.
const (
	preStopPollAttempts = 10
	preStopPollInterval = "0.5" // seconds, passed to `sleep`
)

var breakingStatefulSetVersion, _ = semver.Parse("0.0.1")
