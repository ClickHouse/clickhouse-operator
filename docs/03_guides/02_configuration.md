---
position: 2
slug: /clickhouse-operator/guides/configuration
title: 'Configuration'
keywords: ['kubernetes']
description: 'This guide covers how to configure ClickHouse and Keeper clusters using the ClickHouse operator.'
doc_type: 'guide'
---

# ClickHouse Operator configuration guide {#clickhouse-operator-configuration-guide}

This guide covers how to configure ClickHouse and Keeper clusters using the operator.

## ClickHouseCluster configuration {#clickhousecluster-configuration}

### Basic configuration {#basic-configuration}

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
    resources:
      requests:
        storage: 10Gi
```

### Replicas and shards {#replicas-and-shards}

- **Replicas**: Number of ClickHouse instances per shard (for high availability)
- **Shards**: Number of horizontal partitions (for scaling)

```yaml
spec:
  replicas: 3  # Default: 3
  shards: 2    # Default: 1
```

A cluster with `replicas: 3` and `shards: 2` will create 6 ClickHouse pods total.

### Keeper integration {#keeper-integration}

Every ClickHouse cluster must reference a KeeperCluster for coordination:

```yaml
spec:
  keeperClusterRef:
    name: my-keeper  # Name of the KeeperCluster in the same namespace
```

## KeeperCluster configuration {#keepercluster-configuration}

```yaml
apiVersion: clickhouse.com/v1alpha1
kind: KeeperCluster
metadata:
  name: my-keeper
spec:
  replicas: 3  # Must be odd: 1, 3, 5, 7, 9, 11, 13, or 15
  dataVolumeClaimSpec:
    resources:
      requests:
        storage: 5Gi
```

## Storage configuration {#storage-configuration}

Configure persistent storage:

```yaml
spec:
  dataVolumeClaimSpec:
    storageClassName: fast-ssd  # Optional: consider your storage class based on the installed CSI
    accessModes:
      - ReadWriteOnce # Optional: this is default value
    resources:
      requests:
        storage: 100Gi
```

:::note
Operator can modify existing PVC only if the underlying storage class supports volume expansion.
:::

## Pod configuration {#pod-configuration}

### Automatic topology spread and affinity {#automatic-topology-spread-and-affinity}

Distribute pods across availability zones:

```yaml
spec:
  podTemplate:
    topologyZoneKey: topology.kubernetes.io/zone
    nodeHostnameKey: kubernetes.io/hostname
```

:::note
Ensure your Kubernetes cluster has enough nodes in different zones to satisfy the spread constraints.
:::

### Manual configuration {#manual-configuration}

Arbitrary pod affinity/anti-affinity rules and topology spread constraints can be specified.

```yaml
spec:
  podTemplate:
    affinity:
      <your-affinity-rules-here>
    topologySpreadConstraints:
      <your-topology-spread-constraints-here>
```

### See [API Reference](../04_api_reference.mdx#containertemplatespec) for all supported Pod template options. {#pod-template-api-reference}

## Container configuration {#container-configuration}

### Custom image {#custom-image}

Use a specific ClickHouse image:

```yaml
spec:
  containerTemplate:
    image:
      repository: clickhouse/clickhouse-server
      tag: "25.12"
    imagePullPolicy: IfNotPresent
```

### Container resources {#container-resources}

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

### Environment variables {#environment-variables}

Add custom environment variables:

```yaml
spec:
  containerTemplate:
    env:
    - name: CUSTOM_ENV_VAR
      value: "1"
```

### Volume mounts {#volume-mounts}

Add additional volume mounts:

```yaml
spec:
  containerTemplate:
    volumeMounts:
    - name: custom-config
      mountPath: /etc/clickhouse-server/config.d/custom.xml
      subPath: custom.xml
```

:::note
It is allowed to specify multiple volume mounts to the same `mountPath`.
Operator will create projected volume with all specified mounts.
:::

### See [API Reference](../04_api_reference.mdx#containertemplatespec) for all supported Container template options. {#container-template-api-reference}

## TLS/SSL configuration {#tls-ssl-configuration}

### Configure secure endpoints {#configure-secure-endpoints}

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

### SSL certificate secret format {#ssl-certificate-secret-format}

It is expected that the Secret contains the following keys:
- `tls.crt` - PEM encoded server certificate
- `tls.key` - PEM encoded private key
- `ca.crt` - PEM encoded CA certificate chain

:::note
This format is compatible with cert-manager generated certificates.
:::

### ClickHouse-Keeper communication over TLS {#clickhouse-keeper-communication-over-tls}

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

## ClickHouse settings {#clickhouse-settings}

### Default user password {#default-user-password}

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

:::note
It isn't recommended to use ConfigMap to store plain text passwords.
:::

Create the secret:

```bash
kubectl create secret generic clickhouse-password --from-literal=password='your-secure-password'
```

#### Using ConfigMap for user passwords {#using-configmap-for-user-passwords}

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

### Custom users in configuration {#custom-users-in-configuration}

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

### Database sync {#database-sync}

Enable automatic database synchronization for new replicas:

```yaml
spec:
  settings:
    enableDatabaseSync: true  # Default: true
```

When enabled, the operator synchronizes Replicated and integration tables to new replicas.

## Custom configuration {#custom-configuration}

### Embedded extra configuration {#embedded-extra-configuration}

Instead of mounting custom configuration files, you can directly specify additional ClickHouse configuration options.

Add custom ClickHouse configuration using `extraConfig`:

```yaml
spec:
  settings:
    extraConfig:
      background_pool_size: 20
```

#### Useful links: {#useful-links}

* [YAML configuration examples](/operations/configuration-files#example-1)
* [All server settings](/operations/server-configuration-parameters/settings)

### Embedded extra users configuration {#embedded-extra-users-configuration}

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

:::note
The `extraUsersConfig` is stored in k8s ConfigMap object. Avoid plain text secrets there.
:::

#### See [documentation](/operations/settings/settings-users) for all supported ClickHouse users configuration options. {#users-settings-reference}

### Configuration example {#configuration-example}

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
