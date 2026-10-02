from praktika import Workflow
from ci.workflows.job_configs import JobConfigs


WORKFLOWS = [
    Workflow.Config(
        name="Pull Request CI",
        event=Workflow.Event.PULL_REQUEST,
        base_branches=["main"],
        enable_job_filtering_by_changes=True,
        jobs=[
            JobConfigs.vale_linter,
            JobConfigs.doc_links,
            JobConfigs.api_reference_generated,
        ],
        enable_cache=True,
        enable_report=True,
        enable_gh_summary_comment=True,
        enable_exit_code_result=True,
    )
]
