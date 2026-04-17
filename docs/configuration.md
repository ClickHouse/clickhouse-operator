# Configuration Guide

This guide covers how to configure ClickHouse and Keeper clusters using the operator.

## Table of Contents

- [ClickHouseCluster Configuration](#clickhousecluster-configuration)
- [KeeperCluster Configuration](#keepercluster-configuration)
- [Storage Configuration](#storage-configuration)
- [Pod Configuration](#pod-configuration)
- [Container Configuration](#container-configuration)
- [TLS/SSL Configuration](#tlsssl-configuration)
- [ClickHouse Settings](#clickhouse-settings)
- [Custom Configuration](#custom-configuration)
- [Example Configuration](#configuration-example)

## ClickHouseCluster Configuration

### Basic Configuration

```yaml
apiVersion: clickhouse.com/v1alpha1
kind: ClickHouseCluster
metadata:
  name: my-cluster
spec:
  replicas: 3           # Number of replicas per shard
  shards: 2             # Number of shards
  keeperClusterRef:
    name: my-keeper     # Reference to KeeperCluster
  dataVolumeClaimSpec:
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 10Gi
```

### Replicas and Shards

- **Replicas**: Number of ClickHouse instances per shard (for high availability)
- **Shards**: Number of horizontal partitions (for scaling)

```yaml
spec:
  replicas: 3  # Default: 3
  shards: 2    # Default: 1
```

A cluster with `replicas: 3` and `shards: 2` will create 6 ClickHouse pods total.

### Keeper Integration

Every ClickHouse cluster must reference a KeeperCluster for coordination:

```yaml
spec:
  keeperClusterRef:
    name: my-keeper  # Name of the KeeperCluster in the same namespace
```

## KeeperCluster Configuration

```yaml
apiVersion: clickhouse.com/v1alpha1
kind: KeeperCluster
metadata:
  name: my-keeper
spec:
  replicas: 3  # Must be odd: 1, 3, 5, 7, 9, 11, 13, or 15
  dataVolumeClaimSpec:
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 5Gi
```

## Storage Configuration

Configure persistent storage:

```yaml
spec:
  dataVolumeClaimSpec:
    storageClassName: fast-ssd  # Optional: consider your storage class based on the installed CSI
    resources:
      requests:
        storage: 100Gi
```

**NOTE:** Operator can modify existing PVC only if the underlying storage class supports volume expansion.

## Pod Configuration

### Automatic Topology Spread and Affinity

Distribute pods across availability zones:

```yaml
spec:
  podTemplate:
    topologyZoneKey: topology.kubernetes.io/zone
    nodeHostnameKey: kubernetes.io/hostname
```

**NOTE**: Ensure your Kubernetes cluster has enough nodes in different zones to satisfy the spread constraints.

### Manual configuration

Arbitrary pod affinity/anti-affinity rules and topology spread constraints can be specified.

```yaml
spec:
  podTemplate:
    affinity:
      <your-affinity-rules-here>
    topologySpreadConstraints:
      <your-topology-spread-constraints-here>
```

### See [API Reference](./api_reference.md#PodTemplateSpec) for all supported Pod template options.

## Container Configuration

### Custom Image

Use a specific ClickHouse image:

```yaml
spec:
  containerTemplate:
    image:
      repository: clickhouse/clickhouse-server
      tag: "25.12"
    imagePullPolicy: IfNotPresent
```

### Container Resources

Configure CPU and memory for ClickHouse containers:

```yaml
# default values
spec:
  containerTemplate:
    resources:
      requests:
        cpu: "250m"
        memory: "256Mi"
      limits:
        cpu: "1"
        memory: "1Gi"
```

### Environment Variables


Add custom environment variables:

```yaml
spec:
  containerTemplate:
    env:
    - name: CUSTOM_ENV_VAR
      value: "1"
```

### Volume Mounts

Add additional volume mounts:

```yaml
spec:
  containerTemplate:
    volumeMounts:
    - name: custom-config
      mountPath: /etc/clickhouse-server/config.d/custom.xml
      subPath: custom.xml
```

**NOTE:** It is allowed to specify multiple volume mounts to the same `mountPath`.
Operator will create projected volume with all specified mounts.

### See [API Reference](./api_reference.md#ContainerTemplateSpec) for all supported Container template options.

## TLS/SSL Configuration

### Configure secure endpoints

Pass a reference to a Kubernetes Secret containing TLS certificates to enable secure endpoints

```yaml
spec:
  settings:
    tls:
      enabled: true
      required: true # Insecure ports are disabled if set
      serverCertSecret:
        name: <certificate-secret-name>
```

### SSL Certificate Secret format

It is expected that the Secret contains the following keys:
- `tls.crt` - PEM encoded server certificate
- `tls.key` - PEM encoded private key
- `ca.crt` - PEM encoded CA certificate chain

**NOTE:** This format is compatible with cert-manager generated certificates.

### ClickHouse-Keeper communication over TLS

If KeeperCluster has TLS enabled, ClickHouseCluster would use secure connection to Keeper nodes automatically.

ClickHouseCluster should be able to verify Keeper nodes certificates. 
If ClickHouseCluster has TLS enabled, is uses `ca.crt` bundle for verification. Otherwise, default CA bundle is used.

User may provide a custom CA bundle reference:

```yaml
spec:
    settings:
        tls:
          caBundle:
            name: <ca-certificate-secret-name>
            key: <ca-certificate-key>
```

## ClickHouse Settings

### Default User Password

Set the default user password:

```yaml
spec:
  settings:
    defaultUserPassword:
      passwordType: <password-type> # Default: password
      <secret|configMap>:
        name: <resource name>
        key: <password>
```

**NOTE** It is not recommended to use ConfigMap to store plain text passwords.

Create the secret:

```bash
kubectl create secret generic clickhouse-password --from-literal=password='your-secure-password'
```

#### Using ConfigMap for User Passwords

You can also use ConfigMap for non-sensitive default passwords:

```yaml
spec:
  settings:
    defaultUserPassword:
      passwordType: password_sha256_hex
      configMap:
        name: clickhouse-config
        key: default_password
```

### Custom Users in configuration

Configure additional users in configuration files.

Create a ConfigMap and Secret for user:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: user-config
data:
  reader.yaml: |
    users:
      reader:
        password:
          - '@from_env': READER_PASSWORD
        profile: default
        grants:
          - query: "GRANT SELECT ON *.*"
---
apiVersion: v1
kind: Secret
metadata:
  name: reader-password
data:
  password: "c2VjcmV0LXBhc3N3b3Jk"  # base64("secret-password")

```

Add custom configuration to ClickHouseCluster:

```yaml
spec:
  podTemplate:
    volumes:
      - name: reader-user
        configMap:
          name: user-config
  containerTemplate:
    env:
      - name: READER_PASSWORD
        valueFrom:
          secretKeyRef:
            name: reader-password
            key: password
    volumeMounts:
      - mountPath: /etc/clickhouse-server/users.d/
        name: reader-user
        readOnly: true
```

### Database Sync

Enable automatic database synchronization for new replicas:

```yaml
spec:
  settings:
    enableDatabaseSync: true  # Default: true
```

When enabled, the operator synchronizes Replicated and integration tables to new replicas.

## Custom Configuration

### Embedded Extra Configuration

Instead of mounting custom configuration files, you can directly specify additional ClickHouse configuration options.

Add custom ClickHouse configuration using `extraConfig`:

```yaml
spec:
  settings:
    extraConfig:
      background_pool_size: 20
```

#### Useful links:
* [YAML configuration examples](https://clickhouse.com/docs/operations/configuration-files#example-1)
* [All server settings](https://clickhouse.com/docs/operations/server-configuration-parameters/settings)

### Embedded Extra Users Configuration

You can also specify additional ClickHouse users configuration using `extraUsersConfig`. This is useful for defining users, profiles, quotas, and grants directly in the cluster specification.

```yaml
spec:
  settings:
    extraUsersConfig:
      users:
        analyst:
          password:
            - '@from_env': ANALYST_PASSWORD
          profile: "readonly"
          quota: "default"
      profiles:
        readonly:
          readonly: 1
          max_memory_usage: 10000000000
      quotas:
        default:
          interval:
            duration: 3600
            queries: 1000
            errors: 100
```

**Note**: The `extraUsersConfig` is stored in k8s ConfigMap object. Avoid plain text secrets there.

#### See [documentation](https://clickhouse.com/docs/operations/settings/settings-users) for all supported ClickHouse users configuration options.

### Configuration Example

Complete configuration example:

```yaml
apiVersion: clickhouse.com/v1alpha1
kind: KeeperCluster
metadata:
  name: sample
spec:
  replicas: 3
  dataVolumeClaimSpec:
    storageClassName: <storage-class-name>
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 10Gi
  podTemplate:
    topologyZoneKey: topology.kubernetes.io/zone
    nodeHostnameKey: kubernetes.io/hostname
  containerTemplate:
    resources:
      requests:
        cpu: "2"
        memory: "4Gi"
      limits:
        cpu: "4"
        memory: "8Gi"
  settings:
    tls:
      enabled: true
      required: true
      serverCertSecret:
        name: <keeper-certificate-secret>
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: default-user-password
data:
  # secret-password
  password: "..." # sha256 hex of the password
---
apiVersion: clickhouse.com/v1alpha1
kind: ClickHouseCluster
metadata:
  name: sample
spec:
  replicas: 2
  dataVolumeClaimSpec:
    storageClassName: <storage-class-name>
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 200Gi
  keeperClusterRef:
    name: sample
  podTemplate:
    topologyZoneKey: topology.kubernetes.io/zone
    nodeHostnameKey: kubernetes.io/hostname
  settings:
    tls:
      enabled: true
      required: true
      serverCertSecret:
        name: clickhouse-cert
    defaultUserPassword:
      passwordType: password_sha256_hex
      configMap:
        key: password
        name: default-password
```
