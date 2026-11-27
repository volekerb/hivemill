# hivemill planner, owner queue and digest

The runner ([option-b-runner.md](option-b-runner.md)) runs only the tasks it is given or finds ready. Before the planner, nobody kept the ready queue full: with `projects` empty, or with backlog specs that fail the spec gate, a night passed with no dispatch. Work held for the owner also waited without the owner being told. The planner replaces the old boss chat agent for this lane.

## Planner session

```
hivemill planner start          # turn it on and start the session (the daemon keeps it alive from now on)
hivemill planner status         # on/off, terminal state, last wake, pending reasons, levels, open questions
hivemill planner wake [REASON]  # wake it now, past the debounce
hivemill planner stop           # turn it off and close the terminal
```

- One interactive Claude Code session in an hc terminal named `planner`, pinned to the local hc core like task sessions (`StartSession`). Model `planner_model` (default `claude-opus-5-5`), effort `planner_effort` (`medium`). Fable is refused. The session runs in `state/hivemill/planner`, with workspace trust pre-registered.
- Prompt: `roles/hivemill/planner.md`. Settings: `roles/hivemill/planner.json.in`. Both are built into the binary like the task roles (`go generate ./internal/hm`).
- Its agents row has role `planner`, and its session ids go to `hm_sessions` with task `planner`. They never count toward a run's budget or the scoreboard.
- The planner owns the active goals. It keeps each project's ready queue at `planner_queue_min` (default 3) lint-passing tasks, sized for one session under `task_budget_usd`, and splits oversized ones. A failed run is retried in place: `hivemill task retry T-x --note TEXT` (after `hivemill task update` when the spec or a proof was wrong) starts a fresh session on the same branch, which updates the same PR, up to `retry_max` (default 2) times; after that it becomes an owner question. A re-file (`task add --refile-of`) is refused while the old task's PR is open, and `hivemill task cancel` closes the task's open PR (the branch stays), so a split cannot leave an orphan PR.
- **What it may run.** The CLI lets the `planner` role run `goal`, `task`, `run status`, `report`, `health` and `ask add|list|withdraw`. It refuses `task update` on a task with an active run, and a status of `done`, `in_progress` or `in_review`. Its PreToolUse hook (`hivemill planner hook`, fail closed) allows in Bash only those hivemill commands, read-only `git` (`-C DIR` allowed, no `-c`, no `--output`) and `gh pr|issue|run list|view`, optionally piped into `head`, `tail`, `wc`, `grep`, `cut`, `sort` or `jq`, which read stdin only (no file operands, no file-reading flags; `git --no-index` is refused). It refuses `;`, `&&`, `||`, redirects and command substitution. One quoted heredoc (`<<'EOF'`) may feed stdin. Edit, Write and NotebookEdit are refused. Read, Grep and Glob are refused under hidden directories of the home directory (a relative path is resolved against the session's cwd first).

## Wakes

The daemon runs `PlannerPass` after every pass:
1. Each held run gets one owner question (ref `held:<task>:<round>`). It is withdrawn once the run is no longer held.
2. With the planner on and its terminal gone, the planner is resumed with `claude --resume <last session id>` (the SessionStart hook keeps the id current), at most `resume_max` times in 24 h. After that: one owner alert `hivemill-planner` and no more tries until `planner start`.
3. Otherwise `DecideWake` decides. It is a pure function, and its state is kept in `hm_config` meta.
   - **Event reasons** come from `events.jsonl` after a byte cursor: `task.failed`, `run.held`, `budget.reached`, `goal.checked`, `ask.answered`.
   - **Level reasons** come from the DB: `queue_low:<project>=<startable>/<min>` for a project with an active goal, `unstartable:<task>` for each todo task of `projects` that fails the spec gate (one work item per task; the pass also logs `task.unstartable` once), `none_startable:todo=N,blocked_by_deps=N,blocked_by_lint=N[,have_a_run=N]` (new only when it appears, not when its counts change) when a run slot is free and todo tasks exist but none can start, `not_dispatched:<project>` for an active goal outside `projects`, and `goal_done:<goal>` when all its tasks are done. `dead_dep:<task>=<dep>(cancelled|failed)` is a todo task of an active goal whose `depends_on` names a task that can never be done: the planner's work item is to re-point or drop the dependency. `hivemill task list --blocked` shows such a dependency as `deps: T-x cancelled` or `deps: T-x failed` (other open dependencies as `T-x (<status>)`); the command covers `projects` only, and a task skipped for a run or the spec gate shows that reason first.
   - It wakes when an event happened, or a level appeared that the planner has not heard about. A level that went away (a refilled queue) is not news.
   - At most one wake per `planner_wake_gap_s` (600). It never types into a working planner. A deferred reason stays pending: the cursor moves only after a delivered wake, so the next wake coalesces everything (`task.failed:T-1,T-2`).
4. The wake is one line: `hivemill-wake v1 at=<utc> asks_open=<n> reasons=<reason> ...`.

## Owner queue

```
hivemill ask add [--task T|--goal G|--project P] TEXT   # planner or owner
hivemill ask list [--all]
hivemill ask answer N TEXT                              # owner only; wakes the planner (ask.answered)
hivemill ask withdraw N [WHY]
```

There is one table, `hm_asks`. A question never blocks other work: the planner keeps filing and the runner keeps dispatching unblocked tasks.

## Alerts (`hivemill health --alert`)

These run whether or not the dead-man check passes. They write owner alert feed rows, never a message to the boss; each row they open (like `hivemill-planner` and `hivemill-digest`) also goes out as an owner alert: `<home>/logs/alerts.log`, then `alert_cmd` or a macOS notification ([option-b-runner.md](option-b-runner.md#health-and-alerts)). `hivemill health` prints them without `--alert`. The exit code reflects the heartbeat and whether `hivemill obs` answers ([option-b-runner.md](option-b-runner.md#health-and-alerts)), never these checks.

- `hivemill-idle`: no task was dispatched for `idle_alert_min` (20 minutes; when `idle_alert_min` is 0, `idle_alert_h` hours is the fallback) while an active goal exists and a run slot is free (drain off). The text names the likely causes: `projects` is empty, the goal's project is not in `projects`, dispatch is held by a cap, there are 0 startable tasks (with how many todo ones wait on deps, fail the spec gate or have a run; `hivemill task list --blocked` lists them), or the planner is off or lost. It is raised at most once per `alert_every_s`, and closed when the condition clears.
- `hivemill-asks`: open owner questions older than `ask_alert_h` (1), in one digest row. A new row replaces the open one only when the list changes.

## Morning digest

`hivemill digest [--date YYYY-MM-DD] [--json] [--send]` covers one local day (default yesterday):
- merged PRs with each task's $;
- the $ of every session started that day (task, judge, planner; failed runs included);
- runs dispatched and failed that day;
- runs held for the owner now;
- open owner questions;
- each active goal's done/total, running and ready.

`--send` writes `state/hivemill/digests/<date>.md` and opens one `hivemill-digest` feed row that points at it.

Schedules at 07:30:
- **Linux:** `systemd/hivemill-digest.timer` with `hivemill-digest.service`. Install with `install -m 644 systemd/hivemill-digest.* ~/.config/systemd/user/ && systemctl --user daemon-reload && systemctl --user enable --now hivemill-digest.timer`.
- **macOS:** `hivemill launchd digest --out ~/Library/LaunchAgents/hivemill.digest.plist && launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/hivemill.digest.plist`.

## Config keys

| key | default | meaning |
|---|---|---|
| `planner_queue_min` | `3` | lint-passing ready tasks wanted per project with an active goal |
| `planner_model` | `claude-opus-5-5` | the planner's model (never Fable) |
| `planner_effort` | `medium` | its `--effort` |
| `planner_wake_gap_s` | `600` | at most one wake per this many seconds |
| `idle_alert_min` | `20` | starvation alert after this many minutes without a dispatch (20 minutes) |
| `idle_alert_h` | `2` | fallback in hours when `idle_alert_min` is 0 |
| `ask_alert_h` | `1` | owner questions older than this go out in the digest alert |

## Rollback

- `hivemill planner stop` turns the planner off. The daemon then only files questions for held runs.
- The tables and meta keys are only read by `hivemill`. Reverting the PR leaves `hm_asks` unused.
