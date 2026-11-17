# Judge hold expiry

A lead, a director or the owner can keep a judge off a task with `factory task update <T> --judge-hold "<reason>"`.
Such a hold had no end: PRs with reviewer PASS and QA PASS waited up to 22 hours behind one (owner decision M37547,
goal G-058). The tick unit `hold_expiry` (package `go/internal/holdexpiry`) now ends these holds.

## The rule

A hold is **expired** when all of this is true:

1. `tasks.judge_hold_reason` is not empty.
2. The task is `in_review` and has a PR.
3. The latest reviewer verdict and the latest QA verdict of the task are both PASS.
4. The hold is at least `judge_hold_expiry_min` minutes old (default 120), counted from its `judge-hold-set` event.
5. The hold text names no valid boss decision.

A hold on a task where a verdict is missing or FAIL, a younger hold, a hold with a valid boss decision, and a task
that is not `in_review` or has no PR are never touched. A hold with no `judge-hold-set` event, or one whose event
time cannot be read, is kept as well (its age is unknown) and the tick prints one line about it.

The unit only clears the flag. It starts no agent and merges nothing, and it does not change the merge gate: after
the hold is gone a judge still needs reviewer PASS and QA PASS on the head, green CI and a head that contains
`origin/main`.

## How to hold longer

Get a boss decision first, then name it in the hold text with the marker `boss-approved M<id>` (case-sensitive):

    factory task update T-1234 --judge-hold "boss-approved M40001: release freeze until Monday"

The marker is valid when message `M<id>` exists, its kind is `decision`, its sender is `owner` or an agent with the
role `boss`, and it is about the task: its `task_id` is the task, or its subject or body contains the task id as a
whole word. A hold with a valid marker is kept without limit.

Valid: `boss-approved M40001: release freeze`, where M40001 is a `decision` message from `claude-boss` whose body
says "hold T-1234 until the release".

Invalid: `boss-approved M40002: waiting`, where M40002 is a `report` from a director, a decision about another
task, or does not exist. An invalid marker protects nothing: the hold expires like one with no marker. When a text
has several markers, one valid marker is enough.

## Setting the hold again

A hold set again on a task whose hold already expired once is cleared at the next tick, without the wait, unless it
names a valid boss decision or a reviewer, QA or judge FAIL verdict was recorded after the expiry. Example: hold
"wait for PR 703" set at 10:00 on a PASS/PASS PR is cleared at the first tick after 12:00; "still waiting" set at
12:05 is cleared at the next tick; "boss-approved M40001: release freeze" is kept.

## What an expiry writes

All of it is one transaction; if any part fails the hold stays and the next tick tries again.

- `tasks.judge_hold_reason` becomes NULL, the same as after `--judge-hold-clear`.
- Event `judge-hold-clear` (entity task, actor `tick`), detail `expired after <n> min: <old reason, first 200
  characters>`. It is the event the Python tick reads to reset its `judge-hold` log line.
- Event `judge-hold-expired` (entity task, actor `tick`) with JSON detail
  `{"task": "T-1234", "pr": 703, "held_min": 121, "set_by": "claude-lead-g-001-a", "reason": "<old reason>",
  "marker": "none"}`. `marker` is `none` or `invalid:M<id>` (the first marker of the text); `pr` is the PR number.
- One message from `tick`, kind `report`, to the agent that set the hold. When the hold was set by `owner`, `tick`
  or an unknown actor it goes to `<org>-director` of the goal's org. Subject `judge hold expired: <task> PR #<n>`.
  Body:

      The judge hold on T-1234 (<PR url>) was cleared by the tick.
      Old reason: hold until PR 703 merges
      Held for: 121 min (set by claude-lead-g-001-a)
      Rule: a hold on a reviewer+QA PASS PR expires after 2 h unless its text names a boss decision: boss-approved M<id>
      To hold again, get a boss decision first.

## Config

`judge_hold_expiry_min` in the config table: the expiry time in minutes. A missing row means 120. `0` turns the
rule off. A value that is not a non-negative whole number means 120.

    factory config judge_hold_expiry_min 90

## Live since 2026-11-17

The unit table (`go/internal/tick/units.go`, `TICK_UNITS` in the Python CLI) has the row `hold_expiry`, placed
directly before `mech_merge` so a hold cleared in a tick is seen by the mechanical merger and the judge autospawn of
the same tick. The Python tick has no implementation: it keeps every hold and only records the lap. A failure of the
unit is printed in the tick output (`judge-hold-expiry: failed, holds kept: ...`) and the tick goes on with the next
unit. Rollback without a deploy: `factory config judge_hold_expiry_min 0`.
