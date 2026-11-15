# Stuck pull request tick unit (G-050)

Package `go/internal/stuckpr` implements one short tick pass over open hivemill pull requests. Each task with a PR URL gets a single `gh pr view --json …,statusCheckRollup` read (no `gh pr checks --watch` and no branch edits).

Classification:

- **Still running** — any latest check is not finished; the tick does nothing.
- **Finished while status says in progress** — a check with non-empty `conclusion` and `completedAt` counts as finished.
- **Red, no live coder** — one wake message to the task's coder; no second wake while a coder is live, starting, or queued.
- **Stale** — no live agent on the task, CI not running, and no qualifying activity for 30 minutes: one escalation to `cursor-director` with the PR URL, age, CI state, and whether the PR conflicts. A `stuckpr:esc:<task>:<head>` config row suppresses repeat escalations for the same episode.

The tick unit registers as `stuck_pr` when that name appears in `go/internal/tick/units.go` / `bin/factory_cli.py` `TICK_UNITS` (wired by a follow-up once the single-writer lock on `factory_cli.py` clears).