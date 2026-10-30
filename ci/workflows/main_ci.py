from praktika import Workflow
from ci.workflows.job_configs import (
    JobConfigs,
    GO_ENV_ARM_ARTIFACT,
    GO_ENV_AMD_ARTIFACT,
)


WORKFLOWS = [
    Workflow.Config(
        name="Main",
        event=Workflow.Event.PUSH,
        branches=["main", "ci/migrate-docs-lint-to-praktika"],
        artifacts=[GO_ENV_ARM_ARTIFACT, GO_ENV_AMD_ARTIFACT],
        jobs=[
            JobConfigs.prepare_go_env_arm,
            JobConfigs.prepare_go_env_amd,
            JobConfigs.vale_linter,
            JobConfigs.doc_links,
            JobConfigs.api_reference_generated,
            JobConfigs.build_and_test,
            JobConfigs.fuzz_specs,
            JobConfigs.lint,
            JobConfigs.helm_test,
            # Docker/Kind jobs (amd-8cpu-16g).
            JobConfigs.bundle,
            *JobConfigs.compat_e2e_test,
            *JobConfigs.e2e_test,
        ],
        enable_cache=True,
        enable_report=True,
        enable_exit_code_result=True,
    )
]
