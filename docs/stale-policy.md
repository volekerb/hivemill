# stale_policy (G-022)

Config key `stale_policy`: `behind_ok` (default) or `strict`. Set with `factory config stale_policy strict` (rollback).

With `behind_ok`, a PR whose head does not contain `origin/main` ("BEHIND") is not refreshed when **all** hold:

- GitHub's branch protection of `main` reads `required_status_checks.strict == false`. It is read live with
  `gh api repos/<owner>/<repo>/branches/main/protection` and cached 10 minutes (config key `stale_strict:<owner/repo>`).
  `strict == true`, no protection or any read failure behaves as `strict`.
- `.github/workflows/tests.yml` is unchanged on main since the merge-base of the head and main (T-1000). A head that predates a
  CI job change must refresh and run CI: the judge refreshes, the mechanical merger refuses with `STALE-ONLY`. Undecidable = changed.
- GitHub reports `mergeable == MERGEABLE` (no conflict). A conflicting or unknown PR is refreshed or sent back as before.

Effects:

- `factory judge --merge` no longer refuses with `STALE-ONLY`; every other check (verdict head-binding, CI/local gate,
  public-text guard, noreply author, push policy, `--match-head-commit`) is unchanged. The judge verdict reason gets
  `merged behind main by stale_policy=behind_ok (strict=false on GitHub)`.
- The reviewer, qa and judge spawn pre-check skips `refresh_branch` for such a PR.
- `judge_pr_ready` treats BEHIND as ready.

Backstop: push CI on main, the red-main freeze (T-0623, not yet wired: TODO in `cmd_judge`) and the fix-or-revert task.

## Wait for the main push run (T-1029, flag `judge_wait_main_run`, default `0`)

`red_main_freeze` reads completed runs only, so merges landed while the push run for main's head was still running. With
`judge_wait_main_run=1`, `factory judge --merge` (and `--train`) refuses, exit 4, event `judge-blocked`, unless the newest
`tests.yml` push run on main whose `headSha` equals the current `commits/main` sha is completed+success:

- run in progress, queued, or none yet for the head: refused (`main run is in_progress ...`);
- run failed: refused, except the task named as the red-main fix (`factory redmain fix`), which may merge;
- `gh` cannot answer: refused (`main run unknown`).

Throughput: merges become serial at about one per main run, 25-30 min each, unless guard-equiv is cut (T-1009). Rollback:
`factory config judge_wait_main_run 0`.

## Reviewer and qa spawn (T-0488)

Independent of `stale_policy`: the reviewer/qa spawn pre-check never refreshes. It runs `refresh_branch(dry_run=True)`; a clean
BEHIND head spawns on its current head, a conflict is still refused (`CONFLICT-ONLY`). Only the judge spawn refreshes
(unless `behind_ok` applies).
