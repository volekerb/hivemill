---
name: blocked-worker
description: Answer a worker that is stuck on a permission dialog without changing the owner's settings. Use when a "blocked >10 min" alert arrives, a worker waits on a permission dialog, or a worker asks about "don't ask again" or adding a directory.
---

# Blocked worker

Deny a stuck worker's permission dialog once, so it keeps working and the owner's user settings stay untouched (L-021).

Follow these steps in order.

1. Read the alert body. The pane tail shows what the worker is waiting on.
2. Decide what the dialog asks for. Dialog options 1 and 2 ("don't ask again", "add directory") change the owner's user settings for every session. A settings change is the owner's call, never the worker's or yours.
3. If it is a permission dialog for something inside the worker's worktree or scratchpad that the task really needs, escalate to the director: `factory msg director "<worker> needs access: <what>" "<pane tail, task id>" --kind escalation --task T`. Do not unblock until the director answers.
4. If it is any other permission dialog (a read or write outside the worktree and scratchpad, a command the worker does not need), deny it once: `factory unblock <worker> --reason "L-021: <what was denied>"`.
   - It sends an UNBLOCK decision message, presses Esc (the dialog's No) and nudges the worker to read its inbox. The worker keeps running; the pane, the task and the stop flag are untouched.
   - `factory unblock <worker> --reason "..." --dry-run` shows the steps and changes nothing.
5. Check the worker moved on: `factory agents` and `factory task show T` after a minute or two.
6. If the worker is blocked again on the same thing, or the pane is not a permission dialog, stop it: `factory stop <worker> --reason "..."`. Then respawn per the spec-change and fix-loop skills; do not repeat their steps here.
7. Never tell a worker to pick option 1 or 2, and never type into a worker pane yourself.

Stop and escalate when:
- the dialog asks for a settings change the task needs: the director decides with the owner.
- the target is a lead, director or boss: `factory unblock` refuses it (L-019).
- you are unsure what the pane shows: ask the director before anything else.

Sources: L-021, M1767.
