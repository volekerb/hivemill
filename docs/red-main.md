# Red-main monitor and merge freeze (G-022)

Flag: `red_main_freeze` (default `0`). Rollback: `factory config red_main_freeze 0` (merges resume at once; stored state is ignored).

With the flag on, the tick (at most every `red_main_interval_s`, default 120 s, while main is not red; a red repo is re-checked every tick, so a green run clears the freeze on the first tick that sees it) reads the newest completed `tests.yml` push run on `main` of each project `gh_repo` (`gh run list`, the T-0614 signal):

- **failure / timed_out / startup_failure**: the repo is frozen. `factory judge --merge` refuses with `red main: run <id> ...` (exit 4, event `judge-blocked`, nothing merged). The director gets one alert per run id, and a coder task "Red main: revert or fix ..." is filed in the project's top active goal, naming the tasks merged since the last green run.
- **success**: the freeze clears (event `red-main-clear`).
- Cancelled, skipped, still-running runs and `gh` errors leave the state unchanged; a failed read is logged as `red-main-unknown`.

**Not every main commit gets its own run (T-1592).** All push runs of `tests.yml` (and `install.yml`) on `main` share one concurrency group (`tests-main`, `install-main`) with `cancel-in-progress` false for pushes. A running main run is never cancelled. When a newer merge lands, an older main run that is still pending is cancelled; the newest pending run still runs once the running one ends. So one main run may cover several merges: a red run names the first commit to look at, but any merge since the last green run can be the cause, which is why the revert task lists all of them. A cancelled run is no information, never red and never green: the monitor skips it, the mechanical merger (`mech_main_run_refusal`) and `judge_wait_main_run` hold on a cancelled run for main's head, and `release_check` `ci-green` does not count it (pick a sha with a completed run, or rerun the cancelled run). Only the newest 5 push runs are read, so after a burst of more than 4 superseded runs the state stays unchanged until a newer run completes.

State is kept in `config` rows `red_main:<owner/name>` and `red_main_green:<owner/name>`. The refusal is `red_main_refusal(con, slugs)`; the mechanical merger (G-022 #4) calls it before merging.

The revert/fix task the check files may itself merge during the freeze (matched by its task id); every other task is refused. "Merged since the last green main" counts from the start of the last green push run.

**Which goal gets the revert task (T-0669).** The active goal of the project whose `gh_repo` is the repo; if that project has none, the active goal with a task whose PR is in the repo (a goal can work in a repo its project row does not name). With no such goal the director is told to file the task by hand.

**Naming the fix task by hand.** `factory redmain fix <OWNER/NAME> <T-id>` (director only) sets the stored fix task of a frozen repo (event `red-main-fix-task`), so that task may merge during the freeze. Use it when no revert task was filed, otherwise the fix PR cannot merge.

## A red `post_merge_cmd`

A project's `post_merge_cmd` ([option-b-runner.md](option-b-runner.md)) writes the same `red_main:<owner/name>` row, with `source: post_merge`. `hivemill` holds that repo's merges on it even with `red_main_freeze` off, and a green command clears it. A green command leaves the monitor's own record alone. The monitor neither replaces nor clears a `post_merge` row: its own record lives under that row's `prev` (kept there by a red command, or written there by a push run that turns red meanwhile, which files its task as usual). A green command puts it back, unless a push run went green in between. While the `post_merge` row holds, the fix-forward task, the auto-revert task, the monitor's fix task and a task named with `factory redmain fix` may merge. A second red command keeps a fix-forward task that is still open. The runner reactivates a `done` goal of the fix-forward task; a cancelled or blocked goal stays so and opens a `post-merge-stuck` alert row. `max_waiting` does not hold the start. A red revert is not reverted again. With a missing project checkout or a bad setting the command cannot run: it stays pending, holds the project's merges and opens a `post-merge-stuck` alert row (a program `bash` cannot find exits 127 and is red). Stopping the daemon (or interrupting `hivemill run`) kills a running command; it runs again. The row is per repository, so give one repository only one project with a `post_merge_cmd`.

## One bisect + revert task per red run (T-1772)

The task filed for a red run is titled `Red main: bisect + revert <repo> run <id>`, has priority 1 and names the merge commits since the last green main. With `judge_ci_gate=none` this is the only CI safety net; see `docs/judge-ci-gate.md`.
