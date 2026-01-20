

[![Website](https://img.shields.io/website?up_message=AVAILABLE&down_message=DOWN&url=https%3A%2F%2Fclickhouse.com&style=for-the-badge)](https://clickhouse.com)
[![Apache 2.0 License](https://img.shields.io/badge/license-Apache%202.0-blueviolet?style=for-the-badge)](https://www.apache.org/licenses/LICENSE-2.0)

<picture>
    <source media="(prefers-color-scheme: light)" srcset="https://clickhouse.com/docs/img/clickhouse-operator-logo-black.svg">
    <img align="left" width="300" style="margin-right: 20px;" src="https://clickhouse.com/docs/img/clickhouse-operator-logo.svg" alt="The ClickHouse Operator logo.">
</picture>

### ClickHouse Operator

The ClickHouse Operator is a Kubernetes operator that automates the deployment, configuration, and management of ClickHouse clusters and ClickHouse Keeper clusters on Kubernetes.
It provides declarative cluster management through custom resources, enabling users to easily create highly-available ClickHouse deployments.

The Operator handles the full lifecycle of ClickHouse clusters including scaling, upgrades, and configuration management.

## Features

- **ClickHouse Cluster Management**: Create and manage ClickHouse clusters
- **ClickHouse Keeper Integration**: Built-in support for ClickHouse Keeper clusters for distributed coordination
- **Storage Provisioning**: Customizable persistent volume claims with storage class selection
- **High Availability**: Fault tolerant installations for ClickHouse and Keeper clusters
- **Security**: Built-in security features TLS/SSL support for secure cluster communication
- **Monitoring**: Prometheus metrics integration for observability

## Getting Started

### Prerequisites
- go version v1.25.0+
- docker version 17.03+
- `kubectl` version v1.33.0+
- Access to a Kubernetes v1.33.0+ cluster

### Quick Start

For users who want to quickly try the operator:

1. Install the Custom Resource Definitions(CRD) and operator (Requires cert-manager to issue webhook certificates):
   1. Using pre-built manifests:
   ```sh
   kubectl apply -f https://github.com/ClickHouse/clickhouse-operator/releases/download/<release>/clickhouse-operator.yaml
   ```
    2. Using helm chart
    ```sh
    helm install clickhouse-operator oci://ghcr.io/clickhouse/clickhouse-operator-helm \
       --create-namespace \
       -n clickhouse-operator-system
    ```

2. Deploy a sample cluster:
```sh
kubectl apply -f https://raw.githubusercontent.com/ClickHouse/clickhouse-operator/refs/heads/main/examples/minimal.yaml
```

3. Verify the deployment:
```sh
kubectl get clickhouseclusters
kubectl get keeperclusters
kubectl get pods
```

### Deploy from the sources
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMAGE_REPO=<some-registry>
```

**NOTE:** This image ought to be pushed to the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the Custom Resource Definitions(CRD) into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMAGE_REPO=<some-registry>
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

## Examples

The `examples/` directory contains various ClickHouse cluster configurations:

- **minimal.yaml**: Basic ClickHouse cluster with Keeper (2 replicas, 1 shard)
- **cluster_with_ssl.yaml**: ClickHouse cluster with TLS/SSL enabled. Requires
- **aws_eks_gp3.yaml**: Configuration for AWS EKS with gp3 storage
- **gcp_gke_ssd.yaml**: Configuration for GCP GKE with SSD storage
- **custom_configuration.yaml**: ClickHouse cluster with configuration overrides
- **prometheus_secure_metrics_scraper.yaml**: Configuration for secure operator metrics scraping. Requires Prometheus operator and enabled secure metrics endpoint

To deploy any example:

```sh
kubectl apply -f examples/<example-file>.yaml
```

### To Uninstall
**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Documentation

For more detailed information, see the [documentation](./docs/README.md):

## Contributing

We welcome contributions to the ClickHouse Operator project! Here's how you can help:

- **Bug Reports**: Open an issue describing the bug and steps to reproduce
- **Feature Requests**: Submit an issue with your feature proposal and use case
- **Pull Requests**: Fork the repository, make your changes, and submit a PR
- **Documentation**: Help improve documentation and examples
- **Testing**: Test the operator in different environments and report issues

Before contributing, please ensure:
- All tests pass: `make test`
- Code follows Go conventions and passes linting: `make lint`
- Commits are well-documented

## Useful Links
* [`Kubebuilder` Documentation](https://book.kubebuilder.io/introduction.html)
* [ClickHouse Documentation](https://clickhouse.com/docs) 
