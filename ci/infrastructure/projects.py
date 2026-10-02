from praktika.infrastructure import (
    CloudInfrastructure,
    Components,
    ImageBuilder,
    Storage,
    VPC,
)
from praktika.settings import Settings


# S3 prefixes the runner instance roles are scoped to. Both the artifact
# and the report bucket are granted (they are often the same bucket, in
# which case the duplicate collapses away).
_PROJECT_S3_PREFIXES = list(
    dict.fromkeys(
        [
            f"{Settings.S3_ARTIFACT_BUCKET}/*",
            f"{Settings.S3_REPORT_BUCKET}/*",
        ]
    )
)


# until published in pip
_PRAKTIKA_PACKAGE_BASE_URL = "https://praktika-artifacts-eu-north-1.s3.amazonaws.com/packages"
_PRAKTIKA_WHL = f"{_PRAKTIKA_PACKAGE_BASE_URL}/praktika-0.1.15-py3-none-any.whl"
_PRAKTIKA_CONTROLLER_WHL = f"{_PRAKTIKA_PACKAGE_BASE_URL}/praktika_controller-0.1.9-py3-none-any.whl"


# Toolchains baked into the runner image for the documentation-lint jobs
# (ci/workflows/pull_request.py). Kept here rather than installed per-job so
# the jobs stay fast and offline. The component below runs on both the arm64
# and x86_64 image builders, so every download is arch-aware.
_GO_VERSION = "1.27.0"  # keep in sync with go.mod
_CRD_REF_DOCS_VERSION = "v0.3.0"  # keep in sync with Makefile CRD_REF_DOCS_VERSION
_NODE_MAJOR = "20"


def _doc_lint_tools_component():
    """Build-phase Image Builder component installing the docs-lint toolchain:
    Go (for `make docs-generate-api-ref`), a pre-warmed crd-ref-docs module
    cache, Vale, and Node + linkspector."""
    commands = [
        # Resolve the Debian CPU arch once (arm64 / amd64) for arch-specific URLs.
        "arch=$(dpkg --print-architecture)",
        # --- Go toolchain (go.mod pins go 1.27.0) ---
        f'curl -fsSL "https://go.dev/dl/go{_GO_VERSION}.linux-${{arch}}.tar.gz" -o /tmp/go.tgz',
        "rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz && rm -f /tmp/go.tgz",
        "ln -sf /usr/local/go/bin/go /usr/local/bin/go",
        "ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt",
        "go version",
        # Pre-warm crd-ref-docs into the Go module + build cache so the job-time
        # `make docs-generate-api-ref` (GOBIN=./bin go install ...) resolves it
        # from cache instead of hitting the network.
        # Image Builder runs components as root with no $HOME, so Go can't
        # derive a default GOPATH/module cache; set it explicitly.
        f"HOME=/root GOPATH=/root/go GOBIN=/usr/local/bin go install github.com/elastic/crd-ref-docs@{_CRD_REF_DOCS_VERSION}",
        "crd-ref-docs version || true",
        # --- Vale (latest release, matching vale-action's default) ---
        # amd64 release assets use the `64-bit` arch token; arm64 uses `arm64`.
        'if [ "$arch" = "amd64" ]; then vale_arch="64-bit"; else vale_arch="arm64"; fi',
        "vale_ver=$(curl -fsSL https://api.github.com/repos/errata-ai/vale/releases/latest | jq -r .tag_name | sed 's/^v//')",
        'curl -fsSL "https://github.com/errata-ai/vale/releases/download/v${vale_ver}/vale_${vale_ver}_Linux_${vale_arch}.tar.gz" -o /tmp/vale.tgz',
        "tar -xzf /tmp/vale.tgz -C /usr/local/bin vale && rm -f /tmp/vale.tgz",
        "vale --version",
        # --- Node + linkspector (docs link checker) ---
        f"curl -fsSL https://deb.nodesource.com/setup_{_NODE_MAJOR}.x | bash -",
        "DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nodejs",
        "npm install -g @umbrelladocs/linkspector",
        "linkspector --version",
    ]
    return {
        "name": "docs-lint-tools",
        "platform": "Linux",
        "phase": "build",
        "description": "Install docs-lint toolchain: Go, crd-ref-docs, Vale, Node + linkspector",
        "commands": commands,
    }


