#!/usr/bin/env python3
"""
ci.yaml :: lint, migrated to praktika.

Reproduces the GitHub `lint` job as a driver that emits a structured praktika
Result instead of the bare pass/fail praktika would otherwise synthesize from the
shell exit code. The job is two concerns, each broken into sub-results so the
report page shows exactly what failed:

  1. "regeneration is committed" gates — `go mod tidy`, `make generate`,
     `make manifests` must not change any tracked file. Each is one sub-result;
     on drift it carries the offending `git diff`. Tracked changes are reverted
     between gates so one gate's drift never pollutes the next gate's diff.

  2. `make lint`'s three linters, run individually so each is its own sub-result:
       - golangci-lint — JSON output parsed into one sub-result per issue
         (file:line [linter] -> message), so the report renders an issue table
         instead of a single pass/fail (the replacement for reading raw logs).
       - codespell
       - actionlint

The top-level Result aggregates (any sub-result FAIL -> job FAIL). The linter
binaries are installed into ./bin by the Makefile targets, served from the warm
Go module + pip caches provisioned by the go-env pre-hook (ci/jobs/go_env.py).
"""
import json
import os
import subprocess
import sys

sys.path.insert(0, os.getcwd())  # ensure repo root is importable for ci.*

from praktika.result import Result  # noqa: E402
from praktika.utils import Utils  # noqa: E402

# Makefile installs the linters into ./bin ($(LOCALBIN)); we invoke them directly
# (instead of `make lint`) to capture each linter's output separately.
GOLANGCI = "bin/golangci-lint"
CODESPELL = "bin/codespell"
ACTIONLINT = "bin/actionlint"


def _run(cmd, env=None):
    """Run a shell command, echoing it and its combined output. Returns (rc, output)."""
    print(f"+ {cmd}", flush=True)
    proc = subprocess.run(cmd, shell=True, text=True, capture_output=True, env=env)
    output = (proc.stdout or "") + (proc.stderr or "")
    if output:
        print(output, end="", flush=True)
    return proc.returncode, output


def _git_restore():
    """Revert tracked-file modifications (best-effort), so each regeneration gate
    starts from the committed tree. Untracked files are left alone."""
    _run("git checkout -- .")


def _ensure_tools():
    """Install the lint tool binaries into ./bin via the Makefile. Cheap thanks to
    the warm Go module + pip caches from the go-env pre-hook. Raises on failure."""
    rc, out = _run("make golangci-lint codespell actionlint")
    if rc != 0:
        raise RuntimeError(f"failed to install lint tools (exit {rc}):\n{out.strip()}")


def _diff_gate(name, cmd):
    """Run a regeneration command, then fail if it dirtied any tracked file —
    mirroring ci.yaml's `<cmd> && git diff --exit-code`."""
    sw = Utils.Stopwatch()
    rc, out = _run(cmd)
    if rc != 0:
        _git_restore()
        return Result.create_from(
            name=name, status=Result.Status.FAIL, stopwatch=sw,
            info=f"`{cmd}` failed (exit {rc}):\n{out.strip()}",
        )
    diff = _run("git diff")[1]
    _git_restore()
    if diff.strip():
        return Result.create_from(
            name=name, status=Result.Status.FAIL, stopwatch=sw,
            info=f"`{cmd}` is not committed — it changed tracked files:\n{diff.strip()}",
        )
    return Result.create_from(name=name, status=Result.Status.OK, stopwatch=sw)


def _golangci_result():
    """Run golangci-lint with JSON output; one leaf result whose info lists every
    reported issue (lint findings aren't independent test cases, so a flat list
    reads better than a nested table)."""
    sw = Utils.Stopwatch()
    # json -> file (parsed here), text -> stdout (human-readable in the job log).
    # They must not share a stream: a single stdout would interleave the JSON
    # object with the text summary and break json.load.
    # Under ci/tmp so codespell (which runs later over the worktree) skips it —
    # its skip list already covers ci/tmp, and the report contains linter names
    # like "decorder" that codespell would otherwise flag.
    report = "ci/tmp/golangci-report.json"
    os.makedirs("ci/tmp", exist_ok=True)
    rc, out = _run(f"{GOLANGCI} run --output.text.path=stdout --output.json.path={report}")

    try:
        with open(report) as f:
            issues = (json.load(f) or {}).get("Issues") or []
    except (OSError, json.JSONDecodeError) as e:
        # No/invalid report -> golangci-lint itself failed (bad config, panic, OOM).
        return Result.create_from(
            name="golangci-lint", status=Result.Status.ERROR, stopwatch=sw,
            info=f"could not read golangci-lint report (exit {rc}): {e}\n{out.strip()[:2000]}",
        )

    if not issues:
        if rc == 0:
            return Result.create_from(name="golangci-lint", status=Result.Status.OK, stopwatch=sw)
        # Non-zero exit with no issues parsed -> a run error, not a lint finding.
        return Result.create_from(
            name="golangci-lint", status=Result.Status.ERROR, stopwatch=sw,
            info=f"golangci-lint exited {rc} with no issues reported",
        )

    lines = [f"{len(issues)} issue(s):", ""]
    for it in issues:
        pos = it.get("Pos") or {}
        loc = f"{pos.get('Filename', '?')}:{pos.get('Line', 0)}:{pos.get('Column', 0)}"
        linter = it.get("FromLinter", "")
        lines.append(f"{loc}: {(it.get('Text') or '').strip()} ({linter})")
    return Result.create_from(
        name="golangci-lint", status=Result.Status.FAIL, stopwatch=sw,
        info="\n".join(lines), files=[report],
    )


def _cmd_result(name, cmd):
    """A leaf sub-result: OK when `cmd` exits 0, else FAIL carrying its output."""
    sw = Utils.Stopwatch()
    rc, out = _run(cmd)
    if rc == 0:
        return Result.create_from(name=name, status=Result.Status.OK, stopwatch=sw)
    return Result.create_from(
        name=name, status=Result.Status.FAIL, stopwatch=sw, info=out.strip(),
    )


def main():
    sw = Utils.Stopwatch()
    try:
        _ensure_tools()
    except Exception as e:
        Result.create_from(
            status=Result.Status.ERROR, stopwatch=sw, info=str(e),
        ).complete_job()
        return

    results = [
        _diff_gate("go mod tidy", "go mod tidy"),
        _diff_gate("make generate", "make generate"),
        _diff_gate("make manifests", "make manifests"),
        _golangci_result(),
        _cmd_result("codespell", f"{CODESPELL} --config ci/.codespellrc"),
        _cmd_result("actionlint", f"{ACTIONLINT} -config-file ci/actionlint.yaml"),
    ]
    # name/status omitted: create_from defaults name to JOB_NAME ("Lint") so the
    # runner's Result.from_fs(job.name) finds this file, and derives the status by
    # aggregating the sub-results (any FAIL/ERROR -> job fails).
    Result.create_from(results=results, stopwatch=sw).complete_job()


if __name__ == "__main__":
    main()
