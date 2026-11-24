# Token discipline: what agents cost, and how to keep it down

An agent pays for its whole context on every turn. A reply of "ok" from an agent holding 600k tokens costs as much to read as a reply that writes a feature. In a team of agents, most of the spend therefore comes from context size, the number of turns that do nothing, and how often new agents start. It does not come from the code they write.

This page gives the measurements, the limits Hivemill sets, and the rules agents follow. The `hm-spend` command measures it all on any machine that runs Claude Code. Two skills apply it outside a factory: [`token-spend-audit`](../plugins/kit-spend/skills/token-spend-audit/SKILL.md) and [`agent-context-limits`](../plugins/kit-spend/skills/agent-context-limits/SKILL.md).

## What one day showed

Measured on 2026-11-17 on two machines: a factory box and a Mac running a 21-agent app team. Costs are at API prices; on a subscription they are the share of the plan's limits.

| Driver | Factory box | App team on the Mac |
|---|---|---|
| Spend in one day | $566 | about $700-800 |
| Average context per request | 90k | 356k; some sessions 650k-1.7M |
| Turns that wrote under 300 tokens (acks, status checks, waits) | 51% | 35% |
| Cache writes (start-up context of each new session) | 40% of cost, 607 sessions | 31% of cost |
| Model mix | Opus for judges, reviewers and pms | three agents on Fable, at twice the Opus price |
| Tool output read into the context, median per session | 10k | 154k (build logs, test runs, simulator) |

That one day used a week's subscription budget. The app team merged little code that day.

## The limits

Every agent except the boss starts with these Claude Code settings (`env` in its settings file):

| Setting | Value | Effect |
|---|---|---|
| `CLAUDE_CODE_DISABLE_1M_CONTEXT` | `1` | a 200k context window instead of 1M |
| `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE` | `60` | the conversation is summarized at about 120k |
| `BASH_MAX_OUTPUT_LENGTH` | `20000` | command output kept in the context is cut at 20,000 characters |
| `CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS` | `15000` | one file read returns at most 15k tokens; read a range for more |
| `MAX_MCP_OUTPUT_TOKENS` | `8000` | an MCP tool result is cut at 8k tokens |
| `CLAUDE_CODE_DISABLE_ADVISOR_TOOL` | `1` | no advisor calls: each one sends the whole transcript to a larger model |

The boss is the one agent that holds the whole picture, so it keeps the 1M window. It is summarized at 40%, about 400k, and allowed larger output (`BASH_MAX_OUTPUT_LENGTH` 40000, file reads 25000). Its advisor setting is left as it is.

In the factory these values are in `roles/settings/<role>.json.in` (boss: `boss.json.in`), and `go/internal/kit/spend` tests that they match. Outside a factory, `hm-spend limits --apply` writes the worker values into `~/.claude/settings.json` and the boss values into a separate file. The boss then starts with `claude --settings <that file>`, which outranks the user file. A setting applies when a session starts; a running agent keeps its old window until it restarts.

### Prompt cache lifetime

Claude Code 2.1.331 has two settings for how long a prompt cache entry lives. Each is `"5m"` or `"1h"`:

- `promptCacheTtl`: the main conversation.
- `subagentPromptCacheTtl`: subagents and helpers.

A cache write costs more than plain input: 1.25x the input price for a 5-minute entry, 2x for a 1-hour entry. Reads cost the same for both. A short session that uses its cache for minutes pays the 2x write price for nothing.

The Option B task and judge settings (`roles/hivemill/*.json.in`) set both to `5m`. Keep `1h` for a long-lived session that idles more than 5 minutes between turns: there a 5-minute entry expires before the next turn and is written again each time. Each rewrite costs 1.25x against a single 2x write for `1h`, so `1h` wins once a session has two or more idle gaps longer than 5 minutes within the hour.

## The rules

