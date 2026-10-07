"""
Central registry of reusable Job.Config definitions.

Workflows import this class and reference the jobs they need, e.g.:

    from ci.workflows.job_configs import JobConfigs
    ...
    jobs=[JobConfigs.vale_linter, JobConfigs.doc_links]

Keeping every Job.Config in one place avoids duplicating job definitions across
workflow files. praktika's parser reads job attributes into a separate
per-workflow config and never mutates these instances, so the same Job.Config
can be shared by multiple workflows safely.

This module intentionally exposes no WORKFLOWS, so praktika's workflow scan
skips it.
"""
import json
from pathlib import Path

from praktika import Artifact, Job
from ci.settings.settings import RunnerLabels


def _clickhouse_versions():
    """Latest + supported ClickHouse versions from the repo's single source of
    truth (test/supported/versions.json), mirroring ci.yaml's jq: keep the
    highest patch per major.minor, sorted descending. Returns (latest, supported)
    where `supported` is a comma-joined list (latest first)."""
    path = Path(__file__).resolve().parents[2] / "test" / "supported" / "versions.json"
    data = json.loads(path.read_text())
    best = {}  # (major, minor) -> (numeric-parts tuple, version string)
    for version in (v for group in data.values() for v in group):
        parts = tuple(int(x) for x in version.split("."))
        mm = parts[:2]
        if mm not in best or parts > best[mm][0]:
            best[mm] = (parts, version)
    ordered = [v for _, v in sorted(best.values(), reverse=True)]
    return ordered[0], ",".join(ordered)


_CH_LATEST, _CH_SUPPORTED = _clickhouse_versions()

# Consumer pre-hook: extract the go-env bundle delivered as a required artifact
# (falls back to self-provisioning from S3/build). The bundle holds the Go SDK +
# helm/kubebuilder/controller-gen/kustomize/golangci-lint/actionlint/
# crd-schema-checker/crd-ref-docs + warm Go/pip caches + envtest assets, so no Go
# tooling is baked into the AMI. See ci/jobs/go_env.py.
_GO_ENV_INSTALL = "python3 ci/jobs/go_env.py install"

# Per-arch go-env bundle, built once per run by the Prepare Go Env jobs and
# consumed (via `requires`) by every Go job of that arch.
GO_ENV_ARM_ARTIFACT = Artifact.Config(
    name="go-env-arm", type=Artifact.Type.S3, path="ci/tmp/go-env-arm.tar.zst"
)
GO_ENV_AMD_ARTIFACT = Artifact.Config(
    name="go-env-amd", type=Artifact.Type.S3, path="ci/tmp/go-env-amd.tar.zst"
)


# ci.yaml :: compat-e2e-test matrix (5 variants). Each entry maps to the three
# env vars ci/jobs/compat_e2e.py consumes; the first field is the GitHub matrix
# `name`, kept as the praktika job-name suffix via Job.ParamSet(parameter=...).
# fetch_tags mirrors ci.yaml's checkout `fetch-tags: true` — only the upgrade
# variant needs release tags (it deploys the latest release, then upgrades).
# ClickHouse versions come from test/supported/versions.json (single source of
# truth, auto-updated) — see _clickhouse_versions(), matching ci.yaml #355.
_COMPAT_E2E_MATRIX = [
    # (name, k8s node image, clickhouse version(s), make target, fetch_tags)
    ("minimal-k8s-all-deploy-methods", "v1.28.15", _CH_LATEST, "test-compat-e2e", False),
    ("maximal-k8s-all-deploy-methods", "v1.36.1", _CH_LATEST, "test-compat-e2e", False),
    ("olm-deploy-method", "v1.28.15", _CH_LATEST, "test-compat-e2e-olm", False),
    (
        "supported-clickhouse-compatibility",
        "v1.30.13",
        f"{_CH_SUPPORTED},{_CH_LATEST}-distroless",
        "test-compat-e2e-manifest",
        False,
    ),
    ("operator-upgrade", "v1.30.13", _CH_LATEST, "test-compat-e2e-upgrade", True),
]


def _compat_e2e_command(k8s_image, clickhouse_version, deploy_target, fetch_tags):
    cmd = (
        f"K8S_IMAGE={k8s_image} "
        f"CLICKHOUSE_VERSION={clickhouse_version} "
        f"DEPLOY_TARGET={deploy_target} "
    )
    if fetch_tags:
        cmd += "FETCH_TAGS=1 "
    return cmd + "python3 ci/jobs/compat_e2e.py"


