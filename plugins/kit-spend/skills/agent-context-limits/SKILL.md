---
name: agent-context-limits
description: Cap the context of worker agents on this machine (200k window, early compaction, short command and file output, no advisor calls) while one boss agent keeps the full 1M window, and add the token rules (blocking waits, logs to files, no acks) to the team's instructions. Use when the user wants agents to use less context or fewer tokens, wants worker agents limited and the boss not, or after a token-spend-audit finding of big-context or chatter.
argument-hint: "[--settings FILE] [--boss-settings FILE]"
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/bin/hm-spend limits *)
---

# Agent context limits

You are a thin guide over `${CLAUDE_PLUGIN_ROOT}/bin/hm-spend limits`. It writes only the `env` keys listed in docs/token-discipline.md (hivemill repo) into two Claude Code settings files and keeps every other setting. Before its first write to a file, it keeps a copy as `<file>.bak-hm-spend`.

## Rules

- Always show the plan (no `--apply`) first, and apply only after the user says yes in this conversation.
- Never edit settings files by hand. Never touch keys other than the ones the command plans.
- The worker limits apply to EVERY Claude Code session of this OS user that starts after the change, the user's own included. Say this before applying. If the user's own interactive sessions must keep the 1M window, they start them like the boss, with `claude --settings <boss file>`.
- A running session keeps its old settings until it restarts. Say so; do not restart anyone's agents yourself.
- In the Hivemill factory, these limits already come from roles/settings. Do not run this skill on a factory box unless the user asks for it.

## Steps

1. Run `${CLAUDE_PLUGIN_ROOT}/bin/hm-spend limits` (add `--settings` or `--boss-settings` only if the user named other files). Show what it would set: the worker file (default `~/.claude/settings.json`) and the boss file (default `~/.claude/boss-settings.json`).
2. Explain in plain words: workers get a 200k window, summarized at about 120k, with command output cut at 20,000 characters, file reads at 15k tokens and no advisor calls. The boss keeps 1M, summarized at about 400k.
3. After an explicit yes, run the same command with `--apply`. Then give the one line the boss must start with: `claude --settings <boss file>`, plus the agent's usual flags.
4. Offer to add the team rules to the file every agent reads (usually the project's CLAUDE.md). Show the text first, and append it only after a yes:

   ```
   ## Token rules (every agent)
   - Every turn re-reads your whole context. Wait with ONE blocking command (`gh pr checks N --watch`, `until …; do sleep 60; done`), never by repeated turns.
   - Long output goes to a file; show the exit code and the failing lines only. Ask gh/APIs for named fields.
   - No ack or status messages between agents; message at milestones only. To see an agent's state, read its screen.
   - Workers end their session at PR ready or about 200k context, after writing a ~10-line handoff file.
   - Opus for judgement, Sonnet for building. Never Fable for agent work. Change models by restarting, never /model.
   ```

5. Check with `hm-spend limits --check` (exit 0 = in place). The next day, offer the `token-spend-audit` skill to compare.
