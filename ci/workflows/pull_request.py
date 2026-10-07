from praktika import Workflow
from ci.workflows.job_configs import (
    JobConfigs,
    GO_ENV_ARM_ARTIFACT,
    GO_ENV_AMD_ARTIFACT,
)
from ci.jobs.filter_job_hook import should_skip_job


WORKFLOWS = [
    Workflow.Config(
        name="PR",
        event=Workflow.Event.PULL_REQUEST,
        base_branches=["main"],
        enable_job_filtering_by_changes=True,
        workflow_filter_hooks=[should_skip_job],
        artifacts=[GO_ENV_ARM_ARTIFACT, GO_ENV_AMD_ARTIFACT],
        jobs=[
            # Build the per-arch Go toolchain bundles once; the Go jobs below
            # `require` the matching one and extract it in their pre-hook.
            JobConfigs.prepare_go_env_arm,
            JobConfigs.prepare_go_env_amd,
            # Runs first (gated to Dependabot go.mod/go.sum bumps by the filter
            # hook): regenerates derived files and pushes them to the PR branch
            # before the rest of the suite evaluates the bot PR.
            JobConfigs.dependabot_regenerate,
            JobConfigs.vale_linter,
            JobConfigs.doc_links,
            JobConfigs.api_reference_generated,
            JobConfigs.build_and_test,
            JobConfigs.fuzz_specs,
            JobConfigs.lint,
            JobConfigs.helm_test,
            JobConfigs.check_crd_compat,
            JobConfigs.bundle,
            *JobConfigs.compat_e2e_test,
            *JobConfigs.e2e_test,
            JobConfigs.code_review,
        ],
        enable_cache=True,
        enable_report=True,
        enable_gh_summary_comment=True,
        enable_exit_code_result=True,
        enable_merge_ready_status=True,
    )
]
