# API Reference

This document provides detailed API reference for the ClickHouse Operator custom resources.




## ClickHouseCluster

ClickHouseCluster is the Schema for the `clickhouseclusters` API.
### API Version and Kind

```yaml
apiVersion: clickhouse.com/v1alpha1
kind: ClickHouseCluster
```

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `spec` | [ClickHouseClusterSpec](#clickhouseclusterspec) |  | true |  |
| `status` | [ClickHouseClusterStatus](#clickhouseclusterstatus) |  | true |  |

Appears in:
- [ClickHouseClusterList](#clickhouseclusterlist)


## ClickHouseClusterList

ClickHouseClusterList contains a list of ClickHouseCluster.
### API Version and Kind

```yaml
apiVersion: clickhouse.com/v1alpha1
kind: ClickHouseClusterList
```

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `items` | [ClickHouseCluster](#clickhousecluster) array |  | true |  |


## ClickHouseClusterSpec

ClickHouseClusterSpec defines the desired state of ClickHouseCluster.

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `replicas` | integer | Number of replicas in the single shard. | false | 3 |
| `shards` | integer | Number of shards in the cluster. | false | 1 |
| `keeperClusterRef` | [LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#localobjectreference-v1-core) | Reference to the KeeperCluster that is used for ClickHouse coordination. | true |  |
| `podTemplate` | [PodTemplateSpec](#podtemplatespec) | Parameters passed to the ClickHouse pod spec. | false |  |
| `containerTemplate` | [ContainerTemplateSpec](#containertemplatespec) | Parameters passed to the ClickHouse container spec. | false |  |
| `dataVolumeClaimSpec` | [PersistentVolumeClaimSpec](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#persistentvolumeclaimspec-v1-core) | Specification of persistent storage for ClickHouse data. | false |  |
| `labels` | object (keys:string, values:string) | Additional labels that are added to resources. | false |  |
| `annotations` | object (keys:string, values:string) | Additional annotations that are added to resources. | false |  |
| `settings` | [ClickHouseSettings](#clickhousesettings) | Configuration parameters for ClickHouse server. | false |  |
| `clusterDomain` | string | ClusterDomain is the Kubernetes cluster domain suffix used for DNS resolution. | false | cluster.local |

Appears in:
- [ClickHouseCluster](#clickhousecluster)


## ClickHouseClusterStatus

ClickHouseClusterStatus defines the observed state of ClickHouseCluster.

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `conditions` | [Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#condition-v1-meta) array |  | false |  |
| `readyReplicas` | integer | ReadyReplicas Total number of replicas ready to serve requests. | false |  |
| `configurationRevision` | string | ConfigurationRevision indicates target configuration revision for every replica. | true |  |
| `statefulSetRevision` | string | StatefulSetRevision indicates target StatefulSet revision for every replica. | true |  |
| `currentRevision` | string | CurrentRevision indicates latest applied ClickHouseCluster spec revision. | true |  |
| `updateRevision` | string | UpdateRevision indicates latest requested ClickHouseCluster spec revision. | true |  |
| `observedGeneration` | integer | ObservedGeneration indicates latest generation observed by controller. | true |  |

Appears in:
- [ClickHouseCluster](#clickhousecluster)



## ClickHouseSettings

ClickHouseSettings defines ClickHouse server settings options.

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `defaultUserPassword` | [DefaultPasswordSelector](#defaultpasswordselector) | Specifies source and type of the password for `default` ClickHouse user. | false |  |
| `logger` | [LoggerConfig](#loggerconfig) | Configuration of ClickHouse server logging. | false |  |
| `tls` | [ClusterTLSSpec](#clustertlsspec) | TLS settings, allows to configure secure endpoints and certificate verification for ClickHouse server. | false |  |
| `enableDatabaseSync` | boolean | Enables synchronization of ClickHouse databases to the newly created replicas and cleanup of stale replicas<br />after scale down.<br />Supports only Replicated and integration databases. | false | true |
| `extraConfig` | [RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#rawextension-runtime-pkg) | Additional ClickHouse configuration that will be merged with the default one. | false |  |
| `extraUsersConfig` | [RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#rawextension-runtime-pkg) | Additional ClickHouse users configuration that will be merged with the default one. | false |  |

Appears in:
- [ClickHouseClusterSpec](#clickhouseclusterspec)


## ClusterTLSSpec

ClusterTLSSpec defines cluster TLS configuration.

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `enabled` | boolean | Enabled indicates whether TLS is enabled, determining if secure ports should be opened. | false | false |
| `required` | boolean | Required specifies whether TLS must be enforced for all connections. Disables not secure ports. | false | false |
| `serverCertSecret` | [LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#localobjectreference-v1-core) | ServerCertSecretRef is a reference to a TLS Secret containing the server certificate.<br />It is expected that the Secret has the same structure as certificates generated by cert-manager,<br />with the certificate and private key stored under "tls.crt" and "tls.key" keys respectively. | false |  |
| `caBundle` | [SecretKeySelector](#secretkeyselector) | CABundle is a reference to a TLS Secret containing the CA bundle.<br />If empty and ServerCertSecret is specified, the CA bundle from certificate will be used.<br />Otherwise, system trusted CA bundle will be used.<br />Key is defaulted to "ca.crt" if not specified. | false |  |

Appears in:
- [ClickHouseSettings](#clickhousesettings)
- [KeeperSettings](#keepersettings)




## ConfigMapKeySelector

ConfigMapKeySelector selects a key of a ConfigMap.

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `name` | string | The name of the ConfigMap in the cluster's namespace to select from. | true |  |
| `key` | string | The key of the ConfigMap to select from. Must be a valid key. | true |  |

Appears in:
- [DefaultPasswordSelector](#defaultpasswordselector)


## ContainerImage

ContainerImage defines a container image with repository, tag or hash.

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `repository` | string | Container image registry name<br />Example: docker.io/clickhouse/clickhouse | false |  |
| `tag` | string | Container image tag, mutually exclusive with 'hash'.<br />Example: 25.3 | false |  |
| `hash` | string | Container image hash, mutually exclusive with 'tag'. | false |  |

Appears in:
- [ContainerTemplateSpec](#containertemplatespec)


## ContainerTemplateSpec

ContainerTemplateSpec describes the container configuration overrides for the cluster's containers.

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `image` | [ContainerImage](#containerimage) | Image is the container image to be deployed. | true |  |
| `imagePullPolicy` | [PullPolicy](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#pullpolicy-v1-core) | ImagePullPolicy for the image, which defaults to IfNotPresent. | false |  |
| `resources` | [ResourceRequirements](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#resourcerequirements-v1-core) | Resources is the resource requirements for the container.<br />This field cannot be updated once the cluster is created. | false |  |
| `volumeMounts` | [VolumeMount](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#volumemount-v1-core) array | VolumeMounts is the list of volume mounts for the container. | false |  |
| `env` | [EnvVar](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#envvar-v1-core) array | Env is the list of environment variables to set in the container. | false |  |
| `securityContext` | [SecurityContext](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#securitycontext-v1-core) | SecurityContext defines the security options the container should be run with.<br />If set, the fields of SecurityContext override the equivalent fields of PodSecurityContext.<br />More info: https://kubernetes.io/docs/tasks/configure-pod-container/security-context/ | false |  |

Appears in:
- [ClickHouseClusterSpec](#clickhouseclusterspec)
- [KeeperClusterSpec](#keeperclusterspec)


## DefaultPasswordSelector

DefaultPasswordSelector selects the source for the default user's password.

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `passwordType` | string | Type of the provided password. Consider documentation for possible values https://clickhouse.com/docs/operations/settings/settings-users#user-namepassword | true | password |
| `secret` | [SecretKeySelector](#secretkeyselector) | Select password value from a Secret key | false |  |
| `configMap` | [ConfigMapKeySelector](#configmapkeyselector) | Select password value from a ConfigMap key | false |  |

Appears in:
- [ClickHouseSettings](#clickhousesettings)




## KeeperCluster

KeeperCluster is the Schema for the `keeperclusters` API.
### API Version and Kind

```yaml
apiVersion: clickhouse.com/v1alpha1
kind: KeeperCluster
```

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `spec` | [KeeperClusterSpec](#keeperclusterspec) |  | true |  |
| `status` | [KeeperClusterStatus](#keeperclusterstatus) |  | true |  |

Appears in:
- [KeeperClusterList](#keeperclusterlist)


## KeeperClusterList

KeeperClusterList contains a list of KeeperCluster.
### API Version and Kind

```yaml
apiVersion: clickhouse.com/v1alpha1
kind: KeeperClusterList
```

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `items` | [KeeperCluster](#keepercluster) array |  | true |  |


## KeeperClusterSpec

KeeperClusterSpec defines the desired state of KeeperCluster.

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `replicas` | integer | Number of replicas in the cluster | false | 3 |
| `podTemplate` | [PodTemplateSpec](#podtemplatespec) | Parameters passed to the Keeper pod spec. | false |  |
| `containerTemplate` | [ContainerTemplateSpec](#containertemplatespec) | Parameters passed to the Keeper container spec. | false |  |
| `dataVolumeClaimSpec` | [PersistentVolumeClaimSpec](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#persistentvolumeclaimspec-v1-core) | Specification of persistent storage for ClickHouse Keeper data. | false |  |
| `labels` | object (keys:string, values:string) | Additional labels that are added to resources. | false |  |
| `annotations` | object (keys:string, values:string) | Additional annotations that are added to resources. | false |  |
| `settings` | [KeeperSettings](#keepersettings) | Configuration parameters for ClickHouse Keeper server. | false |  |
| `clusterDomain` | string | ClusterDomain is the Kubernetes cluster domain suffix used for DNS resolution. | false | cluster.local |

Appears in:
- [KeeperCluster](#keepercluster)


## KeeperClusterStatus

KeeperClusterStatus defines the observed state of KeeperCluster.

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `conditions` | [Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#condition-v1-meta) array |  | false |  |
| `readyReplicas` | integer | ReadyReplicas Total number of replicas ready to serve requests. | false |  |
| `configurationRevision` | string | ConfigurationRevision indicates target configuration revision for every replica. | true |  |
| `statefulSetRevision` | string | StatefulSetRevision indicates target StatefulSet revision for every replica. | true |  |
| `currentRevision` | string | CurrentRevision indicates latest applied KeeperCluster spec revision. | true |  |
| `updateRevision` | string | CurrentRevision indicates latest requested KeeperCluster spec revision. | true |  |
| `observedGeneration` | integer | ObservedGeneration indicates latest generation observed by controller. | true |  |

Appears in:
- [KeeperCluster](#keepercluster)



## KeeperSettings

KeeperSettings defines ClickHouse Keeper server configuration.

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `logger` | [LoggerConfig](#loggerconfig) | Configuration of ClickHouse Keeper server logging. | false |  |
| `tls` | [ClusterTLSSpec](#clustertlsspec) | TLS settings, allows to configure secure endpoints and certificate verification for ClickHouse Keeper server. | false |  |
| `extraConfig` | [RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#rawextension-runtime-pkg) | Additional ClickHouse Keeper configuration that will be merged with the default one. | false |  |

Appears in:
- [KeeperClusterSpec](#keeperclusterspec)


## LoggerConfig

LoggerConfig defines server logging configuration.

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `logToFile` | boolean | If false then disable all logging to file. | false | true |
| `jsonLogs` | boolean | If true, then log in JSON format. | false | false |
| `level` | string | Server logger verbosity level. | false | trace |
| `size` | string | Maximum log file size. | false | 1000M |
| `count` | integer | Maximum number of log files to keep. | false | 50 |

Appears in:
- [ClickHouseSettings](#clickhousesettings)
- [KeeperSettings](#keepersettings)


## PodTemplateSpec

PodTemplateSpec describes the pod configuration overrides for the cluster's pods.

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `terminationGracePeriodSeconds` | integer | Optional duration in seconds the pod needs to terminate gracefully. May be decreased in delete request.<br />Value must be non-negative integer. The value zero indicates stop immediately via<br />the kill signal (no opportunity to shut down).<br />If this value is nil, the default grace period will be used instead.<br />The grace period is the duration in seconds after the processes running in the pod are sent<br />a termination signal and the time when the processes are forcibly halted with a kill signal.<br />Set this value longer than the expected cleanup time for your process.<br />Defaults to 30 seconds. | false |  |
| `topologySpreadConstraints` | [TopologySpreadConstraint](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#topologyspreadconstraint-v1-core) array | TopologySpreadConstraints describes how a group of pods ought to spread across topology<br />domains. Scheduler will schedule pods in a way which abides by the constraints.<br />All topologySpreadConstraints are ANDed. | false |  |
| `imagePullSecrets` | [LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#localobjectreference-v1-core) array | ImagePullSecrets is an optional list of references to secrets in the same namespace to use for pulling any of the images used by this PodSpec.<br />If specified, these secrets will be passed to individual puller implementations for them to use.<br />More info: https://kubernetes.io/docs/concepts/containers/images#specifying-imagepullsecrets-on-a-pod | false |  |
| `nodeSelector` | object (keys:string, values:string) | NodeSelector is a selector which must be true for the pod to fit on a node.<br />Selector which must match a node's labels for the pod to be scheduled on that node.<br />More info: https://kubernetes.io/docs/concepts/configuration/assign-pod-node/ | false |  |
| `affinity` | [Affinity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#affinity-v1-core) | If specified, the pod's scheduling constraints | false |  |
| `tolerations` | [Toleration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#toleration-v1-core) array | If specified, the pod's Tolerations. | false |  |
| `schedulerName` | string | If specified, the pod will be dispatched by specified scheduler.<br />If not specified, the pod will be dispatched by default scheduler. | false |  |
| `serviceAccountName` | string | ServiceAccountName is the name of the ServiceAccount to use to run this pod.<br />More info: https://kubernetes.io/docs/tasks/configure-pod-container/configure-service-account/ | false |  |
| `volumes` | [Volume](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#volume-v1-core) array | Volumes defines the list of volumes that can be mounted by containers belonging to the pod.<br />More info: https://kubernetes.io/docs/concepts/storage/volumes | false |  |
| `securityContext` | [PodSecurityContext](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#podsecuritycontext-v1-core) | SecurityContext holds pod-level security attributes and common container settings. | false |  |
| `topologyZoneKey` | string | TopologyZoneKey is the key of node labels.<br />Nodes that have a label with this key and identical values are considered to be in the same topology zone.<br />Set it to enable default TopologySpreadConstraints and Affinity rules to spread pods across zones.<br />Recommended to be set to "topology.kubernetes.io/zone" | false |  |
| `nodeHostnameKey` | string | NodeHostnameKey is the key of node labels.<br />Nodes that have a label with this key and identical values are considered to be on the same node.<br />Set it to enable default AntiAffinity rules to spread replicas from the different shards across nodes.<br />Recommended to be set to "kubernetes.io/hostname" | false |  |

Appears in:
- [ClickHouseClusterSpec](#clickhouseclusterspec)
- [KeeperClusterSpec](#keeperclusterspec)


## SecretKeySelector

SecretKeySelector selects a key of a Secret.

| Field | Type | Description | Required | Default |
|-------|------|-------------|----------|---------|
| `name` | string | The name of the secret in the cluster's namespace to select from. | true |  |
| `key` | string | The key of the secret to select from.  Must be a valid secret key. | true |  |

Appears in:
- [ClusterTLSSpec](#clustertlsspec)
- [DefaultPasswordSelector](#defaultpasswordselector)

