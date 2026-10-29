# Review autospawn and reserved review slots (G-022 #6)

When a coder's PR opens (task `in_review` with a `pr_url`), the tick starts the reviewer and the QA itself instead of
waiting for the lead's wake. Design: staff-engineer ADR M10886, "Parallel R+Q at PR open". Off by default.

## Config

- `review_autospawn` = `off` (default) | `on`. Rollback: `factory config review_autospawn off`; the next tick starts nothing.
- `review_reserved_slots` (default `2`): with `review_autospawn=on`, this many of `max_live_agents` are kept free for
  reviewer and QA. `pm`, `coder` and `cleaner` spawns are queued ("N slots kept for reviewer/qa") when taking a slot would
  eat into them. Slots already held by a live or starting reviewer/qa count toward the reserve, so the reserve never
  exceeds what review needs. It is carved from the existing cap: `max_live_agents` and every other `OWNER_CAP_KEYS` value are unchanged.
  Leads, judges and relaunches are not held back.

## What the tick does (step 2c)

A task is a candidate when it is `in_review` with a `pr_url`, has a team, its goal is `active`, and it has no qa agent row (nor a
`combined-<task>` one) and no verdict yet. A task with only a reviewer row (partial start) gets its qa on the next tick. A retry round after a FAIL is not a candidate: the lead spawns it as before.

- Stored tier T2/T3 or unset (or `risk_tiers` off): `reviewer-<task>` and `qa-<task>` start in the same tick.
- Stored tier T0/T1 with `risk_tiers=on`: one `combined-<task>` agent (role reviewer). It records the reviewer verdict, then
  `factory verdict <T> --as qa ...` for the QA verdict on the same head. Only the agent named `combined-<task>` may use
  `--as qa` (a plain reviewer's `--as` is ignored); the QA `## Gates` rules apply unchanged.
- The team's worker cap (`dispatch_max_team_workers`) is not consulted. `gate()` is: agent, process, memory, load, tmp and usage caps all still apply.
  A closed gate or a queued spawn stops the loop; the next tick retries.
- The parked lead gets an `fyi` mail (does not wake it). Events: `review-autospawn` and `review-autospawn-error` on the task.
- The brief tells them to run only `ci/affected.py --run` locally; CI runs the full suite.

The tick never classifies a PR: the tier is whatever `factory task tier` stored (G-022 #1). Merging is unchanged.

Tests: `tests/test_review_autospawn.py`.
