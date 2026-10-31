#!/usr/bin/env python3
"""
The crd-breaking-change label gate lives in the workflow filter hook
(ci/jobs/filter_job_hook.py), not here.

What the job does: it obtains each CRD's baseline (the manifest at the baseline
ref below) and runs `make check-crd-compat`, which compares each
baseline against the PR's current CRD with crd-schema-checker and fails on
backward-incompatible schema changes. The verdict is returned as a praktika
Result (OK / FAIL / ERROR) carrying the baseline ref/source and — on failure —
the checker's violations.

The baseline commit is the PR's merge-base from Info().base_git_history() (entry
0, newest first) — a real base-branch commit the controller resolves even for
jobs on a history-free S3 snapshot. Falls back to the base branch name when
unavailable (local runs).

The baseline is read straight from git history: a shallow `git fetch origin <ref>`
makes `git show FETCH_HEAD:<path>` resolve the base CRDs, no per-file network
call. This works on S3-snapshot runners too — the controller now bakes a
token-less `origin` into the snapshot (see praktika merge.py
_build_and_publish_snapshot), so there is no longer a no-remote fallback. A fetch
failure is therefore a real error (ERRORs the job) rather than a silent degrade
to the GitHub contents API, which could vacuously pass if every per-file lookup
404'd.
"""
import subprocess
import sys

from praktika.info import Info
from praktika.result import Result
from praktika.utils import Utils


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


def _fetch_git_base(ref):
    """Shallow-fetch `ref` so `git show FETCH_HEAD:<path>` resolves base CRDs.

    Raises on failure: `origin` is always present (a normal clone, or an S3
    snapshot with the controller-baked token-less origin), so a failed fetch means
    the ref is genuinely unreachable — not a missing remote — and must not be
    masked."""
    cmd = ["git", "fetch", "--no-tags", "--depth=1", "origin", ref]
    print(f"+ {' '.join(cmd)}", flush=True)
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if proc.returncode != 0:
        sys.stderr.write(proc.stderr)
        raise RuntimeError(
            f"git fetch of baseline ref {ref} failed (rc={proc.returncode}): "
            f"{proc.stderr.strip()}"
        )


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

    _fetch_git_base(ref)
    info_lines.append("baseline source: git history (FETCH_HEAD)")
    return _run_make(["CRD_BASE_REF=FETCH_HEAD"])


def main():
    sw = Utils.Stopwatch()
    info_lines = []
    try:
        rc, output = _check(info_lines)
    except Exception as e:  # unexpected: resolution/fetch/make plumbing failure
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
