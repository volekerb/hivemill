---
name: token-spend-audit
description: Find out where Claude tokens go on this machine (cost by model, the most expensive agent sessions, oversized contexts, turns that do nothing, cache writes, Fable use) and what to change. Use when the user asks why Claude usage or cost is so high, why the weekly limit ran out, which agent is expensive, or wants a token or cost report.
argument-hint: "[--hours N]"
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/bin/hm-spend report *)
---

# Token spend audit

You are a thin guide over one read-only command: `${CLAUDE_PLUGIN_ROOT}/bin/hm-spend report`. It reads the Claude Code transcripts on this machine and changes nothing. Background and the numbers behind each finding: docs/token-discipline.md in the hivemill repo.

## Rules

- Run only `hm-spend report`. Do not read transcript files yourself; they are large, and reading them costs the tokens this audit is about.
- Costs are at API prices. On a subscription, say they show the share of the plan's limits, not a bill.
- Name sessions by their 8-character id and project folder. Do not guess which agent a session is; ask the user, or say how to match it (the id is in the agent's `--resume` or `--session-id` argument).
- Report numbers from the output only. Do not estimate savings the output does not show.

## Steps

1. Run `${CLAUDE_PLUGIN_ROOT}/bin/hm-spend report --hours 24` (or the hours the user gave). On exit 1 with "not installed", show the build line it printed and stop.
2. Tell the user, in plain words and in this order:
   - the total and the split by model;
   - the three most expensive sessions, each with its average context and its share of tiny turns;
   - each finding with its fix.
3. Map the findings to actions:
   - `fable`: move those agents to Sonnet, or Opus for judgement. Restart them; do not use `/model`.
   - `big-context`: apply context limits. Offer the `agent-context-limits` skill.
   - `chatter`: the waiting and no-ack rules in docs/token-discipline.md (one blocking wait command, no acks, read screens instead of asking).
   - `cache-writes`: fewer short-lived agents; mechanical steps such as rebases and CI waits become scripts.
   - `unpriced`: the totals are missing that model; say so.
4. If the user wants a check that can run on a schedule: `hm-spend report --check` exits 3 on any high finding. `--json` gives the data to a script.
