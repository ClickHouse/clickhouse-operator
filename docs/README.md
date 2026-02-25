# ClickHouse Operator Documentation

## To read the documentation, visit the [ClickHouse Docs](https://clickhouse.com/docs/clickhouse-operator/overview).

This directory contains the documentation sources for the ClickHouse Operator.

The [clickhouse-docs](https://github.com/ClickHouse/clickhouse-docs) repository copies `.md` and `.yml` files into its `docs/kubernetes-operator/` directory during its
build process.

## API Reference Generation

The API reference (`04_api_reference.md`) is generated from CRD types:

```bash
make docs-generate-api-ref
```
`templates/` contains the templates used for generating the API reference documentation.
