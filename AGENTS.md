# AGENTS.md

This file provides guidance to coding agents when working with code in this repository.

## Project Overview

Kubernetes operator for managing ClickHouse database clusters and ClickHouse Keeper clusters. Built with `Kubebuilder` and `controller-runtime`. Manages two CRDs: `ClickHouseCluster` and `KeeperCluster` (both `v1alpha1` in the `clickhouse.com` API group).

## Common Commands

### Build & Run
```bash
make build                  # Build binary (bin/clickhouse-manager). Runs manifests, generate, fmt, vet first.
make build-linux-manager    # Build binary (bin/clickhouse-manager) for linux platform. Used to build Docker image.
make docker-build           # Build Docker image
```

### Testing
```bash
make test                   # Unit tests (excludes e2e and deploy tests). Uses `envtest` with Ginkgo.
make test-ci                # CI tests with coverage (cover.out), race detection, JUnit report
make fuzz                   # Fuzz tests for keeper and clickhouse spec validation
make test-e2e               # Full e2e tests (requires Kind cluster, 30m timeout)
make test-keeper-e2e        # Keeper-only e2e tests (--ginkgo.label-filter keeper)
make test-clickhouse-e2e    # ClickHouse-only e2e tests (--ginkgo.label-filter clickhouse)
make test-compat-e2e        # Compatibility smoke tests across ClickHouse versions
```

Run a single test file or spec:
```bash
# Single package
go test ./internal/controller/keeper/ -v --ginkgo.v
# Single spec by name
go test ./internal/controller/keeper/ -v --ginkgo.v --ginkgo.focus="spec name pattern"
```

### Testing rules
- Run `make lint-fix` before tests
- Run `make test` or specific unit tests
- Do NOT run `make test-e2e`, `test-keeper-e2e`, `test-clickhouse-e2e`, `test-compat-e2e`
- If changes complex and require e2e testing run target with `--ginkgo.focus` to limit scope to a single test at a time

### Test patterns
- **Functional tests** (`controller_test.go`): Use `testutil.SetupEnvironment()` which starts `envtest` (real API server + `etcd`). Use for full reconciliation flow testing.
- **Unit tests** (`sync_test.go`, `commands_test.go`): Use `fake.NewClientBuilder()` for faster, focused tests on individual methods.
- **E2E tests** (`test/e2e/`): Real Kind cluster. Label tests with `Label("clickhouse")` or `Label("keeper")` for filtered runs.

### Code Generation (run after modifying api/v1alpha1/ types)
```bash
make generate               # DeepCopy methods (zz_generated.deepcopy.go)
make manifests              # CRDs, RBAC roles, webhooks → config/crd/bases/
make generate-helmchart-ci  # Generate Helm chart templates and reset manually maintained files (dist/chart/templates/)
make docs-generate-api-ref  # Generate API reference docs (docs/api-reference.md)
```

### Helm Chart
The Helm chart in `dist/chart/` is partially **generated from `Kustomize` configs** — do not edit templates directly. Regenerate with `make generate-helmchart-ci`. Except you are directly asked.

### Linting & Formatting
```bash
make lint                   # golangci-lint + codespell + actionlint
make lint-fix               # golangci-lint with --fix
make golangci-fmt           # gofmt + goimports formatting
make docs-lint              # markdownlint for docs
```

### Running E2E Locally
Kind cluster configuration at `ci/kind-cluster.config` creates 1 control-plane + 3 workers with zone topology labels:
```bash
kind create cluster --config ci/kind-cluster.config
```

### CRD Compatibility
```bash
make check-crd-compat       # Check CRD backward compatibility against origin/main
```

## Coding Rules
- Do not introduce new dependencies without justification
- Follow existing error handling patterns
- Try to minimize diff
- Avoid API changes unless necessary
- Always ensure `make lint` passes, use `make lint-fix` to automatically fix issues before writing code

