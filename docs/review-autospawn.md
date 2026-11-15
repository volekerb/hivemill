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
- `review_verdict_timeout_min` (default `45`; `0` = off): with `review_autospawn=on`, a tick-started reviewer or QA that is live
  this many minutes with no verdict gets a stop request (see [Ended without a verdict](#ended-without-a-verdict-one-replacement-then-the-alert-t-1600)).
  Not an owner cap. Rollback: `factory config review_verdict_timeout_min 0`.

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
  first starts one replacement, and only when that one also ends with no verdict sends one `request` to the lead (or director) and logs
  one `review-no-verdict` event, once per task, head and role. It starts no third agent. Details in the next section.
- **Verdict bound to the started head.** The `review-autospawn` event stores the full head (`sha`). A `factory verdict --pass` whose `--sha`
  differs from the head that worker was started on is refused; a `--fail` without `--sha` is recorded on the started head. Verdicts of workers
  the lead spawned (no event) behave as before, as do those of a same-name worker the lead respawned after the tick's event (the event
  describes the earlier worker). No schema change; the merge gate is untouched.

## Ended without a verdict: one replacement, then the alert (T-1600)

A dead reviewer or QA used to park the PR until a human acted (the alert was the first and only step). Now, per task, head and role:

1. **First ending** (the `<role>-<task>-<sha8>` row is not `queued`, `starting` or `live`, and no verdict of that role is on the head):
   the re-run pass starts one replacement under the same name, through the same loop as any re-run role. The PR must still be open with
   green CI, no coder live, and `gate()` open (agent cap, `review_reserved_slots`, memory, load and usage caps all apply). Events:
   `review-autospawn` (as for every start; it binds the replacement's verdict to the head) and `review-respawn`
   (`agent`, `role`, `head`). No mail. A closed gate or refused spawn logs the usual `review-autospawn-wait` / `review-autospawn-error`
   and the replacement starts on a later tick. Throttle row `review-respawn:<task>:<head>:<role>` marks the replacement as used.
2. **Second ending** (the replacement also ended with no verdict): the behaviour from before, one `review-no-verdict` event and one
   `request` to the lead or director (throttle row `review-noverdict:<task>:<head>:<role>`); the tick starts no further agent of that
   role on this head. A head that was already alerted before this rule existed gets no replacement.
3. **Timeout** (`review_verdict_timeout_min`, default 45, `0` = off): a `live` reviewer or QA of an `in_review` task that the tick
   started (it has a `review-autospawn` event on its head; first-round, combined and re-run agents alike) and that has been live longer
   than the limit with no verdict of its role on that head (the combined agent: both verdicts) gets one stop request: a `STOP` decision
   message, `stop_requested_at`, Esc and a nudge, events `stop-requested` (agent) and `review-verdict-timeout` (task). The tick's stop
   fallback closes the pane after `stop_timeout_s` if it does not leave by itself. Once a re-run row (`<role>-<task>-<sha8>`) has ended,
   rule 1 or 2 applies on the next tick. A stopped first-round or combined agent is not named per head, so rules 1 and 2 do not match
   it: the re-run pass treats its head as a head with no round and starts a fresh `<role>-<task>-<sha8>` reviewer + qa (with the usual
   `fyi`, no `review-respawn` event), and those are then under rules 1 and 2. Agents a lead or director spawned are never stopped by
   this rule, nor is an agent that was queued at its start (its start time is later than its `review-autospawn` event, which reads
   like a lead's respawn of the name). The default is 15 times the median review + QA time.

The merge gate is unchanged: a verdict of both roles on the merged head is still required, the replacement is a fresh, independent
agent of the same role, and nothing is carried over or waived. Limit: while the other role's agent on the task is still queued,
starting or live, the re-run pass does not look at the task, so the replacement starts when that agent ends.

## Judge autospawn needs both verdicts on the current head; wait events (T-1098)

- `judge_autospawn_tick` starts a judge only when the latest reviewer PASS and the latest QA PASS are each recorded on the PR's current head, or carry to it by the T-0095 rule (pure `origin/main` merges), the same test the merge gate applies. One verdict still on an older head (the other role is running on the new head) or an unverifiable carry starts no judge; the tick logs one `judge-autospawn-wait` event per task and reason and tries again next tick. Local-gate projects (`gate_cmd`) are not checked.
- The first-round review pass used to skip silently while CI was not green under `coder_ci_handoff`, a coder agent of the task was live, or `gate()` was closed. It now logs one `review-autospawn-wait` event per task, head and kind (`ci`, `coder`, `gate`) per hour, with the reason. Behaviour is unchanged: it still retries each tick. The re-run pass (a task with an earlier round, new head) logs the same events for `ci`, `coder`, `verdict` and `gate`. A hung or missing `gh` (timeout, OSError) in the judge carry check counts as unverifiable: that task gets no judge, later candidates still do.
