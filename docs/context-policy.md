# Context policy for persistent sessions

Persistent roles (director, leads) run with `--resume`, so their context only grows and every request re-reads it.
All factory state lives in the DB, so a fresh session loses nothing.

## `factory relaunch <name> --fresh [--when-idle] [--dry-run]`
Starts the agent without `--resume`: a new session id (`--session-id`), the same model, effort, settings, plugin dirs and
prompt file as a plain relaunch. The new id is stored on the agent row and the event `relaunched-fresh {old_sid, new_sid}`
is written. `--when-idle` uses the same queue as the plain relaunch (the tick restarts the agent once it is idle).
Permissions are the same as a plain relaunch (owner/director; the boss only the director; a director needs
`--when-idle` for itself; workers are refused).

The kickoff of a fresh session tells it to run `factory whoami` and `factory inbox`, then gives a short one-pager from the
DB (at most ~12 KB, about 3k tokens). A lead gets its goal (intent, success criteria), open tasks and PRs, and the latest
decisions. The director gets the active goals with open task counts, and the unread escalations/requests to the boss.
Everything else is read on demand (`factory goal show`, `factory task show`, `factory inbox --show M####`).

## Tick recycling
Config `recycle_context_tokens` (default 250000, `0` = off). Each tick queues `relaunch --fresh --when-idle` for a
director or lead when ALL hold:

- the latest main-thread request of its session read more than the limit (input + cache read + cache creation tokens,
  `web/tokens.py latest_context`);
- herdr reports it idle (never mid-turn);
- its inbox has no unread message;
- no relaunch is already pending for it;
- it was not recycled in the last 6 hours.

The boss (the owner's pane) and all workers are never recycled. Each queued recycle writes `recycle-queued
{context_tokens, limit, session}`; the existing `relaunch-run` / `relaunched-fresh` events follow when the tick runs it.

## Parked leads
A lead with its next step queued runs `factory park "<resume note>"`; its pane closes and the next unread message wakes
a fresh lead. The wake kickoff carries the resume note and the re-orient one-pager, so a lead never relies on a
previous pane's context. Commands, caps and rollback: [lead-parking.md](lead-parking.md).
