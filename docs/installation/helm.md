# Installation with Helm

This guide covers installing the ClickHouse Operator using Helm charts.

## Prerequisites

- Kubernetes cluster v1.33.0 or later
- Helm v3.0 or later
- kubectl configured to communicate with your cluster

## Install Helm

If you don't have Helm installed:

```bash
curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
```

Verify installation:

```bash
helm version
```

## Install the Operator

**NOTE:**  By default Helm chart deploys ClickHouse Operator with webhooks enabled and requires cert-manager installed.

### From OCI Helm Repository

Install the latest release
```bash
    helm install clickhouse-operator oci://ghcr.io/clickhouse/clickhouse-operator-helm \
       --create-namespace \
       -n clickhouse-operator-system
```

Install a specific operator version
```bash
    helm install clickhouse-operator oci://ghcr.io/clickhouse/clickhouse-operator-helm \
       --create-namespace \
       -n clickhouse-operator-system \
       --set-json="manager.container.tag=<operator version>
```

### From Local Chart

Clone the repository and install from the local chart:

```bash
git clone https://github.com/<org>/clickhouse-operator.git
cd clickhouse-operator
helm install clickhouse-operator ./dist/chart
```

### Configuration Options
For advanced configuration options, refer to the [values.yaml](../../dist/chart/values.yaml) file in the Helm chart
