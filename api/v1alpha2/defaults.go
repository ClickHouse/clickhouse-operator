package v1alpha2

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
	// DefaultClickHouseVersion is the ClickHouse version of the default server image.
	// The default image tag is pinned to it, so the built-in image version catalog entry is always correct.
	DefaultClickHouseVersion         = "25.8.2.29"
	DefaultClickHouseContainerTag    = DefaultClickHouseVersion
	DefaultClickHouseContainerPolicy = "IfNotPresent"

	DefaultClickHouseCPULimit      = "1"
	DefaultClickHouseCPURequest    = "250m"
	DefaultClickHouseMemoryLimit   = "512Mi"
	DefaultClickHouseMemoryRequest = "512Mi"

	DefaultClickHouseShardCount   = 1
	DefaultClickHouseReplicaCount = 3

	DefaultMaxLogFileSize    = "100M"
	DefaultMaxLogFiles       = 10
	DefaultLogLevel          = "information"
	DefaultSystemLogsTTLDays = 30

	// DefaultClusterDomain is the default Kubernetes cluster domain suffix for DNS resolution.
	DefaultClusterDomain = "cluster.local"
	DefaultAccessMode    = corev1.ReadWriteOnce

	// ClickHouseServerContainerName is the name of the ClickHouse server container in pod templates.
	ClickHouseServerContainerName = "clickhouse"
)

// DefaultClickHouseImage returns the image reference of the default ClickHouse server container.
func DefaultClickHouseImage() string {
	return DefaultClickHouseContainerRepository + ":" + DefaultClickHouseContainerTag
}

// BuiltinImageVersionCatalog returns the image version catalog entries known to the operator.
// It covers the default ClickHouse server image, so clusters that do not override the image
// do not need to declare an imageVersionCatalog.
func BuiltinImageVersionCatalog() []ImageVersionEntry {
	return []ImageVersionEntry{{Image: DefaultClickHouseImage(), Version: DefaultClickHouseVersion}}
}
