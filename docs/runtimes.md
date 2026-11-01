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

## Config

| Key | Example | Meaning |
|---|---|---|
| `agent_runtimes` | `pm=cursor` | Per-role runtime. Empty = all Claude. |
| `agent_runtimes` | `*=cursor` or `all=cursor` | Every Cursor-capable role (not `evaluator`). |
| `cursor_pilot_roles` | `pm,coder` | Legacy alias → `pm=cursor,coder=cursor`. Prefer `agent_runtimes`. |

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
- Claude **nsbox** is not used for Cursor; `sandbox_roles` → Cursor `--sandbox enabled` + worker-guard.
- **worker ghcred** works (same pane env as Claude).
- User `~/.cursor/mcp.json` is not fully blinded.
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

## Safe claim wording

**OK:** “Hivemill can run the factory org on Cursor Agent via `agent_runtimes=*=cursor` (herdr `--kind cursor`), with Claude still the default and skilleval on Claude. The UI shows both runtimes.”

**Not OK yet:** “Cursor is bit-for-bit equivalent to Claude for skilleval / MCP strict / flagless audits.”
