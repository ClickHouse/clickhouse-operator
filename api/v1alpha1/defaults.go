package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
)

const (
	DefaultKeeperContainerRepository = "docker.io/clickhouse/clickhouse-keeper"
	DefaultKeeperContainerTag        = "latest"
	DefaultKeeperContainerPolicy     = "IfNotPresent"

	DefaultKeeperCPULimit      = "1"
	DefaultKeeperCPURequest    = "250m"
	DefaultKeeperMemoryLimit   = "512Mi"
	DefaultKeeperMemoryRequest = "512Mi"

	DefaultKeeperReplicaCount = 3

	DefaultClickHouseContainerRepository = "docker.io/clickhouse/clickhouse-server"
	DefaultClickHouseContainerTag        = "latest"
	DefaultClickHouseContainerPolicy     = "IfNotPresent"

	DefaultClickHouseCPULimit      = "1"
	DefaultClickHouseCPURequest    = "250m"
	DefaultClickHouseMemoryLimit   = "512Mi"
	DefaultClickHouseMemoryRequest = "512Mi"

	DefaultClickHouseShardCount   = 1
	DefaultClickHouseReplicaCount = 3

	DefaultMaxLogFiles = 50

	// DefaultClusterDomain is the default Kubernetes cluster domain suffix for DNS resolution.
	DefaultClusterDomain = "cluster.local"
	DefaultAccessMode    = corev1.ReadWriteOnce

	VersionProbeContainerName = "version-probe"
)
