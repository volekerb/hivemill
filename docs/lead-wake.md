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
- no process other than the agent (`claude`/`node`) is in the pane's foreground, and no shell under the agent's `claude`
  process (found by `FACTORY_AGENT` in `/proc`; Bash-tool shells run in their own session) does other work than waiting for CI.
  A shell whose whole command is `gh run watch|list|view`, `gh pr checks|view` or a sleep/grep loop around those does not protect
  the coder; any other shell, a bare shell, a command with `$(`, backticks or `>`, or no `claude` process found protects it
  (unreadable = not stopped).

The flag stays `off` until T-1004 (resume on red CI) has merged.

Never stopped: judges, reviewers, QA, pm, leads (coders only). At most 3 stops per tick, oldest idle first. `dry-run` logs one
`idle-coder-would-stop` per idle episode and stops nothing. Work comes back through the existing paths: review autospawn at green
CI, and the T-1004 resume path on red CI. Roll back: `factory config idle_coder_reap off`.
