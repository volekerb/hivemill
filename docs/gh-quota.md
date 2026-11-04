# GitHub GraphQL quota: tick probe cache and low-quota guard

`gh pr view`, `gh pr checks`, `gh pr list` and `gh repo view` use the GitHub GraphQL API (5000 points per hour, shared by everything
that uses the factory's token). On 2026-11-04 the quota ran out between 08:09 and 08:21; every `gh pr view` then failed for all agents.
This page describes what the tick does to spend less of it. REST calls (`gh api repos/...`, `gh pr diff`, `gh run list`) have their own quota.

## Settings

| key | default | meaning |
|---|---|---|
| `pr_view_ttl_s` | `120` | the tick's optional PR probes reuse one `gh pr view` per PR for this many seconds, as long as origin's head sha is the one that was probed. `0` = off (every probe calls gh, as before). |
| `gh_quota_min` | `300` | GraphQL points left below which optional probes are skipped and one alert is raised. `0` = never read the quota. |

Rollback: `factory config pr_view_ttl_s 0` and `factory config gh_quota_min 0`.

## Probe cache

Callers: `tick_ci_wake` (CI-wake mail to a parked lead), `tick_red_pr_owner` (red PR alert) and the review autospawn passes (first review and
new-head re-run). They all read the same fields, so one `gh pr view <url> --json number,state,headRefOid,mergeStateStatus,statusCheckRollup`
serves all of them. The answer is kept in config row `prview:<pr_url>` (`{at, head, pr}`); it is used when it is younger than `pr_view_ttl_s` and
its head equals the head `git ls-remote` reports now (one ls-remote per tick, no GraphQL). A new push is therefore always a fresh call. A failed gh
read is never stored, so the next tick asks again. Rows of PRs that are no longer open are removed by the `judge_ci_prune` tick step.

Not cached, not changed: every read that decides a merge or a spawn gate. `pr_state` (used by `factory judge`, `factory refresh`, the mechanical
merger, prcheck, `factory done`), `judge_pr_ready` (its own 120 s cache from before) and the local-gate readers. They fail closed as before.

## Low-quota guard

When an optional probe would call gh and the cache cannot answer, the tick first reads `rateLimit` through GraphQL (1 point), at most once per tick
(the answer is kept in config row `gh_quota` for 50 s). Below `gh_quota_min` (or when gh answers that the rate limit is exceeded) the probe is skipped:
no gh call, no throttle mark, so the next tick tries again. The first skip in an hour writes one `gh-quota-low` event and one mail to the director;
there is no second alert for an hour. A quota read that fails for another reason counts as "not low". Skipped: CI-wake and red-PR probes only.
The review autospawn probes use the cache but are not skipped (a review that cannot start must show as a probe error, not silently wait).

## Restriction check of the consumers

| consumer | change | merge gate affected? |
|---|---|---|
| review autospawn | cache per head (first review reads origin's head with ls-remote for it); never skipped for quota; a gh error is still a `review-autospawn-probe-error` | no: reviewers/qa start later at worst by one TTL; the spawn pre-check reads the PR fresh |
| judge autospawn (`judge_pr_ready`, `judge_hold`) | none | no |
| red PR alert | cache + skipped while quota is low | no: it only mails |
| CI wake | cache + skipped while quota is low | no: it only mails |
| prcheck / `factory done` | none (`pr_state`, uncached) | no |
| mechanical merger | none (`judge_pr_ready` + `pr_state`) | no |

## Where the quota goes (estimate)

Per tick, per open PR, before the change: CI wake 1 (parked team, CI pending), review autospawn 1 (first review waits for green with
`coder_ci_handoff`, or a re-run is pending), red PR alert 0.1 (once per 600 s), `judge_pr_ready` 0.5 (only with a queued judge or a mech candidate),
drift 1 per hour. After: the first three share one call per 2 ticks. Agents add their own calls: `gh pr checks --watch` polls every 10 s
(360 points per hour per watching coder or reviewer) and `gh pr view` once per spawn pre-check. Those are not changed here.