They are in `roles/_common.md` for factory agents. For any other team, put them where every agent reads them (its `CLAUDE.md` or role prompts).

1. **Wait with one blocking command, not repeated turns.** `gh pr checks N --watch --interval 60`, `hc wait <t> --timeout 1800`, or `until <check>; do sleep 60; done` with a timeout. A command that runs for 20 minutes is one request. Twenty "still waiting?" turns are twenty full context reads.
2. **Logs go to a file.** Show only the exit code and the failing lines, for example: `xcodebuild … > /tmp/build.log 2>&1; echo exit=$?; grep -E "error:|\*\* (BUILD|TEST)" /tmp/build.log | tail -30`. The same applies to test runners, simulators and installers.
3. **Ask for named fields.** Use `gh pr view N --json state,mergeable -q …`, not the whole JSON. Do not paste images or base64 into the conversation unless they are the point of the work.
4. **No acks.** Agents message each other at milestones (PR ready, blocked, done), never "got it" or "still working". To see what an agent is doing, read its screen (`hc read`, which costs no tokens). Do not ask it.
5. **Short sessions for workers.** A worker ends its session when its PR is ready, or when it passes about 200k. It writes a handoff file of about 10 lines, and the next session starts from that file. An agent with nothing to do ends its session; it does not wait idle to be pinged.
6. **Models.** Opus for judgement (the boss, a lead, a high-risk review), Sonnet for building, testing and routine review. Never use Fable for agent work. Change a model by restarting the agent, never with `/model` mid-session: the cache is per model, so the switch rewrites the whole context.
7. **Fewer, longer-lived spawns for small steps.** A rebase without conflicts, a CI wait or a status poll is a script or a tick job, not a new agent. Each new agent pays its start-up context (40-60k tokens) as a cache write before it does anything.

## Measuring

`hm-spend report` reads `~/.claude/projects/**/*.jsonl` for the last 24 hours (`--hours N`). It prints:
- the cost by model;
- the top sessions, with each one's average, peak and start-up context and its share of tiny turns;
- findings with a fix: `fable`, `big-context`, `chatter`, `cache-writes`, `unpriced`;
- a `By role:` table (role, sessions, requests, $, spawn $, polling $, work $, median start-up context).

The role comes from the session's first user message, the factory kickoff "You are `<agent>`, the factory <role>.", matched against the known roles (boss, director, lead, pm, coder, qa, reviewer, judge, cleaner, staff-engineer, researcher, scout, retro, evaluator); no match gives `other`. A subagent transcript (`<session>/subagents/agent-*.jsonl`) takes its parent session's role, or `other` when the parent is not in the scan.

Every request falls in exactly one cost bucket, and the three add up to the total:
- **spawn**: the cache-write cost of a session's first request, its start-up context;
- **polling**: every later request that wrote fewer than 300 output tokens (the same threshold as the `chatter` finding), at full cost;
- **work**: everything else.

`--merged-prs N` (integer, 0 or more) adds the dollars per merged PR, the total divided by N, and the same per role. Without it, or with 0, the text prints `merged PRs: not given` and no per-PR figure. The command reads no factory DB, so the caller passes N. In `--json` the fields are `by_role` (same fields as the table), `total.spawn_usd`, `total.polling_usd`, `total.work_usd`, `merged_prs` and `per_merged_pr_usd`.

`--json` gives the same data to a script. `--check` exits 3 when a high finding exists, so a scheduled job can alert on it.

Prices come from `go/internal/tokens/prices.go`, the same table as the factory's token view, `web/prices.py`. Fable 5.2 was missing from both until 2026-11-17, so the factory view showed its cost as "n/a".

Building it until the kit release ships it (ADR 0007): `cd go && go build -o ~/.local/share/hivemill/bin/hm-spend ./cmd/hm-spend` with Go 1.27, or cross-build with `GOOS=darwin GOARCH=arm64`. The command reads only transcripts and settings files: no factory DB, no network.
