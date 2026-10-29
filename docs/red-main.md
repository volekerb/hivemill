# Red-main monitor and merge freeze (G-022)

Flag: `red_main_freeze` (default `0`). Rollback: `factory config red_main_freeze 0` (merges resume at once; stored state is ignored).

With the flag on, the tick (at most every `red_main_interval_s`, default 120 s) reads the newest completed `tests.yml` push run on `main` of each project `gh_repo` (`gh run list`, the T-0614 signal):

- **failure / timed_out / startup_failure**: the repo is frozen. `factory judge --merge` refuses with `red main: run <id> ...` (exit 4, event `judge-blocked`, nothing merged). The director gets one alert per run id, and a coder task "Red main: revert or fix ..." is filed in the project's top active goal, naming the tasks merged since the last green run.
- **success**: the freeze clears (event `red-main-clear`).
- Cancelled, skipped, still-running runs and `gh` errors leave the state unchanged; a failed read is logged as `red-main-unknown`.

State is kept in `config` rows `red_main:<owner/name>` and `red_main_green:<owner/name>`. The refusal is `red_main_refusal(con, slugs)`; the mechanical merger (G-022 #4) calls it before merging.
