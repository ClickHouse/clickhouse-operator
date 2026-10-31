import base64

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

_TRUSTED_RESOURCES = [
    f"arn:aws:s3:::{Settings.S3_ARTIFACT_BUCKET}/REFs/*",
    f"arn:aws:s3:::{Settings.S3_ARTIFACT_BUCKET}/repo-snapshots/v1/REFs/*",
]
_PR_RESOURCES = [
    f"arn:aws:s3:::{Settings.S3_ARTIFACT_BUCKET}/PRs/*",
    f"arn:aws:s3:::{Settings.S3_ARTIFACT_BUCKET}/repo-snapshots/v1/PRs/*",
]
# Untrusted (pr-*): may read trusted tiers, must not write them (no poisoning).
_UNTRUSTED_DENY_WRITE_TRUSTED = {
    "Sid": "DenyUntrustedWriteToTrusted",
    "Effect": "Deny",
    "Action": [
        "s3:PutObject",
        "s3:PutObjectTagging",
        "s3:DeleteObject",
        "s3:AbortMultipartUpload",
    ],
    "Resource": _TRUSTED_RESOURCES,
}
# Trusted (non-pr): must not read or write PR tiers (no tainted input).
_TRUSTED_DENY_ACCESS_UNTRUSTED = {
    "Sid": "DenyTrustedAccessToUntrusted",
    "Effect": "Deny",
    "Action": [
        "s3:GetObject",
        "s3:GetObjectTagging",
        "s3:PutObject",
        "s3:PutObjectTagging",
        "s3:DeleteObject",
        "s3:AbortMultipartUpload",
    ],
    "Resource": _PR_RESOURCES,
}


# until published in pip
_PRAKTIKA_PACKAGE_BASE_URL = "https://praktika-artifacts-eu-north-1.s3.amazonaws.com/packages"
_PRAKTIKA_WHL = f"{_PRAKTIKA_PACKAGE_BASE_URL}/praktika-0.1.25-py3-none-any.whl"
_PRAKTIKA_CONTROLLER_WHL = f"{_PRAKTIKA_PACKAGE_BASE_URL}/praktika_controller-0.1.25-py3-none-any.whl"

_NODE_MAJOR = "20"


def _doc_lint_tools_component():
    """Build-phase Image Builder component installing Vale and Node + linkspector
    for the documentation-lint jobs."""
    commands = [
        # Resolve the Debian CPU arch once (arm64 / amd64) for arch-specific URLs.
        "arch=$(dpkg --print-architecture)",
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
        "description": "Install docs-lint toolchain: Vale, Node + linkspector",
        "commands": commands,
    }


def _docker_kind_component():
    """Build-phase component that disables Docker's containerd image store so
    `kind load docker-image` works on the Docker+Kind jobs (bundle, compat-e2e, e2e).

    praktika's base setup installs docker-ce and writes /etc/docker/daemon.json;
    this component runs afterwards (custom components are appended last) and merges
    `features.containerd-snapshotter = false` into it, preserving praktika's keys.

    Why: `kind load docker-image` exports each image with `docker save` and
    re-imports it via `ctr ... --all-platforms --digests`. With the containerd
    image store enabled, even a single-arch `docker pull` keeps a multi-platform
    image index, so the import fails on the other platforms' / attestation blobs
    ("content digest ... not found"). The classic image store emits a flat
    single-platform archive, matching GitHub-hosted runners where `kind load`
    works.

    The image-builder component YAML escaper only escapes double quotes, so the
    JSON-heavy script is base64-wrapped (same technique as praktika's docker
    registry-mirror component).
    """
    script = """#!/usr/bin/env bash
set -euo pipefail
f=/etc/docker/daemon.json
[ -f "$f" ] || echo '{}' > "$f"
tmp=$(mktemp)
jq '.features."containerd-snapshotter" = false' "$f" > "$tmp"
mv "$tmp" "$f"
jq -e '.features."containerd-snapshotter" == false' "$f"
"""
    b64 = base64.b64encode(script.encode()).decode()
    return {
        "name": "docker-kind-image-store",
        "platform": "Linux",
        "phase": "build",
        "description": "Disable Docker containerd image store so kind load works",
        "commands": [f"printf '%s' '{b64}' | base64 -d | bash"],
    }


def _image_builders():
    # Bump whenever the recipe/components change so Image Builder creates a new
    # recipe + component versions and rebuilds the AMI.
    image_recipe_version = "1.0.11"
    prebuilt_venvs = [
        # The `infrastructure` extra pulls Praktika's runtime deps
        # (boto3/PyJWT/cryptography/requests) automatically; pytest and the
        # Bedrock AI SDK are optional extras, so list them explicitly. The
        # orchestrator's AI advisor (AI_PROVIDER="bedrock-anthropic") imports
        # `anthropic[bedrock]` lazily at decide() time; baking it into this shared
        # venv keeps it present on every AMI (harmless on job runners).
        ImageBuilder.PrebuiltVenv(
            name=Settings.PRAKTIKA_BASE_VENV,
            packages=[
                "pytest>=7.0.0",
                "pytest-reportlog>=0.4.0",
                "anthropic[bedrock]",
                f"praktika[infrastructure] @ {_PRAKTIKA_WHL}",
            ],
            description="CI runtime venv (+ Bedrock AI SDK for the orchestrator)",
        ),
    ]
    custom_components = [
        # Build-phase: install the docs-lint toolchain (Vale + Node/linkspector).
        # Go tooling is provisioned per job via the go-env pre-hook, not baked.
        _doc_lint_tools_component(),
        # Build-phase: disable Docker's containerd image store (kind load fix).
        _docker_kind_component(),
        # Test-phase: validate the image after build.
        Components.create_image_test_component(
            name="project-image-test",
            commands=[
                "test -d /opt/praktika/work",
                "test -w /opt/praktika/work",
                "vale --version",
                "linkspector --version",
            ],
        ),
    ]
    # The Ubuntu 24.04 parent AMI ships an 8 GB gp3 root volume on /dev/sda1;
    # double it to 16 GB so the baked tooling + Docker layers have room.
    block_device_mappings = [
        {
            "deviceName": "/dev/sda1",
            "ebs": {
                "volumeSize": 16,
                "volumeType": "gp3",
                "deleteOnTermination": True,
            },
        }
    ]
    builders = [
        Components.create_ubuntu_image_builder_config(
            name="ci-arm64-image",
            version=image_recipe_version,
            controller_package=_PRAKTIKA_CONTROLLER_WHL,
            prebuilt_venvs=prebuilt_venvs,
            components=custom_components,
            instance_types=["c7g.2xlarge"],
        ),
        Components.create_ubuntu_image_builder_config(
            name="ci-x86_64-image",
            version=image_recipe_version,
            controller_package=_PRAKTIKA_CONTROLLER_WHL,
            prebuilt_venvs=prebuilt_venvs,
            components=custom_components,
            instance_types=["c7i.2xlarge"],
        ),
    ]
    for builder in builders:
        builder.block_device_mappings = block_device_mappings
    return builders


