# factory prcheck: the coder's pre-PR check (G-022)

`factory prcheck` runs in a coder's task worktree and answers one question before the PR goes to review: would the usual
first-round rejections (stale or conflicting branch, wrong identity, bad PR body, public-text hit, red tests) happen?
It prints one `PASS`/`FAIL` line per check and exits 0 only when all pass.

| # | check | fails when |
|---|---|---|
| 1 | fresh | `origin/<base>` cannot be fetched, or merging it into HEAD conflicts. Behind but clean passes (the refresh lane brings the branch up to date) |
| 2 | identity | any commit in `origin/<base>..HEAD` has a non-noreply author or committer, or there are no commits |
| 3 | body | the PR body departs from `.github/pull_request_template.md` (same rules as `in_review`), `## Test evidence` has no backticked command, or the `def test_` count under `tests/` is lower than at the merge base with `origin/<base>` and the body has no `Tests removed: <what and why>` line outside `<details>` |
| 4 | public-text | the repo is in `public_text_repos` and the G-021 guard (same code as the judge) finds a hit or cannot decide. `n/a` for other repos |
| 5 | affected | `ci/affected.py --base origin/<base> --run` exits non-zero or reports no tests run |
| 6 | tier | informational: the risk tier of the diff, once the classifier (G-022 item 1) is in the build; otherwise `unavailable` |

Uncommitted tracked changes stop the run (exit 3): the result is recorded for a HEAD. `--pr <url>` overrides the task's PR.

## Gating `factory done`

Config `prcheck_required` (default `0` = off). With `1`, `factory done` of a coder whose task has a PR is refused (exit 3,
`--force` does not skip it) unless the latest `prcheck` event of the task is a pass on the worktree's current HEAD. A new
commit, or a later failing run on the same HEAD, needs a new pass. The record is a `prcheck` task event
(`{"head", "ok", "failed"}`).

Rollback: `factory config prcheck_required 0`.
