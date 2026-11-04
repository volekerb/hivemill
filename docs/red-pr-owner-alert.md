# Red-PR owner alert (G-022 SC3)

Config `red_pr_owner_alert` (default `0`, off). With `1`, the tick (step 1c'') mails the task's lead
(the director when the task has no team or the team has no lead) once per `(task, head sha)` when:

- the task is `in_progress`, `in_review` or `blocked` and has an open PR,
- no coder agent is live, starting or queued for the task,
- the head is red (all checks done, one failed) or the PR is conflicted (`mergeStateStatus` DIRTY).

A blocked task is mailed too, with its waits value and recorded reason. It is independent of `lead_parking` and never spawns anything; the mail is
`kind report` with event `red-pr-unowned`, so a parked lead is woken by it.

Throttling: a red head uses `tick_ci_wake`'s `ci-red:<task>:<sha>` row (a parked team gets one mail, not two);
a conflict uses `red-pr:<task>:<sha>`. A head already mailed makes no `gh` call (one batched `git ls-remote`
per tick); an unreadable head is re-probed every 10 minutes (`red-pr-probe:<task>`).

Rollback: `factory config red_pr_owner_alert 0`.
