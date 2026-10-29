# Status lifecycle: goals and tasks

Status must be mechanical: it is correct because the CLI sets it, not because an agent remembered to.
This file lists which transitions the factory makes by itself and which stay manual.

## Goals

`proposed -> planning -> active -> done`, with `blocked` and `cancelled` as side exits.

| Transition | How |
|---|---|
| proposed -> planning | automatic: `factory plan` records the first plan |
| proposed / planning -> active | **automatic** (event `goal-auto-active`, detail names the trigger): `factory team new` staffs the goal, or any of its tasks moves to `in_progress` (`factory task update --status in_progress`, a worker spawn that starts a todo task, or a judge rejection that sends a task back) |
| active -> done / closed | **manual and gated**: goal signoff and close stay with the owner/boss through the existing gates |
| any -> blocked / cancelled | manual |

The auto-activation only ever touches a goal in `proposed` or `planning`. A goal in `done`, `closed`, `cancelled`, `blocked` or any other status is never changed by it.

## Tasks

`todo -> in_progress -> in_review -> done`, with `blocked` and `cancelled` as side exits.

| Transition | How |
|---|---|
| todo -> in_progress | **automatic** (event `task-auto-start`) when `factory spawn <role> --task T` actually starts the worker. The tick does the same when it later starts a queued spawn. A QUEUED spawn leaves the task in `todo`. One shared helper (`task_auto_start`) serves both paths. |
| in_progress -> in_review | coder: `factory task update --status in_review --pr` |
| in_review -> done | the judge, via `factory judge --merge` |
| in_review -> in_progress | the judge, via `factory judge --reject` |
| blocked / cancelled | manual |

A spawn never changes a task that is in `in_review`, `blocked`, `done` or `cancelled`: a reviewer, QA or judge spawned on an `in_review` task leaves it in `in_review`.

A coder's `--status in_review` reads the PR body through `gh` (the `--pr` url or the task's `pr_url`) and is refused unless a line equals `## Acceptance walk`
(the pre-pr-check skill writes it). If `gh` cannot read the body it is refused too (fail closed): retry. Other roles and statuses are unchanged.

Both automatic transitions are idempotent: once the status has moved, a second trigger changes nothing and records no second event.

No merge, verdict or signoff gate is affected by this.

## Drift alerts

Status is set by the CLI, but old data and edge cases can still drift. The tick looks for it (`tick_drift`, called from `factory tick`) at most once an hour (throttle key `drift`).

| Rule | Flagged when |
|---|---|
| D1 | a goal in `proposed` / `planning` has a done task or an active team (F1 prevents new cases; this catches old ones) |
| D2 | a task is `in_review` but `gh pr view <pr> --json state` says `MERGED` or `CLOSED` |
| D3 | a task in an `active` goal is `todo` / `in_progress`, has no live, starting or queued agent, and shows no activity for over 2 h (task `updated`, a task event, or an agent start/end for it). A `todo` task that still waits on an unfinished dependency is not flagged, but one whose dependency is `cancelled` is (`D3 T-… todo but its dependency T-… is cancelled`): it would wait forever. |
| D4 | a task in an `active` goal has been `blocked` for over 24 h (since its last update) |

Delivery: one `alert` message per goal per run, subject `drift G-xxx: N items`, body one line per item (for example `D2 T-0123 in_review but PR #88 MERGED`). It goes to `lead:<team>` of the goal's active team whose lead is live, else to the director. Item lines carry no ages, so the same set gives the same key (goal + sorted items): an identical set is not re-sent within 24 h, a changed set is sent at once. Each message records a `drift` event on the goal.

If `gh` fails for a task, D2 is skipped for it and the body ends with a `D2 unchecked T-… (reason)` line. All `gh` calls of one run share a 60 s budget (`FACTORY_DRIFT_GH_BUDGET_S`); tasks reached after it is used up are `D2 unchecked` too. The drift check runs after the tick has reaped finished agents, so a just-reaped agent is not seen as missing. That note alone never causes a message. Any other error in the check records a `drift-failed` event; the tick always goes on.

`factory digest` prints `Drift: none` or the item list (joined by `; `) for every goal it shows, computed live from the DB. D2 needs a PR state and the digest makes no gh call: it reuses the states the last drift tick stored (config key `drift_pr_states`), so before the first tick D2 is not shown.

The next line, `Freshness:`, is the agent freshness check (see [freshness.md](freshness.md)); it does not change the `Drift:` line.
