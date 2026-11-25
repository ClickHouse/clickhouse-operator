package controller

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	RequeueOnRefreshTimeout       = time.Second
	RequeueProbePoll              = 5 * time.Second
	LoadReplicaStateTimeout       = 10 * time.Second
	TLSFileMode             int32 = 0o444
	DefaultUser             int64 = 101
)

// Service port appProtocol values, read by service meshes instead of sniffing the traffic.
const (
	AppProtocolHTTP  = "http"
	AppProtocolHTTPS = "https"
	AppProtocolTCP   = "tcp"
)

const (
	DefaultRevisionHistory = 10
	MaximalAffinityWeight  = 100
)

const (
	PersistentVolumeDataSubPath = "var-lib-clickhouse"
	PersistentVolumeLogSubPath  = "var-log-clickhouse"
)

const (
	OpenSSLVerificationModeRelaxed = "relaxed"
	OpenSSLDisabledProtocols       = "sslv2,sslv3"
)

// KeeperClusterReferenceField indexes ClickHouseClusters by their referenced KeeperCluster "namespace/name".
// Registered by the ClickHouse controller setup; the Keeper controller requires it on the same manager.
const KeeperClusterReferenceField = "clickhouse.com/keeperClusterReference"

const (
	DefaultProbeCPULimit        = "1"
	DefaultProbeCPURequest      = "250m"
	DefaultProbeMemoryLimit     = "256Mi"
	DefaultProbeMemoryRequest   = "256Mi"
	versionProbeBinary          = "/usr/bin/clickhouse"
	versionProbeQuery           = "INSERT INTO FUNCTION file('/dev/termination-log', 'RawBLOB', 'version String') SELECT version()"
	versionProbeBackoffLimit    = int32(4)
	versionProbeDeadline        = int64(300)
	versionProbeRetryAnnotation = "clickhouse.com/version-probe-retry"
	versionProbeRetryMaxDelay   = 32 * time.Minute
	versionProbeTmpVolumeName   = "clickhouse-version-probe-tmp-volume"
	versionProbeTmpPath         = "/tmp"
	versionProbeWorkPath        = "/tmp/clickhouse-local"
)

// versionProbeRetryBackoff is a backoff config for failed version probe jobs recreation.
var versionProbeRetryBackoff = wait.Backoff{
	Duration: time.Minute,
	Factor:   2,
	Cap:      versionProbeRetryMaxDelay,
}

var (
	// DefaultLivenessProbeSettings defines default settings for Kubernetes liveness probes.
	//nolint:mnd // Magic numbers are used as constants.
	DefaultLivenessProbeSettings = corev1.Probe{
		InitialDelaySeconds: 60,
		TimeoutSeconds:      10,
		PeriodSeconds:       5,
		FailureThreshold:    10,
		SuccessThreshold:    1,
	}

	// DefaultReadinessProbeSettings defines default settings for Kubernetes readiness probes.
	//nolint:mnd // Magic numbers are used as constants.
	DefaultReadinessProbeSettings = corev1.Probe{
		InitialDelaySeconds: 5,
		TimeoutSeconds:      10,
		PeriodSeconds:       1,
		SuccessThreshold:    5,
		FailureThreshold:    10,
	}
)
