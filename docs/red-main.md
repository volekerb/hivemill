# Red-main monitor and merge freeze (G-022)

Flag: `red_main_freeze` (default `0`). Rollback: `factory config red_main_freeze 0` (merges resume at once; stored state is ignored).

With the flag on, the tick (at most every `red_main_interval_s`, default 120 s) reads the newest completed `tests.yml` push run on `main` of each project `gh_repo` (`gh run list`, the T-0614 signal):

- **failure / timed_out / startup_failure**: the repo is frozen. `factory judge --merge` refuses with `red main: run <id> ...` (exit 4, event `judge-blocked`, nothing merged). The director gets one alert per run id, and a coder task "Red main: revert or fix ..." is filed in the project's top active goal, naming the tasks merged since the last green run.
- **success**: the freeze clears (event `red-main-clear`).
- Cancelled, skipped, still-running runs and `gh` errors leave the state unchanged; a failed read is logged as `red-main-unknown`.

State is kept in `config` rows `red_main:<owner/name>` and `red_main_green:<owner/name>`. The refusal is `red_main_refusal(con, slugs)`; the mechanical merger (G-022 #4) calls it before merging.

The revert/fix task the check files may itself merge during the freeze (matched by its task id); every other task is refused. "Merged since the last green main" counts from the start of the last green push run.

**Which goal gets the revert task (T-0669).** The active goal of the project whose `gh_repo` is the repo; if that project has none, the active goal with a task whose PR is in the repo (a goal can work in a repo its project row does not name). With no such goal the director is told to file the task by hand.

**Naming the fix task by hand.** `factory redmain fix <OWNER/NAME> <T-id>` (director only) sets the stored fix task of a frozen repo (event `red-main-fix-task`), so that task may merge during the freeze. Use it when no revert task was filed, otherwise the fix PR cannot merge.
