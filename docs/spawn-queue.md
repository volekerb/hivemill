# Spawn queue order

When the gate is closed a spawn is queued; each tick starts queued rows while the gate is open, in this order:

1. Role class (drain before build): director, lead, judge, reviewer, qa, then everything else (coder, pm, cleaner, staff-engineer).
2. Aged goal priority within the class: the goal priority (no goal counts as 2) minus 1 per full 30 minutes queued, floored at 0. Aging never crosses a class, so an old P3 coder still loses to any reviewer.
3. `queued_at`, then agent name, for a stable order.

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
