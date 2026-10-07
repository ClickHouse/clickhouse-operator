#!/usr/bin/env python3
"""
Workflow pre-hook: post the CI report link into the PR description.

Wired via `Workflow.Config(pre_hooks=["python3 ./ci/jobs/ci_links.py"])`. It runs
once at the start of a PR run (before the jobs) and appends a machine-owned block
with a link to this run's report, so a reviewer can jump from the PR straight to
the live CI report without hunting for the check run.

Modeled on ClickHouse/ClickHouse ci/jobs/scripts/workflow_hooks/ci_links.py, minus
the upstream-sync bits that don't apply to this single-repo project.
"""
import tempfile
import traceback

from praktika.gh import GH
from praktika.info import Info

BLOCK_START = "<!-- CI automatic block start :ci_links: -->"
BLOCK_END = "<!-- CI automatic block end :ci_links: -->"


def has_block(body):
    return BLOCK_START in body and BLOCK_END in body


def append_block(body, block):
    if body.strip():
        return body.rstrip() + "\n\n" + block + "\n"
    return block + "\n"


def main():
    info = Info()
    if info.pr_number <= 0:
        print("NOTE: Not a PR run - skip PR description update")
        return

    if has_block(info.pr_body or ""):
        print("NOTE: CI links already present - skip PR description update")
        return

    workflow_line = (
        f"Workflow [[{info.workflow_name}]({info.get_report_url(latest=True)})]"
    )
    # A horizontal rule visually separates the machine-owned block from the
    # user-authored description. It lives inside the block markers so it is
    # dropped together with the block.
    block = f"{BLOCK_START}\n\n---\n{workflow_line}\n{BLOCK_END}"

    title, body, _labels, _is_draft = GH.get_pr_title_body_labels()
    if not title:
        print("WARNING: Failed to fetch PR data - skip PR description update")
        return

    new_body = append_block(body or "", block)

    with tempfile.NamedTemporaryFile(mode="w", suffix=".txt", encoding="utf-8") as f:
        f.write(new_body)
        f.flush()
        if not GH.update_pr_body(body_file=f.name):
            print("WARNING: Failed to update PR description")
            return

    info.env.PR_BODY = new_body
    info.env.dump()
    print("PR description updated with CI links")


if __name__ == "__main__":
    try:
        main()
    except Exception:
        traceback.print_exc()
