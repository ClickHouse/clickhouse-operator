# Go cache: recommendations for the remaining regressions

Follow-up to [CACHE_MIGRATION_GAPS.md](./CACHE_MIGRATION_GAPS.md). Praktika has no
`actions/cache`-style per-run restore/save, so three Go caches are cold on any
job that actually runs: the operator **module cache** (`pkg/mod`), the operator
**build cache** (`go-build`), and the **golangci-lint analysis cache**. These are
the right tools to close that gap — all are shared services, which suit Praktika
better than reimplementing per-run file caching.

No single "sccache for Go" exists (sccache does not support Go), but Go's
official toolchain hook plus a module proxy cover it.

## Solutions

| Regression | Solution | Why it fits |
|---|---|---|
| Build cache (`go-build`) | **`GOCACHEPROG`** → S3-backed cache server | Official Go hook (stable since Go 1.24; we run 1.27). Content-addressed, shared across all runners/branches — a strict upgrade over GHA's branch-scoped tarball. The real sccache analogue. |
| Module cache (`pkg/mod`) | **GOPROXY** (self-hosted module proxy) → `GOPROXY=http://<proxy>,direct` | A warm shared proxy, auto-fresh, no key to invalidate and no bake needed. Also speeds the tool `go install`s currently baked into the AMI. |
| golangci-lint analysis | S3 save/restore of `~/.cache/golangci-lint`, or leave cold | No remote-backend protocol exists for it. Its Go type-checking layer still benefits from `GOCACHEPROG` via `GOCACHE`. |

## Affected jobs

Defined in `ci/workflows/job_configs.py`.

| Job | Compiles operator code | Benefits from |
|---|---|---|
| **Build and Unit Tests** (`go build` + `make test-ci`) | yes (heavy, `-race`) | `GOCACHEPROG`, GOPROXY |
| **Fuzz Specs** (`make fuzz`) | yes | `GOCACHEPROG`, GOPROXY |
| **Lint** (`make generate`/`manifests`/`lint`) | yes | `GOCACHEPROG`, GOPROXY, golangci cache |
| API Reference Generated (`make docs-generate-api-ref`) | no — tool only (crd-ref-docs) | GOPROXY (for go install) |
| Helm Test (kubebuilder/kustomize) | no — tools only | GOPROXY |
| Check CRD Compatibility (crd-schema-checker) | no — tool only | GOPROXY |
| Vale Linter / Doc Links / Code Review | no Go | — |

Primary targets: **Build and Unit Tests, Fuzz Specs, Lint**. The rest only
`go install` pinned tools (already AMI-baked) and gain mainly from GOPROXY.

## Suggested rollout

1. **GOPROXY first** — lowest effort, biggest freshness win: stand up a proxy
   with S3 storage, set `GOPROXY` on runners. Removes module cold-downloads and
   makes AMI tool pre-warming far less version-sync-sensitive.
2. **GOCACHEPROG next** — set `GOCACHEPROG` on the three compile-heavy jobs,
   pointed at an S3-backed cache server. Run a **local-disk tier in front of the
   remote** so a cache hit isn't gated on a network round-trip.
3. **golangci cache** — only if lint latency still matters after step 2.

## Self-hosted options & trust

- **Module proxy:** **Athens** (`gomods/athens`) is the well-known self-hosted
  standard — mature, widely deployed, S3/GCS/disk backends. (Artifactory/Nexus
  if an enterprise registry already exists.)
- **Build cache (`GOCACHEPROG`):** the hook is new (Go 1.24), so there is no
  single dominant self-hosted project yet. The minimal reference is
  `bradfitz/go-tool-cache` (`cacheserver`, S3/HTTP backends) — low stars because
  it is an **example/reference** by a Go core author demonstrating the protocol,
  not a marketed product; quality is high but it is thin on maintenance and
  you'd own it. For a well-known self-hostable alternative, **BuildBuddy**
  (open-source, Apache-2) provides a remote cache with `GOCACHEPROG` support and
  a real community. The protocol is simple enough that many orgs run a small
  (~200-line) S3-backed `cacheprog` of their own; trust comes from the hook
  being first-party, not from any one server.
- **Security:** a remote build cache serves compiled objects — a poisoning
  surface. Keep it inside our own account/S3 with CI-only access, same trust
  boundary as the runners.
