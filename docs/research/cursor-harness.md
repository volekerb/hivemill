# Cursor CLI harness research (Hivemill ↔ cursor-agent)

**Date:** 2026-10-30  
**Scope:** Research only. No code, config, commit, push, or `factory` runs.  
**CLI under study:** Cursor Agent CLI (`agent`; legacy symlink `cursor-agent`). Preferred command in current docs is `agent`.

**Verdict: GO-for-a-pilot** — safety gating is *possible* via Cursor hooks, but Hivemill’s Claude-shaped spawn path, tool names, and `--settings` injection do not drop in. A narrow pilot (prefer **pm**) is warranted before any broader cutover.

## Pilot implementation status (2026-10-30)

Multi-runtime harness landed (`docs/runtimes.md`). Claude path unchanged when `agent_runtimes` is empty.

| Piece | Path |
|---|---|
| Runtime registry | `scout/agent_runtime.py` |
| Cursor policy + adapter | `scout/cursor_harness.py`, `roles/hooks/cursor-guard-adapter.py` |
| CLI | `factory cursor-pilot status\|prepare\|probe\|run` |
| Config | `agent_runtimes=pm=cursor` (legacy: `cursor_pilot_roles=pm`) |
| Tests | `tests/test_cursor_pilot.py` |

Operators: auth (`agent login` or `CURSOR_API_KEY`), config, probe, spawn, and org/runtime rules are in **`docs/runtimes.md`** (including `factory spawn` via `herdr agent start --kind cursor` on main since PR #242).

---

## 1. Hivemill Claude Code surface (as used today)

Spawn path: every role (including relaunch / resume / skilleval evaluator) goes through `_spawn_start` in `bin/factory_cli.py` (entry `bin/factory`). Agents are started as:

```text
herdr agent start <name> --kind claude --pane <pane> -- -- <claude_args...>
```

### 1.1 CLI flags passed at spawn

| Flag / arg | Where | Purpose |
|---|---|---|
| `--model <id>` | `_spawn_start` | Role model from `MODELS` / `role_model()` (`docs/model-routing.md`) |
| `--permission-mode auto` | `_spawn_start` | Non-interactive tool approval |
| `--settings <path>` | `_spawn_start` | Rendered `run/settings/<role>.json` from `roles/settings/<role>.json.in` |
| `--append-system-prompt-file <path>` | `_spawn_start` | `_common.md` + `roles/<role>.md` → `run/<name>.prompt.md` |
| `--add-dir <FACTORY_HOME>` | `_spawn_start` (not evaluator) | Extra readable root |
| `-n <name>` | `_spawn_start` | Session / pane display name |
| `--effort <level>` | `effort_args()` | Opus only (`opus_effort` config) |
| `--strict-mcp-config` + `--mcp-config '{"mcpServers":{}}'` | every role except boss | Strip user/project MCP (`docs/spawn-context.md`) |
| `--plugin-dir roles/plugins/<role>` | when dir exists | Per-role text skills (`docs/per-role-plugins.md`) |
| `--plugin-dir roles/plugins/goals/G-nnn` | coder + matching goal | Goal-scoped skills |
| `--resume <session>` + `--system-prompt-snapshot off` | resume / relaunch | Keep transcript; re-read prompt file |
| `--session-id <uuid>` | fresh spawn | Stable process identity for audits |

Kickoff text is sent after start via `herdr agent prompt` (not a Claude flag).

### 1.2 Settings templates (`roles/settings/*.json.in`)

Rendered by `render_settings()` with `@FACTORY_LIVE_HOME@`, `@FACTORY_HOME_DIR@`, `@FACTORY_USER_CONFIG@` (`docs/settings-templates.md`).

**Permissions**

- `permissions.deny` / `permissions.allow` — Claude Code prefix rules (`Bash(...)`, `Edit(...)`, `Write(...)`, `NotebookEdit(...)`, `Advisor`, `Workflow(...)`).
- Workers: dense denies on live-home DB/bin/roles/docs/web/…, PRIV verbs (`sudo`/`su`/`doas`/`pkexec`/`docker`), herdr unit verbs, force-push / main push, etc.
- **pm** additionally denies bare `Edit` / `Write` / `NotebookEdit` and `git commit|push|merge|rebase|reset` (planning-only write posture).
- HQ (boss/director/lead): lighter worker denies; PRIV still present.

**Env (settings `env`)**

| Var | Typical | Purpose |
|---|---|---|
| `ENABLE_CLAUDEAI_MCP_SERVERS=false` | non-boss | No claude.ai connectors |
| `CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1` | all roles | Advisor tool off (`docs/spawn-context.md`) |
| `HERDR_SOCKET_PATH=/proc/nonexistent/worker-no-herdr.sock` | workers | Socket isolation (guard layer 3) |
| `FACTORY_ROLE=<role>` | all | Identity |
| `CAVEMAN_DEFAULT_MODE=off` | workers | Plugin/default mode off |

Spawn also injects pane env: `FACTORY_AGENT`, `FACTORY_ROLE`, `FACTORY_HOME` (and sandbox / scrub / gh-cred extras when enabled).

**Hooks**

Every worker template wires Claude Code `hooks.PreToolUse` → `python3 <live>/roles/hooks/worker-guard.py --role <role>` on:

- `Bash`
- `Read|Grep` (terms / scrub paths where applicable)
- `Edit|Write|MultiEdit|NotebookEdit` (persist / repo-config / path denies)

HQ templates use `--hq` (PRIV + mutating systemd only). Boss also has `boss-inbox.py` on a separate matcher.

**Other settings keys**

- `attribution.pr`
- `statusLine` → Claude usage script (Claude-specific)

### 1.3 `worker-guard.py` (mandatory safety)

Protocol (Claude Code PreToolUse): JSON on stdin; **exit 0 = allow**; **exit 2 + stderr = block**; parse/internal errors → exit 2 (**fail closed**). Claude treating exit 1 as allow is why the hook never uses exit 1 for policy.

Inspects and can block:

- **Every Bash command** (parser + raw-text R1/R2/R3): herdr, claude, factory unit/broad-read, PRIV, kills, shared-checkout git, cleaner branch scope, coder push pin, identity scrub, etc. (`docs/worker-guard.md`).
- **Edit/Write/MultiEdit/NotebookEdit** paths (persist files, live-home trees, git config/hooks).
- **Read/Grep** of owner scrub-terms file for some roles.

Defense in depth also relies on settings `permissions.deny` and dead `HERDR_SOCKET_PATH` for workers.

### 1.4 Plugins / skills

- `--plugin-dir` only; plugins are text-only skills under `roles/plugins/<role>/…` (no hooks/MCP/scripts in plugins; enforced by tests).
- Goal plugins for coders under `roles/plugins/goals/G-nnn`.
- Anthropic built-in skills cannot be stripped per session (`docs/spawn-context.md`).

### 1.5 Resume / lifecycle coupling

- Session id recorded in DB; `factory relaunch` / tick resume use `--resume`.
- Flagless / herdr-auto-resume panes are audited and repaired (`--system-prompt-snapshot off` required on resume).
- Process detection and sandbox escape checks assume a **`claude` binary argv** (`claude_procs`, `_is_claude_cmd`).

### 1.6 Feature inventory (Claude Code used by Hivemill)

| Feature | Used? |
|---|---|
| Headless / pane-hosted interactive CLI | Yes (herdr pane + Claude TUI; not Claude `-p`) |
| `--model` | Yes |
| `--permission-mode` | Yes (`auto`) |
| `--settings` (per-spawn absolute path) | Yes (central) |
| permissions allow/deny (and ask via product) | Yes (allow/deny; mode auto) |
| PreToolUse command hooks | Yes (mandatory) |
| `--plugin-dir` | Yes |
| `--resume` / `--session-id` | Yes |
| `--append-system-prompt-file` | Yes |
| `--add-dir` | Yes |
| `--effort` | Yes (Opus) |
| MCP strict empty config | Yes (non-boss) |
| Env in settings | Yes |
| Skills via plugins | Yes |
| Advisor disable | Yes |
| statusLine | Yes (Claude-only script) |

---

## 2. What Cursor Agent CLI offers today (official docs)

Binary: `agent` (install also links `cursor-agent`). Docs treat CLI and desktop agent as the same security controls ([deployment patterns](https://cursor.com/docs/enterprise/deployment-patterns)).

### 2.1 Feature parity matrix

| Hivemill need | Cursor today | Notes |
|---|---|---|
| Headless / non-interactive | **Yes** — `-p` / `--print`; pair with `--force`/`--yolo` to apply edits; `--trust` for headless workspace trust | [Headless](https://cursor.com/docs/cli/headless), [Parameters](https://cursor.com/docs/cli/reference/parameters) |
| Interactive pane agent | **Yes** — default `agent` (no `-p`) | [Using CLI](https://cursor.com/docs/cli/using) |
| Model per run | **Yes** — `--model`; `agent models` / `--list-models` | Slugs account/plan-dependent; bracket params e.g. `claude-opus-4-8[effort=high]` on local `--help`. No separate Hivemill-style `--effort` flag required if encoded in model string. |
| Hook **before every shell** and **block** | **Yes** — `beforeShellExecution` and/or `preToolUse` matcher `Shell` | Exit `2` = deny. Default non-2 failures are **fail-open** unless `failClosed: true`. [Hooks](https://cursor.com/docs/hooks) |
| Hook **before file write** and **block** | **Partial / workable** — `preToolUse` matcher `Write` (and `Delete`) can `permission: "deny"`. **`afterFileEdit` cannot block** (runs after). No dedicated `beforeFileEdit` in the published event list. | [Hooks](https://cursor.com/docs/hooks) |
| Hook before file read | **Yes** — `beforeReadFile` / `preToolUse` `Read` | |
| Per-agent permission allow/deny | **Yes, but not via `--settings`** — global `~/.cursor/cli-config.json` or project `<ws>/.cursor/cli.json` (`Shell`/`Read`/`Write`/`WebFetch`/`Mcp`) | No documented per-run settings path flag. Deny beats allow. [Permissions](https://cursor.com/docs/cli/reference/permissions), [Configuration](https://cursor.com/docs/cli/reference/configuration) |
| `ask` permission list | **Limited** — CLI permissions doc is allow/deny; `beforeShellExecution` can return `ask`; `preToolUse` accepts `ask` but docs say **not enforced** today | |
| Approval / auto mode | **Yes** — `approvalMode`, `--force`/`--yolo`, `--auto-review`, `--sandbox enabled\|disabled` | Closest to `--permission-mode auto` is unrestricted/`--force` + deny list + hooks |
| Rules / system prompt | **Yes** — `.cursor/rules`, root `AGENTS.md` / `CLAUDE.md` | No documented `--append-system-prompt-file`. Role prompt would be written into rules/AGENTS at spawn. [Using CLI](https://cursor.com/docs/cli/using), [Rules](https://cursor.com/docs/rules) |
| Skills | **Yes** — `.cursor/skills/`, `.agents/skills/`, user dirs; also loads `.claude/skills/` for compatibility; `--plugin-dir`; headless can invoke `/skill` | [Skills](https://cursor.com/docs/skills), [CLI changelog](https://cursor.com/docs/cli/changelog) |
| Session resume | **Yes** — `--resume [id]`, `--continue`, `agent ls` / `agent resume`, `create-chat`; `persist` for detach | [Parameters](https://cursor.com/docs/cli/reference/parameters) |
| Parallel agents in git worktrees | **Yes** — `-w`/`--worktree`, `--worktree-base`, `--workspace`; product docs encourage multi-agent worktrees | Hivemill already manages its own worktrees under `~/factory/worktrees/`; Cursor’s `~/.cursor/worktrees/…` is optional, not required |
| MCP isolation | **Partial** — project/user `mcp.json`; `agent mcp disable`; `--approve-mcps`. **No documented `--strict-mcp-config` equivalent** | Unknown whether empty project mcp fully blinds user MCP in CLI |
| Claude Code hooks import | **Yes** — reads `.claude/settings.json` (+ local/user) when third-party imports enabled; maps Claude hook names; CLI changelog: Claude hooks merged; Claude-format responses accepted | [Third-party hooks](https://cursor.com/docs/reference/third-party-hooks), [CLI changelog](https://cursor.com/docs/cli/changelog) |
| `--settings <arbitrary path>` | **No (documented)** | Must place config at known paths (`.cursor/…` or `.claude/settings.json` in workspace) |
| `--permission-mode` | **No** | Use approvalMode / `--force` / permissions |
| Advisor / `CLAUDE_CODE_*` env | **N/A / unknown** | Claude-specific |
| statusLine Claude script | **Different** | Cursor has its own statusline skill/config |

### 2.2 Tool naming gap (blocker for raw reuse)

| Claude Code (Hivemill) | Cursor Agent |
|---|---|
| `Bash` | `Shell` |
| `Edit` / `MultiEdit` / `NotebookEdit` | primarily `Write` (and `Delete`) |
| `Read` / `Grep` | `Read` / `Grep` (same names) |

Hivemill settings matchers use `Bash` and `Edit|Write|…`. Cursor third-party docs example matchers use `Shell`. `worker-guard.py` only runs the command parser when `tool_name == "Bash"` and only path-guards Claude edit tool names. **Unadapted, the guard would no-op on Cursor Shell/Write traffic.**

`beforeShellExecution` input shape is also different (`{command, cwd, sandbox}` top-level, JSON **out** with `permission`), vs Claude PreToolUse (`tool_name` + `tool_input.command`, exit code semantics). An adapter (or dual-protocol guard) is required either way.

### 2.3 Fail-open vs fail-closed

| | Claude Code (Hivemill assumption) | Cursor |
|---|---|---|
| Policy block | exit 2 | exit 2 or `permission: "deny"` |
| Hook crash / bad exit | Claude exit 1 → **allow** (Hivemill avoids exit 1) | Non-2 exit → **allow by default** |
| Hardening | N/A | Set **`failClosed: true`** on security hooks |

For a mandatory guard, Cursor hooks **must** use `failClosed: true` (and preferably emit valid deny JSON, not only stderr).

---

## 3. Can Cursor enforce `worker-guard.py` on every shell and file write?

### Short answer

**Shell: yes, if wired correctly.**  
**File writes via the Write tool: yes via `preToolUse` (not via `afterFileEdit`).**  
**File writes via shell redirects (`>`, `tee`, etc.): only if the shell hook runs** (same as today under Claude Bash PreToolUse).  
**Drop-in of current settings + unmodified guard: no.**

### Exact enforcement path

1. **Preferred Cursor-native wiring** (per worker workspace / worktree at spawn):
   - Write `<cwd>/.cursor/hooks.json` with:
     - `beforeShellExecution` → wrapper that translates Cursor stdin → Claude-shaped payload (or calls a dual-protocol guard), `failClosed: true`
     - `preToolUse` matchers `Write|Delete|Read|Grep|Shell` → same guard, `failClosed: true`
   - And/or project `.cursor/cli.json` deny lists translated from `roles/settings/<role>.json.in` (`Shell(…)`, `Write(…)`).
2. **Claude-compat path:** write hooks into `<cwd>/.claude/settings.json` (Cursor merges these when third-party imports are on). Still need matcher/`tool_name` remaps (`Bash`→`Shell`, edit tools→`Write`). **Unknown:** whether CLI headless always honors the IDE “Third-Party Imports” toggle the same way; enterprise doc says hooks work in CLI, but the settings UI gate is desktop-oriented — treat CLI enablement as **verify-in-pilot**.
3. **Permissions layer:** keep path/command denies in `.cursor/cli.json` as defense in depth (prefix rules remain weaker than the parser; same reason Hivemind added the hook after M86).

### What Cursor cannot do (documented gaps)

- No `--settings /abs/path` per spawn like Claude Code — config must be **materialized into the workspace** (or user global hooks, which is wrong for multi-role isolation).
- No documented hook that blocks **after** seeing a Write payload but before disk **except** `preToolUse` on `Write` (must confirm Write `tool_input` path field name in live CLI; docs show generic `tool_input` object — **field names for Write paths: verify in pilot**).
- `afterFileEdit` is audit/format only.
- Global `~/.cursor/hooks.json` would apply to every Cursor agent on the machine — unsafe for mixed HQ/worker roles.

### Mandatory adapter work (even for a pilot)

- Translate `Shell` ↔ `Bash` and Write path fields into what `worker-guard.py` expects, **or** teach the guard both schemas.
- Always set `failClosed: true`.
- Map role via argv `--role` still works if the hook command keeps it.
- Keep rendering absolute live-home paths for any permission deny lists you port.

---

## 4. Plan usage, limits, and terms (~10 parallel agents)

### Usage / limits (docs)

- CLI usage draws from the **same plan pools** as the editor (not a separate CLI quota).
- **The X Subscription:** one **weekly** pool for Cursor models (Grok / Composer family), shared across editor Agent, CLI, Cloud Agents, Grok Bot, apps. Third-party models: API list price from prepaid credit (Premium+).
- **Legacy plans:** two **monthly** pools (Cursor Models / Other Models) + optional on-demand.
- Exhaustion: wait for reset, use prepaid/on-demand, or upgrade — docs say quality is not silently downgraded.
- Check: dashboard / editor usage; CLI `/usage` mentioned in changelog.
- Published numeric RPM limits in [API overview](https://cursor.com/docs/api) target Admin/Analytics/Cloud Agents HTTP APIs — **not** a published “max concurrent local CLI agents” number.
- Team admins can **disable headless mode** org-wide (CLI changelog) — relevant if factory uses `-p`.

Sources: [Usage and limits](https://cursor.com/help/models-and-usage/usage-limits), [Models & pricing](https://cursor.com/docs/models-and-pricing), [API](https://cursor.com/docs/api), [CLI changelog](https://cursor.com/docs/cli/changelog).

**Practical implication for ~10 parallel Hivemill workers:** allowed by product features, but will burn the shared weekly/monthly pool quickly on third-party/frontier models. Cost forecasting is plan-specific — **unknown without the target account’s plan meter**.

### Terms / AUP (~10 parallel automated agents)

- [Terms of Service](https://cursor.com/terms-of-service) (updated 2026-09-03): limited license; restrictions on scraping, competitive training, probing, etc.; auto-code execution risk acknowledgment (§1.7). **No numeric cap on parallel local agents.**
- Product docs explicitly support headless scripts, CI, and multi-agent worktrees ([Headless](https://cursor.com/docs/cli/headless), [GitHub Actions](https://cursor.com/docs/cli/github-actions), [Worktrees](https://cursor.com/docs/configuration/worktrees)).
- [Acceptable Use Policy](https://cursor.com/acceptable-use-policy) (updated 2026-08-11) prohibits circumventing rate limits / metering, and includes a broad line against “Accessing the Service through automated or non-human means, whether through a bot, script, or otherwise.” That line **sits in tension** with official headless/CI documentation.

**Reading (not legal advice):** ~10 parallel **local** CLI agents for development automation is a **documented product pattern** and is **not forbidden by a published numeric ToS limit**. You must stay within plan metering and not abuse/circumvent limits. Whether AUP “non-human means” could be read against factory automation is **ambiguous**; for a binding answer, ask Cursor support/legal. This document does not resolve that ambiguity.

---

## 5. Recommendation

### **GO-for-a-pilot**

Not **GO** for full multi-role cutover: spawn is Claude/herdr-shaped end-to-end (`--kind claude`, argv audits, settings templates, Bash tool names, prompt-file flags).  
Not **NO-GO**: Cursor can headless-run, choose models, resume, load plugins/skills, run many worktree agents, and — with adapters — **block shell and Write tools** before execution.

### Pilot role

**Prefer `pm`.**

| Why pm | Why not first coder |
|---|---|
| Settings already deny all `Edit`/`Write`/`NotebookEdit` and mutating git | Coder must Write + constrained `git push`; higher blast radius |
| Still exercises factory CLI + Bash/Shell guard (herdr/PRIV/broad-read) | Docs-only coder would need a new deny profile anyway |
| Opus routing; planning workload fits Ask/Plan modes if useful | Docs-only coder is a fine **second** pilot if pm guard wiring works |

Alternate: a **docs-only coder** (synthetic permission profile: Write limited to `docs/**` + no push) if the goal is to test Write-path `preToolUse` blocking — do that as pilot phase 2.

Do **not** pilot boss/director/lead first (HQ herdr coupling, interactive owner workflows).

### What landed vs what still needs work

| Item | Status |
|---|---|
| Adapter Shell/Write → worker-guard | **WORKS** (`cursor-pilot probe`) |
| Materialize `.cursor/hooks.json` (`failClosed`), `cli.json`, role rule, empty `mcp.json` | **WORKS** (`cursor-pilot prepare`) |
| Permission translation `Bash→Shell`, `Edit→Write(**)` | **WORKS** |
| Headless argv builder (`agent -p --force --trust`) | **WORKS** |
| `factory spawn` for Cursor-capable roles | **WORKS** on main (#242) — `place()` → `herdr agent start --kind cursor` → `herdr agent prompt` (see `docs/runtimes.md`) |
| Session resume / `herdr agent prompt` / tick list | **NEEDS_WORK** |
| MCP user-blind parity with `--strict-mcp-config` | **NEEDS_WORK** |
| Process audits for `cursor-agent` argv | **NEEDS_WORK** |
| Live confirm hooks fire inside `agent -p` for this account | **UNKNOWN** — run `cursor-pilot run` and watch a blocked herdr attempt |
| Plugin `.claude-plugin` packs via `--plugin-dir` | **UNKNOWN** |

### Rough effort

| Slice | Days (order of magnitude) |
|---|---|
| Hook/permissions adapter + failClosed wiring + pm materialize | 2–3 |
| factory spawn/relaunch/session plumbing + herdr kind/command | 2–3 |
| Guard dual-protocol tests + live probe (block herdr/sudo/Write) | 1–2 |
| Model mapping, usage metering, ops docs | 1 |
| **Pilot total** | **~5–8 days** |
| Broader roles (coder push pin, sandbox, MCP strict, process audits) | +2–4 weeks (unknowns dominate) |

### Risks

1. **Guard false sense of safety** if matchers stay on `Bash`/`Edit` while Cursor emits `Shell`/`Write`.
2. **Fail-open hooks** if `failClosed` omitted — worse than Claude’s documented exit-1 quirk because crashes allow by default.
3. **Policy file placement** — workers share git objects; writing `.cursor/` into a task worktree must not leak into shared checkout `main` (same class of problem as shared `.git/config`).
4. **herdr / process audits** assume `claude` argv — cursor-agent panes look “flagless” or foreign until detectors are updated.
5. **Usage burn** on 10× parallel frontier models; weekly pool exhaustion mid-goal.
6. **AUP ambiguity** on automation (see §4); admin may disable headless.
7. **MCP / third-party import** isolation less proven than Claude `--strict-mcp-config`.
8. **Write tool schema drift** — path field names must be confirmed live.
9. **Skills/plugins layout** — Claude `--plugin-dir` exists on Cursor too, but plugin manifest (`.claude-plugin` vs Cursor plugin) may need packaging tweaks — **verify in pilot**.

---

## 6. Doc URLs relied on

| Topic | URL |
|---|---|
| CLI overview | https://cursor.com/docs/cli/overview |
| Headless / print mode | https://cursor.com/docs/cli/headless |
| Using CLI (rules, worktrees, resume, non-interactive) | https://cursor.com/docs/cli/using |
| Parameters / flags | https://cursor.com/docs/cli/reference/parameters |
| Configuration | https://cursor.com/docs/cli/reference/configuration |
| Permissions | https://cursor.com/docs/cli/reference/permissions |
| Hooks | https://cursor.com/docs/hooks |
| Third-party / Claude Code hooks | https://cursor.com/docs/reference/third-party-hooks |
| Skills | https://cursor.com/docs/skills |
| Rules | https://cursor.com/docs/rules |
| Worktrees | https://cursor.com/docs/configuration/worktrees |
| CLI changelog | https://cursor.com/docs/cli/changelog |
| Installation | https://cursor.com/docs/cli/installation |
| Enterprise deployment (CLI = same controls) | https://cursor.com/docs/enterprise/deployment-patterns |
| Models & pricing | https://cursor.com/docs/models-and-pricing |
| Usage and limits | https://cursor.com/help/models-and-usage/usage-limits |
| API rate limits (HTTP APIs) | https://cursor.com/docs/api |
| Terms of Service | https://cursor.com/terms-of-service |
| Acceptable Use Policy | https://cursor.com/acceptable-use-policy |
| GitHub Actions cookbook | https://cursor.com/docs/cli/github-actions |

### Hivemill sources (in-repo)

- `bin/factory` → `bin/factory_cli.py` (`_spawn_start`, `MODELS`, `effort_args`, resume flags)
- `roles/settings/*.json.in`
- `roles/hooks/worker-guard.py`
- `roles/plugins/` + `docs/per-role-plugins.md`
- `docs/spawn-context.md`, `docs/model-routing.md`, `docs/worker-guard.md`, `docs/settings-templates.md`

### Explicit unknowns (do not guess further)

- Whether Cursor CLI always loads `.claude/settings.json` hooks without the desktop “Third-Party Imports” toggle.
- Whether Claude `permissions.*` inside `.claude/settings.json` are honored by CLI, or only hooks are imported.
- Exact Write `tool_input` path key(s) on current CLI builds.
- Account-specific weekly pool size and whether ~10 Opus-class agents are affordable.
- Binding interpretation of AUP “automated or non-human means” vs documented headless CLI.
- herdr support for a non-`claude` agent kind without a shell wrapper.
- Full equivalence of Cursor `--plugin-dir` with Hivemill’s `.claude-plugin` skill packs.

---

*End of research note.*
