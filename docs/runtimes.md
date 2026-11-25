# Agent runtimes

> **Legacy pipeline.** This page describes the paused chain of agents, kept as the rollback. The current flow is the runner: [option-b-runner.md](option-b-runner.md).

Hivemill defaults every role to **Claude Code** via `herdr agent start --kind claude`. Other LLM harnesses plug in through `scout/agent_runtime.py` without changing the boss / director / teams org model.

Empty `agent_runtimes` keeps the historical all-Claude factory. Setting Cursor for some or all roles does **not** remove Claude support.

## Owner guidance (short)

| Goal | What to do |
|---|---|
| Stay on Claude (default) | Leave `agent_runtimes` empty (or `factory config agent_runtimes ''`) |
| One role on Cursor | `factory config agent_runtimes pm=cursor` |
| Whole org on Cursor | `factory config agent_runtimes '*=cursor'` then relaunch HQ (`factory relaunch boss --fresh`, same for director) and let the tick respawn workers |
| Mix | e.g. `boss=claude,director=claude,pm=cursor,coder=cursor` |
| Check what is running | `factory agents` (RUNTIME column) · web **Agents** page · `factory cursor-pilot status` |
| Auth for Cursor | `agent login` or `CURSOR_API_KEY` on the host |

**Do not** flip live HQ during a critical owner session without a plan: relaunch drops the old transcript (`--fresh`). Workers finish naturally; new spawns pick up the new runtime.

**skilleval** `evaluator` always stays on Claude, even with `*=cursor`.

## Where you see Claude vs Cursor

| Surface | What you see |
|---|---|
| Web `/agents` | Runtime chips (`claude` / `cursor`), Runtime filter chips, Runtime column |
| Web home Agents strip | Same chip next to each agent name |
| Web `/resources` | Live mix `claude N · cursor M` next to agent cap (Claude process cap still counts host `claude` only) |
| CLI `factory agents` | `RUNTIME` column (`claude` default when spawn_args has no runtime) |
| herdr panes | `herdr agent list` → `agent: claude` or `agent: cursor` |
| Pane title / display | Unchanged role·task label; kind is in herdr + factory UI |

Kickoff text and `spawn_args` bodies are **never** shown in the web UI (unchanged). Only `spawn_args.runtime` is derived for display.

## Two orgs: claude and cursor (G-028)

The main way to pick a runtime is the **org**, not `agent_runtimes`. An org is a complete chain of command (boss, director, team leads, workers) on one runtime with one model map (`scout/orgs.py`):

| Org | Runtime | Judgement roles (boss, director, lead, pm, reviewer, judge, staff-engineer) | Build roles (coder, qa, cleaner, scout) |
|---|---|---|---|
| `claude` (default) | Claude Code | `claude-opus-5-5` | `claude-sonnet-5-5` (cleaner: Haiku) |
| `cursor` | Cursor Agent | `grok-4.7-high` | `composer-2.5` (qa: `grok-4.7-high`) |

In the cursor org, qa runs on `grok-4.7-high` (owner decision 2026-11-15, G-058): QA is part of the review gate. The Cursor pane limit gate follows the role's model, so a `grok-4.7-high` usage limit also stops cursor qa spawns (they queue until the reset), not only the judgement roles. Running cursor qa agents keep the model they started with; new spawns get the new one.

The skilleval `evaluator` always runs on Claude.

