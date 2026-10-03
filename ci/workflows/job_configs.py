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
    api_reference_generated = Job.Config(
        name="API Reference Generated",
        runs_on=[RunnerLabels.SMALL_ARM],
        command="make docs-generate-api-ref && git diff --exit-code docs/",
        timeout=15 * 60,
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
            # envtest K8s assets are baked into the AMI at /opt/kubebuilder-envtest
            # (see projects.py _go_ci_tools_component). Seed ./bin/k8s from there
            # when present so `make test-ci`'s `setup-envtest use` is an offline
            # hit; on a non-baked runner (e.g. local) the dir is absent and
            # setup-envtest downloads as usual. Works both ways.
            "if [ -d /opt/kubebuilder-envtest/k8s ]; then mkdir -p bin && cp -rn /opt/kubebuilder-envtest/k8s bin/; fi && "
            "go build -v cmd/main.go && make test-ci"
        ),
        timeout=25 * 60,
        digest_config=_GO_CODE_DIGEST,
    )

    # ci.yaml :: fuzz_specs. Go-only (two 60s fuzz runs); no new tooling.
    fuzz_specs = Job.Config(
        name="Fuzz Specs",
        runs_on=[RunnerLabels.SMALL_ARM],
        command="make fuzz",
        timeout=20 * 60,
        digest_config=_GO_CODE_DIGEST,
    )

    # ci.yaml :: lint. golangci-lint/codespell/actionlint are installed by the
    # Makefile into ./bin; their builds + pip cache are pre-warmed in the image
    # (ci/infrastructure/projects.py _go_ci_tools_component). Runs on a medium
    # runner because golangci-lint over the whole module is memory-hungry.
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
        digest_config=_GO_CODE_DIGEST,
    )

    # ci.yaml :: helm-test. helm + kubebuilder are baked into the image; kustomize
    # self-installs via go-install-tool (cache pre-warmed). KUBEBUILDER points the
    # Makefile at the baked binary instead of re-downloading it.
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
            "--prompt ./ci/prompts/code_review.md"
        ),
        allow_failure=True,
        enable_gh_auth=True,
    )
