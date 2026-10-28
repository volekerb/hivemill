# Adoption metrics (`web/adoption.py`)

Read-only measurement of how many tokens the factory spends per merged PR and how agents use Claude Code
features (Workflow, background CI waits, Explore). Built for G-011 SC4/SC5 so the baseline and the
after-measurement run the same reviewed code.

```
python3 -m web.adoption report [--since YYYY-MM-DD] [--until YYYY-MM-DD] [--json]
```

Run it from the repo root. `FACTORY_HOME` picks the database and `FACTORY_CLAUDE_PROJECTS` the
transcripts directory (default `~/.claude/projects`), the same as `python3 -m web.context`. Days are local days.
Exit 0 on success, 2 when the database is missing.

## Read-only

The database is opened with `web.context.connect_ro` (`mode=ro`, `query_only`). Transcripts are only read. `gh` is
only called as `gh pr view <url> --json files,additions,deletions`. Message text and tool output are never kept or
printed; only usage numbers, tool names, a few tool inputs and result sizes are.

## What is measured

A task is **merged** when its latest judge verdict is `merge` and that verdict's local day is inside the window.
Sessions are attributed to tasks exactly as `python3 -m web.context per-pr` does: `agents.session_id`, then the
tick's session-change events, then, for a session no agent row claims (an earlier attempt, a respawned reviewer or
QA: the row was replaced), the task named by the worktree path in the transcript's `cwd`. The role of such a
session comes from the transcript's own `agent-name` record (looked up in the agents table, else a role word in the
name; anything else is `other`). HQ sessions never count. Sessions with neither a row nor a task worktree are
skipped and counted as `unattributed_sessions`. `tokens.total` of a task equals `per-pr`'s `input`.

1. **tokens** per merged PR: `input + cache_read + cache_write` (5m and 1h together), summed over all
   requests of every agent session attributed to the task, split by role (`coder`, `reviewer`, `qa`, `judge`,
   `other`). Same formula as `web.context.context_of`, the G-014 SC5 basis. Requests are not window-filtered:
   the window picks the merged tasks. Only main transcripts count (sidechain lines and
   `<session>/subagents/*.jsonl` are excluded, as in `web.context`); `total` sums the known roles and
   `complete` says whether coder, reviewer, qa and judge were all known.
2. **reviewer**: `tokens` (the reviewer role above); `workflow_calls` (`Workflow` tool calls in the reviewer's
   main transcript); `workflow_subagent_tokens` (same formula over the `<session>/subagents/*.jsonl` files whose
   sibling `.meta.json` `toolUseId` equals the id of a `Workflow` call; sub-agents of `Agent`/`Task` calls and
   files without a matching id are not counted; `0` with no Workflow call, `"not logged"` when there were
   Workflow calls but no matching sub-agent file); `scope_gate`: `gated` is true when the diff touches
   `roles/hooks/`, `roles/settings/`, `ci/` or `.github/`, or has more than 500 changed lines
   (additions + deletions; exactly 500 is not gated; `bin/factory` alone no longer gates, lead decision T-0242) with the matching `reasons`. A `gh` failure gives `"unknown"`.
3. **coder CI wait**: `ci_checks_calls` = Bash calls whose command contains `gh pr checks`;
   `ci_checks_background` = those with `run_in_background: true`; `turns_pr_create_to_done` = distinct assistant
   turns strictly between the first `gh pr create` call and the first later `factory done` call, counted across
   all of the task's coder sessions in time order (the PR may be created in attempt 1 and `factory done` run in a
   resumed or respawned session). It is `"unknown"`, never 0, when no coder transcript contains a `gh pr create`
   (PR opened elsewhere, transcript pruned) or none contains a `factory done` after it (the task was closed by
   someone else, or the coder is still running).
4. **coder search**: `explore_calls` = `Agent` (or `Task`) calls with `subagent_type` `Explore`;
   `search_results` / `search_result_chars` = results of the coder's lookups in its main transcript and their text
   length in characters: `Read`, `Grep` and `Glob` calls, plus Bash calls that read files with `grep`, `egrep`,
   `fgrep`, `rg`, `find`, `ls`, `cat`, `head`, `tail`, `sed` (not `sed -i`), `git grep` or `git ls-files`. Only the
   first command of a pipeline and of each `&&`/`;` step counts: `... | tail`, `cat > f <<EOF` and `gh`/`git
   status` are not searches. Why Bash: in the live transcripts coders almost never use `Read` (93 calls over 153
   coder sessions) and never `Grep`/`Glob`; they search through Bash (`tail`, `grep`, `head`, `sed`, `cat` are the
   most frequent commands). The first version counted only the three tools and so reported 0.
5. **cost_sessions**: per cleaner and scout session, USD from `web/prices.py` over requests in the window
   (sidechain and sub-agent lines included, they cost money). A model without a price gives `"unknown"`.

`medians` repeats items 1-4 as medians over the merged tasks of the window, skipping `"unknown"` values (a
median of nothing is `"unknown"`).

## Blind spots

- Missing data is `"unknown"`, never 0: a session without usage lines, a role with no attributed agent, a failed
  `gh`. A role summed over several agents (retries) is known if any of its sessions is.
- Transcripts are pruned after about 30 days, so tasks merged earlier have `"unknown"` tokens.
- Sub-agent usage (Explore, Workflow) is not part of tokens per PR, consistent with `web.context`; only the
  reviewer's `workflow_subagent_tokens` shows it.
- Workflow sub-agents are often not logged under `subagents/` at all; then the value is `"not logged"`, not 0.
- Search result sizes count only the inline text; a result spilled to a `<persisted-output>` file counts the
  preview characters only.
- Turn counting follows file order; a resumed session that rewrites old lines is deduplicated by request id.
- The scope gate uses the PR's final diff, which can differ from the diff the reviewer saw.
- Tasks whose agent rows were reused or never recorded a `session_id` are invisible to attribution.
