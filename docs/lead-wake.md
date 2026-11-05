# Waking parked leads (G-014)

A lead that ran `factory park` (needs `config lead_parking=1`) has no pane; its team has `parked_at` set.

- **Tick step 1d** (`tick_wake_leads`): for every active parked team whose lead row is not live, starting or queued, a
  fresh lead session is started when the lead has unread mail other than kind `fyi` (`kind != 'fyi'`; trigger `M<oldest unread id>`). The mail stays unread;
  the kickoff carries the resume note, the re-orient one-pager and `unread: N`. One wake per team per 120 s.
- The wake goes through `spawn()` and the normal gate. A closed gate leaves the lead `queued` and the team parked; the
  dequeue starts it later and only then is `parked_at` cleared.
- A wake never jumps the queue (T-0481): if a lead of a higher-priority goal (lower number) is already `queued`, the woken
  lead is queued too and step 2 starts them in `queue_order_key` order. Equal priority keeps the direct start.
- **Rollback**: with `lead_parking=0` and no mail, the tick wakes one parked team per tick (trigger `flag-off`).
- **Stale park**: parked over 12 h, no unread non-`fyi` mail and no live or queued agent of the team: one alert to the director.
- **`factory team wake <team>`** (director): wake by hand. Exit 3 if the team is not parked, 2 if the lead is already waking.
- Blocked-worker and drift alerts for a parked team go to its lead (they wake it) instead of the director.
- `factory up` and `factory relaunch` never resurrect a parked lead; role-change notices go to live agents only.

## CI result wake (T-0400)

With `lead_parking=1`, tick step 1c' (`tick_ci_wake`) reads each `in_review` PR of a parked team whose lead is not live
(one `gh pr view` per task per tick). If `mergeStateStatus` is CLEAN and every check is COMPLETED with a
success/neutral/skipped conclusion, it mails the lead `CI green: <task> PR #N @<sha8>`; if every check is COMPLETED and one
failed or was cancelled, `CI red: ... : <failed checks>`. One message per (task, head sha) (throttle key `ci-green:`/`ci-red:`),
pending checks send nothing, a gh failure logs a `ci-wake-error` event. The mail is unread, so step 1d wakes the lead in the same tick.

## Coder restart on red CI (T-1004)

With `coder_ci_handoff` on (`resume` or `fresh`, see [prcheck.md](prcheck.md#ci-handoff-coder_ci_handoff-t-1003)) the coder leaves at PR
open, so a red head needs it back. Tick step 2c' (`coder_ci_resume_tick`, after the review autospawn) takes each `in_review` task of an
active goal with a PR, no judge hold, and no coder agent that is live, starting or queued, reads the PR once, and when every check is done
and one failed starts the coder again, once per (task, head sha) (throttle row `coder-resume:<task>:<sha>`).

- **resume** mode: `spawn(resume=<session_id>)` of the recorded coder row, in the task's worktree. A resume sends no kickoff of its own, so the
  tick mails the coder (`CI red: <task> PR #N @<sha8>`); the idle nudge wakes it.
- **fresh** start, with the same kickoff: mode `fresh`, no recorded session, no worktree on disk, or a runtime other than Claude (resume is unproven
  for Cursor). A missing worktree is rebuilt by spawn; the kickoff then tells the coder to `git reset --hard origin/<branch>` first.
- The kickoff names the head, each failed check with its run id and `gh run view <id> --log-failed`.
- Limits: `coder_ci_resume_max` starts per task (default 2); the next red head mails the lead once (`coder resume limit reached`) and starts
  nothing. At most 2 starts per tick. Nothing starts for a task whose own repo has a red main (`red_main_freeze=1`); tasks of other repos still
  start. A closed `gate()` stops the step for the tick; both retry next tick.
- A refused spawn skips that task and the step goes on with the next one. After 3 refusals on one head the tick stops retrying that head and
  mails the lead once (`coder restart refused`).
- A conflicted PR (`mergeStateStatus` DIRTY), or one without any checks for over 60 minutes, mails the lead (once per head) and starts nothing.
- Blocked and judge-held tasks start nothing (`red_pr_owner_alert` mails the lead for a blocked one). The task stays `in_review`; `attempts`
  is not bumped; the review rerun on a new head, the scorecard retry count and the worktree are untouched.
- The lead gets an `fyi` per start (`coder restarted on red CI`); event `coder-ci-resume` on the task records mode and head.

## Idle coder reap (T-1028)

Config `idle_coder_reap` = `off` (default) | `dry-run` | `on` (unknown = dry-run); threshold `idle_coder_reap_s` (default 900).
Tick step `reap_idle_coders` (after the exited-pane reap) watches every live **coder** row. The host's `agent_status` must be
`idle` or `done`; the tick keeps a first-seen-idle clock per coder in `throttle` (`idlereap:<name>`), reset the moment the agent
works again or the flag is off. Past the threshold the coder is stopped (STOP decision message from `tick`, events
`stop-requested` and `idle-coder-stopped`, a task note, Esc + deferred nudge; the usual `stop_timeout_s` hard fallback applies)
only when all of these hold:

- the task is `in_review` or `in_progress` and has a PR;
- no unread mail for the coder;
- the worktree is clean (`git status --porcelain` empty, untracked files count) and local HEAD is the head of the open PR (pushed);
- no process other than the agent (`claude`/`node`) is in the pane's foreground, and no shell at all under the agent's `claude`
  process (found by `FACTORY_AGENT` in `/proc`; Bash-tool shells run in their own session). Every shell protects the coder,
  a CI-watch shell included: it ends on its own and role guidance says slow checks. No `claude` process found = not stopped
  (unreadable = not stopped).

The flag stays `off` until T-1004 (resume on red CI) has merged.

Never stopped: judges, reviewers, QA, pm, leads (coders only). At most 3 stops per tick, oldest idle first. `dry-run` logs one
`idle-coder-would-stop` per idle episode and stops nothing. Work comes back through the existing paths: review autospawn at green
CI, and the T-1004 resume path on red CI. Roll back: `factory config idle_coder_reap off`.
