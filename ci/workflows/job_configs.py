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
from praktika import Job
from ci.settings.settings import RunnerLabels

# Pre-hook that provisions the Go toolchain + CI tools (helm, kubebuilder,
# controller-gen, kustomize, golangci-lint, actionlint, crd-schema-checker,
# crd-ref-docs, envtest assets) from an S3 cache. Used by every Go job so the
# tools are not baked into the AMI — see ci/jobs/go_env.py.
_GO_ENV_PREHOOK = "python3 ci/jobs/go_env.py"


class JobConfigs:
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
        pre_hooks=[_GO_ENV_PREHOOK],
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
            "./hack",
            "./go.mod",
            "./go.sum",
            "./Makefile",
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
        pre_hooks=[_GO_ENV_PREHOOK],
        digest_config=_GO_CODE_DIGEST,
    )

    # ci.yaml :: fuzz_specs. Go-only (two 60s fuzz runs).
    fuzz_specs = Job.Config(
        name="Fuzz Specs",
        runs_on=[RunnerLabels.SMALL_ARM],
        command="make fuzz",
        timeout=20 * 60,
        pre_hooks=[_GO_ENV_PREHOOK],
        digest_config=_GO_CODE_DIGEST,
    )

    # ci.yaml :: lint. golangci-lint/codespell/actionlint are installed by the
    # Makefile into ./bin using the warm Go/pip caches from the go-env pre-hook.
    # Runs on a medium runner because golangci-lint over the whole module is
    # memory-hungry.
    lint = Job.Config(
        name="Lint",
        runs_on=[RunnerLabels.MEDIUM_ARM],
        command=(
            "go mod tidy && git diff --exit-code && "
            "make generate && git diff --exit-code && "
            "make manifests && git diff --exit-code && "
            "make lint"
        ),
        timeout=15 * 60,
        pre_hooks=[_GO_ENV_PREHOOK],
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
        pre_hooks=[_GO_ENV_PREHOOK],
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
        pre_hooks=[_GO_ENV_PREHOOK],
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
    # ClickHouse version, no OLM). Runs on amd-medium (c7a.4xlarge) to match the
    # existing self-hosted e2e placement and to pull amd64 ClickHouse images, as
    # the GitHub job does. Needs a working Docker daemon on the runner; ci/jobs/
    # compat_e2e.py installs Kind + kubectl and creates the cluster in-script.
    # Go/helm come from the go-env pre-hook. Once this is green on a real runner,
    # fan the full matrix out with Job.parametrize (see ci/CI_YAML_MIGRATION.md
    # §3.4); the `operator-upgrade` variant additionally needs `git fetch --tags`.
    compat_e2e_test = Job.Config(
        name="Compat E2E (maximal-k8s-all-deploy-methods)",
        runs_on=[RunnerLabels.MEDIUM_AMD],
        command=(
            "K8S_IMAGE=v1.36.1 "
            "CLICKHOUSE_VERSION=26.7.5.10 "
            "DEPLOY_TARGET=test-compat-e2e "
            "python3 ci/jobs/compat_e2e.py"
        ),
        timeout=60 * 60,
        pre_hooks=[_GO_ENV_PREHOOK],
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
