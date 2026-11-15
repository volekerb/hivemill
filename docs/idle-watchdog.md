# Idle-progress watchdog (T-1622, G-058)

The tick watches every live worker (coder, qa, reviewer, pm; claude and cursor runtimes) that holds an open task. When
a worker makes no progress, the tick nudges it, then relaunches it fresh, then alerts both directors. Leads, HQ agents
and the judge are never watched.

Code: `idle_watchdog` in `bin/factory_cli.py`. It runs in the idle-coder tick step, right after `reap_idle_coders`.

## Config

| key | default | meaning |
|---|---|---|
| `idle_watchdog` | `shadow` | `off`: nothing. `shadow`: the steps are written as events only, with no pane action, no message and no task note. `on`: the full ladder runs. An unknown value counts as `shadow`. |
| `idle_watchdog_min` | `30` | W, the minutes without progress before the first nudge. |

The director flips it on with `factory config idle_watchdog on`. To roll back: `factory config idle_watchdog shadow` (or `off`).

## Progress signal

The idle clock starts at the newest of these:

- the run's start (`agents.started`);
- a worktree change in the agent's cwd: the HEAD commit time and the mtimes of changed or untracked files (`git status --porcelain -uall`);
- a transcript write. For claude this is the mtime of `~/.claude/projects/<cwd with non-alphanumerics as ->/<session>.jsonl`, or of the newest jsonl in that dir. For cursor it is the newest file under `~/.cursor/projects/<dir>/agent-transcripts`, where `<dir>` matches the agent's policy workspace (`spawn_args.cursor_workspace`, else its cwd) with separators ignored;
- a DB write by the agent itself: an event it wrote, a message it sent, or reading its mail.

## Ladder (claude, and cursor without an ended turn)

1. Idle for W: one nudge is typed into the pane through T-1599's `kickoff_submit` (prompt, plus Enter on cursor). The step is recorded as an `idle-watchdog-nudge` event and a task note.
2. Still idle W/2 after the nudge (and W overall): one `relaunch --fresh` through T-1599's `relaunch_fresh_from_tick`, on the same task and branch. This waits behind the spawn gate (the agent keeps its slot) and runs at most once per tick. Event: `idle-watchdog-relaunch`.
3. Still idle W after the relaunch: one `idle-watchdog-alert` message goes to both directors (claude and cursor), naming the agent, the task, the minutes idle and the actions taken. After that the watchdog does nothing more for this agent on this task attempt.

## Cursor ended turns (T-1621 findings W1 to W4)

If the newest transcript's last line is `{"type": "turn_ended", ...}`:

- `status` error (W4): the org director is alerted at once with the error text, for example a usage-limit message. No nudge is sent.
- otherwise (W1, W3): once the turn ended at least 10 minutes ago, the agent gets up to 2 nudges at least 10 minutes apart. After that comes the alert to both directors, quoting the last 300 characters of the agent's final text. This path never relaunches.

A kickoff message that is still unread belongs to T-1599's `tick_cursor_kickoff` (resend, then relaunch), and the watchdog skips that agent.

## Refused `factory done` (W5)

When a finish check refuses `factory done`, the refusal is recorded as an agent `done-refused` event with `{"gate", "code", "why"}`. `gate` is `judge` (the judge's outcome check) or `coder-checks` (unpushed work, acceptance walk or prcheck). `why` is the refusal's last stderr line, which names the check. The watchdog then alerts the org director once per task attempt, quoting `why`. The unread-orders refusal is not recorded, because it delivers the orders itself.

## Never acted on

- tasks that are done, cancelled or blocked (a blocked task names its wait in `tasks.waits`);
- goals that are not active;
- rows that are stopping, have a relaunch queued, or are still starting;
- a pane the host does not list, or reports as `working`;
- a claude agent with a shell running under it, such as a CI wait or a test run.

A hold (a judge hold reason, or `HOLD` in the task spec) counts as a named wait: it is alerted, never nudged.

## State

All state lives in `idle-watchdog-*` events, keyed `agent|task|attempts`. A relaunch keeps the key, so no step repeats across ticks or runs, and a retry attempt starts fresh.
