# Staleness protocol (G-013 B)

Main moves while review and QA run. A head that is only *behind* `origin/main`, or that conflicts with a PR merged
meanwhile, used to get a full FAIL -> coder -> reviewer -> QA loop even when its content was approved. This
protocol handles staleness with a mechanical merge and the verdict carry-over instead.

## The protocol

| stage | rule |
|---|---|
| reviewer, QA (S1) | Head behind origin/main and `git merge-tree --write-tree origin/main HEAD` exits 0: also run the suite on the merged tree (scratch worktree), judge the content, and PASS with `STALE-BASE: behind origin/main by N; merge-tree clean; merged-tree suite Ran N OK`. If it conflicts: FAIL with first line `CONFLICT-ONLY: <files>`, and still verify the content. |
| QA (Q5) | The C-item "origin/main merged before the PR" is PASS-with-STALE-BASE when the only gap is main moving after the coder's last merge. A coder who never merged main is still FAIL. |
| judge (J4) | Merge only if `git merge-base --is-ancestor origin/main <head>` and CI is green on that head. The judge runs `factory judge --merge` in the background and waits (up to `judge_ci_wait_s`, 2400 s by default, plus refreshes). The CLI refreshes a clean stale head itself (at most `judge_max_refreshes` times, default 4, then `STALE-LOOP`). The judge rejects as stale only when the CLI refuses with `STALE-ONLY:`, `STALE-LOOP:` or `CONFLICT-ONLY:` and nothing else is wrong; other failures are listed first. |
| lead (fix-loop step 6) | A STALE-BASE PASS: `factory refresh T` (docs/refresh.md), CI green, then the judge. A `STALE-LOOP` reject: `factory refresh T` by hand, then the judge. `CONFLICT-ONLY` (judge reject or refresh exit 3): a merge-only coder that starts with `git pull` of its own branch, then a delta reviewer + QA on the conflict hunks only. None of this is an attempt. |

A PASS on an earlier head stays valid when the newer commits are only origin/main merges: the CLI decides
(docs/verdict-carry-over.md). The pure main merge pushed by `factory judge --merge` or `factory refresh` is a
mechanical CLI step, not code authorship; the judge never edits code.

## The gate is not weakened

- The final merged head still contains origin/main and has its own green CI run, so semantic conflicts are still
  caught. E2276 is the example: a clean merge-tree with red tests. Clean merge-tree is never taken as "safe".
- The verdict carries over only by the machine rule of T-0095 (pure merges only, checked by the CLI). A merge
  with conflict resolution gets a delta re-verdict.
- Reviewer and QA additionally test the merged tree when the head is stale (S1).
- The C-item "origin/main merged" is still enforced. The stage that enforces it is now the `cmd_judge` ancestor
  check of T-0152 (`factory judge --merge` refuses with `STALE-ONLY: head does not contain origin/main` unless
  origin/main is an ancestor of the head, fail closed), plus J4 in the judge prompt, instead of a QA FAIL
  mid-review.

## Evidence (retro T-0147)

| event | what happened |
|---|---|
| E4522 | reviewer/QA/judge failed a head for staleness only, although T-0095 carry-over existed; the carry-over was never used |
| E4478 | same: content approved, FAIL because behind origin/main |
| E4424 | same: stale-base FAIL restarted the full loop |
| E2276 | clean merge-tree but red tests on the merged tree: why CI on the final head and S1 merged-tree runs stay |

Classes C1b+C1c: 20 rejections (15 residual after T-0096).

## Spawn pre-check (T-0172)

A verifier spawned on a head that is already stale burns a round later, so `factory spawn reviewer|qa|judge --task T`
(and the tick when it starts a queued spawn, because main moves while a spawn waits) checks the PR head first. The
pre-check only applies to a task with a PR; coder, pm and every other role are never checked (no git or gh call).

Three stages catch staleness; each has one job:

| stage | where | what it does |
|---|---|---|
| spawn (T-0172, T-0488) | `factory spawn`, queued start | judge: refreshes a clean-behind head; reviewer/qa: only refuse a conflict (no refresh); refuses a refresh in flight |
| review (T-0149 S1) | reviewer / QA verdict | a clean-behind head still gets PASS with STALE-BASE |
| merge (T-0152 STALE-ONLY) | `factory judge --merge` | the fail-closed gate: head must contain origin/main |

After the verified fetch of `refresh_branch` (the spawn pre-check reuses `refresh_branch`, see docs/refresh.md):

- head contains origin/main: the spawn proceeds, nothing is pushed.
- reviewer/qa, head behind and the merge is clean (T-0488): nothing is pushed (`refresh_branch` dry run), the spawn
  proceeds on the current head: a refresh would restart CI before review starts and the judge refreshes before merging.
- judge, head behind and the merge is clean: `refresh_branch` pushes one pure main merge, events `refreshed` (`"how": "by spawn
  pre-check"`) and `note` are recorded, the refresh line goes to stderr, and the spawn proceeds on the new head.
- head behind and the merge conflicts: refused for all three roles, exit 2, `CONFLICT-ONLY: <files>; spawn a merge-only
  coder (fix-loop, not an attempt)`, event `spawn-bounced`, no agent row. A queued spawn is cancelled through the
  normal dequeue path and the team lead gets a report.
- refresh in flight (per-PR lock held): refused, `REFRESH-IN-FLIGHT: PR #<n>; ...`, exit 2, `spawn-bounced`.

**Fail-open rule:** any fetch, git or gh error, or a refresh error (including a lost push lease), lets the spawn
proceed with one stderr line `staleness pre-check skipped: <reason>` and an event `precheck-skipped`. Only these error
cases are allowed through; a verified conflict or in-flight refresh is never allowed. The pre-check writes no verdict and
skips none: the merge gate (T-0152 STALE-ONLY, CI green, verdict carry-over) is unchanged.