# ci.yaml :: e2e-test — number of shards. Each shard runs the full e2e suite
# filtered to its slice; sharding is round-robin by spec-name hash in the Go
# harness, so no e2e-shard-plan job / cross-run timings artifact is needed
# (ci.yaml's plan-based mode still exists for GitHub Actions — see sharding.go).
_E2E_SHARD_TOTAL = 4
# e2e Kind node image, matching ci.yaml's pin.
_E2E_K8S_IMAGE = "v1.34.3"


def _e2e_command(shard_index):
    return (
        f"E2E_SHARD={shard_index}/{_E2E_SHARD_TOTAL} "
        f"K8S_IMAGE={_E2E_K8S_IMAGE} "
        "python3 ci/jobs/e2e.py"
    )


class JobConfigs:
    # Prep jobs rebuild the bundle only when the tool version sources or the
    # provisioning scripts change (praktika job cache + the go-env S3 cache both
    # key off this). Each builds natively on its arch and publishes the tarball.
    _GO_ENV_DIGEST = Job.CacheDigestConfig(
        include_paths=[
            "./go.mod",
            "./go.sum",
            "./Makefile",
            "./ci/jobs/go_env.py",
            "./ci/jobs/s3_cache.py",
        ],
    )

    prepare_go_env_arm = Job.Config(
        name="Prepare Go Env (arm)",
        runs_on=[RunnerLabels.SMALL_ARM],
        command=f"python3 ci/jobs/go_env.py prepare {GO_ENV_ARM_ARTIFACT.path}",
        provides=[GO_ENV_ARM_ARTIFACT.name],
        timeout=30 * 60,
        digest_config=_GO_ENV_DIGEST,
    )
    prepare_go_env_amd = Job.Config(
        name="Prepare Go Env (amd)",
        runs_on=[RunnerLabels.SMALL_AMD],
        command=f"python3 ci/jobs/go_env.py prepare {GO_ENV_AMD_ARTIFACT.path}",
        provides=[GO_ENV_AMD_ARTIFACT.name],
        timeout=30 * 60,
        digest_config=_GO_ENV_DIGEST,
    )
    # dependabot-regenerate.yaml, migrated. Re-derives the generated artifacts
    # after a Dependabot go.mod/go.sum bump and pushes them onto the PR branch.
    # Applicability (actor is dependabot[bot], same-repo, go.mod/go.sum touched)
    # is enforced by the workflow filter hook (ci/jobs/filter_job_hook.py), so
    # this job is listed in every PR run but only executes for a bot bump.
    # Needs the full Go toolchain (controller-gen/kubebuilder/crd-ref-docs) from
    # the go-env pre-hook, hence `requires` the arm bundle like the other Go jobs.
    # enable_gh_auth so it can mint a contents:write token to push; allow_failure
    # so a regenerate/push hiccup never blocks merge — Lint still independently
    # fails on any stale generated file, so correctness stays guarded. The digest
    # keys on go.mod/go.sum so an unrelated PR that somehow reaches here (it
    # won't, the hook gates it) is change-filtered out too.
    dependabot_regenerate = Job.Config(
        name="Dependabot Regenerate",
        runs_on=[RunnerLabels.SMALL_ARM],
        command="python3 ci/jobs/dependabot_regenerate.py",
        timeout=20 * 60,
        allow_failure=True,
        enable_gh_auth=True,
        pre_hooks=[_GO_ENV_INSTALL],
        requires=[GO_ENV_ARM_ARTIFACT.name],
        digest_config=Job.CacheDigestConfig(
            include_paths=["./go.mod", "./go.sum"],
        ),
    )

    # --- Documentation lint (migrated from .github/workflows/docs-lint.yaml) ---
    # The lint toolchain (Vale, Node + linkspector, Go, pre-warmed crd-ref-docs)
    # is baked into the runner image by ci/infrastructure/projects.py
    # (_doc_lint_tools_component), so these jobs just run the Makefile targets.

    # docs-lint.yaml :: vale (vale-linter)
    vale_linter = Job.Config(
        name="Vale Linter",
        runs_on=[RunnerLabels.SMALL_ARM],
        command="make docs-lint-vale",
        timeout=10 * 60,
        digest_config=Job.CacheDigestConfig(
            include_paths=["./docs", "./*.md", "./.vale.ini"],
        ),
    )

    # docs-lint.yaml :: doc-links (Doc links)
    doc_links = Job.Config(
        name="Doc Links",
        runs_on=[RunnerLabels.SMALL_ARM],
        command="make docs-link-check",
        timeout=10 * 60,
        digest_config=Job.CacheDigestConfig(
            include_paths=["./docs", "./.linkspector.yml"],
        ),
    )

    # docs-lint.yaml :: api-reference-generated (API Reference Generated)
    # Needs Go + crd-ref-docs, provisioned by the go-env pre-hook.
    api_reference_generated = Job.Config(
        name="API Reference Generated",
        runs_on=[RunnerLabels.SMALL_ARM],
        command="make docs-generate-api-ref && git diff --exit-code docs/",
        timeout=15 * 60,
        pre_hooks=[_GO_ENV_INSTALL],
        requires=[GO_ENV_ARM_ARTIFACT.name],
        digest_config=Job.CacheDigestConfig(
            include_paths=[
                "./api/v1alpha1",
                "./docs/templates",
                "./docs/reference",
                "./Makefile",
                "./go.mod",
            ],
        ),
    )

    # --- Operator CI (migrated job-by-job from .github/workflows/ci.yaml) ---
    # Go source paths that gate the Go jobs below — the praktika equivalent of
    # ci.yaml's `changes` non-docs paths-filter. Shared (read-only) across jobs.
    _GO_CODE_DIGEST = Job.CacheDigestConfig(
        include_paths=[
            "./api",
            "./cmd",
            "./internal",
            "./test",
            "./tools",
            "./config",
            "./hack",
            "./go.mod",
            "./go.sum",
            "./Makefile",
            "./.golangci.yml",
        ],
    )

    # ci.yaml :: build_and_test. Go is baked into the runner image; controller-gen
    # and setup-envtest self-install via `go-install-tool`, and envtest downloads
    # the kubebuilder assets (K8s 1.36.2) — all in-process, no Docker/cluster.
    # Runs on a medium runner because `go test -race` across the suite is heavier
    # than the small pool's 4 GB. The dorny/test-reporter step is dropped; the
    # job's exit code drives pass/fail (enable_exit_code_result).
    build_and_test = Job.Config(
        name="Build and Unit Tests",
        runs_on=[RunnerLabels.MEDIUM_ARM],
        command=(
            # The go-env pre-hook caches the envtest K8s assets at
            # /opt/ci-go/envtest. Seed ./bin/k8s from there so `make test-ci`'s
            # `setup-envtest use` is an offline hit; absent (e.g. local) it just
            # downloads as usual. Works both ways.
            "if [ -d /opt/ci-go/envtest/k8s ]; then mkdir -p bin && cp -rn /opt/ci-go/envtest/k8s bin/; fi && "
            "go build -v cmd/main.go && make test-ci"
        ),
        timeout=25 * 60,
        pre_hooks=[_GO_ENV_INSTALL],
        requires=[GO_ENV_ARM_ARTIFACT.name],
        digest_config=_GO_CODE_DIGEST,
    )

    # ci.yaml :: fuzz_specs. Go-only (two 60s fuzz runs).
    fuzz_specs = Job.Config(
        name="Fuzz Specs",
        runs_on=[RunnerLabels.SMALL_ARM],
        command="make fuzz",
        timeout=20 * 60,
        pre_hooks=[_GO_ENV_INSTALL],
        requires=[GO_ENV_ARM_ARTIFACT.name],
        digest_config=_GO_CODE_DIGEST,
    )

    # ci.yaml :: lint. golangci-lint/codespell/actionlint are installed by the
    # Makefile into ./bin using the warm Go/pip caches from the go-env pre-hook.
    # The driver (ci/jobs/lint.py) runs the regeneration gates + the three linters
    # as separate sub-results (golangci-lint issues become a per-issue table).
    # Runs on a medium runner because golangci-lint over the whole module is
    # memory-hungry.
    lint = Job.Config(
        name="Lint",
        runs_on=[RunnerLabels.MEDIUM_ARM],
        command="python3 ci/jobs/lint.py",
        timeout=15 * 60,
        pre_hooks=[_GO_ENV_INSTALL],
        requires=[GO_ENV_ARM_ARTIFACT.name],
        digest_config=_GO_CODE_DIGEST,
    )

    # ci.yaml :: helm-test. helm + kubebuilder come from the go-env pre-hook (on
    # PATH); KUBEBUILDER points the Makefile at the provisioned binary instead of
    # re-downloading it.
    helm_test = Job.Config(
        name="Helm Test",
        runs_on=[RunnerLabels.SMALL_ARM],
        command=(
            "make generate-helmchart-ci KUBEBUILDER=/usr/local/bin/kubebuilder && "
            "git diff --exit-code dist/chart/ dist/chart-cluster/ && "
            "make build-helmchart-dependencies && "
            "helm lint ./dist/chart && "
            "make lint-cluster-chart"
        ),
        timeout=15 * 60,
        pre_hooks=[_GO_ENV_INSTALL],
        requires=[GO_ENV_ARM_ARTIFACT.name],
        digest_config=Job.CacheDigestConfig(
            include_paths=[
                "./api",
                "./config",
                "./dist/chart",
                "./dist/chart-cluster",
                "./tools/gen-cluster-chart",
                "./go.mod",
                "./go.sum",
                "./Makefile",
            ],
        ),
    )

    # ci.yaml :: check-crd-compat. PR-only, advisory (allow_failure). The
    # crd-breaking-change label gate is a workflow filter hook
    # (ci/jobs/filter_job_hook.py); the helper fetches the base branch (praktika
    # checks out an ephemeral merge commit with no base history) and runs the
    # check. crd-schema-checker self-installs via go-install-tool (cache
    # pre-warmed).
    check_crd_compat = Job.Config(
        name="Check CRD Compatibility",
        runs_on=[RunnerLabels.SMALL_ARM],
        command="python3 ci/jobs/check_crd_compat.py",
        timeout=15 * 60,
        allow_failure=True,
        # On ENABLE_S3_REPO_SNAPSHOT runs there is no `origin`, so the checker
        # falls back to `gh api` for the base manifests — which needs auth, else
        # it exits 4 and the job ERRORs instead of actually checking CRDs.
        enable_gh_auth=True,
        pre_hooks=[_GO_ENV_INSTALL],
        requires=[GO_ENV_ARM_ARTIFACT.name],
        digest_config=Job.CacheDigestConfig(
            include_paths=[
                "./api",
                "./config/crd",
                "./ci/jobs/check_crd_compat.py",
                "./go.mod",
                "./go.sum",
                "./Makefile",
            ],
        ),
    )

    # ci.yaml :: compat-e2e-test (SPIKE — one matrix variant of five).
    # Proof-of-concept for the Docker+Kind path on praktika: the single
    # `maximal-k8s-all-deploy-methods` variant (newest Kind node image, one
    # ci.yaml :: compat-e2e-test — the full 5-variant matrix, fanned out with
    # Job.parametrize (see _COMPAT_E2E_MATRIX / _compat_e2e_command above). Each
    # variant is one Job.Config differing only in the K8S_IMAGE/CLICKHOUSE_VERSION/
    # DEPLOY_TARGET it passes to ci/jobs/compat_e2e.py. Runs on amd-medium
    # (c7a.4xlarge) to match the existing self-hosted e2e placement and to pull
    # amd64 ClickHouse images, as the GitHub job does. Needs a working Docker
    # daemon on the runner; compat_e2e.py installs Kind + kubectl, creates the
    # cluster, pre-pulls images, runs the suite, and renders per-spec results.
    # Go/helm come from the go-env artifact (amd). The base `command` is a
    # placeholder — parametrize overrides it per variant. This attribute is a LIST
    # of Job.Config; spread it into a workflow's jobs with `*JobConfigs...`.
    compat_e2e_test = Job.Config(
        name="Compat E2E",
        runs_on=[RunnerLabels.MEDIUM_AMD],
        command=_compat_e2e_command(*_COMPAT_E2E_MATRIX[0][1:]),  # overridden per variant
        timeout=60 * 60,
        # The operator-upgrade variant resolves the latest release tag via `gh api`
        # (FETCH_TAGS=1) from a snapshot checkout with no `origin` remote; without
        # pre-authenticated gh that call exits 4. parametrize/ParamSet can't set
        # enable_gh_auth per variant, so it's set on the base for the whole matrix.
        enable_gh_auth=True,
        pre_hooks=[_GO_ENV_INSTALL],
        requires=[GO_ENV_AMD_ARTIFACT.name],
        digest_config=Job.CacheDigestConfig(
            include_paths=[
                "./api",
                "./cmd",
                "./internal",
                "./config",
                "./test",
                "./tools",
                "./ci/kind-cluster.config",
                "./ci/jobs/compat_e2e.py",
                "./ci/jobs/junit_result.py",
                "./go.mod",
                "./go.sum",
                "./Makefile",
            ],
        ),
    ).parametrize(
        *[
            Job.ParamSet(
                parameter=name,
                command=_compat_e2e_command(img, ver, tgt, fetch),
            )
            for (name, img, ver, tgt, fetch) in _COMPAT_E2E_MATRIX
        ]
    )

    # ci.yaml :: e2e-test — the full e2e suite, split into _E2E_SHARD_TOTAL shards
    # fanned out with Job.parametrize. Each shard passes E2E_SHARD="n/total" to
    # ci/jobs/e2e.py; the Go harness assigns specs round-robin by hash, so there
    # is no e2e-shard-plan job. Same Docker+Kind runner (amd-medium) and go-env
    # (amd) as compat-e2e; e2e.py creates the cluster, runs `make test-e2e`,
    # collects kind/kubectl diagnostics on failure, and renders per-spec results.
    # Base `command` is a placeholder — parametrize overrides it per shard. This
    # attribute is a LIST of Job.Config; spread it with `*JobConfigs...`.
    e2e_test = Job.Config(
        name="E2E Tests",
        runs_on=[RunnerLabels.MEDIUM_AMD],
        command=_e2e_command(1),  # overridden per shard
        timeout=45 * 60,
        pre_hooks=[_GO_ENV_INSTALL],
        requires=[GO_ENV_AMD_ARTIFACT.name],
        digest_config=Job.CacheDigestConfig(
            include_paths=[
                "./api",
                "./cmd",
                "./internal",
                "./config",
                "./test",
                "./ci/kind-cluster.config",
                "./ci/jobs/e2e.py",
                "./ci/jobs/kind_env.py",
                "./ci/jobs/junit_result.py",
                "./go.mod",
                "./go.sum",
                "./Makefile",
            ],
        ),
    ).parametrize(
        *[
            Job.ParamSet(
                parameter=f"{shard}/{_E2E_SHARD_TOTAL}",
                command=_e2e_command(shard),
            )
            for shard in range(1, _E2E_SHARD_TOTAL + 1)
        ]
    )

    # ci.yaml :: bundle. Builds the OLM bundle image and runs operator-sdk
    # scorecard against a throwaway Kind cluster — ci/jobs/bundle.py. Same
    # Docker+Kind runner (amd-medium) and go-env (amd) as compat/e2e; the bundle
    # image is manifest-only (arch-independent). operator-sdk self-installs via the
    # Makefile targets. Verdict is the scorecard exit code.
    bundle = Job.Config(
        name="Bundle",
        runs_on=[RunnerLabels.MEDIUM_AMD],
        command="VERSION=0.0.1 python3 ci/jobs/bundle.py",
        timeout=30 * 60,
        pre_hooks=[_GO_ENV_INSTALL],
        requires=[GO_ENV_AMD_ARTIFACT.name],
        digest_config=Job.CacheDigestConfig(
            include_paths=[
                "./api",
                "./cmd",
                "./internal",
                "./config",
                "./bundle.Dockerfile",
                "./ci/jobs/bundle.py",
                "./ci/jobs/kind_env.py",
                "./go.mod",
                "./go.sum",
                "./Makefile",
            ],
        ),
    )

    # --- AI code review ---
    # `praktika review` consults an OpenAI model on Bedrock and posts a summary
    # plus inline findings, managing its own review threads. It runs on the
    # dedicated arm-small-bedrock pool (the only one granted bedrock:InvokeModel).
    # allow_failure so a review hiccup never blocks merge; enable_gh_auth so the
    # job can post comments and resolve threads.
    code_review = Job.Config(
        name="Code Review",
        runs_on=[RunnerLabels.SMALL_ARM_BEDROCK],
        command=(
            "python3 -I -m praktika review --provider bedrock-openai "
            "--model global.openai.gpt-5.6-sol --reasoning-effort high "
            "--prompt ./ci/prompts/code_review.md --fail-for-draft-pr"
        ),
        allow_failure=True,
        enable_gh_auth=True,
    )
