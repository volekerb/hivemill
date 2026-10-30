# Scout scheduling (tick)

The tick starts the skill scout twice a day and keeps each run inside a time and token budget. The code is `scout/schedule.py`; `bin/factory` has the config keys, `scout_tick(con)` (the wrapper) and one call to it in `cmd_tick`. No LLM tokens and no network calls are used by the tick itself.

## Config

| key | default | meaning |
|---|---|---|
| `scout_enabled` | `1` | `0` = no new runs (a running one is still bounded by its budgets) |
| `scout_slots` | `09,18` | local hours at which a run is due |
| `scout_max_minutes` | `20` | wall budget of one run |
| `scout_max_tokens` | `300000` | input + output + cache creation tokens of the scout session; cache reads are not counted |
| `scout_triage_per_run` | `8` | candidates `factory scout pending` lists per run when `--limit` is not given; integer 1..50 |
| `scout_queue_max_h` | `6` | a scout queued (gate closed) longer than this is cancelled and its run skipped |

Change them with `factory config <key> <value>`.

Catch-up after a backlog builds up: raise the per-run triage cap for one run, then put it back (an explicit `--limit N` on `factory scout pending` always wins over the key):

```
factory config scout_triage_per_run 36
# ... let the next scout slot run ...
factory config scout_triage_per_run 8
```

## One run per slot

- The current slot is the latest slot hour at or before now, as `YYYY-MM-DDTHH` in local time. Before the day's first slot it is yesterday's last slot. Only that slot is ever run: a box that was down from 08:00 to 20:00 runs slot 18 once and never slot 09.
- `scout_runs.slot` is UNIQUE and the decision is one `BEGIN IMMEDIATE`: a second tick, or a second connection racing the first, finds the row and does nothing. A slot is never retried, not even after a failed spawn.
- If an agent named `scout` is still live, starting or queued, the slot is recorded as `skipped` with the reason `previous run active`.
- Otherwise the run is `claimed`, then `spawn(..., "scout", name="scout")` runs through the normal gate: `running` when it started, `queued` when the gate is full (the usual tick dequeue starts it later). A spawn that raises ends the run as `failed` with the error.
- The step runs on the normal tick path only, after the herdr check, so a tick with herdr unreachable starts nothing.

## Status and budgets (every tick)

The newest open run follows the `scout` agents row: queued/live → `queued`/`running`; done or exited → `done`; lost → `failed`; stopped → `stopped`; cancelled → `skipped`. Older open runs are failed as superseded. The run's `tokens` are filled when it ends, and so are `new_candidates` (candidates first seen) and `triaged` (triage rows created) inside the run's window, which opens at the earlier of the run's `started` and the agent's `started` (a superseded run with no `started` of its own ignores the agent's, since `scout_runs` has no created column and the row may belong to an earlier run) and closes at `ended` (julianday comparison, so UTC offsets do not matter). A skipped run, and a failed run that was never spawned (spawn failed, claim never spawned, agent row missing), keep both NULL. The run report to the director ends with the two counts.

A live scout over `scout_max_minutes` (now − `agents.started`) or over `scout_max_tokens` (parsed from the session's JSONL under `~/.claude/projects` by `web/tokens.py`) is stopped with `force_stop(con, "scout", "budget")`; the run ends `stopped` with `stop_reason` `wall` or `tokens`. If the pane cannot be confirmed closed the agent stays live and the tick's normal stop fallback retries.

## Failures

`scout_tick` never breaks the tick. Any exception becomes one `scout-failed` event per day (for example a malformed `scout_slots`).

`factory scout runs` lists the runs with slot, status, agent, counts, tokens, start and end, and the stop reason.
