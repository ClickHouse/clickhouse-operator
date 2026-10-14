class RunnerLabels:
    # Labels are arch-Ncpu-Mg: N vCPUs, M GiB of memory.
    ARM_2CPU_4G = "arm-2cpu-4g"
    AMD_2CPU_4G = "amd-2cpu-4g"
    ARM_8CPU_16G = "arm-8cpu-16g"
    AMD_8CPU_16G = "amd-8cpu-16g"
    # arm-2cpu-4g plus a scoped bedrock:InvokeModel grant for the AI Code Review job.
    ARM_2CPU_4G_BEDROCK = "arm-2cpu-4g-bedrock"


PROJECT_NAME = "clickhouse-operator"
PROJECT_SLUG = "clickhouseoperator"
MAIN_BRANCH = "main"

CI_CONFIG_RUNS_ON = [RunnerLabels.ARM_2CPU_4G]

AWS_REGION = "eu-north-1"
AWS_PROFILE = "Box"

S3_ARTIFACT_BUCKET = f"{PROJECT_SLUG}-artifacts-{AWS_REGION}"
S3_REPORT_BUCKET = S3_ARTIFACT_BUCKET
CACHE_S3_PATH = f"{S3_ARTIFACT_BUCKET}/ci_cache"
ENABLE_S3_REPO_SNAPSHOT = True
ENABLE_PR_EPHEMERAL_MERGE_COMMIT = True

S3_BUCKET_TO_HTTP_ENDPOINT = {
    S3_REPORT_BUCKET: f"{S3_REPORT_BUCKET}.s3.amazonaws.com",
}

VALIDATE_FILE_PATHS = False

GH_AUTH_LAMBDA_NAME = f"{PROJECT_SLUG}-gh-token"
GH_AUTH_LAMBDA_REGION = AWS_REGION
PRAKTIKA_BASE_VENV = "praktika-runtime-0.0.1"

