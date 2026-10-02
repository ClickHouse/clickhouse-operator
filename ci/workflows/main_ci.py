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
        ],
        enable_cache=True,
        enable_report=True,
        enable_exit_code_result=True,
    )
]
