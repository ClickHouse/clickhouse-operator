# clickhouse-operator code-review guidance

Project-specific guidance for the `praktika review` job. This is appended to the
fixed review protocol; keep it focused on what a generic reviewer would not know
about this repository.

## What this repo is
A Kubernetes operator for managing ClickHouse database clusters and ClickHouse
Keeper clusters, built with Kubebuilder and controller-runtime. It manages two
CRDs — `ClickHouseCluster` and `KeeperCluster` — both `v1alpha1` in the
`clickhouse.com` API group. The code is Go; reconcilers live under
`internal/controller/`, the API types under `api/v1alpha1/`.

## What to prioritise
- **Reconciliation correctness**: reconcilers must be idempotent and converge.
  Flag non-deterministic desired-state construction, missing requeue on
  transient errors, swallowed errors, and status updates that don't reflect
  actual cluster state. Watch for unbounded or hot-looping requeues.
- **StatefulSet / PVC / ordinal handling**: ClickHouse shards+replicas and
  Keeper members are stateful. Flag changes that could reorder, renumber, or
  orphan pods/PVCs, or that mutate immutable StatefulSet fields (forcing a
  recreate that drops data). Scale-down must not silently delete persistent data.
- **Keeper quorum & safety**: adding/removing Keeper members and leadership
  changes must preserve raft quorum. Flag graceful-shutdown / leadership-yield
  ordering bugs and anything that could lose quorum during a rollout.
- **API / CRD compatibility**: `api/v1alpha1` is a published API. Flag
  breaking changes to existing fields, defaults that change behaviour on
  existing clusters, and validation that would reject previously valid specs.
  Generated artifacts (deepcopy, CRDs, Helm chart, API-ref docs) must stay in
  sync with the types — call out when a type change isn't regenerated.
- **Concurrency**: controller-runtime caches are shared; never mutate objects
  returned from the client cache without a DeepCopy. Flag data races on
  shared maps/fields in the manager.
- **Security/RBAC**: flag over-broad RBAC in generated roles, secrets logged or
  put into env/args, and privilege-escalating pod security settings.

## What to skip
- Formatting, import ordering, and lint nits — `make lint-fix` / other jobs
  cover these.
- Speculative refactors unrelated to the diff.
- Generated files' contents line-by-line (`zz_generated.*`, CRD bases, chart
  templates, `docs/reference/api-reference.mdx`) — instead verify they are
  consistent with the source types, not whether their formatting is pretty.

## Style
- Prefer a small number of high-signal findings over many low-value ones.
- When you flag something, say concretely how it fails (inputs -> wrong outcome),
  e.g. "scaling replicas 3->2 here deletes the PVC for replica-2, losing its data".
