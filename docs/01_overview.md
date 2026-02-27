---
position: 1
slug: /clickhouse-operator/overview
title: 'ClickHouse Operator'
keywords: ['kubernetes']
description: 'Overview page for the ClickHouse Operator - a Kubernetes operator that automates the deployment, configuration, and management of ClickHouse clusters and ClickHouse Keeper clusters on Kubernetes.'
doc_type: 'guide'
sidebar_label: 'Overview'
---

The ClickHouse Operator is a Kubernetes operator that automates the deployment, configuration, and management of ClickHouse clusters and ClickHouse Keeper clusters on Kubernetes.
It provides declarative cluster management through custom resources, enabling users to easily create highly-available ClickHouse deployments.

The Operator handles the full lifecycle of ClickHouse clusters including scaling, upgrades, and configuration management.

## Features {#features}

- **ClickHouse Cluster Management**: Create and manage ClickHouse clusters
- **ClickHouse Keeper Integration**: Built-in support for ClickHouse Keeper clusters for distributed coordination
- **Storage Provisioning**: Customizable persistent volume claims with storage class selection
- **High Availability**: Fault tolerant installations for ClickHouse and Keeper clusters
- **Security**: Built-in security features TLS/SSL support for secure cluster communication
- **Monitoring**: Prometheus metrics integration for observability

## Installation {#installation}

Choose your preferred installation method:

- [Manifests Installation](./02_install/kubectl.md) - Install using kubectl/kustomize
- [Helm Installation](./02_install/helm.md) - Install using Helm charts
- [Operator Lifecycle Manager (OLM) Installation](./02_install/olm.md) - Install using OLM

## Guides {#guides}

- **[Introduction](./03_guides/01_introduction.md)** - General overview of ClickHouse Operator concepts
- **[Configuration Guide](./03_guides/02_configuration.md)** - Configure ClickHouse and Keeper clusters

## Reference {#reference}

- **[API Reference](./04_api_reference.md)** - Complete API documentation for custom resources
- **[Source Code](https://github.com/ClickHouse/clickhouse-operator)** - Explore the Operator's source code on GitHub