def _image_builders():
    image_recipe_version = "1.0.1"
    prebuilt_venvs = [
        # The `infrastructure` extra pulls Praktika's runtime deps
        # (boto3/PyJWT/cryptography/requests) automatically; pytest is
        # an optional extra the runner needs, so list it explicitly.
        ImageBuilder.PrebuiltVenv(
            name=Settings.PRAKTIKA_BASE_VENV,
            packages=[
                "pytest>=7.0.0",
                "pytest-reportlog>=0.4.0",
                f"praktika[infrastructure] @ {_PRAKTIKA_WHL}",
            ],
            description="CI runtime venv",
        ),
    ]
    custom_components = [
        # Build-phase: install the docs-lint toolchain into the AMI.
        _doc_lint_tools_component(),
        # Test-phase: validate the image after build.
        Components.create_image_test_component(
            name="project-image-test",
            commands=[
                "test -d /opt/praktika/work",
                "test -w /opt/praktika/work",
                "go version",
                "vale --version",
                "linkspector --version",
            ],
        ),
    ]
    return [
        Components.create_ubuntu_image_builder_config(
            name="ci-arm64-image",
            version=image_recipe_version,
            controller_package=_PRAKTIKA_CONTROLLER_WHL,
            prebuilt_venvs=prebuilt_venvs,
            components=custom_components,
            instance_types=["t4g.small"],
        ),
        Components.create_ubuntu_image_builder_config(
            name="ci-x86_64-image",
            version=image_recipe_version,
            controller_package=_PRAKTIKA_CONTROLLER_WHL,
            prebuilt_venvs=prebuilt_venvs,
            components=custom_components,
            instance_types=["t3.small"],
        ),
    ]


_GH_TOKEN_MINTER = Components.GitHubTokenMinter(
    permissions={
        "checks": "write",
        "contents": "write",
        "issues": "write",
        "metadata": "read",
        "pages": "write",
        "pull_requests": "write",
        "statuses": "write",
    },
    repositories=[Settings.PROJECT_NAME],
)
_IMAGE_BUILDERS = _image_builders()
_IMAGE_BUILDERS_BY_NAME = {builder.name: builder for builder in _IMAGE_BUILDERS}


# The Code Review job (`praktika review`) calls an OpenAI model on Bedrock via
# the Converse API, which requires bedrock:InvokeModel. Only the dedicated
# code-review runner pool below carries this grant (scoped to Bedrock
# foundation-model / inference-profile resources) — general job runners stay
# Bedrock-less, so an arbitrary job cannot reach the model API.
_CODE_REVIEW_BEDROCK_IAM_STATEMENT = {
    "Sid": "BedrockRuntimeInference",
    "Effect": "Allow",
    "Action": ["bedrock:InvokeModel"],
    "Resource": [
        "arn:aws:bedrock:*::foundation-model/*",
        "arn:aws:bedrock:*:*:inference-profile/*",
    ],
}


