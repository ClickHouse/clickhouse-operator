#!/usr/bin/env python3
"""
ci.yaml :: check-crd-compat, migrated to praktika.

The crd-breaking-change label gate lives in the workflow filter hook
(ci/jobs/filter_job_hook.py), not here.

What the job does: it obtains each CRD's baseline (the manifest as it currently
exists on the base branch) and runs `make check-crd-compat`, which compares each
baseline against the PR's current CRD with crd-schema-checker and fails on
backward-incompatible schema changes. The verdict is returned as a praktika
Result (OK / FAIL / ERROR) carrying the baseline source, base branch, staged
counts, and — on failure — the checker's violations.

Two ways to get the baseline, picked automatically:

1. git history (preferred, when applicable). A real clone with an `origin`
   remote — the legacy, non-snapshot checkout — lets us shallow-fetch the base
   branch and compare against `git show origin/<base>:...` with no network API
   call. We try this first.

2. GitHub contents API (fallback). With ENABLE_S3_REPO_SNAPSHOT every job
   restores a history-free snapshot of the controller's pinned merge commit:
   there is no `origin` remote and no base history, so the git fetch above fails.
   We then pull the base manifests straight from GitHub (`gh` is authenticated in
   the job) into a temp dir and point the Make target at it via CRD_BASELINE_DIR.
   A path that does not exist on the base branch (HTTP 404) is left unstaged,
   which the Make target reads as a brand-new CRD and skips.
"""
import glob
import os
import subprocess
import sys
import tempfile

from praktika.info import Info
from praktika.result import Result
from praktika.utils import Utils

CRD_GLOB = "config/crd/bases/*.yaml"


def _try_fetch_git_base(base):
    """Shallow-fetch the base branch so origin/<base> is resolvable locally.

    Returns True when the fetch succeeds (legacy clone with an `origin` remote),
    False when it does not (snapshot checkout has no remote). Depth 1 is enough —
    the check only reads the base tip's CRD blobs via `git show`.
    """
    cmd = [
        "git", "fetch", "--no-tags", "--depth=1", "origin",
        f"+refs/heads/{base}:refs/remotes/origin/{base}",
    ]
    print(f"+ {' '.join(cmd)}", flush=True)
    return subprocess.run(cmd).returncode == 0


def _stage_baselines(repo, base, dest):
    """Download each current CRD's base-branch version into `dest` via gh api.

    Returns (staged, skipped): a file absent on the base branch (404) is not
    written, so the Makefile treats the corresponding current CRD as new and
    skips it. Raises on any other gh failure.
    """
    staged = skipped = 0
    for crd in sorted(glob.glob(CRD_GLOB)):
        cmd = [
            "gh", "api",
            "-H", "Accept: application/vnd.github.raw",
            f"repos/{repo}/contents/{crd}",
            "-f", f"ref={base}",
        ]
        print(f"+ {' '.join(cmd)}", flush=True)
        proc = subprocess.run(cmd, capture_output=True, text=True)
        if proc.returncode == 0:
            with open(os.path.join(dest, os.path.basename(crd)), "w") as f:
                f.write(proc.stdout)
            print(f"  staged baseline for {crd}", flush=True)
            staged += 1
        elif "404" in proc.stderr or "Not Found" in proc.stderr:
            print(f"  {crd} absent on {base} — new CRD, will skip", flush=True)
            skipped += 1
        else:
            sys.stderr.write(proc.stderr)
            raise RuntimeError(f"gh api failed for {crd}: {proc.stderr.strip()}")
    return staged, skipped


def _run_make(make_args):
    """Run `make check-crd-compat <args>`, echoing output. Returns (rc, output)."""
    cmd = ["make", "check-crd-compat"] + make_args
    print(f"+ {' '.join(cmd)}", flush=True)
    proc = subprocess.run(cmd, capture_output=True, text=True)
    output = (proc.stdout or "") + (proc.stderr or "")
    print(output, end="", flush=True)
    return proc.returncode, output


def _check(info_lines):
    """Run the compatibility check. Returns (rc, output); appends to info_lines."""
    info = Info()
    base = info.base_branch or "main"
    info_lines.append(f"base branch: {base}")

    if _try_fetch_git_base(base):
        info_lines.append(f"baseline source: git history (origin/{base})")
        return _run_make([f"CRD_BASE_REF=origin/{base}"])

    info_lines.append(
        "baseline source: GitHub API (snapshot checkout, no local git base)"
    )
    repo = info.repo_name
    if not repo:
        raise RuntimeError("Could not resolve repository (Info().repo_name is empty)")
    with tempfile.TemporaryDirectory(prefix="crd-baseline-") as dest:
        staged, skipped = _stage_baselines(repo, base, dest)
        info_lines.append(f"baselines: {staged} staged, {skipped} new/absent (skipped)")
        return _run_make([f"CRD_BASELINE_DIR={dest}"])


def main():
    sw = Utils.Stopwatch()
    info_lines = []
    try:
        rc, output = _check(info_lines)
    except Exception as e:  # unexpected: resolution/gh/make plumbing failure
        Result.create_from(
            status=Result.Status.ERROR,
            stopwatch=sw,
            info="\n".join(info_lines + [f"ERROR: {e}"]),
        ).complete_job()
        return

    if rc == 0:
        info_lines.append("No backward-incompatible CRD changes.")
        status = Result.Status.OK
    else:
        status = Result.Status.FAIL
        info_lines += ["", "Backward-incompatible CRD changes detected:", output.strip()]

    Result.create_from(
        status=status,
        stopwatch=sw,
        info="\n".join(info_lines),
    ).complete_job()


if __name__ == "__main__":
    main()
