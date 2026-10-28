# `factory clean --remote-branch`

`factory clean --remote-branch NAME [NAME...] [--dry-run]` (the flag may repeat) deletes stale `factory/*` branches on `origin` so nobody needs raw `git push --delete`. Allowed: director, cleaner, owner; other workers exit 3.

## Safety checks (all must pass, else the name is refused)
1. The name matches `^factory/[A-Za-z0-9._/-]+$`, is at most 200 chars and passes `git check-ref-format refs/heads/<name>`. `main`, `state`, `HEAD` and anything outside `factory/` are refused.
2. The branch exists on origin (`git ls-remote`); its head is H.
3. `gh pr list --state all --head <name> --limit 100` yields same-repo PRs only (fork PRs are ignored; none left = refuse). Any OPEN PR refuses. Otherwise the PR whose `headRefOid` equals H is used (newest if several); none means refuse (this catches a branch force-pushed after its PR closed). That PR must be MERGED or CLOSED.
4. `refs/pull/<n>/head` on origin equals H, so the content stays recoverable after the delete.
5. No worktree has the branch checked out, no task with `branch == name` is non-terminal (not done/merged/cancelled), and no queued/starting/live agent works on such a task.

Any git or gh error, timeout (60 s), bad JSON or empty answer refuses. All git calls use the hardened helper (`docs/git-hardening.md`).

## Action
`git push --porcelain --force-with-lease=refs/heads/<name>:<H> origin :refs/heads/<name>`; the lease pins H, so a branch that moved after the check is not deleted. A non-zero exit or output without a `[deleted]` line refuses with `push failed`. The local branch is deleted only if it equals H or is an ancestor of H; otherwise it is kept and reported.

## Output and log
A per-name table (`branch`, `result`, `reason`) and a total line; exit 4 if any name is refused. `--dry-run` runs the checks and changes nothing, logs nothing. Events: `branch-deleted` `{name, sha, pr, pr_state}` and `branch-delete-refused` `{name, reason}`.

Tests: `tests/test_clean_remote_branch.py` (a local bare origin, stubbed `gh pr list`).
