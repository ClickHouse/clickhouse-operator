# Introduction to the ClickHouse Operator

This document provides an overview of key concepts and usage patterns for the ClickHouse Operator.

## Table of Contents

- [What is the ClickHouse Operator](#what-is-the-clickhouse-operator)
- [Custom Resources](#custom-resources)
- [Coordination](#coordination)
- [Schema Replication](#schema-replication)
- [Storage Management](#storage-management)
- [Default configuration Highlights](#default-configuration-highlights)

## What is the ClickHouse Operator

The ClickHouse Operator is a Kubernetes operator that automates the deployment and management of ClickHouse clusters on Kubernetes. Built using the operator pattern, it extends the Kubernetes API with custom resources that represent ClickHouse clusters and their dependencies.

The operator handles:
- Cluster lifecycle management (creation, updates, scaling, deletion)
- ClickHouse Keeper cluster coordination
- Automatic configuration generation
- Database schema synchronization
- Rolling updates and upgrades
- Storage provisioning

## Custom Resources

The operator provides two main custom resource definitions (CRDs):

### ClickHouseCluster

Represents a ClickHouse database cluster with configurable replicas and shards.

```yaml
apiVersion: clickhouse.com/v1alpha1
kind: ClickHouseCluster
metadata:
  name: sample-cluster
spec:
  replicas: 3
  shards: 2
  keeperClusterRef:
    name: sample-keeper
  dataVolumeClaimSpec:
    resources:
      requests:
        storage: 100Gi
```

### KeeperCluster

Represents a ClickHouse Keeper cluster for distributed coordination (ZooKeeper replacement).

```yaml
apiVersion: clickhouse.com/v1alpha1
kind: KeeperCluster
metadata:
  name: sample-keeper
spec:
  replicas: 3
  dataVolumeClaimSpec:
    resources:
      requests:
        storage: 10Gi
```

## Coordination

### ClickHouse Keeper is Required

Every ClickHouseCluster requires a ClickHouse Keeper cluster for distributed coordination. 
The Keeper cluster must be referenced in the ClickHouseCluster spec using `keeperClusterRef`.

###  One-to-One Keeper Relationship

Each ClickHouseCluster must have its own dedicated KeeperCluster. You cannot share a single KeeperCluster between multiple ClickHouseClusters.

**Why?** The operator automatically generates a unique authentication key for each ClickHouseCluster to access its Keeper. This key is stored in a Secret and cannot be shared.

**Consequences**:
- Multiple ClickHouseClusters cannot reference the same KeeperCluster
- Recreating a ClickHouseCluster requires recreating its KeeperCluster

**NOTE**: Persistent Volumes are not deleted automatically when ClickHouseCluster or KeeperCluster resources are deleted.

When recreating a cluster:
1. Delete the ClickHouseCluster resource
2. Delete the KeeperCluster resource
3. Wait for all pods to terminate
4. Optionally delete PersistentVolumeClaims if you want to start fresh
5. Recreate both KeeperCluster and ClickHouseCluster together

To avoid authentication errors, either delete the Persistent Volumes manually or recreate both clusters together with fresh storage.

## Schema Replication

The ClickHouse Operator automatically replicates database definitions across all replicas in a cluster.

### What Gets Replicated

The operator synchronizes:
- [Replicated](https://clickhouse.com/docs/engines/database-engines/replicated) database definitions
- Integration database engines (PostgreSQL, MySQL, etc.)

The operator does **not** synchronize:
- Non-replicated databases (Atomic, Ordinary, etc.)
- Local tables in non-replicated databases
- Table data (handled by ClickHouse replication)

### Recommended: Use Replicated Database Engine

**✅ Best Practice**: Always use the [Replicated](https://clickhouse.com/docs/engines/database-engines/replicated) database engine for production deployments.

Benefits:
- Automatic schema replication across all nodes
- Simplified table management
- Operator can sync to new replicas
- Consistent schema across the cluster

Create databases with distributed DDL:

```sql
CREATE DATABASE my_database ON CLUSTER 'default' ENGINE = Replicated;
```

### Avoid Non-Replicated Engines

Non-replicated database engines (Atomic, Lazy, SQLite, Ordinary) require manual schema management:
- Tables must be created individually on each replica
- Schema drift can occur between nodes
- Operator cannot automatically sync new replicas

### Disable Schema Replication

To disable automatic schema replication, set `spec.settings.enableDatabaseSync` to `false` in the ClickHouseCluster resource.

## Storage Management

The operator manages storage through Kubernetes PersistentVolumeClaims (PVCs).

### Data Volume Configuration

Specify storage requirements in `dataVolumeClaimSpec`:

```yaml
spec:
  dataVolumeClaimSpec:
    storageClassName: fast-ssd
    accessModes:
      - ReadWriteOnce
    resources:
      requests:
        storage: 500Gi
```

### Storage Lifecycle

- **Creation**: PVCs are created automatically with the cluster
- **Expansion**: Supported if StorageClass allows volume expansion
- **Retention**: PVCs are **not** deleted automatically on cluster deletion
- **Reuse**: Existing PVCs can be reused if cluster is recreated with same name

To completely remove storage:

```bash
# Delete cluster
kubectl delete clickhousecluster my-cluster

# Wait for pods to terminate
kubectl wait --for=delete pod -l app.kubernetes.io/instance=my-cluster

# Delete PVCs
kubectl delete pvc -l app.kubernetes.io/instance=sample-cluster
```

## Default configuration Highlights

* **Pre-configured Cluster:** Cluster named 'default' containing all ClickHouse nodes.
* **Default macros:** Some useful macros are pre-defined:
  - `{cluster}`: Cluster name (`default`)
  - `{shard}`: Shard number
  - `{replica}`: Replica number
* **Replicated storage for Role Based Access Control(RBAC) entities**
* **Replicated storage for User Defined Functions(UDF)**

## Next Steps

- [Configuration Guide](./configuration.md) - Detailed configuration options
- [API Reference](./api_reference.md) - Complete API documentation
