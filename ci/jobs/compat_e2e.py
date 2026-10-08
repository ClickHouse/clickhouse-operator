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
  FETCH_TAGS         if set, resolve the release to upgrade from (the upgrade
                     variant deploys the latest release). Legacy clones fetch
                     tags so the test's own `git tag --list` works; snapshot
                     checkouts (no `origin` remote / history) resolve the highest
                     vX.Y.Z tag via the GitHub API and export
                     UPGRADE_FROM_VERSION, which deploy_test.go honors instead.

Go/helm/kubebuilder come from the go-env pre-hook (on PATH via /usr/local/bin);
Docker must be provided by the runner image. The Docker/Kind plumbing is shared
with the e2e job in ci/jobs/kind_env.py.

Reporting: Ginkgo writes a JUnit report per suite (`--ginkgo.junit-report`); we
parse them into a praktika Result with one sub-result per test case
(ci/jobs/junit_result.py) and `complete_job()` it, so the report page shows a
per-spec table — the replacement for the dropped dorny/test-reporter. The job's
verdict comes from the parsed cases reconciled with the `make` exit code, so
`enable_exit_code_result` is only a last-resort fallback here.

Report-artifact upload (GHA `upload-artifact e2e-report-*`) is intentionally NOT
carried over: its only consumer (`e2e-report`) is not migrated yet.
"""
import os
import re
import subprocess
import sys

sys.path.insert(0, os.getcwd())  # ensure repo root is importable for ci.*
from ci.jobs import kind_env  # noqa: E402
from ci.jobs.junit_result import build_job_result  # noqa: E402

from praktika.info import Info  # noqa: E402
from praktika.result import Result  # noqa: E402
from praktika.utils import Utils  # noqa: E402


def _latest_release_tag(repo):
    """Highest vX.Y.Z tag in `repo` via the GitHub API (mirrors deploy_test.go's
    `git tag --list "v[0-9]*.[0-9]*.[0-9]*" --sort=-v:refname`)."""
    out = subprocess.run(
        ["gh", "api", "--paginate", f"repos/{repo}/tags", "--jq", ".[].name"],
        capture_output=True, text=True, check=True,
    ).stdout
    versions = []
    for name in out.split():
        m = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)", name)
        if m:
            versions.append((tuple(int(x) for x in m.groups()), name))
    if not versions:
        raise RuntimeError(f"no vX.Y.Z release tags found in {repo}")
    return max(versions)[1]


def _resolve_upgrade_from():
    """Make the release-to-upgrade-from discoverable by the upgrade test.

    Honors an explicit UPGRADE_FROM_VERSION. Otherwise tries to fetch tags into
    local git (legacy clone — the test then reads them itself); if there is no
    `origin` remote (snapshot checkout), resolves the latest tag via the GitHub
    API and exports UPGRADE_FROM_VERSION, which deploy_test.go honors instead.
    """
    if os.environ.get("UPGRADE_FROM_VERSION"):
        return
    if kind_env.sh("git fetch --tags --force origin", check=False).returncode == 0:
        return
    print(
        "NOTE: no git remote for tags — resolving latest release via GitHub API",
        flush=True,
    )
    tag = _latest_release_tag(Info().repo_name)
    os.environ["UPGRADE_FROM_VERSION"] = tag
    print(f"  UPGRADE_FROM_VERSION={tag}", flush=True)


def main():
    sw = Utils.Stopwatch()
    try:
        node_image = os.environ["K8S_IMAGE"]
        clickhouse_version = os.environ["CLICKHOUSE_VERSION"]
        deploy_target = os.environ["DEPLOY_TARGET"]

        if os.environ.get("FETCH_TAGS"):
            _resolve_upgrade_from()
        kind_env.require_docker()
        # Must run before the cluster is created / any image is pulled: it may
        # restart the Docker daemon, which would wipe cluster containers and
        # pulled images.
        kind_env.disable_containerd_image_store()
        kind_env.install_kind()
        kind_env.install_kubectl()
        # operator-sdk + opm, matching ci.yaml's unconditional
        # `make operator-sdk opm` (needed by the OLM deploy path; cheap no-ops
        # for the others).
        kind_env.sh("make operator-sdk opm")
        kind_env.create_cluster(node_image)
        kind_env.prepull(clickhouse_version)

        # Run the suite without raising on failure: Ginkgo still writes the JUnit
        # report on test failures, and we want to parse it either way. The job
        # verdict comes from the parsed cases reconciled with this exit code.
        env = {**os.environ, "CLICKHOUSE_VERSION": clickhouse_version}
        print(f"+ make {deploy_target}", flush=True)
        exit_code = subprocess.run(
            f"make {deploy_target}", shell=True, env=env
        ).returncode
    except Exception as e:  # setup/provisioning failure before the suite runs
        Result.create_from(
            status=Result.Status.ERROR,
            stopwatch=sw,
            info=f"compat-e2e setup failed: {e}",
        ).complete_job()
        return

    # Ginkgo writes report/junit-report.xml under each suite's package dir.
    result = build_job_result(
        report_glob="**/report/junit-report.xml",
        run_exit_code=exit_code,
        stopwatch=sw,
    )
    result.complete_job()  # dumps the Result and exits with the matching code


if __name__ == "__main__":
    main()
