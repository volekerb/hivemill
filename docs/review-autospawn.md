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

A task is a candidate when it is `in_review` with a `pr_url`, its goal is `active`, and it has no qa agent row (nor a
`combined-<task>` one) and no verdict yet. A task with only a reviewer row (partial start) gets its qa on the next tick.

A task that already had a review round on an older head is handled by the re-run pass (`review_rerun_tick`, run first in the same step). It works per role on the current PR head: once all checks are green on a new head, each of reviewer and qa that is not done gets a fresh agent named `<role>-<task>-<sha8>` (first 8 sha chars). A role is done on a head when its agent row `<role>-<task>-<sha8>` exists or a verdict of that role on that head exists. The re-run is always reviewer + qa, never the combined agent. A gate-refused qa is retried on the next tick; a queued one is started by the spawn queue, not by the tick. The `fyi` mail goes out for whatever did start. So a retry round after a FAIL needs no lead action: the tick starts it when the new head goes green.

- Skipped, not spawned (both passes): the tick starts nothing and logs one `review-autospawn-skip` event per task per day (reason in the detail) when the PR url's OWNER/NAME is not the project's `gh_repo` (else origin's), `gh pr view` fails, the PR is closed or merged, or the task's head branch is not on origin. If origin cannot be read the tick fails closed with a `review-autospawn-error` event. The task status is never changed; the lead or director decides.
- Stored tier T2/T3 or unset (or `risk_tiers` off): `reviewer-<task>` and `qa-<task>` start in the same tick.
- Stored tier T0/T1 with `risk_tiers=on`: one `combined-<task>` agent (role reviewer). It records the reviewer verdict, then
  `factory verdict <T> --as qa ...` for the QA verdict on the same head. Only the agent named `combined-<task>` may use
  `--as qa` (a plain reviewer's `--as` is ignored); the QA `## Gates` rules apply unchanged.
- The team's worker cap (`dispatch_max_team_workers`) is not consulted. `gate()` is: agent, process, memory, load, tmp and usage caps all still apply.
  A closed gate or a queued spawn stops the loop; the next tick retries.
- A team is not required: a task with no team (lead-less director dispatch) qualifies like any other. Its reviewer and QA
  are spawned without a team and the brief names the `director` as the report target.
- The parked lead gets an `fyi` mail (does not wake it); for a team-less task the `fyi` goes to the `director` instead. Events: `review-autospawn` and `review-autospawn-error` on the task.
- The brief tells them to run only `ci/affected.py --run` locally; CI runs the full suite.

The tick never classifies a PR: the tier is whatever `factory task tier` stored (G-022 #1). Merging is unchanged.

Tests: `tests/test_review_autospawn.py`.

## With `coder_ci_handoff` on (T-1003)

When `coder_ci_handoff` is `resume` or `fresh`, the coder leaves at PR open while CI runs, so the first-round pass (`review_candidates`)
starts the reviewer and QA only once `ci_verdict` of the PR head is green, as the new-head pass already does. A pending or red head
starts no reviewer or QA row; the next tick retries. `judge_autospawn_candidates` and `mech_candidates` are unchanged (they still need
verdicts and green CI). With the flag `off` the first-round pass starts review at PR open as before. See [prcheck.md](prcheck.md#ci-handoff-coder_ci_handoff-t-1003).

## Ordering, missing roles and verdict heads (T-1048)

- **No review while the coder works.** Both passes skip a task (retry next tick, nothing logged) while an agent of
  role `coder` for it is `queued`, `starting` or `live`. The prcheck result does not gate review.
- **A verdict of one role never closes the head.** The new-head pass starts the role that has neither an agent row `<role>-<task>-<sha8>`
  (the stored, org-prefixed name, e.g. `claude-qa-t-0001-1234abcd`) nor a verdict of that role on the head, even when the other role already has a verdict. A head whose only verdict is under
  `REVIEW_VERDICT_GRACE_S` (300 s) and that has no tick-started agent waits, so a lead who is about to start the other half is not doubled.
- **Ended without a verdict.** If a tick-started `<role>-<task>-<sha8>` row has ended with no verdict of its role on that head, the tick
  sends one `request` to the lead (or director) and logs one `review-no-verdict` event, once per task, head and role. It starts no third agent.
- **Verdict bound to the started head.** The `review-autospawn` event stores the full head (`sha`). A `factory verdict --pass` whose `--sha`
  differs from the head that worker was started on is refused; a `--fail` without `--sha` is recorded on the started head. Verdicts of workers
  the lead spawned (no event) behave as before, as do those of a same-name worker the lead respawned after the tick's event (the event
  describes the earlier worker). No schema change; the merge gate is untouched.

## Judge autospawn needs both verdicts on the current head; wait events (T-1098)

- `judge_autospawn_tick` starts a judge only when the latest reviewer PASS and the latest QA PASS are each recorded on the PR's current head, or carry to it by the T-0095 rule (pure `origin/main` merges), the same test the merge gate applies. One verdict still on an older head (the other role is running on the new head) or an unverifiable carry starts no judge; the tick logs one `judge-autospawn-wait` event per task and reason and tries again next tick. Local-gate projects (`gate_cmd`) are not checked.
- The first-round review pass used to skip silently while CI was not green under `coder_ci_handoff`, a coder agent of the task was live, or `gate()` was closed. It now logs one `review-autospawn-wait` event per task, head and kind (`ci`, `coder`, `gate`) per hour, with the reason. Behaviour is unchanged: it still retries each tick.
