# Owner-defined recurring jobs (`factory schedule`)

G-038 F-31 adds up to five owner- or boss-defined jobs that mail the director on a weekday/hour. Each job owns one **dedicated Claude-org goal**; the tick raises that goal's `token_limit` for one run so spend is capped through the existing per-goal tally and `goal_token_refusal` gate (see [goal lifecycle](goal-lifecycle.md)).

## Commands

| Command | Who | Purpose |
|---------|-----|---------|
| `factory schedule add --hour H [--days mon,wed,...] --goal G --token-cap N "<job text>"` | owner, boss | Create `S-###`; prints the id |
| `factory schedule list [--all] [--json]` | anyone | Active jobs (or all rows with `--all`) plus a `fixed schedules:` line for digest, retro and scout hours |
| `factory schedule remove S-###` | owner, boss | Soft-remove (`removed_at`); row kept for audit |

`schedule` is implemented only in the Go CLI. `bin/factory` always execs `factory-go` for it; a missing or stale binary exits 2 with a rebuild hint (no Python fallback).

## Token cap

`token_cap` is a **per-run** budget. At fire time the tick sets `goals.token_limit = baseline_tally + token_cap`. Warn at 80% and refuse new coder/pm spawns at 100% use the same machinery as a manually set limit. Tally cadence (`goal_token_tally_s`) means a run can overshoot slightly while agents already live; panes are not stopped mid-run.

## Job text is data

Job text is validated at add (single line, 1–500 characters, no control characters, no `<<<` or `>>>`). It is stored and quoted in mail as data, not as instructions to agents. The director files normal tasks; job text never becomes a spawn prompt.

## Overlap with fixed schedules

Digest (`digest_hour`), retro (`retro_hour`) and scout (`scout_slots`) stay separate config keys. `schedule add` warns on stderr when `--hour` matches one of those hours; `schedule list` prints their values on the `fixed schedules:` line.