from praktika import Workflow
from ci.workflows.job_configs import JobConfigs
from ci.jobs.filter_job_hook import should_skip_job


WORKFLOWS = [
    Workflow.Config(
        name="PR",
        event=Workflow.Event.PULL_REQUEST,
        base_branches=["main"],
        enable_job_filtering_by_changes=True,
        workflow_filter_hooks=[should_skip_job],
        pre_hooks=[
            # Populate the Go toolchain S3 cache once per run (if missing) so the
            # Go jobs' pre-hooks only download instead of each rebuilding on a miss.
            "python3 ci/jobs/go_env.py ensure",
        ],
        jobs=[
            JobConfigs.vale_linter,
            JobConfigs.doc_links,
            JobConfigs.api_reference_generated,
            JobConfigs.build_and_test,
            JobConfigs.fuzz_specs,
            JobConfigs.lint,
            JobConfigs.helm_test,
            JobConfigs.check_crd_compat,
            JobConfigs.code_review,
        ],
        enable_cache=True,
        enable_report=True,
        enable_gh_summary_comment=True,
        enable_exit_code_result=True,
        enable_merge_ready_status=True,
    )
]
