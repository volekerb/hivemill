# Verdict carry-over

Reviewer and QA verdicts survive a pure `git merge origin/main`. `factory judge` accepts a PASS recorded at head X for the PR's current head Y when Y == X, or when every commit on the first-parent path from Y back to X is a pure main merge. For each such commit C:

1. C has exactly two parents, and its first parent is the next commit on the path (the last step's first parent is X).
2. Its second parent is reachable from `origin/main` (`git merge-base --is-ancestor`, after `git fetch origin main`).
3. The merge has no manual edits: `git merge-tree --write-tree <first parent> <second parent>` exits 0 and prints the same tree as `C^{tree}`. A conflict-resolved merge or a merge with extra edits is refused.

The walk covers at most 20 commits. If X is not reached, the verdict is stale. CI green on Y, open PR, not a draft, mergeable and the required checks are unchanged.

- The helper `verdict_carries` runs in the task's worktree (else FACTORY_HOME) and only fetches main and `pull/<n>/head`, each with a 60 s timeout. Every git call runs with `GIT_NO_REPLACE_OBJECTS=1` and `GIT_GRAFT_FILE=/dev/null`, so replace refs and grafts cannot disguise a commit. It does not trust the shared git config: it fetches from the URL given by `gh repo view` with an explicit refspec into private refs (`refs/factory-judge/*`), then refuses unless the fetched main equals `gh api repos/<owner>/<repo>/commits/main` and the fetched PR head equals the head being judged. A failing `merge-base --is-ancestor` (exit other than 0/1) is reported as a git error, not as "not on origin/main". It never checks out, resets or changes a working tree.
- An accepted carry records a `verdict-carried` event per role (`role`, `from`, `to`, `main`, `merges`) and prints a line saying so, only after `gh pr merge` has succeeded; a failed merge records no carried event.
- A refused carry keeps the usual text and appends the reason, e.g. `reviewer PASS is for 1234abcd, head is now 5678ef01 (not a pure main merge: tree differs at 9abc0123)`.
- Git hardening (hooks, fsmonitor, merge drivers, filters, attributes, commit-graph) and the `gh api` timeout refusal are described in [git-hardening.md](git-hardening.md).

## Fix carry

T-1611, owner decision M37570 D3. After a FAIL only the failed role re-checks; the other role's PASS carries across a small, non-sensitive fix. The gate is still judge + reviewer + QA + CI: the merged head has a PASS of one role recorded on the head itself (the role that found the defect) and the other role's PASS carried from an older head, and CI must be green on that head.

Terms: X is the head a PASS was recorded at, Y is the PR's current head, the "failed role" is the role (reviewer or qa) with a FAIL at X or later, the "other role" is the one whose latest verdict is a PASS at X.

The other role's PASS at X counts for Y when all seven hold. Everything is evaluated by one helper (`fix_carry_for` / `fix_carry_walk`, called from `verdict_pass_holds_for_pr_head` after the pure-main-merge carry above), so `factory judge`, the mechanical merger (`factory mech-merge`), the train check and the judge autospawn agree.

1. `fix_carry` is `on` (default). `off` restores the old behaviour exactly.
2. The failed role's latest verdict is a PASS recorded at Y, or at a head from which the pure-main-merge carry reaches Y.
3. The failed role has at least one FAIL at X or at a head between X and Y, and the other role has no FAIL after its PASS at X (its latest verdict is that PASS).
4. Every commit on the first-parent path from Y back to X is a pure main merge (the `verdict_carries` test, unchanged) or a non-merge commit (exactly one parent). A merge with manual edits or a conflict resolution: no carry.
5. The non-merge commits together add + delete at most `fix_carry_max_lines` lines (default 150, the `small_lines` value of `ci/tier_rules.json`), counted with `git diff --numstat` per commit. A binary file change: no carry.
6. None of those commits touches a sensitive path or sensitive def of `ci/tier_rules.json` (the matcher of `ci/risk_tier.py` of the main checkout, `reasons()`; the lists are not copied), and none removes or renames a test function: a removed `def test_` or `func Test` line in the commit's diff.
7. The walk covers at most 20 commits, every git call uses the hardened flags and private refs of `verdict_carries`, and any git, fetch, gh or rules-file error means no carry ("cannot verify").

Otherwise the PASS at X is stale as before and both roles are needed on Y.

Examples (reviewer = R, qa = Q):

- (a) R FAIL @X, Q PASS @X; the coder pushes one 40-line commit touching `bin/web/x.py` -> Y; R PASS @Y. Q's PASS carries, the judge may merge.
- (b) Same, but the fix is 300 lines: no carry, Q re-runs. The refusal reads `qa PASS is for 1234abcd, head is now 5678ef01 (...; fix-carry refused: 300 lines > 150)`.
- (c) Same, but the fix touches `roles/hooks/x`: no carry (also: a changed `judge_*` def, a binary file).
- (d) Q FAIL @X, R PASS @X; a 20-line fix; Q PASS @Y. R's PASS carries.
- (e) R FAIL @X, Q PASS @X, fix, R FAIL @Y2, second fix (120 lines in total), R PASS @Y3: Q's PASS carries. The limit is cumulative from X (160 lines in total: refused).
- (f) Both roles FAIL @X: there is no other role, both re-run on Y as before.
- (g) Q PASS @X, then a fix that removes `def test_foo`: no carry.

Event: an accepted fix carry records `verdict-carried-fix` per carried role after `gh pr merge` has succeeded, next to `verdict-carried`: JSON `role`, `from`, `to`, `lines`, `commits` (the non-merge commit shas), `failed_role`. A refused fix carry appends `fix-carry refused: <reason>` to the usual refusal text, only when the failed role has a FAIL (otherwise the text is unchanged).

Keys: `fix_carry` (`on`/`off`) and `fix_carry_max_lines` (`150`), both ordinary config keys, not owner cap keys.

Rollback: `factory config fix_carry off`. No deploy is needed; the next judge, mechanical-merger and review tick use the old rule. The review rerun (see [review-autospawn.md](review-autospawn.md)) follows the same switch.

Tests: `tests/test_verdict_carry.py` (`FixCarryTests`), `tests/test_mech_merge.py`, `tests/test_review_autospawn.py` (`FixCarryRerunTests`). The Go tree has no port of the judge gate: `go/internal/prflow` only maps the event names to stages (`verdict-carried-fix` is not counted until T-1591 does).
