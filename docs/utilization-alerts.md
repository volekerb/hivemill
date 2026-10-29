# Utilization alerts

The tick (`util_alerts`, called right after `util_sample`) turns the utilization samples described in [utilization.md](utilization.md) into messages. Every alert is a message of kind `alert` from `tick`. The hook reads only `util_samples`, `events`, `throttle` and `config`.

## Conditions and recipients

| Condition | After | Recipient | Subject starts with |
|---|---|---|---|
| Workers under target and there is demand (a queued worker or a ready task) | 15 min | director | `utilization: workers W/target under target for` |
| Same condition | 30 min | boss | `escalation: workers under target for` |
| An active P1 goal has no worker live or queued | 15 min | director | `starved P1 goal G-xxx:` |
| Same goal | 30 min | boss | `escalation: P1 goal G-xxx starved` |
| An active P2 goal has no worker | 60 min | director only | `starved P2 goal G-xxx:` |
| An owner-cap key was set by a factory agent | at once | boss | `owner-cap breach: <actor> set <key>=<value>` |

The workers alerts carry the utilization line and "Rule R1: act this cycle: ...". The boss gets an escalation only after the director has had the 15 minutes first. Without demand, a low worker count does not alert (the P1 alert covers a goal that has nothing queued).

## Episodes

An episode is the run of consecutive samples where the condition holds, counted back from the newest sample. A gap of more than 5 min between two samples ends it. Its duration is the newest sample time minus the episode start.

Stale-data rule: if the newest sample is older than 5 min (the tick was down), no alert is sent.

## Dedupe

At most one message per condition, goal and level per episode. The episode start is stored in the throttle table under keys such as `util:workers:director` and `util:p1:G-xxx:boss`. The same start means "already sent". When the condition clears, the next episode has a new start and alerts again.

## Cap breach

Breaches are the `config set` events from `cap_breaches()` (see utilization.md). The config key `util_cap_breach_last_id` holds the last event id handled. On the first run it is set to the current highest event id, so history never alerts. Sets made by the owner are not breaches. The alert body ends with "Rule R4: owner caps are fixed; report to owner."

## Failures

The hook never breaks the tick. A failure logs the event `util-alert-failed` at most once per hour.

## Owner-only blockers at night (T-0609)

`owner_blocker_alerts()` (tick) sends the boss one `escalation` ("OWNER-ONLY BLOCKER (night): ...") for each owner-only blocker raised outside `[day_start_hour, day_end_hour)`. Sources: an unread escalation to the boss whose subject contains "owner action" (case-insensitive; the tick's own messages are ignored), and a spawn gate closed on `/tmp` usage. One alert per blocker (throttle key `alerted:owner-blocker:...`). `factory res` prints `owner-only blockers: N` with each blocker's age. The boss tells the owner at once and again at `day_start_hour` (roles/boss.md).
