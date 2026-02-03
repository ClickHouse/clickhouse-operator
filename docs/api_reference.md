# API Reference

This document provides detailed API reference for the ClickHouse Operator custom resources.

## Table of Contents

- [ClickHouseCluster](#clickhousecluster)
- [KeeperCluster](#keepercluster)
- [Common Types](#common-types)

## ClickHouseCluster

ClickHouseCluster is the schema for the ClickHouse cluster API.

### API Version and Kind

```yaml
apiVersion: clickhouse.com/v1alpha1
kind: ClickHouseCluster
```

### ClickHouseClusterSpec

ClickHouseClusterSpec defines the desired state of a ClickHouse cluster.

| Field                 | Type                        | Required | Default      | Description                                           |
|-----------------------|-----------------------------|----------|--------------|-------------------------------------------------------|
| `replicas`            | `*int32`                    | No       | `3`          | Number of replicas in a single shard. Must be >= 0.   |
| `shards`              | `*int32`                    | No       | `1`          | Number of shards in the cluster. Must be >= 0.        |
| `keeperClusterRef`    | `LocalObjectReference`      | Yes      | -            | Reference to the KeeperCluster used for coordination. |
| `podTemplate`         | `PodTemplateSpec`           | No       | -            | Parameters for the pod spec.                          |
| `containerTemplate`   | `ContainerTemplateSpec`     | No       | See defaults | Parameters for the ClickHouse container spec.         |
| `dataVolumeClaimSpec` | `PersistentVolumeClaimSpec` | Yes      | -            | Storage configuration for data volumes.               |
| `labels`              | `map[string]string`         | No       | -            | Additional labels added to all resources.             |
| `annotations`         | `map[string]string`         | No       | -            | Additional annotations added to all resources.        |
| `settings`            | `ClickHouseConfig`          | No       | -            | ClickHouse configuration parameters.                  |

#### Example

```yaml
apiVersion: clickhouse.com/v1alpha1
kind: ClickHouseCluster
metadata:
  name: example-cluster
spec:
  replicas: 3
  shards: 2
  keeperClusterRef:
    name: example-keeper
  dataVolumeClaimSpec:
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 100Gi
  containerTemplate:
    image:
      repository: clickhouse/clickhouse-server
      tag: "25.12"
    resources:
      requests:
        cpu: "2"
        memory: "8Gi"
      limits:
        cpu: "4"
        memory: "16Gi"
  settings:
    defaultUserPassword:
      secret:
        name: clickhouse-password
        key: password
    tls:
      enabled: true
      required: true
      serverCertSecret:
        name: clickhouse-cert
```

### ClickHouseConfig

ClickHouse configuration parameters.

| Field                 | Type                       | Required | Default      | Description                                                       |
|-----------------------|----------------------------|----------|--------------|-------------------------------------------------------------------|
| `defaultUserPassword` | `*DefaultPasswordSelector` | No       | -            | Password for the 'default' user from a Secret or ConfigMap.       |
| `logger`              | `LoggerConfig`             | No       | See defaults | Logger configuration.                                             |
| `tls`                 | `ClusterTLSSpec`           | No       | -            | TLS settings for ClickHouse.                                      |
| `enableDatabaseSync`  | `bool`                     | No       | `true`       | Enable synchronization of databases to new replicas.              |
| `extraConfig`         | `runtime.RawExtension`     | No       | -            | Additional ClickHouse configuration (merged with defaults).       |
| `extraUsersConfig`    | `runtime.RawExtension`     | No       | -            | Additional ClickHouse users configuration (merged with defaults). |

### ClickHouseClusterStatus

ClickHouseClusterStatus defines the observed state of a ClickHouse cluster.

| Field                   | Type                 | Description                                            |
|-------------------------|----------------------|--------------------------------------------------------|
| `conditions`            | `[]metav1.Condition` | Current conditions of the cluster.                     |
| `observedGeneration`    | `int64`              | Most recently observed generation.                     |
| `replicas`              | `int32`              | Number of replicas currently running.                  |
| `readyReplicas`         | `int32`              | Number of ready replicas.                              |
| `configurationRevision` | `string`             | Target configuration revision applied by the operator. |
| `statefulSetRevision`   | `string`             | Target StatefulSet controller revision.                |
| `currentRevision`       | `string`             | Last applied cluster spec revision.                    |
| `updateRevision`        | `string`             | Last requested cluster spec revision.                  |

## KeeperCluster

KeeperCluster is the schema for the ClickHouse Keeper cluster API.

### API Version and Kind

```yaml
apiVersion: clickhouse.com/v1alpha1
kind: KeeperCluster
```

### KeeperClusterSpec

KeeperClusterSpec defines the desired state of a Keeper cluster.

| Field                 | Type                        | Required | Default      | Description                                                                 |
|-----------------------|-----------------------------|----------|--------------|-----------------------------------------------------------------------------|
| `replicas`            | `*int32`                    | No       | `3`          | Number of replicas. Must be an odd number: 0, 1, 3, 5, 7, 9, 11, 13, or 15. |
| `podTemplate`         | `PodTemplateSpec`           | No       | -            | Parameters for the pod spec.                                                |
| `containerTemplate`   | `ContainerTemplateSpec`     | No       | See defaults | Parameters for the Keeper container spec.                                   |
| `dataVolumeClaimSpec` | `PersistentVolumeClaimSpec` | Yes      | -            | Storage configuration for data volumes.                                     |
| `labels`              | `map[string]string`         | No       | -            | Additional labels added to all resources.                                   |
| `annotations`         | `map[string]string`         | No       | -            | Additional annotations added to all resources.                              |
| `settings`            | `KeeperConfig`              | No       | -            | Keeper configuration parameters.                                            |

#### Example

```yaml
apiVersion: clickhouse.com/v1alpha1
kind: KeeperCluster
metadata:
  name: example-keeper
spec:
  replicas: 3
  dataVolumeClaimSpec:
    storageClassName: fast-ssd
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 10Gi
  containerTemplate:
    resources:
      requests:
        cpu: "500m"
        memory: "512Mi"
      limits:
        cpu: "1"
        memory: "1Gi"
  podTemplate:
    topologyZoneKey: topology.kubernetes.io/zone
  settings:
    tls:
      enabled: true
      required: true
      serverCertSecret:
        name: keeper-cert
```

### KeeperConfig

Keeper configuration parameters.

| Field         | Type                   | Required | Default      | Description                                             |
|---------------|------------------------|----------|--------------|---------------------------------------------------------|
| `logger`      | `LoggerConfig`         | No       | See defaults | Logger configuration.                                   |
| `tls`         | `ClusterTLSSpec`       | No       | -            | TLS settings for Keeper.                                |
| `extraConfig` | `runtime.RawExtension` | No       | -            | Additional Keeper configuration (merged with defaults). |

### KeeperClusterStatus

KeeperClusterStatus defines the observed state of a Keeper cluster.

| Field                   | Type                 | Description                                            |
|-------------------------|----------------------|--------------------------------------------------------|
| `conditions`            | `[]metav1.Condition` | Current conditions of the cluster.                     |
| `observedGeneration`    | `int64`              | Most recently observed generation.                     |
| `replicas`              | `int32`              | Number of replicas currently running.                  |
| `readyReplicas`         | `int32`              | Number of ready replicas.                              |
| `configurationRevision` | `string`             | Target configuration revision applied by the operator. |
| `statefulSetRevision`   | `string`             | Target StatefulSet controller revision.                |
| `currentRevision`       | `string`             | Last applied cluster spec revision.                    |
| `updateRevision`        | `string`             | Last requested cluster spec revision.                  |

## Common Types

### PodTemplateSpec

Parameters for pod configuration.

| Field             | Type                         | Required | Description                                                                          |
|-------------------|------------------------------|----------|--------------------------------------------------------------------------------------|
| `topologyZoneKey` | `string`                     | No       | Kubernetes topology key for zone distribution (e.g., `topology.kubernetes.io/zone`). |
| `nodeHostnameKey` | `string`                     | No       | Kubernetes label key for node hostname (e.g., `kubernetes.io/hostname`).             |
| `nodeSelector`    | `map[string]string`          | No       | Node selector for pod placement.                                                     |
| `affinity`        | `corev1.Affinity`            | No       | Pod affinity/anti-affinity rules.                                                    |
| `tolerations`     | `[]corev1.Toleration`        | No       | Tolerations for tainted nodes.                                                       |
| `securityContext` | `*corev1.PodSecurityContext` | No       | Pod security context overrides.                                                      |

#### Example

```yaml
podTemplate:
  topologyZoneKey: topology.kubernetes.io/zone
  nodeHostnameKey: kubernetes.io/hostname
  nodeSelector:
    disktype: ssd
  tolerations:
  - key: "dedicated"
    operator: "Equal"
    value: "clickhouse"
    effect: "NoSchedule"
  affinity:
    podAntiAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
      - labelSelector:
          matchLabels:
            app.kubernetes.io/name: clickhouse
        topologyKey: kubernetes.io/hostname
```

### ContainerTemplateSpec

Parameters for container configuration.

| Field             | Type                          | Required | Description                                      |
|-------------------|-------------------------------|----------|--------------------------------------------------|
| `image`           | `ContainerImage`              | No       | Container image configuration.                   |
| `imagePullPolicy` | `corev1.PullPolicy`           | No       | Image pull policy (Always, IfNotPresent, Never). |
| `resources`       | `corev1.ResourceRequirements` | No       | CPU and memory resource requirements.            |
| `env`             | `[]corev1.EnvVar`             | No       | Environment variables.                           |
| `volumeMounts`    | `[]corev1.VolumeMount`        | No       | Additional volume mounts.                        |
| `securityContext` | `*corev1.SecurityContext`     | No       | Container security context overrides.            |

#### Example

```yaml
containerTemplate:
  image:
    repository: clickhouse/clickhouse-server
    tag: "25.12"
  imagePullPolicy: IfNotPresent
  resources:
    requests:
      cpu: "2"
      memory: "8Gi"
    limits:
      cpu: "4"
      memory: "16Gi"
  env:
  - name: TZ
    value: "UTC"
```

### ContainerImage

Container image specification.

| Field        | Type     | Required | Default                                                                              | Description                 |
|--------------|----------|----------|--------------------------------------------------------------------------------------|-----------------------------|
| `repository` | `string` | No       | ClickHouse: `clickhouse/clickhouse-server`<br>Keeper: `clickhouse/clickhouse-keeper` | Container image repository. |
| `tag`        | `string` | No       | `latest`                                                                             | Image tag.                  |

### ClusterTLSSpec

TLS configuration for clusters.

| Field              | Type                          | Required | Description                                                    |
|--------------------|-------------------------------|----------|----------------------------------------------------------------|
| `enabled`          | `bool`                        | No       | Enable TLS.                                                    |
| `required`         | `bool`                        | No       | Require TLS for all connections (disables insecure endpoints). |
| `serverCertSecret` | `corev1.LocalObjectReference` | No       | Secret containing server certificate (tls.crt, tls.key).       |
| `caBundle`         | `*SecretKeySelector`          | No       | Secret containing CA bundle for verification.                  |

#### Example

```yaml
tls:
  enabled: true
  required: true
  serverCertSecret:
    name: clickhouse-cert
  caBundle:
    secretKeyRef:
      name: ca-cert-secret
      key: ca.crt
```

### LoggerConfig

Logger configuration for ClickHouse and Keeper.

| Field       | Type     | Required | Default   | Description                                           |
|-------------|----------|----------|-----------|-------------------------------------------------------|
| `logToFile` | `bool`   | No       | `true`    | Write logs to file.                                   |
| `level`     | `string` | No       | `"trace"` | Log level: trace, debug, information, warning, error. |
| `size`      | `string` | No       | `"1000M"` | Maximum log file size.                                |
| `count`     | `int`    | No       | `50`      | Number of log files to keep.                          |

#### Example

```yaml
logger:
  logToFile: true
  level: "information"
  size: "1000M"
  count: 50
```

### DefaultPasswordSelector

Reference to a Secret or ConfigMap key containing a password.

| Field          | Type                    | Required | Default     | Description                                                                                                                                                                                                                                                    |
|----------------|-------------------------|----------|-------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `passwordType` | `string`                | No       | `plaintext` | Type of password encoding. Possible values: `plaintext`, `sha256_password`, `sha256_hash`, `double_sha1_password`, `double_sha1_hash`. See [ClickHouse documentation](https://clickhouse.com/docs/en/operations/settings/settings-users#password) for details. |
| `secret`       | `*SecretKeySelector`    | No       | -           | Select password from a Secret key. Mutually exclusive with `configMap`.                                                                                                                                                                                        |
| `configMap`    | `*ConfigMapKeySelector` | No       | -           | Select password from a ConfigMap key. Mutually exclusive with `secret`.                                                                                                                                                                                        |

**Note**: You must specify either `secret` OR `configMap`, not both.

#### Example with Secret (Recommended)

```yaml
defaultUserPassword:
  passwordType: plaintext  # Optional, default
  secret:
    name: clickhouse-password
    key: password
```

#### Example with SHA256

```yaml
defaultUserPassword:
  passwordType: sha256_password
  secret:
    name: clickhouse-password
    key: password_hash
```

#### Example with ConfigMap

```yaml
defaultUserPassword:
  passwordType: plaintext
  configMap:
    name: clickhouse-config
    key: default_password
```

## Default Values

### ClickHouse Defaults

| Setting              | Default Value                  |
|----------------------|--------------------------------|
| Replicas             | `3`                            |
| Shards               | `1`                            |
| Image Repository     | `clickhouse/clickhouse-server` |
| Image Tag            | `latest`                       |
| Image Pull Policy    | `IfNotPresent`                 |
| CPU Request          | `2`                            |
| CPU Limit            | `4`                            |
| Memory Request       | `4Gi`                          |
| Memory Limit         | `8Gi`                          |
| Logger Level         | `trace`                        |
| Logger Log to File   | `true`                         |
| Logger Size          | `1000M`                        |
| Logger Count         | `50`                           |
| Enable Database Sync | `true`                         |

### Keeper Defaults

| Setting            | Default Value                  |
|--------------------|--------------------------------|
| Replicas           | `3`                            |
| Image Repository   | `clickhouse/clickhouse-keeper` |
| Image Tag          | `latest`                       |
| Image Pull Policy  | `IfNotPresent`                 |
| CPU Request        | `500m`                         |
| CPU Limit          | `1`                            |
| Memory Request     | `512Mi`                        |
| Memory Limit       | `1Gi`                          |
| Logger Level       | `trace`                        |
| Logger Log to File | `true`                         |
| Logger Size        | `1000M`                        |
| Logger Count       | `50`                           |

## Conditions

### ClickHouseCluster conditions

| Type                      | Description                                                                                        |
|---------------------------|----------------------------------------------------------------------------------------------------|
| `SpecValid`               | Whether ClickHouseCluster CustomResource passes validation. Useful in deployments without webhooks |
| `ReconcileSucceeded`      | The success of the last reconciliation cycle                                                       |
| `ReplicaStartupSucceeded` | Whether all replicas of the ClickHouseCluster are able to start.                                   |
| `Healthy`                 | Readiness of all requested replicas.                                                               |
| `ClusterSizeAligned`      | Cluster have the same amount of replicas/shards as requested.                                      |
| `ConfigurationInSync`     | Represents Configuration deployment state                                                          |
| `Ready`                   | ClickHouseCluster is ready to serve client requests. At least one replica in every shard is Ready  |
| `SchemaInSync`            | All replicas have the same database schema after scaling operations.                               |

### KeeperCluster conditions

| Type                      | Description                                                                                    |
|---------------------------|------------------------------------------------------------------------------------------------|
| `SpecValid`               | Whether KeeperCluster CustomResource passes validation. Useful in deployments without webhooks |
| `ReconcileSucceeded`      | The success of the last reconciliation cycle                                                   |
| `ReplicaStartupSucceeded` | Whether all replicas of the KeeperCluster are able to start.                                   |
| `Healthy`                 | Readiness of all requested replicas.                                                           |
| `ClusterSizeAligned`      | Cluster have the same amount of replicas as requested.                                         |
| `ConfigurationInSync`     | Represents Configuration deployment state                                                      |
| `Ready`                   | KeeperCluster is ready to serve client requests. Leader elected.                               |
| `ScaleAllowed`            | Represents the operator's ability to add/remove a node in the quorum.                          |
