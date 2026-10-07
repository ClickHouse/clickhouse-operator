#!/usr/bin/env python3
"""
.github/workflows/dependabot-regenerate.yaml, migrated to praktika.

What it does: when Dependabot opens a PR that bumps go.mod/go.sum, the committed
*generated* artifacts (CRDs/RBAC, deepcopy, helm chart, API-reference docs) go
stale. This job re-derives them and pushes the result back onto the PR branch, so
the bot PR is self-consistent before the rest of the suite judges it.

Applicability is decided *outside* this script, in the workflow filter hook
(ci/jobs/filter_job_hook.py): it runs only for a same-repo `dependabot[bot]` PR
that actually touched go.mod/go.sum. So here we assume we should regenerate.

Why this can't be a `git diff --exit-code` check like Lint: Lint only *detects*
staleness and fails; this job *fixes* it and pushes, which is the whole point of
the original dependabot-regenerate workflow — a human never has to touch a bot PR.

Branch handling: praktika checks out the PR's ephemeral `refs/pull/<N>/merge`
commit (detached, no remote). Committing there would commit the merge, not the
head. So we fetch the real head branch, check it out, regenerate on top of it, and
push that — exactly what the original workflow's `checkout ref: head.ref` did.

Push credentials: the original pushed with GITHUB_TOKEN, whose pushes GitHub
deliberately does NOT re-trigger workflows on — hence its explicit trailing
`gh workflow run`. We push with a GitHub App installation token minted here
(GHAuth), and App-token pushes DO emit a `synchronize` event, so the fresh PR run
starts on its own; no manual re-dispatch needed. The token minter must grant
`contents: write`.
"""
import subprocess
import sys

from praktika.info import Info
from praktika.gh_auth import GHAuth

# Mirrors dependabot-regenerate.yaml's commit identity.
_GIT_USER_NAME = "github-actions[bot]"
_GIT_USER_EMAIL = "41898282+github-actions[bot]@users.noreply.github.com"

# dependabot-regenerate.yaml :: "Regenerate after dependency bump". KUBEBUILDER
# points generate-helmchart-ci at the binary the go-env pre-hook provisioned,
# matching the Helm Test job, instead of re-downloading it.
_REGEN_CMDS = [
    ["go", "mod", "tidy"],
    [
        "make",
        "generate",
        "manifests",
        "generate-helmchart-ci",
        "docs-generate-api-ref",
        "KUBEBUILDER=/usr/local/bin/kubebuilder",
    ],
]


def _run(cmd, **kw):
    print(f"+ {' '.join(cmd)}", flush=True)
    return subprocess.run(cmd, check=True, **kw)


def _dirty() -> bool:
    out = subprocess.run(
        ["git", "status", "--porcelain"], check=True, capture_output=True, text=True
    ).stdout.strip()
    return bool(out)


def main() -> int:
    info = Info()
    repo = info.repo_name  # owner/repo
    head = info.git_branch  # dependabot's head branch (same-repo, hook-guarded)
    if not head:
        print("No head branch in context; nothing to regenerate")
        return 0

    # contents:write to push the regenerated commit back onto the PR branch.
    token = GHAuth.get_installation_token(required_permissions={"contents": "write"})
    remote = f"https://x-access-token:{token}@github.com/{repo}.git"

    # The ephemeral merge checkout has no remote; add one and lay the real head
    # branch on top of the working tree before regenerating.
    subprocess.run(["git", "remote", "remove", "origin"], check=False)
    _run(["git", "remote", "add", "origin", remote])
    _run(["git", "fetch", "--no-tags", "--depth=1", "origin", head])
    _run(["git", "checkout", "-B", head, "FETCH_HEAD"])

    for cmd in _REGEN_CMDS:
        _run(cmd)

    if not _dirty():
        print("Nothing to regenerate")
        return 0

    _run(["git", "config", "user.name", _GIT_USER_NAME])
    _run(["git", "config", "user.email", _GIT_USER_EMAIL])
    _run(["git", "add", "-A"])
    _run(["git", "commit", "-m", "chores: regenerate after dependency bump"])
    # App-token push → GitHub emits `synchronize` → fresh PR run starts itself.
    _run(["git", "push", "origin", f"HEAD:refs/heads/{head}"])
    print(f"Pushed regenerated files to {head}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
