# Unblocking a worker

## The problem (L-021)
A worker once sat 15+ minutes on a Claude Code permission dialog (a read outside its working directories). Options 1 and 2 of that dialog ("don't ask again", "add directory") would have changed the owner's user settings for every session. A lead had no tool to answer it: leads have no herdr access, and `factory stop` ends the worker.

## The rule
`roles/_common.md` tells every role: read and write only in your working directory and session scratchpad; never pick a permission-dialog option that changes user settings; pick No.

## Alert routing
The tick marks an agent blocked when herdr reports it blocked, and sends one `<name> blocked >10 min` alert (kind alert, sender tick, with the pane tail) once `blocked_alert_s` (default 600) has passed:
- a team worker: to its team lead. The body has a hint line first: the L-021 warning, `factory unblock <name> --reason "..."` or `factory stop <name>`, and the `blocked-worker` skill.
- a worker with no team (staff-engineer), a worker whose team has no live lead, and every lead: to the director.
- the director: to the boss.

## `factory unblock <agent> --reason TEXT [--dry-run]`
Deny a blocked worker's permission dialog once; the worker keeps running.
- Callers: the director (any worker) and a lead (its own team's workers). A worker caller is refused.
- Target: a live worker whose blocked state the tick has recorded (`agents.blocked_since`). Otherwise exit 2, "use factory stop for a busy worker".
- Refusals: boss, director and lead targets (exit 3, L-019: never type into HQ panes); another team's worker for a lead (exit 3); an unknown agent (exit 2).
- Steps: a `decision` message `UNBLOCK` to the worker (the reason, and that the request was denied once), an `unblock` event, one Esc keystroke to the pane, a wait for idle/done/blocked (up to 30 s), then a nudge to read the inbox. No digits, no Enter and no text are ever sent into the dialog.
- `--dry-run` prints the steps and changes nothing.

## Difference from `factory stop`
`factory stop` ([stopping-a-worker.md](stopping-a-worker.md)) ends a busy worker: it sets `stop_requested_at` and the tick closes the pane after `stop_timeout_s`. `factory unblock` never closes the pane, never sets the stop flag and never touches the task. Both share the same Esc, wait, nudge sequence. If a worker is blocked again on the same thing, use `factory stop` and respawn.
