# Lessons

Corrections and incidents are kept in the factory DB (`lessons`, plus `lesson_links` for the later apply/verify step), not in messages.

```
factory lesson add --title TEXT --roles r1,r2 --rule TEXT|- [--what TEXT|-] [--source M733,T-0016,#11,G-003]
factory lesson edit L-001 [--title] [--roles] [--rule TEXT|-] [--what TEXT|-] [--source ...]
factory lesson list [--status open|applied|verified] [--role R]
factory lesson show L-001
factory lesson link L-001 (--task T-0001 | --pr URL) [--via prompt|settings|hook|skill|cli|other]
factory lesson apply L-001 [--note TEXT]
factory lesson verify L-001 --evidence TEXT|-
factory lesson reopen L-001 --reason TEXT|- [--source ...]
```

- `add` and `edit` are for boss, director, lead and owner. Every worker role is refused (exit 3), including a worker process that claims another `FACTORY_AGENT`; workers report lessons to their lead. `list` and `show` are read-only and also work for an unknown identity.
- Fields: `id` (L-001…), `title`, `roles` (comma list of roles, or `all`), `what` (optional description), `rule` (the instruction), `sources` (comma list of `M<n>` messages, `T-nnnn`, `G-nnn`, `#<pr>` or a GitHub PR URL), `status` (`open`, later `applied`/`verified`), `recurrences`, `author`, timestamps.
- Only one of `--rule` and `--what` may be `-` (stdin). `edit --source` appends new tokens without duplicates; `edit` with nothing to change exits 2. Invalid input exits 2 and writes nothing.
- `list --role R` matches lessons whose roles contain `R` or `all`. `show` ends with `## links` and `## history`.
- The tables are created by `migrate()` on the first command after an upgrade.
- Lifecycle `open → applied → verified`. `link` ties a lesson to the task or PR that applied it (boss, director, lead, owner; the task must exist, the same task or PR twice exits 2). A link is *merged* once the task has a judge `merge` verdict or the PR is MERGED (`--pr` is for lessons applied by old PRs). `apply` needs status `open`, at least one link and every link merged (exit 2 listing the unmerged ones otherwise). `verify` is director, boss, owner only and needs status `applied` plus `--evidence`. `reopen` (applied or verified lessons) records a repeat of the mistake: status back to `open`, `recurrences` +1, verified fields cleared, event `recurrence`. Workers are refused (exit 3) on all four.
- Judge post-merge hook: after `factory judge T --merge` succeeds the links of T get `merged_at`, and every open lesson linked to T whose links are all merged becomes `applied` (lessons not linked to T are never touched). If the PR changed `roles/_common.md`, `roles/hooks/`, `roles/<r>.md`, `roles/settings/<r>.json` or `roles/plugins/<r>/`, each live boss/director/lead affected gets a "role files changed: relaunch pending" request, the director gets one message listing `factory relaunch <name> --when-idle` per agent (the boss too when the director is affected), and a `roles-changed` event is recorded. The hook never changes the merge result; a failure is recorded as event `post-merge-hook-failed`.
- The daily digest (not `--goal` digests) gets a `## Lessons` section (New / Applied / Verified in the window and an `Open: N · applied, awaiting verification: M` line) once the lessons table has a row.

## Lesson checks (bin/factory)

Four recorded lessons are checked mechanically by the CLI, and a fifth by the tick (see the end of this section):

- **Goal creation is the boss's job** (G-003, M661/M664). `factory goal new` accepts the boss and the owner; every other role exits 3 with "only the boss creates goals". `goal edit` and `goal status` are unchanged.
- **No spec edit under a live worker** (T-0016, M253/M255). `factory task update T --spec/--acceptance` exits 2, before any write, while a worker of a worker role is `live` on T with no stop requested. The owner is not exempt. Spec change = `factory stop <name> --reason ...`, then `task update`, then `factory spawn <role> --task T`. A queued, done, exited or stop-requested worker does not block; `--title`, `--status`, `--note`, `--pr` and `--after` are never blocked.
- **Spec by message is detected** (same incident). When a lead, director, boss or owner messages a live worker that holds a task, and the subject or body contains spec, acceptance, criteri*, requirement* or scope, `factory msg` stays exit 0 and delivers the message, but prints a warning on stderr and records a task event `spec-by-msg`. Messages of kind `decision` are not checked.
- **Spawn needs the spec in the DB** (M451). `factory spawn --task T` exits 2 if T does not exist, has an empty spec, or (role coder or qa) has an empty acceptance. Only the `spawn` command checks this; the tick dequeue, `up` and `relaunch` are unchanged.
- **Claude outside `factory spawn` is audited by the tick** (T-0024, M343/M347). At most once per 300 s (throttle key `claude-audit`) the tick walks the process table. Agent pids are the claude processes whose `--resume`/`--session-id` is the session of a `live` agent. Any other claude process whose parent chain (at most 64 steps) reaches an agent pid was started from inside an agent pane. Each such pid, once (throttle key `claude-outside:<pid>`), gets an `agent` event `claude-outside-spawn` (`pid`, first 8 argv tokens, `reasons`) and one `alert` to the director from `tick`. Top-level claude processes (the owner's panes), the agent's own process (unless resumed, see below) and anything under a non-agent shell or a done/exited agent are never findings. Backstop for stale prompts: any claude process of an agent, its own included, with `--resume` but without `--system-prompt-snapshot off` is flagged the same way (reason `resume-without-snapshot-off`, its own alert subject). Only processes whose argv[0] (or the cli after `node`) is claude count, so `grep claude` is ignored. The audit only reads /proc and sends messages; it never kills anything, an unreadable /proc skips it silently, and an exception never stops the tick.
