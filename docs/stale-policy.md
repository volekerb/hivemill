# stale_policy (G-022)

Config key `stale_policy`: `behind_ok` (default) or `strict`. Set with `factory config stale_policy strict` (rollback).

With `behind_ok`, a PR whose head does not contain `origin/main` ("BEHIND") is not refreshed when **all** hold:

- GitHub's branch protection of `main` reads `required_status_checks.strict == false`. It is read live with
  `gh api repos/<owner>/<repo>/branches/main/protection` and cached 10 minutes (config key `stale_strict:<owner/repo>`).
  `strict == true`, no protection or any read failure behaves as `strict`.
- `.github/workflows/tests.yml` is unchanged on main since the merge-base of the head and main (T-1000). A head that predates a
  CI job change must refresh and run CI: the judge refreshes, the mechanical merger refuses with `STALE-ONLY`. Undecidable = changed.
- No file changed on main since the merge-base is also changed by the PR (T-1025). Merge-tree is clean in that case, but no CI
  ever ran the combined tree (T-0435 #369 merged behind T-0880 #361 on `worker-guard.py`; main went red). The head must refresh and
  run CI: the judge refreshes, the mechanical merger refuses with `STALE-ONLY ... main and the PR both changed <files>`.
  One definition (`behind_ok_blocker`), shared with the `tests.yml` rule above, used by the judge and the mech merger.
  A head with no shared file still merges behind main. A git error or unknown merge-base counts as shared (fail closed).
- GitHub reports `mergeable == MERGEABLE` (no conflict). A conflicting or unknown PR is refreshed or sent back as before.

Effects:

- `factory judge --merge` no longer refuses with `STALE-ONLY`; every other check (verdict head-binding, CI/local gate,
  public-text guard, noreply author, push policy, `--match-head-commit`) is unchanged. The judge verdict reason gets
  `merged behind main by stale_policy=behind_ok (strict=false on GitHub)`.
- The reviewer, qa and judge spawn pre-check skips `refresh_branch` for such a PR.
- `judge_pr_ready` treats BEHIND as ready.

Backstop: push CI on main, the red-main freeze (T-0623, not yet wired: TODO in `cmd_judge`) and the fix-or-revert task.

## Reviewer and qa spawn (T-0488)

Independent of `stale_policy`: the reviewer/qa spawn pre-check never refreshes. It runs `refresh_branch(dry_run=True)`; a clean
BEHIND head spawns on its current head, a conflict is still refused (`CONFLICT-ONLY`). Only the judge spawn refreshes
(unless `behind_ok` applies).

## Who merges or is refused when the overlap rule blocks behind_ok (T-1025)

| path | result |
|---|---|
| `factory judge --merge` (judge) | refreshes the branch, CI reruns on the combined tree, merges on the new head |
| mechanical merger (`mech_try`) | refuses `STALE-ONLY` and never refreshes; the PR goes to the LLM judge, which refreshes |
| `refresh_lane=on` | still treats a clean BEHIND head as "no refresh needed" (`refresh_lane_hold` only knows `stale_behind_ok`), so a judge blocked by the overlap rule is not held by the lane and refreshes outside it. Cost only, not safety. Follow-up. |
| red-main fix task named with `factory redmain fix` | exempt from the freeze (`red_main_refusal`), refreshes like any head: it cannot deadlock behind the red main it repairs |
| dependabot PRs | have no factory task and no `factory judge` path: unaffected |

Cost: only a PR that shares a file with a newer main commit refreshes; it pays one extra CI run, the same as under `strict`.
