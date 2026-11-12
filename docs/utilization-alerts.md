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

`owner_blocker_alerts()` (tick) sends the boss one `escalation` ("OWNER-ONLY BLOCKER (night): ...") for each owner-only blocker raised outside `[day_start_hour, day_end_hour)`. Sources: an escalation to the boss whose subject contains "owner action" (case-insensitive; the tick's own messages are ignored), which stays open until a boss `--kind decision` quotes its M-id, and a spawn gate closed on `/tmp` usage. One alert per blocker (throttle key `alerted:owner-blocker:...`). `factory res` prints `owner-only blockers: N` with each blocker's age. The boss tells the owner at once and again at `day_start_hour` (roles/boss.md).

At night the tick also prompts the boss pane once per such blocker (the only night nudge; the boss pane is otherwise never prompted), when the boss is idle, retrying each tick until delivered; each prompt is logged as event `owner-blocker-nudge`.

## P1 waiting on owner/CI (T-1401, owner decision M33717)

A P1 goal with no worker (coder/pm/qa/reviewer/researcher/judge live, starting or queued) is **starved** unless it is **waiting**. Waiting means it has open tasks, none of them ready, and every open task is `blocked` (`waits=owner`, or `waits=internal` on another task), `in_review` (CI pending or review/judge about to be autospawned), or `todo` with an unfinished dependency. `utilization()` decides from DB state only (no `gh` call).

- Starved goals keep the `P1 goals without workers: ...` part of the utilization line, are stored in `util_samples.p1_starved`, and are the only ones that alert (`starved P1 goal`, boss escalation) or count in the scorecard's `P1 starved` minutes.
- Waiting goals are stored nowhere as starved, never alert, and are printed by `factory res` on a separate line right below the utilization line: `P1 waiting on owner/CI: G-010 (35 min), ...`. The minutes count from the goal's last ended worker (the goal's creation if it never had one). The line is absent when no goal waits.
- A ready task (status `todo`, all dependencies `done`, same rule as `factory task ready`) always makes the goal starved, even next to blocked tasks.
- A goal with no open tasks at all, or with an `in_progress` task and no live worker, stays starved (as before): nobody is working and nothing explains why.
