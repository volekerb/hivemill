# What a spawned agent carries

## The advisor tool is off for every role

Claude Code can expose an `advisor` tool (tool definition `{"type":"advisor_20260301","name":"advisor","model":"claude-opus-5-5"}`).
It takes no parameters and forwards the whole transcript to a second model on every call, so it is a hidden cost. Decision: off for
every factory agent, boss included (director M3599/M3625; G-011 may trial it later with a cost measurement).

- One flip point per role file in `roles/settings/<role>.json.in`: `env.CLAUDE_CODE_DISABLE_ADVISOR_TOOL = "1"` (the documented Claude Code
  switch, found in the 2.1.310 binary; it removes the tool from the session) plus `"advisor"` in `permissions.deny` as a second layer.
  To trial it, remove both lines from the role file under test.
- `tests/test_advisor_deny.py` fails if any settings file lacks either line.
- Check a live agent: its tool list has no `advisor`.

### Audit of past use

    python3 -m web.advisor_audit [--projects DIR] [--json]

Read-only. Scans `~/.claude/projects/**/*.jsonl` for real `advisor` calls (`tool_use` / `server_tool_use` blocks, not tool definitions),
for assistant messages whose model id contains `fable`, and for the advisor model named in the session (any model other than
`claude-opus-5-5` or `claude-sonnet-5-5` is flagged). Per session it prints the agent (the project directory name, `G-nnn-T-nnnn` for a
worktree), the session id, counts and timestamps. Message content is never printed.

## Context sources and how each is removed (G-014 T-0186)

Every session pays a fixed prefix of tool schemas and skill lists. Factory agents other than **boss** start with only the factory's
own tools and their role plugin dir. Boss is excluded on purpose (director decision M2819): the owner works in the boss pane and uses
the claude.ai connectors there. All spawns, including `relaunch`, resume and the skilleval evaluator, go through `_spawn_start` in
`bin/factory`, so there is one copy of the flag list.

| Source | Removed by | Where | Doc it relies on |
|---|---|---|---|
| User/project MCP servers (e.g. `unifi` in `~/.claude.json`) | `--strict-mcp-config --mcp-config '{"mcpServers":{}}'` | `claude_args` in `_spawn_start`, every role but boss | Claude Code CLI reference: `--mcp-config`, `--strict-mcp-config` ("only use MCP servers from `--mcp-config`") |
| claude.ai account connectors (Gmail, Google Calendar, Google Drive, Claude Docs, ...) | env `ENABLE_CLAUDEAI_MCP_SERVERS=false` | `env` of `roles/settings/<role>.json.in`, every role but boss | Claude Code MCP docs, "Use MCP servers from Claude.ai" (`ENABLE_CLAUDEAI_MCP_SERVERS=false`) |
| Account/user plugins (`design`, `engineering`, `product-management`, `caveman`, ...) and the MCP servers they bundle | **not removed yet, see T-0305** (the ids were wrong in the first attempt; this PR adds no `enabledPlugins`) | n/a | `code.claude.com/docs/en/plugins/loading` |
| Role plugin dir `roles/plugins/<role>` | kept: `--plugin-dir` | `_spawn_start` | `claude --help`: `--plugin-dir` |
| Goal plugin dir `roles/plugins/goals/G-nnn` (coders only) | kept: `--plugin-dir` | `_spawn_start` | same |
| `anthropic-skills:*` (built-in Anthropic skills) and the built-in `init`, `loop`, `schedule`, ... skills | **cannot be removed per session** by any mechanism found; they are part of the client | n/a | n/a |

Notes:
- `syncClaudeAiPlugins` / `syncClaudeAiSkills` are machine-wide (they would also hit boss and trash synced plugins and skills) and must NOT be used.
- `disableClaudeAiConnectors` is the documented settings-key alternative to `ENABLE_CLAUDEAI_MCP_SERVERS=false`; the env is kept (director-verified, M5114).
- The doc lines above were not checked by running `claude --help` (workers may not start Claude). The lead's real-agent start from the
  PR branch (L-038) is the check that the settings keys raise no Settings Warning and that the connectors are gone.
- `tests/test_spawn_strict_mcp.py` asserts the flags per role (boss without them), on relaunch/resume too, and the env key.