_GH_TOKEN_MINTER = Components.GitHubTokenMinter(
    permissions={
        "checks": "write",
        "contents": "write",
        "issues": "write",
        "metadata": "read",
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


# The orchestrator's AI advisor (AI_PROVIDER="bedrock-anthropic") reaches Claude
# through Bedrock Runtime via `anthropic[bedrock]`, so the orchestrator instance
# role must be allowed to invoke the model. Scoped to Bedrock foundation-model /
# inference-profile resources (covers the global.anthropic.* inference profiles
# the advisor targets).
_ORCHESTRATOR_BEDROCK_IAM_STATEMENT = {
    "Sid": "BedrockRuntimeInference",
    "Effect": "Allow",
    "Action": ["bedrock:InvokeModel"],
    "Resource": [
        "arn:aws:bedrock:*::foundation-model/*",
        "arn:aws:bedrock:*:*:inference-profile/*",
    ],
}


def _runner_pool(name, instance_type, image_builder, untrusted, capacity_reserve=0, extra_iam=None):
    """A job runner pool. `untrusted=True` pools back the PR (pr-*) fleet and get
    the untrusted S3 trust-deny; the rest back trusted main/dispatch runs."""
    trust_deny = (
        _UNTRUSTED_DENY_WRITE_TRUSTED if untrusted else _TRUSTED_DENY_ACCESS_UNTRUSTED
    )
    iam_statements = [trust_deny] + list(extra_iam or [])
    return Components.RunnerPool(
        name=name,
        instance_type=instance_type,
        scaling=Components.RunnerPool.Scaling.Auto,
        size=0,
        max_size=50,
        capacity_reserve=capacity_reserve,
        volume_size_gb=100,
        image_builder=_IMAGE_BUILDERS_BY_NAME[image_builder],
        allowed_ssm_parameters=[],
        allowed_secrets=[],
        allowed_s3_prefixes=_PROJECT_S3_PREFIXES,
        allow_all_ssm_parameters=False,
        allow_all_secrets=False,
        allow_all_s3_prefixes=False,
        allow_ssm_debug=False,
        ext={"iam_statements": iam_statements},
    )


# Trusted fleet backs the Main (push) workflow; the pr-* untrusted fleet backs
# the PR workflow (via its runs_on_label_prefix="pr-"). Both reference the same
# job labels, so each label needs a pool in both fleets. The AI Code Review job
# is PR-only, so the bedrock grant lives only on the untrusted bedrock pool.
_ARM, _AMD = "ci-arm64-image", "ci-x86_64-image"
_RUNNER_POOLS = [
    _runner_pool("arm-2cpu-4g", "c7g.large", _ARM, untrusted=False, capacity_reserve=4),
    _runner_pool("amd-2cpu-4g", "c7a.large", _AMD, untrusted=False),
    _runner_pool("arm-8cpu-16g", "c7g.2xlarge", _ARM, untrusted=False),
    _runner_pool("amd-8cpu-16g", "c7a.2xlarge", _AMD, untrusted=False),
    _runner_pool("pr-arm-2cpu-4g", "c7g.large", _ARM, untrusted=True, capacity_reserve=4),
    _runner_pool("pr-amd-2cpu-4g", "c7a.large", _AMD, untrusted=True),
    _runner_pool("pr-arm-8cpu-16g", "c7g.2xlarge", _ARM, untrusted=True),
    _runner_pool("pr-amd-8cpu-16g", "c7a.2xlarge", _AMD, untrusted=True),
    _runner_pool(
        "pr-arm-2cpu-4g-bedrock", "c7g.large", _ARM, untrusted=True,
        extra_iam=[_CODE_REVIEW_BEDROCK_IAM_STATEMENT],
    ),
]


PROJECTS = [
    CloudInfrastructure.Config(
        name=Settings.PROJECT_NAME,
        min_praktika_version="0.1.25",
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
            capacity_reserve=4,
            image_builder=_IMAGE_BUILDERS_BY_NAME["ci-arm64-image"],
            ext={
                "allowed_push_branches": ['main'],
                "allowed_pr_base_branches": ['main'],
                "allowed_users": [],
                # The orchestrator's AI advisor calls Bedrock, so its role needs
                # a scoped bedrock:InvokeModel grant.
                "iam_statements": [_ORCHESTRATOR_BEDROCK_IAM_STATEMENT],
            },
        ),
        runner_pools=_RUNNER_POOLS,
    )
]
