---
name: park
description: Hand your pane back with `factory park` while your next step is queued or the goal waits, and resume cleanly after a wake. Use when workers are spawned or queued, a PR waits on CI, review or judge, or the goal is blocked on the director or owner.
---

# Park

A parked lead has no pane; the tick wakes a fresh lead on your next unread message (worker done, report, alert, CI green/red, director or boss mail). Parking saves a slot for workers. Details for operators: docs/lead-parking.md.

## When to park

Park when nothing is left for you to do until a message arrives:
- workers are spawned or queued for your tasks, and you have no other task to start;
- a PR waits on CI, reviewer, QA or judge;
- the goal is blocked or waiting on the director or owner and you already sent the question.

## When not to park

- A fix-loop decision is open (retry, spec change, escalation): decide and act first.
- A blocked-worker alert is unanswered: handle it first (blocked-worker skill).
- A task is `todo` and ready and the gate has room: spawn it first.
- You have unread mail: `factory park` refuses.

## Steps

1. `factory inbox`. Read and act on everything; park refuses while mail is unread.
2. Check `factory task ready --goal G` is empty or all its tasks are spawned or queued.
3. Write the resume note: what you wait for, and the next step when it arrives. One or two sentences, with task ids, e.g. `waiting: T-0412 PR #230 in judge; next: when merged spawn coder for T-0413`.
4. `factory park "<resume note>"` (use `-` and a heredoc for a longer note). Then stop; the tick closes the pane.
5. If it says parking is off (`lead_parking=0`), stay up and wait for the next 📬 as before.

## After a wake

You are a fresh session. The kickoff holds the resume note and `unread: N`.
1. `factory whoami`, then `factory inbox`.
2. Act on each message; `factory task show T` for anything you need, never trust the note over the DB.
3. Park again when the next step is queued.

## Never

- Park to avoid a decision, or with a worker alert unanswered.
- Run `factory team wake` (director only).
