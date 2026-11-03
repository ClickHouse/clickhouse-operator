#!/usr/bin/env python3
"""Shared Docker + Kind provisioning for the praktika Docker/Kind jobs
(bundle, compat-e2e, e2e).

Kind + kubectl are installed here (not baked into the go-env bundle) because
only the Docker/Kind jobs need them; Docker itself must be provided by the
runner image.
"""
import os
import subprocess
import sys

KIND_VERSION = "v0.32.0"
# kubectl is not strictly required by the Go test harness (it uses client-go),
# but several deploy/diagnostic helpers shell out to it.
KUBECTL_VERSION = "v1.31.1"
INSTALL_DIR = "/usr/local/bin"
KIND_CONFIG = "ci/kind-cluster.config"
DEFAULT_CLUSTER = "kind"
CLICKHOUSE_IMAGES = (
    "clickhouse/clickhouse-keeper",
    "clickhouse/clickhouse-server",
)
DIAG_DIR = "failure-diagnostics"
DIAG_ARCHIVE = "failure-diagnostics.tgz"


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
    # Tear down any leftover cluster of the same name first: autoscaled runners
    # are reused, so a prior job's cluster can still be present, and `kind create`
    # refuses to clobber it ("node(s) already exist"). `kind delete` is idempotent.
    sh(f"kind delete cluster --name {name}", check=False)
    # `kind delete` can leave the node containers behind when they are only
    # stopped (e.g. after a Docker daemon restart), which still trips the
    # "already exist" check. Force-remove any container carrying this cluster's
    # kind label as a backstop.
    sh(
        f"ids=$(docker ps -aq --filter label=io.x-k8s.kind.cluster={name}); "
        f'[ -n "$ids" ] && docker rm -f $ids || true',
        check=False,
    )
    # --retain keeps the node containers alive when creation fails (e.g. a worker's
    # kubelet never goes healthy during `kubeadm join`). Without it kind tears the
    # partial cluster down on failure, and the later `kind export logs` in
    # collect_diagnostics finds nothing ("unknown cluster"), so the kubelet journal
    # is lost. The idempotent teardown above cleans the retained cluster next run.
    cmd = f"kind create cluster --name {name} --retain --wait 120s"
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


def collect_diagnostics(report_dir, cluster=DEFAULT_CLUSTER):
    """Archive kind logs, cluster-wide pods/jobs/events and the suite's report dir
    (per-spec resource dumps and pod logs); return the archive path or None.
    Best-effort: every command is allowed to fail."""
    sh(f"mkdir -p {DIAG_DIR}", check=False)
    sh(f"kind export logs --name {cluster} {DIAG_DIR}/kind", check=False)
    sh(f"kubectl get pods,jobs -A -o wide > {DIAG_DIR}/pods-jobs.txt 2>&1", check=False)
    sh(
        f"kubectl get events -A --sort-by=.metadata.creationTimestamp "
        f"> {DIAG_DIR}/events.txt 2>&1",
        check=False,
    )
    sh(f"cp -r {report_dir} {DIAG_DIR}/report", check=False)
    proc = sh(f"tar czf {DIAG_ARCHIVE} {DIAG_DIR}", check=False)
    return DIAG_ARCHIVE if proc.returncode == 0 and os.path.isfile(DIAG_ARCHIVE) else None
