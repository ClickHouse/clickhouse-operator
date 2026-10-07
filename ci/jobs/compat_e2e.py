#!/usr/bin/env python3
"""
ci.yaml :: compat-e2e-test, migrated to praktika.

Driver for every compat-e2e matrix variant. The 5-way matrix is fanned out in
ci/workflows/job_configs.py (_COMPAT_E2E_MATRIX + Job.parametrize); each variant
invokes this script with a different K8S_IMAGE / CLICKHOUSE_VERSION /
DEPLOY_TARGET. It reproduces, in-script, what the GitHub Actions job did via
marketplace actions:

  - `helm/kind-action`  -> install the pinned Kind CLI + `kind create cluster`
                           with the per-variant node image and ci/kind-cluster.config
  - pre-pull step       -> `docker pull` (with retry) + `kind load docker-image`
                           for each ClickHouse version
  - `make <target>`     -> the actual compatibility test run

It is driven by three env vars so the same script serves every matrix variant
once parametrized:

  K8S_IMAGE          Kind node image tag, e.g. "v1.36.1"
  CLICKHOUSE_VERSION comma-separated ClickHouse versions to pre-pull / test
  DEPLOY_TARGET      Makefile target, e.g. "test-compat-e2e"
  FETCH_TAGS         if set, `git fetch --tags` before the run (the upgrade
                     variant deploys the latest release, discovered from tags;
                     praktika's ephemeral merge checkout has none)

Go/helm/kubebuilder come from the go-env pre-hook (on PATH via /usr/local/bin);
Docker must be provided by the runner image. Kind + kubectl are installed here
(not in go-env) because only the Docker/Kind jobs need them.

Reporting: Ginkgo writes a JUnit report per suite (`--ginkgo.junit-report`); we
parse them into a praktika Result with one sub-result per test case
(ci/jobs/junit_result.py) and `complete_job()` it, so the report page shows a
per-spec table — the replacement for the dropped dorny/test-reporter. The job's
verdict comes from the parsed cases reconciled with the `make` exit code, so
`enable_exit_code_result` is only a last-resort fallback here.

Report-artifact upload (GHA `upload-artifact e2e-report-*`) is intentionally NOT
carried over in this spike: its only consumer (`e2e-report`) is not migrated yet.
"""
import json
import os
import subprocess
import sys
import time

sys.path.insert(0, os.getcwd())  # ensure repo root is importable for ci.*
from ci.jobs.junit_result import build_job_result  # noqa: E402

from praktika.utils import Utils  # noqa: E402

# Pinned to match ci.yaml's `env.kind-version`.
KIND_VERSION = "v0.32.0"
# kubectl is not strictly required by the Go test harness (it uses client-go),
# but several deploy helpers shell out to it; install a recent stable build.
KUBECTL_VERSION = "v1.31.1"
INSTALL_DIR = "/usr/local/bin"
CLUSTER_NAME = "kind"
KIND_CONFIG = "ci/kind-cluster.config"
CLICKHOUSE_IMAGES = (
    "clickhouse/clickhouse-keeper",
    "clickhouse/clickhouse-server",
)


def _sh(cmd, **kwargs):
    print(f"+ {cmd}", flush=True)
    subprocess.run(cmd, shell=True, check=True, **kwargs)


def _arch():
    # kind/kubectl release assets use the Go arch tokens amd64 / arm64, which is
    # exactly what `dpkg --print-architecture` returns on Ubuntu.
    return subprocess.check_output(["dpkg", "--print-architecture"], text=True).strip()


def _require_docker():
    try:
        _sh("docker version")
    except subprocess.CalledProcessError:
        print(
            "ERROR: Docker daemon not available on this runner. The compat-e2e "
            "job needs a working Docker daemon for Kind-in-Docker. This is the "
            "infra blocker the spike exists to surface.",
            file=sys.stderr,
        )
        raise


def _docker_driver():
    return subprocess.check_output(
        ["docker", "info", "-f", "{{.Driver}}"], text=True
    ).strip()


def _restart_docker():
    for cmd in ("systemctl restart docker", "service docker restart"):
        try:
            _sh(cmd)
            break
        except subprocess.CalledProcessError:
            continue
    else:
        raise RuntimeError("could not restart the Docker daemon")
    # Wait for the daemon to come back before anything else touches it.
    for _ in range(30):
        if subprocess.run("docker version", shell=True).returncode == 0:
            return
        time.sleep(2)
    raise RuntimeError("Docker daemon did not come back after restart")


