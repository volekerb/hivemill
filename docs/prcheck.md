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

Green-CI alternative (T-0873): with no passing event on HEAD, `factory done` still passes when the PR is OPEN, its
`headRefOid` equals the local HEAD, it has checks, every check is green and the required checks are present (the judge's
`checks_ok`/`missing_checks`). The cheap parts (fresh, identity, body, public text) are rerun; the affected-tests run is
skipped. Anything undecidable (gh error, no checks, head mismatch) refuses as before.

Rollback: `factory config prcheck_required 0`.

## CI handoff (`coder_ci_handoff`, T-1003)

Config `coder_ci_handoff` is `off` (default), `resume` or `fresh`. With `resume` or `fresh` and `prcheck_required=1`, a coder's
`factory done` on an `in_review` task with a PR no longer waits for CI: the PR must be open and its head must be the pushed local
HEAD, and the cheap parts (fresh, identity, body, public text) must pass; the affected-tests/CI part is skipped, so a pending or red
head is accepted. An unpushed commit, a dirty tree, a body or public-text FAIL still exit 3. The done summary to the lead gets the line
`CI pending, tick starts review at green, do not spawn reviewer.` whenever CI was not green at done. The coder kickoff says not to wait
for CI. `resume` and `fresh` behave the same today (the names are reserved for how a later red-CI fix round restarts the coder).
With `off` the gate is byte-identical to before. The merge gate (`factory judge`, merger) is untouched: a red or pending head is
still refused. First-round review waits for green: see [review-autospawn.md](review-autospawn.md).

The handoff is active only when `review_autospawn=on` (otherwise nothing would start the review, so the old CI gate stays) and
only for a task that is `in_review`: an `in_progress` task, e.g. after a judge reject, keeps the CI gate. The handoff line is added
whenever the PR head is not green, including a PR that has no checks yet.
