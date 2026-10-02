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
