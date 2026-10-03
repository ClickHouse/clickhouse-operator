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

    return False, ""
