# Utilization

`factory res`, the tick sample and the web home show one mechanical count of the live agents. It is display and
recording only: `gate()` and `spawn()` do not read it. The code is `utilization(con)` in `bin/factory` (G-018).

## Definitions

- **D1 worker**: an agent with status `live` or `starting` and role `pm`, `coder`, `qa` or `reviewer`
  (`UTIL_WORKER_ROLES`). Leads, judges and HQ (`boss`, `director`) are counted on their own; every other role
  (cleaner, staff-engineer, scout, evaluator) counts only in the live total N.
- **D2 line**: the exact text used by `factory res`, the web home and the reports:
  `live N: workers W / leads L / judges J / HQ H / queued Q; P1 goals without workers: G-a, G-b` (`none` when empty).
  N is all live and starting agents, Q is all queued agents.
- **D3 target**: `min(10, ceil(0.6 * max_live_agents))`, so cap 22 gives 10, cap 15 gives 9, cap 6 gives 4. A cap
  that is not an integer falls back to the default cap. Under target means workers < target. HQ-heavy means
  `(leads + judges) * 3 > N`.
- **D4 starved goal**: status `planning` or `active`, priority 1 (P1) or 2 (P2), with no agent of role `pm`, `coder`,
  `qa`, `reviewer` or `judge` on that goal in status `live`, `starting` or `queued`.
- **D5 demand**: a queued worker-role agent exists, or a ready task exists (status `todo`, every `depends_on` done, worker
  role, no live/starting/queued agent on it). "UNDER TARGET" is shown only with demand.

`factory res` prints two lines directly after the `agents live ...` line: the D2 line, then
`utilization: workers W/target target · leads+judges X/N (max 1/3)` plus ` · UNDER TARGET` (under target and demand)
and ` · HQ-HEAVY`.

## util_samples

The tick calls `util_sample(con)` after the relaunch step. It writes one row of `utilization(con)` (columns `ts`, `live`,
`workers`, `leads`, `judges`, `hq`, `other`, `queued`, `queued_workers`, `cap`, `target`, `demand`, `p1_starved` and
`p2_starved` as JSON lists) unless the last row is under 50 s old. `ts` comes from `FACTORY_NOW` when set. Once a day it deletes rows older
than 35 days. A failure never breaks the tick: it logs `util / util-sample-failed` at most once per hour.
`factory export` leaves `util_samples` out of `state/factory.sql`, as it does `events`.

The web home shows the newest row in the D2 format, plus ` (UNDER TARGET)` when workers < target and demand. With no
row, or a newest row older than 5 minutes, it shows the friendly line `Utilization appears after the next tick sample`.

Gap: when herdr is unreachable the tick returns early and writes no sample, so the web home shows the friendly line "Utilization appears after the next
tick sample" until herdr is back.

## Owner caps

`OWNER_CAP_KEYS`: `max_live_agents`, `max_total_claude`, `min_mem_available_mb`, `max_five_hour_pct`,
`max_seven_day_pct`, `gate_max_load_ratio`, `tmp_max_pct`, `max_live_leads`, `reserved_worker_slots`, `lead_parking`,
`max_live_judges` (a key that is not in `DEFAULT_CONFIG` never matches). `cap_breaches(con, start_iso, end_iso)` returns the
`config set` events on these keys with `start <= ts < end` made by a factory agent (an `agents` row exists). Sets by the
owner are not breaches.

## Director row

`factory scorecard` prints one extra line after the team table (a blank line, then the line), and the daily digest has a `## Director` section before the first goal section. Both show how the director did in the window, from `util_samples`, `cap_breaches` and the boss's score messages.

Fields: average daytime worker count (`avg workers`) and share of live agents (`share`), `target` of the newest sample, median workers over samples with a queue, `P1 starved` minutes (and the longest single episode of one P1 goal), owner-cap `cap breaches` (0 expected) and the boss's last two `director score YYYY-MM-DD: N/5` scores. Two scores of 2 or lower add ` · TWO LOW SCORES`. Without daytime samples the line says `no utilization samples` and keeps cap breaches and scores.

Daytime keys: `day_start_hour` (default 8) and `day_end_hour` (default 20), local time, every day of the week. A non-integer, a value outside 0-24 or start >= end falls back to 8 and 20.

Weights: a sample weighs the gap to the next daytime sample, at most 5 minutes; the last sample weighs 1 minute. A gap over 5 minutes ends a starvation episode.

`factory scorecard --days N` (integer >= 1, default 1) sets the window to the last N x 24 hours; other values exit 2.

Use by the boss: the daily director score (R7) is read from this line; the SC6 2-day check uses `factory scorecard --days 2` (filtering working days itself).
