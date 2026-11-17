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

## Post-merge: red main

With `red_main_freeze=1` the tick reads the latest completed `tests.yml` push run on main (`docs/red-main.md`). When it is red, the tick files **exactly one** P1 task per red run: `Red main: bisect + revert <repo> run <id>`. Its spec names the merge commits since the last green main so the coder can bisect them and revert the culprit. A repeat tick on the same red run files nothing; a new red run files a new task. The director is messaged once per run and merges freeze until main is green.

Run both together: `factory config judge_ci_gate none` and `factory config red_main_freeze 1`. `none` without the freeze has no safety net.

## Scope and limits

- `none` covers the judge `--merge` gate and the judge spawn hold only. Projects in local-gate mode (`gate_cmd`) keep their local gate run; that is a local run, not a GitHub PR check, and is out of scope here.
- `judge --train` and the reviewer auto-spawn/rerun ticks still require green PR checks. Under `none` a PR whose checks stay pending does not get its reviewer from the tick until a follow-up task changes that; the lead may spawn the reviewer by hand.
- A BEHIND head merges under `none` only when it may merge unrefreshed: `stale_no_refresh=on` (default) or `stale_policy=behind_ok`, and `behind_ok_blocker` allows it (it also refuses when `tests.yml` changed on main).
