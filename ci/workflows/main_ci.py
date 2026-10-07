from praktika import Workflow
from ci.workflows.job_configs import JobConfigs, GO_ENV_ARM_ARTIFACT


WORKFLOWS = [
    Workflow.Config(
        name="Main",
        event=Workflow.Event.PUSH,
        branches=["main"],
        artifacts=[GO_ENV_ARM_ARTIFACT],
        jobs=[
            # Build the arm Go toolchain bundle once; the Go jobs below `require`
            # it and extract it in their pre-hook. (Main has no amd Go jobs.)
            JobConfigs.prepare_go_env_arm,
            JobConfigs.vale_linter,
            JobConfigs.doc_links,
            JobConfigs.api_reference_generated,
            JobConfigs.build_and_test,
            JobConfigs.fuzz_specs,
            JobConfigs.lint,
            JobConfigs.helm_test,
        ],
        enable_cache=True,
        enable_report=True,
        enable_exit_code_result=True,
    )
]
