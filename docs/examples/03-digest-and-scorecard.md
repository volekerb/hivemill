# Example 3: the daily digest and the team scorecard

`factory digest` is the owner's morning read: one section per goal, what moved, what is stuck, how each team is doing.
`factory scorecard` is the same team table on its own. Both outputs below are real, from the demo home. The digest
window is the 24 hours before "now", so pin the clock to get the same text again:

```
python3 ci/demo_seed.py --home <tmp>/demo --now 2026-10-20T12:00:00
HOME=<tmp>/demo/home FACTORY_HOME=<tmp>/demo FACTORY_NOW=2026-10-20T12:00:00+00:00 TZ=UTC bin/factory digest
HOME=<tmp>/demo/home FACTORY_HOME=<tmp>/demo bin/factory scorecard
```

Seed with `--now 2026-10-20T12:00:00`; `FACTORY_NOW` is the digest's test clock (without it, the window ends at the
real current time and the demo's October timestamps fall outside it). Below, commands are shown as `factory …`.

The director mails the same text to the boss each morning with `factory digest --send`; the boss summarises it for the
owner. `--goal G-003` limits it to one goal, `--date YYYY-MM-DD` ends the window at the end of that day and `--hours N`
changes the window length.

## `factory digest`

```
# Factory digest 2026-10-20

Leads: 5 live, 1 parked, 0 queued; workers live 10

## G-002 — Build the checkout flow (active)

### Done
_nothing_

### In progress
- T-0010 Add payment step [SC3] (in_review, coder-t-0010, attempts 2)
- T-0011 Verify checkout end to end (in_progress, qa-t-0011, attempts 1)
- T-0012 Add order confirmation email [SC4] (in_progress, coder-t-0012, attempts 1)

### Blocked
_nothing_

### Up next
1 ready todo task(s)

Drift: D3 T-0009 todo with no live or queued agent and no activity for over 2 h

### Team scores
| team | tasks | done | merged | rejections | retries | score |
|---|---|---|---|---|---|---|
| g-002-a | 9 | 3 | 2 | 2 | 2 | 4.0 |

## G-003 — Add account settings (active)

### Done
- T-0020 Add avatar upload — https://github.com/example-org/acme-web/pull/110 @dfb0c7d6

### In progress
- T-0019 Add notification preferences [SC3] (in_progress, coder-t-0019, attempts 1)

### Blocked
_nothing_

### Up next
0 ready todo task(s)

Drift: none

### Team scores
| team | tasks | done | merged | rejections | retries | score |
|---|---|---|---|---|---|---|
| g-003-a | 6 | 4 | 3 | 1 | 1 | 4.0 |

## G-004 — Prototype the mobile app shell (blocked)

### Done
_nothing_

### In progress
_nothing_

### Blocked
- T-0022 Add push notification stub [SC2] (blocked)

### Up next
2 ready todo task(s)

Drift: none

### Team scores
| team | tasks | done | merged | rejections | retries | score |
|---|---|---|---|---|---|---|
| g-004-a | 4 | 1 | 0 | 0 | 0 |  |
```

The demo has twelve goals. The real output continues with one block of the same shape for every other goal that is not
closed (`G-006`, `G-007`, `G-009`, and the not-yet-staffed `G-010`, `G-011` and `G-012`) and ends with `## Skill scout`
(`No new finds.`). The block above stops after `G-004` to keep the page short.

### Reading it, section by section

| heading | what it shows | what to do with it |
|---|---|---|
| `# Factory digest 2026-10-20` | The window's end date. | Nothing; check it is today's. |
| `## G-002 — … (active)` | One block per goal that is not closed, with the goal's status. A goal closed within the last 24 h still gets a block; a goal closed earlier in a longer `--hours` window is listed under `## Closed earlier` (absent here). | A `blocked` goal (G-004) needs your attention first. |
| `### Done` | Tasks merged in the window, with PR link and merge SHA. `_nothing_` is normal for a goal mid-build. G-003 merged T-0020 `@dfb0c7d6`. | Confirm the merged work matches what you asked for. |
| `### In progress` | Tasks `in_progress` or `in_review`, with assignee and `attempts`. T-0010 shows `attempts 2`: it has already been sent back once. | An attempt count of 3 is the last one: ask the lead why ([example 2](02-handling-a-rejection.md)). |
| `### Blocked` | Tasks with status `blocked` and why, plus judge-blocked or failed merges, lost agents and agents running without role flags. G-004's T-0022 is blocked with no reason recorded (`(blocked)`). | A blocked task is waiting on a decision, usually the owner's or the director's. Ask the boss what unblocks it. |
| `### Up next` | How many `todo` tasks have all dependencies done and could start. G-002 has 1, G-004 has 2. | Ready tasks and no one working them is a drift signal (below). |
| `Drift:` | Computed from the database: `D1` a goal still proposed or planning but already has teams or tasks, `D2` a task in review whose PR is already merged or closed, `D3` a todo or in-progress task with no live or queued agent and no activity for over 2 h, `D4` blocked over 24 h. `Drift: none` is the good case. G-002 shows `D3 T-0009`. | Tell the boss to have the director respawn or reassign the task. D3 is the usual "work fell through the cracks" signal. |
| `Leads:` | Factory-wide line, printed once under the title (not per goal): leads live, parked and queued, and workers live. | A parked lead is waiting for a message; see [../lead-wake.md](../lead-wake.md). |
| `### Team scores` | The scorecard rows for this goal's team (explained next). | See below. |
| `## Skill scout` | New third-party skills found by the scout in the window. `No new finds.` here. | Review finds only if you want to adopt skills. |

