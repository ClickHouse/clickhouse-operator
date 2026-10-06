from praktika import Workflow
from ci.workflows.job_configs import JobConfigs


WORKFLOWS = [
    Workflow.Config(
        name="Main",
        event=Workflow.Event.PUSH,
        branches=["main"],
        jobs=[
            JobConfigs.vale_linter,
            JobConfigs.doc_links,
            JobConfigs.api_reference_generated,
            JobConfigs.build_and_test,
            JobConfigs.fuzz_specs,
            JobConfigs.lint,
            JobConfigs.helm_test,
        ],
        # Populate the Go toolchain S3 cache once per run (if missing) so the Go
        # jobs' pre-hooks only download instead of each rebuilding on a miss.
        pre_hooks=["python3 ci/jobs/go_env.py ensure"],
        enable_cache=True,
        enable_report=True,
        enable_exit_code_result=True,
    )
]
