# Agent runtimes

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
| `cursor` | Cursor Agent | `grok-4.7-high` | `composer-2.5` |

The skilleval `evaluator` always runs on Claude.

- **Goals belong to one org** (`goals.org`). `factory goal new --org cursor ...` (default: the creating boss's org) · `factory goal list --org cursor` · `factory goal org G-031 claude` moves a goal: new spawns use the new org, and live agents finish under the old one.
- **Every spawn inherits its goal's org**: runtime, model, and name prefix `cursor-` (`cursor-director`, `cursor-lead-g-031-a`, `cursor-coder-t-0900`). The default org is prefixed only with `org_prefix_default=1` (`claude-boss`); with `0` a single-org install keeps `boss`, `director`, `coder-t-0001`.
- **Routing**: `factory msg boss|director|owner|staff-engineer` resolves inside the message's org: the goal's org when `--goal`/`--task` is given, otherwise the sender's org.
- **Chain of command**: only bosses and directors message across orgs (e.g. `factory msg cursor-director "capacity" ...`). Leads and workers are refused (exit 3).
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

**Resolution (`org_runtime`):** On the default org, **empty** `agent_runtimes` resolves **every** role to Claude. `*=cursor` (or `all=cursor`) resolves every role in `scout/cursor_harness.CURSOR_ROLES` to `cursor`. The skilleval **evaluator always stays Claude**, even with `*=cursor`. A **non-default org** ignores `agent_runtimes` for runtime choice and uses `scout/orgs.py` instead — the **cursor org is `cursor` even when `agent_runtimes` is empty** on the default org.

Allow-lists: `scout/agent_runtime.ALLOWED` / `scout.cursor_harness.CURSOR_ROLES`.

## Pure Cursor factory

```bash
factory config agent_runtimes '*=cursor'
factory relaunch director --fresh --force   # when gate allows
factory relaunch boss --fresh --force
# workers: stop+respawn or wait for the next task cycle
```

Each Cursor agent: `place()` → `herdr agent start --kind cursor` → policy in `run/cursor/<name>/` (HQ) or the worktree → `herdr agent prompt`.

Models: factory `MODELS` / `model_<role>` pass through to Cursor `--model`.

### Caveats

- **skilleval evaluator** stays Claude.
- **nsbox vs Cursor sandbox:** Claude **nsbox** (user/mount namespace around `claude`) is **not** wrapped around Cursor agents. For a Cursor role listed in `sandbox_roles`, spawn turns on Cursor **`--sandbox enabled`** and still runs **worker-guard** via hooks — that is separate from nsbox.
- **Worker GitHub credentials (M15876):** When the role is in config **`worker_gh_roles`**, an unsandboxed Cursor worker pane gets the same **`ghcred` shim env** as a Claude worker (`GIT_CONFIG_GLOBAL`, `GH_CONFIG_DIR`, `PATH` prefix, token file paths). This is normal spawn wiring, **not** a spawn-time refusal.
- **User MCP blinding:** Materializing an empty project `.cursor/mcp.json` at spawn does **not** hide the owner's **`~/.cursor/mcp.json`**. User MCP is **not fully blinded** for Cursor the way Claude uses `--strict-mcp-config`. Status **NEEDS_WORK** — tracked as **T-0828**; do not document full MCP isolation as done.
- **flagless / claude_procs** audits ignore Cursor argv (expected).
- Auth: `agent login` or `CURSOR_API_KEY`.

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
- Per-agent `.cursor/cli.json` under `run/cursor/<name>/` sets `attribution.attributeCommitsToAgent` and `attribution.attributePRsToAgent` to JSON `false` so the Cursor CLI does not add agent Co-authored-by trailers on commits or PRs. The worker-guard public-text rule is not loosened; those trailers and the Cursor agent attribution address remain refused on push (same as any other blocked public text).

## Safe claim wording

**OK:** “Hivemill can run the factory org on Cursor Agent via `agent_runtimes=*=cursor` (herdr `--kind cursor`), with Claude still the default and skilleval on Claude. The UI shows both runtimes.”

**Not OK yet:** “Cursor is bit-for-bit equivalent to Claude for skilleval / MCP strict / flagless audits.”
