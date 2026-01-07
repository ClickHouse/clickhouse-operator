# Installation with kubectl

This guide covers installing the ClickHouse Operator using kubectl and manifest files.

## Prerequisites

- Kubernetes cluster v1.33.0 or later
- kubectl v1.33.0 or later
- Cluster admin permissions

## Install from Release Manifests

**NOTE:** Requires cert-manager to issue webhook certificates.

Install the operator and CRDs from the latest release:

```bash
kubectl apply -f https://github.com/ClickHouse/clickhouse-operator/releases/download/<release>/clickhouse-operator.yaml
```

This will:
1. Create the `clickhouse-operator-system` namespace
2. Install CustomResourceDefinitions (CRDs) for ClickHouseCluster and KeeperCluster
3. Create necessary ServiceAccounts, Roles, and RoleBindings
4. Deploy the operator controller manager
5. Register webhooks for resource validation and defaulting
6. Configure SSL certificates using cert-manager
7. Enable metrics endpoint

## Verify Installation

Check that the operator is running:

```bash
kubectl get pods -n clickhouse-operator-system
```

Expected output:
```
NAME                                                 READY   STATUS    RESTARTS   AGE
clickhouse-operator-controller-manager-xxxxxxxxxx    1/1     Running   0          1m
```

Verify the CRDs are installed:

```bash
kubectl get crd | grep clickhouse.com
```

Expected output:
```
clickhouseclusters.clickhouse.com    2025-01-06T00:00:00Z
keeperclusters.clickhouse.com        2025-01-06T00:00:00Z
```

## Configure Custom Deployment Options

If you want to configure operator deployment options, follow the steps below.

### 1. Clone the Repository

```bash
git clone https://github.com/ClickHouse/clickhouse-operator.git
cd clickhouse-operator
```

### 2. Configure installation options

Edit config/default/kustomization.yaml to enable/disable features as needed.

* To disable webhooks, comment out the `[WEBHOOK]` and `[CERTMANAGER]` sections.
* To enable secure metrics endpoint, comment out `[METRICS]` sections and uncomment `[METRICS SECURE]` and `[CERTMANAGER]` sections.
* To enable ServiceMonitor for Prometheus Operator, uncomment the `[PROMETHEUS]` section.
* To enable operator network policies, uncomment the `[NETWORK POLICY]` section.

### 3. Build and Deploy

Build the operator manifests and apply them:

```bash
make build-installer VERSION=<required operator version> [IMG=<custom registry path>]
kubectl apply -k dist/install.yaml
```
