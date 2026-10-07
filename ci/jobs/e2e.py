#!/usr/bin/env python3
"""
ci.yaml :: e2e-test, migrated to praktika.

Driver for one e2e shard. The 4-way shard matrix is fanned out in
ci/workflows/job_configs.py (Job.parametrize); each shard invokes this script
with a different E2E_SHARD ("index/total", e.g. "2/4"). It reproduces, in-script,
what the GitHub Actions job did via marketplace actions:

  - `helm/kind-action`  -> install the pinned Kind CLI + `kind create cluster`
                           (shared Docker/Kind plumbing in ci/jobs/kind_env.py)
  - failure diagnostics -> `kind export logs` + `kubectl get pods/events`
  - `make test-e2e`     -> the sharded e2e run

Sharding is round-robin: the Go harness (test/testutil/sharding.go) hashes each
spec name to a shard, so no pre-computed plan is needed. (The legacy plan-based
"smart" mode still works via E2E_SHARD_INDEX/TOTAL/PLAN and is what ci.yaml
drives; this job uses the simpler E2E_SHARD form.)

Env:
  E2E_SHARD   "index/total" for the Go harness, e.g. "2/4" (passed to `make`)
  K8S_IMAGE   Kind node image tag (default v1.34.3, matching ci.yaml)

Reporting: Ginkgo writes report/junit-report.xml; we parse it into a praktika
Result with one sub-result per spec (ci/jobs/junit_result.py) and complete_job()
it. On failure, kind/kubectl diagnostics are collected and attached to the
Result. Unlike compat-e2e, there is no image pre-pull and no operator-sdk/opm,
matching the GitHub e2e-test job.
"""
import os
import subprocess
import sys

sys.path.insert(0, os.getcwd())  # ensure repo root is importable for ci.*
from ci.jobs import kind_env  # noqa: E402
from ci.jobs.junit_result import build_job_result  # noqa: E402

from praktika.utils import Utils  # noqa: E402

# ci.yaml pins the e2e Kind node image (its comment cited cgroups v1 on the old
# self-hosted runners; praktika runners are cgroups v2, where this image is fine).
DEFAULT_K8S_IMAGE = "v1.34.3"
DIAG_DIR = "failure-diagnostics"
DIAG_ARCHIVE = "failure-diagnostics.tgz"


def _collect_diagnostics():
    """Mirror ci.yaml's failure-diagnostics step; return the archive path (or None).
    Best-effort: every command is allowed to fail."""
    kind_env.sh(f"mkdir -p {DIAG_DIR}", check=False)
    kind_env.sh(f"kind export logs --name {kind_env.DEFAULT_CLUSTER} {DIAG_DIR}/kind", check=False)
    kind_env.sh(f"kubectl get pods,jobs -A -o wide > {DIAG_DIR}/pods-jobs.txt 2>&1", check=False)
    kind_env.sh(
        f"kubectl get events -A --sort-by=.metadata.creationTimestamp "
        f"> {DIAG_DIR}/events.txt 2>&1",
        check=False,
    )
    proc = kind_env.sh(f"tar czf {DIAG_ARCHIVE} {DIAG_DIR}", check=False)
    return DIAG_ARCHIVE if proc.returncode == 0 and os.path.isfile(DIAG_ARCHIVE) else None


def main() -> int:
    node_image = os.environ.get("K8S_IMAGE") or DEFAULT_K8S_IMAGE

    kind_env.require_docker()
    # Must run before the cluster is created: it may restart the Docker daemon,
    # which would wipe cluster containers.
    kind_env.disable_containerd_image_store()
    kind_env.install_kind()
    kind_env.install_kubectl()
    kind_env.create_cluster(node_image)

    # Run the suite without raising on failure: Ginkgo still writes the JUnit
    # report on test failures, and we want to parse it either way. E2E_SHARD is
    # already in the environment (set by the job command) and inherited by `make`.
    sw = Utils.Stopwatch()
    print("+ make test-e2e", flush=True)
    exit_code = subprocess.run("make test-e2e", shell=True).returncode

    extra_files = []
    if exit_code != 0:
        archive = _collect_diagnostics()
        if archive:
            extra_files.append(archive)
    # Ginkgo's JSON report (if present) carries per-spec timings for future
    # duration-weighted sharding; attach it for download.
    ginkgo_json = "test/e2e/report/ginkgo-report.json"
    if os.path.isfile(ginkgo_json):
        extra_files.append(ginkgo_json)

    result = build_job_result(
        report_glob="**/report/junit-report.xml",
        run_exit_code=exit_code,
        stopwatch=sw,
        extra_files=extra_files,
    )
    result.complete_job()  # dumps the Result and exits with the matching code


if __name__ == "__main__":
    main()
