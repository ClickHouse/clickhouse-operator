#!/usr/bin/env python3
"""
Driver for one e2e shard. The 4-way shard matrix is fanned out in
ci/workflows/job_configs.py (Job.parametrize); each shard invokes this script
with a different E2E_SHARD ("index/total", e.g. "2/4"):

  - install the pinned Kind CLI + `kind create cluster` (ci/jobs/kind_env.py)
  - `make test-e2e` -> the sharded e2e run

Sharding is round-robin: the Go harness (test/testutil/sharding.go) hashes each
spec name to a shard, so no pre-computed plan is needed.

Env:
  E2E_SHARD   "index/total" for the Go harness, e.g. "2/4" (passed to `make`)
  K8S_IMAGE   Kind node image tag (default v1.34.3)

Reporting: Ginkgo writes report/junit-report.xml; we parse it into a praktika
Result with one sub-result per spec (ci/jobs/junit_result.py) and complete_job()
it. On failure, kind/kubectl diagnostics and the suite's report dir (per-spec
resource dumps and pod logs) are archived and attached to the Result. Unlike
compat-e2e, there is no image pre-pull and no operator-sdk/opm.
"""
import os
import subprocess
import sys

sys.path.insert(0, os.getcwd())  # ensure repo root is importable for ci.*
from ci.jobs import kind_env  # noqa: E402
from ci.jobs.junit_result import build_job_result  # noqa: E402

from praktika.result import Result  # noqa: E402
from praktika.utils import Utils  # noqa: E402

DEFAULT_K8S_IMAGE = "v1.34.3"
REPORT_DIR = "test/e2e/report"


def main():
    sw = Utils.Stopwatch()
    try:
        node_image = os.environ.get("K8S_IMAGE") or DEFAULT_K8S_IMAGE

        kind_env.require_docker()
        # Must run before the cluster is created: it may restart the Docker
        # daemon, which would wipe cluster containers.
        kind_env.disable_containerd_image_store()
        kind_env.install_kind()
        kind_env.install_kubectl()
        kind_env.create_cluster(node_image)

        # Run the suite without raising on failure: Ginkgo still writes the JUnit
        # report on test failures, and we want to parse it either way. E2E_SHARD
        # is already in the environment (set by the job command) and inherited by
        # `make`.
        print("+ make test-e2e", flush=True)
        exit_code = subprocess.run("make test-e2e", shell=True).returncode
    except Exception as e:  # setup/provisioning failure before the suite runs
        archive = kind_env.collect_diagnostics(REPORT_DIR)
        Result.create_from(
            status=Result.Status.ERROR,
            stopwatch=sw,
            info=f"e2e setup failed: {e}",
            files=[archive] if archive else [],
        ).complete_job()
        return

    extra_files = []
    if exit_code != 0:
        archive = kind_env.collect_diagnostics(REPORT_DIR)
        if archive:
            extra_files.append(archive)
    # Ginkgo's JSON report (if present) carries per-spec timings for future
    # duration-weighted sharding; attach it for download.
    ginkgo_json = f"{REPORT_DIR}/ginkgo-report.json"
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
