#!/usr/bin/env python3
"""
ci.yaml :: bundle, migrated to praktika.

Builds the OLM bundle and runs operator-sdk scorecard against a throwaway Kind
cluster. Reproduces, in-script, what the GitHub Actions job did via marketplace
actions (shared Docker/Kind plumbing in ci/jobs/kind_env.py):

  - `make bundle`             -> generate + validate the bundle manifests
  - `helm/kind-action`        -> plain Kind cluster named "test-bundle"
  - `kind export kubeconfig`  -> point kubectl/scorecard at that cluster
  - `make bundle-build`       -> docker build the bundle image
  - `kind load docker-image`  -> load it into the cluster
  - `make scorecard`          -> operator-sdk scorecard bundle --wait-time=2m

Env:
  VERSION   bundle version (default 0.0.1, matching ci.yaml), used to derive the
            bundle image tag and FULL_VERSION the Makefile builds.

operator-sdk is installed by the Makefile targets themselves (the `operator-sdk`
prereq), so there is no separate install step. kustomize/controller-gen come from
the go-env bundle. Docker must be provided by the runner image.

Verdict is the scorecard exit code (no JUnit report), surfaced via the job exit
code (enable_exit_code_result).
"""
import os
import sys

sys.path.insert(0, os.getcwd())  # ensure repo root is importable for ci.*
from ci.jobs import kind_env  # noqa: E402

CLUSTER_NAME = "test-bundle"


def main() -> int:
    version = os.environ.get("VERSION") or "0.0.1"
    # Mirrors the Makefile's BUNDLE_IMG for a fixed VERSION (FULL_VERSION=v$VERSION
    # when VERSION is set); same ref ci.yaml kind-loads.
    bundle_img = f"ghcr.io/clickhouse/clickhouse-operator-bundle:v{version}"

    kind_env.require_docker()
    # Before the cluster is created / the bundle image is built: may restart Docker.
    kind_env.disable_containerd_image_store()
    kind_env.install_kind()
    kind_env.install_kubectl()

    kind_env.sh("make bundle")
    # Plain default cluster (no ci/kind-cluster.config, Kind's default node image),
    # matching the bundle job's kind-action usage.
    kind_env.create_cluster(name=CLUSTER_NAME, config=None)
    kind_env.sh(f"kind export kubeconfig --name {CLUSTER_NAME}")
    kind_env.sh("make bundle-build")
    kind_env.sh(f"kind load docker-image {bundle_img} --name {CLUSTER_NAME}")
    kind_env.sh("make scorecard")
    return 0


if __name__ == "__main__":
    sys.exit(main())
