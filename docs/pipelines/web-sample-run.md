# Web sample run: hivemill-www T-1873

This is the recorded run for G-052 SC2. The sample web repo is hivemill-www. No new repo was made.

This run was a task filed by the planner (goal G-066). It was not a goal filed from one sentence through the web skill.

- Task: T-1873, "Install guide: standalone home in any empty directory"
- Merged PR: https://github.com/volekerb/hivemill-www/pull/28 (tier T1)
- The task session ran on the second machine. This record was written on the first machine.

## Steps

All times are UTC, 2026-11-25. Where this machine holds no timestamp, the step says "not recorded here".

1. Spec gate: PASS, 4 criteria. Time: not recorded here.
2. Task session: one commit, authored 10:30:57. Dispatch time: not recorded here.
3. Gate rounds: round 1, 5 gate blocks (from `hivemill report`). Per-block times: not recorded here.
4. CI: PR opened 10:31:36. The `build` check ran 10:31:43 to 10:32:38 and passed. The `deploy` check was skipped.
5. Judge verdict: MERGE. Time: not recorded here.
6. Merge: 11:47:37.

Steps: 6

Elapsed: 1h 16m 01s, from PR open (10:31:36) to merge (11:47:37). Dispatch is not recorded here, so PR open is the start. `hivemill report` shows 1.28 h to merge for this task. It does not say which start it counts from, so the two figures are not claimed to be the same measure.

Cost: $0.80 in total from `hivemill report`: $0.67 for the task session and $0.13 for the judge. The report lists 3 sessions: 2 task and 1 judge.

## Sources

- `hivemill task show T-1873`
- `hivemill report`
- `gh pr view 28 --repo volekerb/hivemill-www --json createdAt,mergedAt,commits,statusCheckRollup`

`hivemill run status` was refused for this session (it is for the owner or the director). Nothing from it is used here.