def _disable_containerd_image_store():
    """Transitional fallback for `kind load docker-image`.

    The authoritative fix lives in the runner AMI (ci/infrastructure/projects.py,
    `_docker_kind_component`, recipe >= 1.0.6): it bakes
    `features.containerd-snapshotter = false` into /etc/docker/daemon.json. Until
    that AMI is built and the pools are rolled, runners still boot with the
    containerd image store, where `kind load` exports via `docker save` and
    re-imports with `ctr ... --all-platforms --digests` — which fails on the other
    platforms' / attestation blobs ("content digest ... not found") because a
    single-arch pull keeps a multi-platform index.

    So on such a runner (Driver=overlayfs) we disable the snapshotter and restart
    Docker here. Once the baked AMI is in use the driver is the classic store and
    this is a no-op (early return, no restart), so it can be deleted after the roll.
    """
    if _docker_driver() != "overlayfs":
        return  # classic image store already in use (e.g. baked AMI); nothing to do
    print("Docker containerd image store detected; disabling for kind load", flush=True)
    path = "/etc/docker/daemon.json"
    try:
        with open(path) as f:
            cfg = json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        cfg = {}
    cfg.setdefault("features", {})["containerd-snapshotter"] = False
    os.makedirs("/etc/docker", exist_ok=True)
    with open(path, "w") as f:
        json.dump(cfg, f)
    _restart_docker()


def _install_kind(arch):
    url = f"https://github.com/kubernetes-sigs/kind/releases/download/{KIND_VERSION}/kind-linux-{arch}"
    _sh(f'curl -fsSL "{url}" -o {INSTALL_DIR}/kind && chmod +x {INSTALL_DIR}/kind')
    _sh("kind version")


def _install_kubectl(arch):
    url = f"https://dl.k8s.io/release/{KUBECTL_VERSION}/bin/linux/{arch}/kubectl"
    _sh(f'curl -fsSL "{url}" -o {INSTALL_DIR}/kubectl && chmod +x {INSTALL_DIR}/kubectl')
    _sh("kubectl version --client")


def _create_cluster(node_image):
    _sh(
        f"kind create cluster --name {CLUSTER_NAME} "
        f"--image kindest/node:{node_image} --config {KIND_CONFIG} --wait 120s"
    )


def _prepull(versions):
    for version in (v.strip() for v in versions.split(",") if v.strip()):
        for image in CLICKHOUSE_IMAGES:
            ref = f"docker.io/{image}:{version}"
            # Retry: the public registry occasionally rate-limits / flakes.
            _sh(
                f"for i in 1 2 3; do docker pull {ref} && break || sleep 15; done; "
                f"docker image inspect {ref} >/dev/null"
            )
            _sh(f"kind load docker-image {ref} --name {CLUSTER_NAME}")


def main() -> int:
    node_image = os.environ["K8S_IMAGE"]
    clickhouse_version = os.environ["CLICKHOUSE_VERSION"]
    deploy_target = os.environ["DEPLOY_TARGET"]
    arch = _arch()

    if os.environ.get("FETCH_TAGS"):
        # ci.yaml's `fetch-tags: true`: the upgrade variant needs release tags,
        # which praktika's ephemeral merge checkout does not carry.
        _sh("git fetch --tags --force origin")
    _require_docker()
    # Must run before the cluster is created / any image is pulled: it may restart
    # the Docker daemon, which would wipe cluster containers and pulled images.
    _disable_containerd_image_store()
    _install_kind(arch)
    _install_kubectl(arch)
    # operator-sdk + opm, matching ci.yaml's unconditional `make operator-sdk opm`
    # (needed by the OLM deploy path; cheap no-ops for the others).
    _sh("make operator-sdk opm")
    _create_cluster(node_image)
    _prepull(clickhouse_version)

    # Run the suite without raising on failure: Ginkgo still writes the JUnit
    # report on test failures, and we want to parse it either way. The job
    # verdict comes from the parsed cases reconciled with this exit code.
    env = {**os.environ, "CLICKHOUSE_VERSION": clickhouse_version}
    sw = Utils.Stopwatch()
    print(f"+ make {deploy_target}", flush=True)
    exit_code = subprocess.run(f"make {deploy_target}", shell=True, env=env).returncode

    # Ginkgo writes report/junit-report.xml under each suite's package dir.
    result = build_job_result(
        report_glob="**/report/junit-report.xml",
        run_exit_code=exit_code,
        stopwatch=sw,
    )
    result.complete_job()  # dumps the Result and exits with the matching code


if __name__ == "__main__":
    main()
