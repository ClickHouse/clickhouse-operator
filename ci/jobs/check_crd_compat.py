#!/usr/bin/env python3
"""
ci.yaml :: check-crd-compat, migrated to praktika.

The crd-breaking-change label gate lives in the workflow filter hook
(ci/jobs/filter_job_hook.py), not here.

What the job does: it obtains each CRD's baseline (the manifest as it currently
exists on the base branch) and runs `make check-crd-compat`, which compares each
baseline against the PR's current CRD with crd-schema-checker and fails on
backward-incompatible schema changes. The verdict is returned as a praktika
Result (OK / FAIL / ERROR) carrying the baseline ref/source, staged counts, and
— on failure — the checker's violations.

The baseline commit is the PR's merge-base from Info().base_git_history() (entry
0, newest first) — a real base-branch commit the controller resolves even for
jobs on a history-free S3 snapshot, which keeps a 404 unambiguous. Falls back to
the base branch name when unavailable (local runs).

Two ways to read the manifests at that ref, picked automatically:

1. git history (preferred): a clone with `origin` shallow-fetches the ref and
   reads `git show FETCH_HEAD:...`, no per-file network call.

2. GitHub contents API (fallback): on an S3 snapshot there is no `origin`, so the
   fetch fails; we pull the base manifests via `gh` into a temp dir and pass it as
   CRD_BASELINE_DIR. A 404 means the path is new on the base and is skipped.
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


def _resolve_base_ref(info):
    """Baseline ref: the PR merge-base SHA from Info().base_git_history() (entry 0,
    newest first), falling back to the base branch name when unavailable (local
    runs). Returns (ref, human_label)."""
    history = info.base_git_history()
    if history:
        sha = history[0]
        return sha, f"merge-base {sha[:12]}"
    base = info.base_branch or "main"
    return base, f"base branch {base}"


def _try_fetch_git_base(ref):
    """Shallow-fetch `ref` so `git show FETCH_HEAD:<path>` resolves base CRDs.
    Returns True on success (clone with `origin`), False otherwise (snapshot has
    no remote)."""
    cmd = ["git", "fetch", "--no-tags", "--depth=1", "origin", ref]
    print(f"+ {' '.join(cmd)}", flush=True)
    return subprocess.run(cmd).returncode == 0


def _stage_baselines(repo, ref, dest):
    """Download each CRD's version at `ref` into `dest` via gh api.

    `--method GET` is required: `gh api` switches to POST once any `-f` field is
    present, and the contents endpoint 404s on POST — which we'd misread as "new
    CRD" for every file and pass vacuously.

    Returns (staged, skipped): a genuine 404 (file absent at `ref`) is left
    unstaged so the Makefile treats it as new; raises on any other gh failure."""
    staged = skipped = 0
    for crd in sorted(glob.glob(CRD_GLOB)):
        cmd = [
            "gh", "api",
            "--method", "GET",
            "-H", "Accept: application/vnd.github.raw",
            f"repos/{repo}/contents/{crd}",
            "-f", f"ref={ref}",
        ]
        print(f"+ {' '.join(cmd)}", flush=True)
        proc = subprocess.run(cmd, capture_output=True, text=True)
        if proc.returncode == 0:
            with open(os.path.join(dest, os.path.basename(crd)), "w") as f:
                f.write(proc.stdout)
            print(f"  staged baseline for {crd}", flush=True)
            staged += 1
        elif "404" in proc.stderr or "Not Found" in proc.stderr:
            print(f"  {crd} absent at {ref} — new CRD, will skip", flush=True)
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
    ref, label = _resolve_base_ref(info)
    info_lines.append(f"baseline ref: {label}")

    if _try_fetch_git_base(ref):
        info_lines.append("baseline source: git history (FETCH_HEAD)")
        return _run_make(["CRD_BASE_REF=FETCH_HEAD"])

    info_lines.append(
        "baseline source: GitHub API (snapshot checkout, no local git base)"
    )
    repo = info.repo_name
    if not repo:
        raise RuntimeError("Could not resolve repository (Info().repo_name is empty)")
    with tempfile.TemporaryDirectory(prefix="crd-baseline-") as dest:
        staged, skipped = _stage_baselines(repo, ref, dest)
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
