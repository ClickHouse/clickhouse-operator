#!/usr/bin/env python3
"""
ci.yaml :: check-crd-compat, migrated to praktika.

The crd-breaking-change label gate lives in the workflow filter hook
(ci/jobs/filter_job_hook.py), not here — so this script makes no `gh` calls.

What the job does: `make check-crd-compat` reads each committed CRD manifest as
it existed at the base ref (`git show origin/<base>:config/crd/bases/*.yaml`),
writes it to a baseline file, and runs crd-schema-checker to compare that
baseline against the PR's current CRD — failing on backward-incompatible schema
changes.

Why fetch the base branch: that `git show origin/<base>:...` needs the base
commit's blobs in the local object store. Praktika checks out a shallow,
ephemeral PR merge commit that does NOT carry base history, so we fetch the base
branch first; otherwise every CRD would look like a brand-new file and the check
would silently pass. We fetch just the one branch, no tags, at depth 1 — the
check only reads the base tip's CRD blobs (`git show origin/<base>:...`), so no
history is needed.
"""
import subprocess
import sys

from praktika.info import Info


def _run(cmd):
    print(f"+ {' '.join(cmd)}", flush=True)
    subprocess.run(cmd, check=True)


def main() -> int:
    base = Info().base_branch or "main"
    _run([
        "git", "fetch", "--no-tags", "--depth=1", "origin",
        f"+refs/heads/{base}:refs/remotes/origin/{base}",
    ])
    _run(["make", "check-crd-compat", f"CRD_BASE_REF=origin/{base}"])
    return 0


if __name__ == "__main__":
    sys.exit(main())
