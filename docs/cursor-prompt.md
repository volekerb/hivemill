# Cursor prompt chain v2 (`cursor_prompt`)

A Cursor worker used to get the Claude role prompt and the Claude kickoff unchanged. That text assumes Claude Code:
skills, subagents, background waits and a harness that keeps the model running. Cursor Agent ends its turn after a
summary and nothing calls it again, so workers stopped without `factory done`.

`cursor_prompt` selects the prompt chain of Cursor agents. Claude agents are never affected.

| Value | Role prompt (`.cursor/rules/factory-role.mdc`, `run/<name>.prompt.md`) | Kickoff |
|---|---|---|
| `v1` (default) | `roles/_common.md` + `roles/<role>.md` + org section | `kickoff_text` (same as Claude) |
| `v2` | `roles/cursor/<role>.md` + org section when that file exists, else as v1 | Cursor worker kickoff; HQ roles (boss, director, lead) as v1 |

```
factory config cursor_prompt v2    # new Cursor spawns use it; running agents keep their prompt
factory config cursor_prompt v1    # back
```

Any other value is refused (exit 2). After a flip the tick's freshness check reports live Cursor agents whose
prompt file no longer matches (reason `prompt`), as it does for any role file change.

## What v2 changes

`roles/cursor/coder.md` is the whole role prompt of a Cursor coder, about half the size of the v1 text.

- **Loop contract first.** No human reads the pane and there is no user. `factory done` is the only allowed end of
  a turn. No plan, summary, question or offer as a last message. Wait for nothing, CI included. "Briefly inform the
  user about the task result", "continue" and a prompt that begins "Factory watchdog: your turn ended" are orders to
  act.
- **Ten steps, each a command.** Exact push form, `factory pr create --title 'T-xxxx: <what the change does>' --body -`
  with the body skeleton inline, `factory prcheck`, `in_review`, `factory done`.
- **No Claude-only tools.** No skill, subagent or background CI wait. Self-review is one read of the own diff.
- **Co-author trailer.** Commit with `env -u CURSOR_AGENT git commit`. A push refused for a `Co-authored-by: Cursor`
  line is rebuilt with `git commit-tree` and pushed again. The public-text guard is unchanged.
- **Stuck.** Commit, push, `factory done "BLOCKED: <exact command and error; what you need>"`. `factory done` sends
  it to the lead, or to the director when the coder has no team.

The v2 kickoff (`scout/cursor_harness.py`, `kickoff_v2`) repeats the loop contract and, for a coder, lists the
seven commands with its own task id and branch filled in. Other Cursor workers get the loop contract and their
task; their role prompt stays v1 until a `roles/cursor/<role>.md` exists.

No hook, deny rule or guard changes. `.cursor/hooks.json`, `cli.json` and `cursor-guard-adapter.py` are as before.

## Why (audit of the v1 chain)

Measured on 40 Cursor coders from their `hook-events.jsonl`: 87% of the logged span (6649 of 7634 minutes) is
gaps of 10 minutes or more with no tool call, and 25 of 40 coders have at least one such gap.

1. No loop contract in the kickoff or the role prompt.
2. Cursor's own follow-up prompt asks the agent to inform "the user"; the prompt never said there is none.
3. The push is refused for the Cursor co-author trailer, and the v1 text says a denied push stays denied and to stop.
4. Step 9 says to wait for CI in the background, which only Claude Code can do.
5. Six steps name Claude-only skills or a subagent.
6. `--title T` was copied literally, which gave PR titles that are only the task id.
7. The steps start after about 7000 characters of rules that are not steps.
8. "Tell your lead" has no working address for a coder without a team.

## A/B after merge

The director picks three small ready Cursor tasks per arm and flips `cursor_prompt` between arms.

| Metric | How to read it | v1 | v2 |
|---|---|---|---|
| Turns | turns in the agent transcript until `factory done` or the kill | | |
| Idle minutes | sum of gaps of 10+ min in `hook-events.jsonl` | | |
| Done rate | runs that ended with `factory done` / runs | | |
| Reviewer FAIL rate | first-round reviewer FAIL / PRs opened | | |

Ship rule: v2 becomes the default when its done rate is higher and its reviewer FAIL rate is not higher. Changing
the default is one line in `DEFAULT_CONFIG` and `bin/flags.json`.

## Not covered

- The Go spawn mirror (`go/internal/spawn`) renders the v1 text only.
- Auto-continue and relaunch of a worker that still ends its turn belong to the tick watchdog.
