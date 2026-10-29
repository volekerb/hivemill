# refresh_lane (G-022, phase 1)

Config key `refresh_lane`: `off` (default) or `on`. Rollback: `factory config refresh_lane off`.

Why: every BEHIND PR whose judge starts is refreshed (merge of main pushed) and then waits for CI, and each merge
makes the others BEHIND again, so N ready PRs cost N refreshes and N CI runs per merge (the refresh convoy).

With `on`, the tick's judge dequeue lets only the **queue head** through: the oldest queued judge (queue order) whose
PR is ready except for being BEHIND. Its judge starts, and the spawn pre-check refreshes it as before. Every other
BEHIND judge stays queued, with no refresh and no CI run, and shows `T-x lane: position N` (event `lane-hold` on the task,
written when the position changes; the head gets `lane-head`).

- The head owns the lane (config key `refresh_lane_owner`) while its task is open and it has a queued, starting or live
  judge. When it merges or its judge ends, the next BEHIND head takes over on the next tick.
- A PR that is not BEHIND is never held. Behind-only is not a failure: the PR simply waits for its turn.
- Nothing is held when `stale_policy=behind_ok` applies (strict=false on GitHub): no refresh happens then.
- Only the tick's dequeue is gated; `factory judge --merge` and its gate are unchanged. No security change, no verdict
  or merge rule is touched.
