# Lead parking (G-014)

A lead whose next step is queued has nothing to do until a message arrives. It parks: its pane is closed, a mail wakes
a fresh lead later. This page is the operator's view: commands, wake triggers, caps, rollback, migration. The wake
mechanics are in [lead-wake.md](lead-wake.md), the spawn gate in [spawn-queue.md](spawn-queue.md).

## Commands

| command | who | what it does |
|---|---|---|
| `factory park "<resume note>"` | lead | Marks the team parked and records the note (1..2000 characters, `-` reads stdin). Refused with unread mail, with `lead_parking=0`, or when the lead has no active team. The tick then closes the pane. |
| `factory team wake <team-id>` | director | Wakes a parked team by hand (trigger `manual`). Exit 3 if the team is not parked, exit 2 if its lead is already waking. |
| `factory team reopen <team-id>` | director | Turns a closed team of an unfinished goal into an active, parked team; the next unread mail wakes its lead. |

## Waking

A parked team wakes on any unread message to its lead: a worker's `done`, a report, a blocked-worker or drift alert, a
spawn bounce, a CI green/red mail for its PR (T-0400), a message from the director or boss. The tick (step 1d) checks every
parked team each minute, so the wake is level-triggered: it is retried every tick until the lead is live, starting or
queued. One wake per team per 120 s (throttle). The woken lead is a fresh session; its kickoff carries the resume note,
the re-orient one-pager and the unread count, and the mail stays unread for the lead to read.

What never wakes a lead: role-change notices (they go to live agents only), and `factory up` / `factory relaunch`, which
never resurrect a parked lead.

A park older than 12 h with no unread mail and no live or queued agent of the team sends one alert to the director, who
either wakes the team (`factory team wake <team>`) or closes it.

## Caps

These apply only to lead spawns and only with `lead_parking=1`. A refused lead stays `queued` and starts later.

| config | default | meaning |
|---|---|---|
| `max_live_leads` | 4 | Live + starting leads. Beyond it a wake queues. |
| `reserved_worker_slots` | 10 | Agent slots kept free for workers. HQ panes (boss, director, leads) may use at most `max(max_live_agents - reserved_worker_slots, 3)`. |

The HQ cap is `max(max_live_agents - reserved_worker_slots, 3)`, so the default `max_live_agents=6` with 10 reserved
still admits 3 HQ panes. Queued leads start in queue order: role class first, then the goal's aged priority
(`factory goal edit <G> --priority N`). `factory res` shows `workers live W · leads live L/cap (parked P, wake-queued Q)`.

## Rollback

`factory config lead_parking 0`:

- `factory park` is refused, so leads stay persistent.
- The lead cap and the worker reserve are off.
- The tick wakes every parked team, one team per tick (trigger `flag-off`), until none is parked.

The shipped default is `0`; `factory config lead_parking 1` turns parking on. The scorecard shows the lead state per team in the `LEAD` column
(`live`, `parked`, `queued`, `-`).

## Migration of g-007-a, g-012-a, g-017-a

Teams that were closed while parking was built come back with `factory team reopen`. The director runs, one team at a
time and waiting for each lead to settle:

```
factory team reopen g-007-a
factory team reopen g-012-a
factory team reopen g-017-a
```

Each team becomes active and parked with the resume note "reopened from closed ...". Its lead starts on the first unread
mail (or `factory team wake <team>`). A team whose goal is `done` or `cancelled` is refused (exit 3).
