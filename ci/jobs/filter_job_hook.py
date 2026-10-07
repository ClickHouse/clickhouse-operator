"""
Workflow filter hooks for the praktika workflows.

A hook has the signature `should_skip_job(job_name) -> (skip: bool, reason: str)`
and is wired via `Workflow.Config(workflow_filter_hooks=[should_skip_job])`. The
orchestrator calls it once per job while building the run (see
praktika.native_jobs), i.e. where `praktika.info.Info` is populated — so label
gating needs no `gh` call from inside the job.
"""
from praktika.info import Info

_info = None


def should_skip_job(job_name):
    global _info
    if _info is None:
        _info = Info()

    # ci.yaml :: check-crd-compat only ran when the PR was NOT labeled as an
    # intentional CRD-breaking change. Reproduce that gate here.
    if (
        job_name == "Check CRD Compatibility"
        and "crd-breaking-change" in _info.pr_labels
    ):
        return True, "PR labeled 'crd-breaking-change'"

    # dependabot-regenerate.yaml's `if:` guard, moved here so the job is listed
    # in every PR run but only *runs* for a Dependabot dependency bump. It pushes
    # to the PR head branch, so it is restricted to same-repo PRs (a fork head is
    # not ours to push to) — exactly the original `head.repo == repository` check.
    if job_name == "Dependabot Regenerate":
        actor = _info.user_name or ""
        if actor != "dependabot[bot]":
            return True, f"PR actor '{actor}' is not dependabot[bot]"
        if _info.fork_name and _info.fork_name != _info.repo_name:
            return True, "PR is from a fork"
        changed = _info.get_changed_files() or []
        if not any(f in ("go.mod", "go.sum") for f in changed):
            return True, "no go.mod/go.sum changes"

    return False, ""
