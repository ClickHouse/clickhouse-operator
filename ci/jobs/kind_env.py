#!/usr/bin/env python3
"""Shared Docker + Kind provisioning for the praktika Docker/Kind jobs
(compat-e2e, e2e).

Reproduces, in plain shell, what the GitHub Actions jobs did via marketplace
actions (helm/kind-action + the pre-pull step) so the praktika jobs are
self-contained. Kind + kubectl are installed here (not baked into the go-env
bundle) because only the Docker/Kind jobs need them; Docker itself must be
provided by the runner image.
"""
import json
import os
import subprocess
import sys
import time

# Pinned to match ci.yaml's `env.kind-version`.
KIND_VERSION = "v0.32.0"
# kubectl is not strictly required by the Go test harness (it uses client-go),
# but several deploy/diagnostic helpers shell out to it; install a recent build.
KUBECTL_VERSION = "v1.31.1"
INSTALL_DIR = "/usr/local/bin"
KIND_CONFIG = "ci/kind-cluster.config"
DEFAULT_CLUSTER = "kind"
CLICKHOUSE_IMAGES = (
    "clickhouse/clickhouse-keeper",
    "clickhouse/clickhouse-server",
)


def sh(cmd, check=True, **kwargs):
    print(f"+ {cmd}", flush=True)
    return subprocess.run(cmd, shell=True, check=check, **kwargs)


def arch():
    # kind/kubectl release assets use the Go arch tokens amd64 / arm64, which is
    # exactly what `dpkg --print-architecture` returns on Ubuntu.
    return subprocess.check_output(["dpkg", "--print-architecture"], text=True).strip()


def require_docker():
    try:
        sh("docker version")
    except subprocess.CalledProcessError:
        print(
            "ERROR: Docker daemon not available on this runner. Kind-in-Docker "
            "needs a working Docker daemon provided by the runner image.",
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
            sh(cmd)
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


def disable_containerd_image_store():
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


def install_kind(a=None):
    a = a or arch()
    url = f"https://github.com/kubernetes-sigs/kind/releases/download/{KIND_VERSION}/kind-linux-{a}"
    sh(f'curl -fsSL "{url}" -o {INSTALL_DIR}/kind && chmod +x {INSTALL_DIR}/kind')
    sh("kind version")


def install_kubectl(a=None):
    a = a or arch()
    url = f"https://dl.k8s.io/release/{KUBECTL_VERSION}/bin/linux/{a}/kubectl"
    sh(f'curl -fsSL "{url}" -o {INSTALL_DIR}/kubectl && chmod +x {INSTALL_DIR}/kubectl')
    sh("kubectl version --client")


def create_cluster(node_image=None, name=DEFAULT_CLUSTER, config=KIND_CONFIG):
    """Create a Kind cluster. node_image=None uses Kind's default image for the
    installed version; config=None creates a plain single-node cluster (no
    ci/kind-cluster.config topology)."""
    cmd = f"kind create cluster --name {name} --wait 120s"
    if node_image:
        cmd += f" --image kindest/node:{node_image}"
    if config:
        cmd += f" --config {config}"
    sh(cmd)


def prepull(versions, cluster=DEFAULT_CLUSTER):
    """Pull each ClickHouse image (comma-separated versions) and load it into the
    Kind cluster, so the test pods don't pull from docker.io in-cluster."""
    for version in (v.strip() for v in versions.split(",") if v.strip()):
        for image in CLICKHOUSE_IMAGES:
            ref = f"docker.io/{image}:{version}"
            # Retry: the public registry occasionally rate-limits / flakes.
            sh(
                f"for i in 1 2 3; do docker pull {ref} && break || sleep 15; done; "
                f"docker image inspect {ref} >/dev/null"
            )
            sh(f"kind load docker-image {ref} --name {cluster}")
