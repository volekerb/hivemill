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
- a transcript write. For claude this is the mtime of `~/.claude/projects/<cwd with non-alphanumerics as ->/<session>.jsonl`, or of the newest jsonl in that dir. For cursor it is the newest file under `~/.cursor/projects/<dir>/agent-transcripts`, where `<dir>` is the agent's policy workspace (`spawn_args.cursor_workspace`, else its cwd) with each non-alphanumeric character as `-`, matched exactly. Only two levels (`<id>.jsonl` or `<id>/<id>.jsonl`) and at most 2000 files are read, and no symlink is followed;
- a DB write by the agent itself: an event it wrote, a message it sent, or reading its mail.

## Ladder (claude, and cursor without an ended turn)

1. Idle for W: one nudge is typed into the pane through T-1599's `kickoff_submit` (prompt, plus Enter on cursor). The step is recorded as an `idle-watchdog-nudge` event and a task note.
2. No progress for W/2, at least W/2 after the nudge (progress after the nudge does not restart a full W): one `relaunch --fresh` through T-1599's `relaunch_fresh_from_tick`, on the same task and branch. It waits until the checks spawn runs after the pane closes pass (agent and process caps, role reserves, the Cursor pane limit, load), and runs at most once per tick. Event: `idle-watchdog-relaunch`, which records the kickoff it replaces. If the spawn gate queues the fresh start anyway, both directors are alerted at once.
3. Still idle W after the relaunch, whether or not the new pane read its kickoff: one `idle-watchdog-alert` message goes to both directors (claude and cursor), naming the agent, the task, the minutes idle and the actions taken. After that the watchdog does nothing more for this agent on this task attempt.

## Cursor ended turns (T-1621 findings W1 to W4)

If the newest transcript of this run (written after `agents.started`) has a last line is `{"type": "turn_ended", ...}`:

- `status` error (W4): the org director is alerted at once with the error text, for example a usage-limit message. No nudge is sent.
- otherwise (W1, W3): once the turn ended at least 10 minutes ago, the agent gets up to 2 nudges at least 10 minutes apart. After that comes one escalation to the org director, quoting the last 300 characters of the agent's final text. This path never relaunches.

A first kickoff message that is still unread belongs to T-1599's `tick_cursor_kickoff` (resend, then relaunch), and the watchdog skips that agent. After a watchdog relaunch, T-1599 resends the new kickoff once and then escalates; it never relaunches a second time.

## Refused `factory done` (W5)

When a finish check refuses `factory done`, the refusal is recorded as an agent `done-refused` event with `{"gate", "code", "why"}`. `gate` is `judge` (the judge's outcome check) or `coder-checks` (unpushed work, acceptance walk or prcheck). `why` is the refusal's last stderr line, which names the check. The watchdog then alerts the org director once per task attempt, quoting `why`. The unread-orders refusal is not recorded, because it delivers the orders itself.

## Never acted on

- tasks that are done, cancelled or blocked (a blocked task names its wait in `tasks.waits`);
- goals that are not active;
- rows that are stopping, have a relaunch queued, or are still starting;
- a pane the host does not list, or reports as `working`;
- a claude agent with a shell running under it, such as a CI wait or a test run.

A hold (a judge hold reason, or `HOLD` in the task spec) counts as a named wait: it is escalated to the org director, never nudged.

## State

All state lives in `idle-watchdog-*` events, keyed `agent|task|attempts`. A relaunch keeps the key, so no step repeats across ticks or runs, and a retry attempt starts fresh.
