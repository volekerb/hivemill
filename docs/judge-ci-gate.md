# judge_ci_gate (G-050, T-1772)

Config key `judge_ci_gate`: `pr` (default) or `none`. Set with `factory config judge_ci_gate none`; `factory config judge_ci_gate pr` is the rollback. Registry entry: `bin/flags.json`.

## What it does

- `pr` (default): nothing changes. The judge waits for the PR checks (`judge_ci_wait_s`), merges only on green checks, and refreshes a BEHIND branch as before.
- `none`: GitHub PR checks are not read, not waited on and not required. `factory judge <T> --merge` merges when all hold:
  - reviewer PASS and QA PASS on the current head (the usual verdict gates);
  - `git merge-tree` of the head against `origin/main` is clean;
  - no file changed by the PR was also changed by a PR merged since the head the verdict covers (the `behind_ok_blocker` overlap rule from `docs/stale-policy.md`).
- Under `none` the judge never refreshes the branch. A head that cannot merge as it is (dirty merge-tree, file overlap) is refused with `judge_ci_gate=none does not refresh the branch: <reason>`; the lead sends it back to the coder. `stale_no_refresh` is still honoured.
- The judge spawn hold (`judge_pr_ready`) does not read the PR checks either.

Why: the merge is protected after the fact by the main push run (below), not by the PR run.

## judge_review_gate=green (G-005, T-1802)

Config key `judge_review_gate`: `passpass` (default, everything above) or `green`. Owner decision M40731/M40734: merge green PRs without waiting for a reviewer/QA PASS. Rollback: `factory config judge_review_gate passpass`. In `green` mode `factory judge --merge`, `--train`, the mechanical merger and the judge autospawn accept a task when there is no reviewer or QA FAIL on the current head (a PASS, a missing verdict or a FAIL on an older head do not block) and the PR checks are green on that exact head. The checks are read even when `judge_ci_gate=none`; pending checks mean "not ready" (the judge waits `judge_ci_wait_s`, then refuses, with no code change). Every other gate stays: PR open and not a draft, mergeable and merge-tree clean, freshness and refresh rules, judge hold, `red_main_freeze`, project `auto_merge`, risk tiers and `main_landing`. The main push run is the safety net: a red main means bisect and revert. The Go port (P1k) must port this flag; the flag is off by default.

## Post-merge: red main

With `red_main_freeze=1` the tick reads the latest completed `tests.yml` push run on main (`docs/red-main.md`). When it is red, the tick files **exactly one** P1 task per red run: `Red main: bisect + revert <repo> run <id>`. Its spec names the merge commits since the last green main so the coder can bisect them and revert the culprit. A repeat tick on the same red run files nothing; a new red run files a new task. The director is messaged once per run and merges freeze until main is green.

Run both together: `factory config judge_ci_gate none` and `factory config red_main_freeze 1`. `none` without the freeze has no safety net.

## Scope and limits

- `none` covers the judge `--merge` gate and the judge spawn hold only. Projects in local-gate mode (`gate_cmd`) keep their local gate run; that is a local run, not a GitHub PR check, and is out of scope here.
- `judge --train` and the reviewer auto-spawn/rerun ticks still require green PR checks. Under `none` a PR whose checks stay pending does not get its reviewer from the tick until a follow-up task changes that; the lead may spawn the reviewer by hand.
- A BEHIND head merges under `none` only when it may merge unrefreshed: `stale_no_refresh=on` (default) or `stale_policy=behind_ok`, and `behind_ok_blocker` allows it (it also refuses when `tests.yml` changed on main).

## Go runner

`hivemill` reads the same factory key, `judge_ci_gate` (`pr`, the default, or `none`), and a per-project override, `projects.ci_gate` (`''` uses the factory key). `hivemill` adds the column when it opens a factory DB that has none; set it with SQL: `UPDATE projects SET ci_gate='pr' WHERE id='<project>'`. Only `none` turns the PR checks off.

Under `none` the project's `gate_cmd` is the CI:

- Dispatch is refused for a project with an empty `gate_cmd`: nothing would test the change.
- The runner reads no PR check, waits on none and does not apply `required_checks`.
- The gate runs `gate_cmd` for every risk tier, docs-only included. It runs in a fresh detached worktree of that commit, removed afterwards, as the old chain did, so uncommitted or untracked files in the task clone do not count. A pass counts for that head only.
- A new PR head (a later push, a refresh) runs `gate_cmd` again, before the judge and before the merge, inside the runner pass (other runs wait meanwhile; the heartbeat and watchdog stay alive). A failure goes back to the task session like red CI and counts against `ci_rounds_max`. `hivemill merge --dry-run` never runs it.
- The runner posts a result once as a PR comment: exit code, head SHA and log tail. A result of the session's own stop check that the runner never takes up (a red one the session fixes first) is not posted. The tail is left out when the public-text guard does not pass it. A failed post is tried again only while that result is the run's latest.
- Merge needs `gate_cmd` green on the current head, the judge's MERGE, a mergeable PR and the other checks as under `pr` (identity, public text, tier, frozen repos, red main). The wait on main's own push run stays: it is not a PR check. A repo with no `tests.yml` workflow is not held by it. Never `--admin`.
- The runner still refreshes a BEHIND branch, then runs `gate_cmd` on the new head. This differs from the old chain.
- `judge_review_gate=green` does not force `pr` here: that mode merged without a review, and the runner always needs the judge's MERGE.
