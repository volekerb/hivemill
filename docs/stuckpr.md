# Stuck pull request tick unit (G-050)

Package `go/internal/stuckpr` implements one short tick pass over open hivemill pull requests. Each task with a PR URL gets a single `gh pr view --json …,statusCheckRollup` read (no `gh pr checks --watch` and no branch edits).

Classification:

- **Still running** — any latest check is not finished; the tick does nothing.
- **Finished while status says in progress** — a check with non-empty `conclusion` and `completedAt` counts as finished.
- **Red, no live coder** — one `factory relaunch` of the task's own earlier coder per head sha (marker `stuckpr:woke:<task>:<head>`); never a second coder. With no coder on record the team lead is mailed instead.
- **Stale** — no live agent on the task, CI not running, and no commit on the head for 30 minutes (task event time only if the commit time is unreadable): one escalation to `cursor-director` with the PR URL, age, CI state, and whether the PR conflicts (yes, no, or unknown; mergeable UNKNOWN is retried until the age reaches 60 minutes). A `stuckpr:esc:<task>:<head>` config row suppresses repeat escalations for the same episode.

The tick unit registers as `stuck_pr` when that name appears in `go/internal/tick/units.go` / `bin/factory_cli.py` `TICK_UNITS` (wired by a follow-up once the single-writer lock on `factory_cli.py` clears).
Only `https://github.com/volekerb/hivemill/pull/<n>` URLs reach gh. A failing PR is reported on stderr and does not stop the scan of the others or the tick.