Two sections appear only when there is something to say: `## Lessons` (New, Applied, Verified lessons of the window and
the open count) once the lessons table has a row, and `## HQ` when a persistent agent (boss, director) runs without
its role flags. The demo has neither.

## `factory scorecard`

```
TEAM     STATUS  TASKS  DONE  MERGED  REJECTIONS  RETRIES  SCORE  LEAD  
g-001-a  closed  5      5     3       3           3        5.0    -     
g-002-a  active  9      3     2       2           2        4.0    live  
g-003-a  active  6      4     3       1           1        4.0    live  
g-004-a  active  4      1     0       0           0               live  
g-005-a  closed  6      6     4       2           2        5.0    -     
g-006-a  active  31     23    22      10          10       4.0    live  
g-007-a  active  27     21    20      10          10       4.0    live  
g-008-a  closed  9      9     7       1           1        4.0    -     
g-009-a  active  13     10    9       4           4               parked
```

The digest's `Team scores` table is these rows, minus `status`, for one goal.

| column | meaning |
|---|---|
| `TEAM`, `STATUS` | The team id (`g-<goal>-<letter>`) and `active` or `closed`. |
| `TASKS`, `DONE` | All tasks of the team's goal, and how many are `done`. g-002-a: 3 of 9. |
| `MERGED` | Judge `merge` verdicts, i.e. PRs that really landed. g-004-a has none yet. |
| `REJECTIONS` | Every reviewer or QA `fail` and every judge `reject`. g-001-a has 3 (a reviewer fail on T-0003, a QA fail and a judge reject on T-0004). |
| `RETRIES` | For each task, attempts beyond the first, added up. g-001-a: T-0003 had 2 attempts and T-0004 had 3, so 1 + 2 = 3. |
| `SCORE` | The director's 1-5 evaluation (`factory eval`), averaged. Empty means the team has not been scored. |
| `LEAD` | `live` (the lead's pane is running), `parked` (the team is active but its lead sleeps until a message arrives; g-009-a waits for the owner's texts), `queued`, or `-` (no lead: closed team). |

How to read the numbers: rejections or retries that are high relative to `MERGED` mean PRs are coming back, which costs
tokens and time. Read them together with the director's score: g-001-a scored 5.0 despite three rejections because
it merged everything, while g-004-a has no score because nothing has finished. A rising rejection count on an active
team is the cue to ask the boss for the team's last verdicts, or for the lead's plan to be tightened.

## The same data in the web UI

`factory serve` is a read-only web UI ([../web-ui.md](../web-ui.md)). It shows the same numbers on these pages:

- `/` (Home): active goals, running agents and recent activity, the screen version of the digest.
- `/goals` and `/goal/<id>`: a goal's tasks and progress, the `Done` / `In progress` / `Blocked` lists.
- `/task/<id>`: one task's verdicts and history, as in [example 2](02-handling-a-rejection.md).
- `/quality`: merged PRs, merged first try versus after a rejection, rejections per merged PR and the per-team table,
  which is the scorecard with ratios.
- `/throughput`: tasks merged over time.
- `/agents`: teams and who is alive.

Screenshots are not part of this example yet (they come with the images task, G-012 T-I, and will live in
`docs/images/`). To see the pages on demo data, serve the demo home
(`HOME=<tmp>/demo/home FACTORY_HOME=<tmp>/demo factory serve --bind 127.0.0.1 --port 8799`). Keep the `HOME=` prefix, or
the Tokens page shows your own usage.