PROJECTS = [
    CloudInfrastructure.Config(
        name=Settings.PROJECT_NAME,
        min_praktika_version="0.1.15",
        vpcs=[
            VPC.Config(
                subnets=[
                    VPC.Subnet(availability_zone="eu-north-1a"),
                ],
            )
        ],
        storages=[
            Storage.Config(
                name="artifacts-eu-north-1",
                retention_days=30,
                public=True,
            ),
        ],
        report_pages=[Components.report_page_config],
        image_builders=_IMAGE_BUILDERS,
        github_token_minters=[_GH_TOKEN_MINTER],
        orchestrator_pool=Components.OrchestratorPool(
            instance_type="t4g.xlarge",
            scaling=Components.OrchestratorPool.Scaling.Auto,
            size=0,
            max_size=50,
            volume_size_gb=100,
            capacity_reserve=1,
            image_builder=_IMAGE_BUILDERS_BY_NAME["ci-arm64-image"],
            ext={"allowed_push_branches": ['NA'], "allowed_pr_base_branches": ['main'], "allowed_users": ['maxknv']},
        ),
        runner_pools=[
            Components.RunnerPool(
                name="arm-small",
                instance_type="t4g.medium",
                scaling=Components.RunnerPool.Scaling.Auto,
                size=0,
                max_size=50,
                volume_size_gb=100,
                image_builder=_IMAGE_BUILDERS_BY_NAME["ci-arm64-image"],
                allowed_ssm_parameters=[],
                allowed_secrets=[],
                allowed_s3_prefixes=_PROJECT_S3_PREFIXES,
                allow_all_ssm_parameters=False,
                allow_all_secrets=False,
                allow_all_s3_prefixes=False,
                allow_ssm_debug=False,
            ),
            Components.RunnerPool(
                name="amd-small",
                instance_type="t3.medium",
                scaling=Components.RunnerPool.Scaling.Auto,
                size=0,
                max_size=50,
                volume_size_gb=100,
                image_builder=_IMAGE_BUILDERS_BY_NAME["ci-x86_64-image"],
                allowed_ssm_parameters=[],
                allowed_secrets=[],
                allowed_s3_prefixes=_PROJECT_S3_PREFIXES,
                allow_all_ssm_parameters=False,
                allow_all_secrets=False,
                allow_all_s3_prefixes=False,
                allow_ssm_debug=False,
            ),
            Components.RunnerPool(
                name="arm-medium",
                instance_type="c7g.4xlarge",
                scaling=Components.RunnerPool.Scaling.Auto,
                size=0,
                max_size=50,
                volume_size_gb=100,
                image_builder=_IMAGE_BUILDERS_BY_NAME["ci-arm64-image"],
                allowed_ssm_parameters=[],
                allowed_secrets=[],
                allowed_s3_prefixes=_PROJECT_S3_PREFIXES,
                allow_all_ssm_parameters=False,
                allow_all_secrets=False,
                allow_all_s3_prefixes=False,
                allow_ssm_debug=False,
            ),
            Components.RunnerPool(
                name="amd-medium",
                instance_type="c7a.4xlarge",
                scaling=Components.RunnerPool.Scaling.Auto,
                size=0,
                max_size=50,
                volume_size_gb=100,
                image_builder=_IMAGE_BUILDERS_BY_NAME["ci-x86_64-image"],
                allowed_ssm_parameters=[],
                allowed_secrets=[],
                allowed_s3_prefixes=_PROJECT_S3_PREFIXES,
                allow_all_ssm_parameters=False,
                allow_all_secrets=False,
                allow_all_s3_prefixes=False,
                allow_ssm_debug=False,
            ),
            # Dedicated pool for the AI Code Review job. Identical to arm-small,
            # plus a scoped bedrock:InvokeModel grant via ext["iam_statements"]
            # so only this pool's role can call the Bedrock model API.
            Components.RunnerPool(
                name="arm-small-bedrock",
                instance_type="t4g.medium",
                scaling=Components.RunnerPool.Scaling.Auto,
                size=0,
                max_size=50,
                volume_size_gb=100,
                image_builder=_IMAGE_BUILDERS_BY_NAME["ci-arm64-image"],
                allowed_ssm_parameters=[],
                allowed_secrets=[],
                allowed_s3_prefixes=_PROJECT_S3_PREFIXES,
                allow_all_ssm_parameters=False,
                allow_all_secrets=False,
                allow_all_s3_prefixes=False,
                allow_ssm_debug=False,
                ext={"iam_statements": [_CODE_REVIEW_BEDROCK_IAM_STATEMENT]},
            ),
        ],
    )
]