- **Goals belong to one org** (`goals.org`). `factory goal new --org cursor ...` (default: the creating boss's org) · `factory goal list --org cursor` · `factory goal org G-031 claude` moves a goal: new spawns use the new org, and live agents finish under the old one.
- **Every spawn inherits its goal's org**: runtime, model, and name prefix `cursor-` (`cursor-director`, `cursor-lead-g-031-a`, `cursor-coder-t-0900`). The default org is prefixed only with `org_prefix_default=1` (`claude-boss`); with `0` a single-org install keeps `boss`, `director`, `coder-t-0001`.
- **Routing**: `factory msg boss|director|owner|staff-engineer` resolves inside the message's org: the goal's org when `--goal`/`--task` is given, otherwise the sender's org.
- **Chain of command**: only bosses and directors message across orgs (e.g. `factory msg cursor-director "capacity" ...`). Leads and workers are refused (exit 3).
- **Weekly-cap offer** (`cap_fallback_offer=1`, default `0`): while the Claude weekly gate is closed (fresh `seven_day` ≥ `max_seven_day_pct`) and `cursor` is in `orgs_enabled` and not limited, the tick mails the org's boss once per goal and cap window (`cap: G-xxx could move to cursor`, with `factory goal org G-xxx cursor`) for each active P1/P2 claude goal that has a todo task and no live agent. It never moves a goal; the boss decides.
- **Capacity is shared**: one agent cap, memory floor and load gate for the box, with P1 goals first across both orgs. The Claude usage caps (5h/7d) measure Claude quota only. Leads (wake, dequeue, spawn) and judges skip only the load gate (T-0867); the memory floor, `max_live_leads`, the agent cap and the usage caps still apply to them.
- **HQ**: `orgs_enabled=claude,cursor` makes `factory up` keep `<org>-director` and `<org>-boss` running for each org. The daily digest goes to every org's boss; a merge freeze escalates to every org's HQ.
- **UI**: an org filter (All, Claude, Cursor) on every page, two org lanes on Home, and org chips and columns on Agents, Goals, Tasks, Tokens and Resources (docs/web-ui.md).

### Go-live (owner)

```bash
factory config agent_runtimes ''          # orgs decide the runtime now
factory config org_prefix_default 1       # claude-boss, claude-director, ...
factory config orgs_enabled claude,cursor
factory goal org G-0xx cursor             # for each goal the cursor org owns
factory up                                # starts the missing <org>-director / <org>-boss
```

## Config

| Key | Example | Meaning |
|---|---|---|
| `agent_runtimes` | `pm=cursor` | Per-role runtime override on the **default** org only. |
| `agent_runtimes` | `*=cursor` or `all=cursor` | Every role in `CURSOR_ROLES` → `cursor` (not `evaluator`). |
| `cursor_pilot_roles` | `pm,coder` | Legacy alias → `pm=cursor,coder=cursor`. Prefer `agent_runtimes`. |
| `cursor_prompt` | `v2` | Prompt chain of Cursor agents: `v1` (default) = the Claude role prompt and kickoff, `v2` = the short loop-until-done prompt. See [cursor-prompt.md](cursor-prompt.md). |

**Resolution (`org_runtime`):** On the default org, **empty** `agent_runtimes` resolves **every** role to Claude. `*=cursor` (or `all=cursor`) resolves every role in `scout/cursor_harness.CURSOR_ROLES` to `cursor`. The skilleval **evaluator always stays Claude**, even with `*=cursor`. A **non-default org** ignores `agent_runtimes` for runtime choice and uses `scout/orgs.py` instead — the **cursor org is `cursor` even when `agent_runtimes` is empty** on the default org.

Allow-lists: `scout/agent_runtime.ALLOWED` / `scout.cursor_harness.CURSOR_ROLES`.

## Pure Cursor factory

```bash
factory config agent_runtimes '*=cursor'
factory relaunch director --fresh --force   # when gate allows
factory relaunch boss --fresh --force
# workers: stop+respawn or wait for the next task cycle
```

Each Cursor agent: `place()` → `herdr agent start --kind cursor` → policy in `$XDG_STATE_HOME/factory/cursor/<name>/` (HQ and task worktrees; see T-1252 below) → `herdr agent prompt`.

Models: factory `MODELS` / `model_<role>` pass through to Cursor `--model`.

### Caveats

- **skilleval evaluator** stays Claude.
- **nsbox vs Cursor sandbox:** Claude **nsbox** (user/mount namespace around `claude`) is **not** wrapped around Cursor agents. For a Cursor role listed in `sandbox_roles`, spawn turns on Cursor **`--sandbox enabled`** and still runs **worker-guard** via hooks — that is separate from nsbox.
- **Worker GitHub credentials (M15876):** When the role is in config **`worker_gh_roles`**, an unsandboxed Cursor worker pane gets the same **`ghcred` shim env** as a Claude worker (`GIT_CONFIG_GLOBAL`, `GH_CONFIG_DIR`, `PATH` prefix, token file paths). This is normal spawn wiring, **not** a spawn-time refusal.
- **User MCP blinding:** Empty project `.cursor/mcp.json` alone does **not** hide **`~/.cursor/mcp.json`**, but spawn materializes a **`beforeMCPExecution`** hook (`failClosed: true`) that denies MCP for every Cursor factory role except **boss** (boss delegates to **worker-guard**, same posture as Claude without `--strict-mcp-config`). User/plugin MCP calls are blocked at the hook, not by config stripping alone.
- **flagless / claude_procs** audits ignore Cursor argv (expected).
- Auth: `agent login` or `CURSOR_API_KEY`.

### Kickoff that was never accepted (T-1599)

A Cursor pane can sit idle for hours because its kickoff prompt was typed but never submitted, or landed in the follow-ups drawer. The tick now catches this for every live Cursor agent, whatever its role (workers, leads, director, boss).

**Signal:** the agent's kickoff message in the factory DB is still unread. An agent that accepted its kickoff runs `factory inbox` as its first command, and that sets the read mark. No transcript or pane text is read.

| When (per pane) | What the tick does | Event |
|---|---|---|
| Kickoff unread for more than `cursor_kickoff_grace_s` (default 180 s) after the spawn | Types and submits the kickoff again, through the same function the spawn used (prompt, then Enter). Once per pane. | `kickoff-resent` |
| Still unread one grace period after that | Closes the pane and starts the agent fresh through the normal relaunch path (`factory relaunch --fresh`). The agent keeps its slot and gets a new session id and a new kickoff message. Once per agent. | `kickoff-relaunch` |
| The relaunched pane is still unread after its own resend and one more grace period, or the relaunch failed | One escalation to the agent's org director (to the boss when the agent is the director). It names the pane, the backend and the seconds idle. Nothing further is done automatically. | `kickoff-escalated` |

- The spawn gate is read before the relaunch, as for any relaunch. While it is closed the pane is left alone and the relaunch waits (`kickoff-relaunch-deferred`, at most one event an hour).
- At most one kickoff relaunch runs per tick.
- An agent that is being stopped (`factory stop`), has a queued `factory relaunch --when-idle`, or is still starting is skipped: the stop path or the relaunch path owns that pane.
- A pane the host reports as working is left alone: it accepted a prompt and may simply not have read its mail yet. So is a pane the host does not list.
- Claude agents are never checked.
- `factory config cursor_kickoff_grace_s 0` turns the check off. The key is a timing key, not an owner cap.
- Each event's detail holds `kick` (the kickoff message id), `idle_s`, `host` and `pane`. To count them: `grep -c '"kickoff-resent"' ~/factory/state/events.jsonl`.

## Adding a runtime

1. `_plan_<name>` in `scout/agent_runtime.py` → `SpawnPlan`.
2. Register in `KNOWN` / `ALLOWED`.
3. Teach `_spawn_start` / tick only if the launch mode is new.
4. Keep Claude settings as the deny-rule source; translate in the runtime.
5. Surface the new kind in web `RUNTIMES` + `runtime_badge` + CLI column.
6. Add tests; do not weaken Claude spawn tests.

## Cursor (first extension)

- Policy: `.cursor/hooks.json` → `cursor-guard-adapter.py` → `worker-guard.py`
- Launch: `herdr --kind cursor`
- Helpers: `factory cursor-pilot status|prepare|probe|run`
- Research: `docs/research/cursor-harness.md`
- Per-agent `.cursor/cli.json` under `$XDG_STATE_HOME/factory/cursor/<name>/` sets `attribution.attributeCommitsToAgent` and `attribution.attributePRsToAgent` to JSON `false` so the Cursor CLI does not add agent Co-authored-by trailers on commits or PRs. The worker-guard public-text rule is not loosened; those trailers and the Cursor agent attribution address remain refused on push (same as any other blocked public text).

## Safe claim wording

**OK:** “Hivemill can run the factory org on Cursor Agent via `agent_runtimes=*=cursor` (herdr `--kind cursor`), with Claude still the default and skilleval on Claude. The UI shows both runtimes.”

**Not OK yet:** “Cursor is bit-for-bit equivalent to Claude for skilleval / MCP strict / flagless audits.”

### Cursor policy dir outside every git work tree (T-1252)

Cursor CLI resolves the project to the git top level and loads `.cursor/hooks.json` from there. A policy dir inside `FACTORY_HOME` (a git work tree) was never read, so no guard hook ran. Policy now lives in `$XDG_STATE_HOME/factory/cursor/<name>/` (default `~/.local/state`, mode 0700), outside every git work tree.

- `policy_workspace()` raises `ValueError` naming `XDG_STATE_HOME` when that dir would sit inside `FACTORY_HOME` or any git work tree. `assert_policy_target_allowed()` refuses any target inside a git work tree or `FACTORY_HOME`.
- `cursor_hooks_ok()` (the flagless gate) is false for a workspace nested under a `.git` holder, so a pane still on the old `run/cursor/*` path is reported as flagless.
- Each Cursor pane gets `FACTORY_CURSOR_HOOK_LOG=<workspace>/hook-events.jsonl`. The adapter appends one line `{ts, agent, role, event, decision}` per hook call (no command text). Write errors never change the decision or exit code. An empty log for a live pane means Cursor did not run the hooks.
