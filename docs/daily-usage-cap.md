# Daily usage cap (`max_day_pct`)

Owner order M41393: cap how much of the weekly Claude usage one day may use.

- Config key `max_day_pct`, default empty = off. A number above 0 and up to 100 turns it on, e.g. `factory config max_day_pct 10`.
- Owner-only, like `max_seven_day_pct`: `factory config <key> <value>` needs the director role or the owner, so a worker agent is
  refused, and the key is in `OWNER_CAP_KEYS` so an agent change shows up as a cap breach.
- The baseline is the weekly usage % (the reader of `usage()`, `~/.claude/usage-limits.json`) first read fresh at or after the
  latest `day_start_hour` (local time, default 8). It lives in the `throttle` row `day-usage-baseline`. A new day start replaces it.
  If the weekly window resets mid-day (current below the baseline) the baseline drops to the current value.
- The spawn gate (Claude runtime only; Cursor agents do not use this quota) closes when `current - baseline > max_day_pct`. The reason
  names `max_day_pct`. A stale or unreadable reading never sets a baseline and never closes this gate (the weekly gate keeps its own latch).
- `factory res` prints `daily cap: used X% of max_day_pct Y% since <day start> (gate open|closed)`, or `daily cap: off`.
- With the key set, the Go `res` and `spawn` hand the command to Python (it owns the throttle row).
- The baseline is recorded by the first gate call after the day start (tick, spawn or `factory res`); usage before that call is not counted.