## CI Pipeline (what runs on PRs)
1. Lint: `go mod tidy` check, `make manifests`/`make generate` freshness, CRD backward compatibility, golangci-lint, codespell, actionlint
2. Build + unit tests (`make test-ci`)
3. Fuzz tests
4. Helm chart generation + lint
5. OLM bundle + scorecard validation
6. Compatibility e2e (multiple K8s + ClickHouse version matrices)
7. Full e2e (Keeper + ClickHouse, on self-hosted runners)

## Architecture

### CRD API Types (`api/v1alpha1/`)
- `clickhousecluster_types.go` — ClickHouseCluster spec: shards, replicas per shard, keeper reference, storage, settings
- `keepercluster_types.go` — KeeperCluster spec: replicas (odd, 0-15), storage, settings
- `common.go` — Shared types: ContainerImage, LoggerConfig, PodDisruptionBudgetSpec, ClickHouseSettings, KeeperSettings
- `defaults.go` — Default value logic applied by webhooks
- `conditions.go` — Status condition helpers

After modifying types, always run `make generate manifests generate-helmchart-ci docs-generate-api-ref`.

### Controllers (`internal/controller/`)
Both controllers follow the same pattern, extending a shared base:

- `reconcilerbase.go` — Base reconciliation logic shared by both controllers
- `clickhouse/controller.go` — ClickHouseCluster reconciler (manages StatefulSets, ConfigMaps, Services)
- `keeper/controller.go` — KeeperCluster reconciler
- `clickhouse/sync.go`, `keeper/sync.go` — Sync desired state to Kubernetes
- `clickhouse/templates.go`, `keeper/templates.go` — Generate Kubernetes resource specs
- `clickhouse/config.go` — Generate ClickHouse YAML configuration
- `clickhouse/commands.go`, `keeper/commands.go` — Interfaces to communicate to running containers
- `overrides.go` — Pod/container spec overrides helpers via strategic merge patch
- `versionprobe.go` — Creates Jobs to detect actual ClickHouse/Keeper versions
- `upgradecheck.go` — Checks for newer version availability
- `resources.go` — Shared Kubernetes resource creation helpers
- `status.go` — Status update logic

### Reconciliation Pattern
Controllers execute reconcile as a sequence of step functions (`func(ctx, log) (*Result, error)`). Steps are defined in `sync()` and executed sequentially.

### Resource Change Detection
Resources are tracked via annotation hashes (`checksum/spec`, `checksum/configuration`). Before updating a K8s resource, compare `util.DeepHashResource()` output against the stored annotation. Skip updates when hashes match. Always call `util.AddSpecHashToObject()` on reconciled resources.

### Webhooks (`internal/webhook/v1alpha1/`)
Validation and defaulting webhooks for both CRDs. `ENABLE_WEBHOOKS` Env var controls whether they are registered.

### Key Packages
- `internal/upgrade/` — Version update checking (fetcher + compatibility checker)
- `internal/version/` — Build version info injected via `ldflags`
- `internal/environment/` — Environment variable processing (`ENABLE_WEBHOOKS`, `WATCH_NAMESPACE`)
- `internal/controllerutil/` — Shared utilities (annotations, dialer, logger)

### Config/Deploy (`config/`)
Kustomize-based configuration: `config/default/` is the main overlay composing CRDs, RBAC, manager deployment, webhooks, and cert-manager integration.

### Entry Point
`cmd/main.go` — Sets up controller-runtime manager, registers both controllers and webhooks, configures metrics and health probes.

## Code Style & Conventions

- **Import ordering**: `stdlib`, third-party, then `github.com/ClickHouse/clickhouse-operator` (enforced by goimports with local-prefixes)
- **`interface{}` → `any`**: gofmt rewrites `interface{}` to `any` automatically
- **Testing**: Ginkgo v2 BDD style with Gomega matchers. Dot imports for `ginkgo/v2` and `gomega` are allowed.
- **Linting**: 80+ linters enabled in `.golangci.yml`. Notable: `wsl_v5` (whitespace), `mnd` (magic numbers, `2` is exempted), `godot` (comment periods), `ireturn` (interface return restrictions with whitelisted types)
- **Generated files**: Never edit `zz_generated.deepcopy.go` or files in `config/crd/bases/`, `config/rbac/` directly
