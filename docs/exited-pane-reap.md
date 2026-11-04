# Exited-pane reaper (T-0972)

A worker whose agents row is `exited` can still have a live pane, which holds a host claude process slot and blocks
spawning. `factory stop` refuses such a row ("not live: nothing to stop"), so the tick closes the pane itself.

## How a row reaches `exited` with its pane alive

The tick's reap loop (`status='done'` rows) and `release_stale_pane` call the pane close with `check=False`, ignore a
failed or timed-out close, and then set `status='exited'` anyway (`release_stale_pane` also clears `pane_id`). The row
leaves the live set while the pane stays.

## Behaviour

Tick hook `reap_exited_panes`, at most every 10 minutes (`throttle` key `exitedpanereap`), config
`exited_pane_reap_mode`:

- `dry-run` (default; an unknown value acts as dry-run): one `exited-pane-would-close` event per (agent, pane), nothing closed.
- `on`: closes the pane, confirms the host no longer lists it, logs `exited-pane-closed`. A failed close logs one
  `exited-pane-close-failed` per (agent, pane) and is retried on the next run without further events.
- `off`: skips.

A pane is closed only when the host lists it under the exact name of an agents row that, re-read in the same
tick, is `exited` for more than 10 minutes, has role coder/qa/reviewer/pm/judge/cleaner, and whose HOST pane id no
live/starting/queued/stopped row holds. The row's own `pane_id` is not compared and never closed: it can differ from the
host's (seen in production, T-1005: host `w7:pPQ`, row `w7:pPR`), so the pane id comes from the host listing of the
same tick. A host pane whose `agent_session` names a session other than the row's `session_id` is skipped (it runs
a newer agent under this name; `relaunch_agent` makes the same check). When the host reports no session (most
workers), the pane is kept while any live/starting/lost row (lost at any age, no time window) is not listed by the host under its
own name: that row may own the pane under a stale label. Trade-off: while such a row exists the reaper closes nothing, so stuck panes wait.
Why ids change after spawn was not found. Panes with no agents row (the owner's), boss/director/lead rows and
non-exited rows are never touched. Tests: `tests/test_exited_pane_reap.py`. Enable with `factory config exited_pane_reap_mode on`
after reading the would-close events.
