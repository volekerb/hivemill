# Spawn queue order

When the gate is closed a spawn is queued; each tick starts queued rows while the gate is open, in this order:

1. Role class (drain before build): director, lead, judge, reviewer, qa, then everything else (coder, pm, cleaner, staff-engineer).
2. Aged goal priority within the class: the goal priority (no goal counts as 2) minus 1 per full 30 minutes queued, floored at level 1: aging lifts a P2/P3 row to at most the level of a fresh P1, and a row queued 240 minutes or more goes first within its class (see `queue_order_key`). Aging never crosses a class, so an old P3 coder still loses to any reviewer.
3. `queued_at`, then agent name, for a stable order. A re-queue (gate closed, failed start, TTL recovery) of a row that is already `queued` or `starting` keeps its original `queued_at`, so it keeps aging; only a new row or one coming from a terminal status (done, stopped, lost, cancelled, exited) gets a fresh one.

## Judge gate (T-0391)

A judge is held in the queue, without a pane, while either condition holds. The tick skips a held judge and starts the rows behind it; judge-first ordering applies only to judges that are not held. `factory spawn judge --task T` is held the same way even when the gate is open. A judge with no task or no PR is never held.

- **PR not ready.** Ready means `gh pr view <pr> --json mergeStateStatus,statusCheckRollup` shows every check COMPLETED with conclusion SUCCESS, SKIPPED or NEUTRAL on the current head, and `mergeStateStatus` is not BEHIND or DIRTY. The answer is cached per PR in the config table (`judge_ci:<pr url>`), so gh is asked at most once per 2 minutes for a PR. A gh error counts as not ready; it is logged as a `judge-ci-check-failed` event at most once per hour and never stops the tick. A PR with no checks at all counts as ready. A BEHIND head passes the tick's first check so the spawn pre-check can refresh it; the ready check then runs again after the pre-check, and a head the pre-check just refreshed stays queued (for at least 2 minutes, then until its checks are green).
- **Judge cap.** `max_live_judges` (default 3; `0` = no cap) limits live plus starting judges. At the cap queued judges wait. A value that is not a non-negative integer is treated as 3. Set it with `factory config max_live_judges N` (director).

Related guards: a gate-closed re-queue by the tick never revives a row that `factory dequeue` cancelled, and a manual `factory spawn` of a row in `starting` exits 2 ("already starting").

`factory goal edit <G> --priority N` applies any given value, including 2; `goal new` without `--priority` gives 2.

## Lead cap and worker reserve (G-014, config `lead_parking=1`)

`gate()` called with `role="lead"` (only `spawn()` does this) adds two refusals after the usual checks:

- `N/max leads live (max_live_leads)`: live + starting lead rows reached `max_live_leads` (default 4).
- `H/C HQ panes; R slots reserved for workers (reserved_worker_slots)`: boss + director + lead rows plus this one would exceed the HQ cap `max(max_live_agents - reserved_worker_slots, 3)`, so the default 6/10 still admits 3 HQ panes.

A refused lead stays `queued`; the tick continues to the next queued row, so workers behind it still start. Queued leads start in `queue_order_key` order (lead role class, then aged goal priority). A relaunch of a lead that already holds its row is never capped. Workers and `role=None` callers see the unchanged gate. A non-integer setting falls back to its default. `lead_parking=0` turns both checks off. `gate()` stats gain `leads`, `leads_cap`, `hq`, `hq_cap`, `workers`.

## Task priority

`factory task update T --priority 1|2|3` (lead, director, owner). A queued row of that task orders by `min(task priority, goal priority)`; aging and the 240-minute cap apply on top. Unset keeps the goal priority.

## Settings render failure (T-0359)

`_spawn_start` renders the role settings first, before any worktree, sandbox agent dir, pane or `agents` row, so a template that does not render (invalid JSON, unknown `@FACTORY_` token, LIVE_HOME pin refusal) leaves nothing behind. A direct `factory spawn` exits 3 (`settings render failed for <role>: ...`). A queued row is not retried each tick: the tick cancels it once (`dequeued` event, `by=settings-render-preflight`) and sends one alert to the team's lead (director with no lead). Fix the template and spawn again.

## Goal token limit (G-015 SC3, F-74 L3)

A Claude-org goal with `token_limit` (`factory goal edit G --token-limit N`) and a cached `goal_token_tally` row with tally >= limit starts no new work: `spawn()` refuses a `coder` or `pm` before the gate and before any `agents` row. `lead`, `judge`, `reviewer`, `qa` and `cleaner` are never refused, so work already done still reaches main. A Cursor-org goal is never refused (Cursor has agent-hours, not tokens). A coder retry on a task already in progress is new work and is refused too. A relaunch of an agent that already ran (`factory relaunch`, `factory up`) is not new work and is not checked.

- Direct `factory spawn`: exit 3, the reason names the goal, the tally and the limit, and says to raise it (`factory goal edit <G> --token-limit N`, director or owner). One `goal-token-limit-refused` event on the task, no `agents` row.
- Queued row: the tick cancels it once (`dequeued` event, `by=goal-token-limit`) and sends one alert to the team's lead (director with no lead), like the settings render failure above. It is not retried each tick.
- Lead-less dispatch checks the same limit before it calls `spawn()`: one `goal-token-limit-refused` event and one alert per task, then the task is skipped each tick. Other callers (review autospawn, coder CI resume, `team new`) reach the check inside `spawn()` and treat the refusal like any other refused spawn.
- The check reads the `goals` and `goal_token_tally` rows only, never a log. No limit, no tally row or a non-Claude goal: allowed, as before. A missing tally fails open on purpose: no figure, no refusal. The tally can also read low (read errors, a missing log root, deleted logs), which makes the refusal come later, never earlier.

## Failed start of a queued row (T-1135)

A queued row whose start dies (`spawn()` exits non-zero, e.g. exit 3 for a missing agent binary) no longer ends the tick: the row goes back to `queued`, the failure is counted on the row (`spawn_args.spawn_failures`) and the tick goes on with the next row and the later phases. The first failure logs one `spawn-start-failed` event and sends one alert to the team's lead (director with no lead); failures 2 to 4 are silent and retried next tick. The 5th failure (`EVALUATOR_SPAWN_RETRY_MAX`, same cap as an evaluator) cancels the row (`dequeued` event, `by=spawn-start-failed`) and sends one more alert, so a lasting failure neither repeats every tick nor stays queued forever. Gate refusals are not failures: the row stays queued with no count, event or alert, as before. Evaluator rows keep their own handling.
