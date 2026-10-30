# Merge queue

How PRs reach `main`. The serial path is `factory judge <task> --merge`; the batch train below merges several PRs
after one gate run. The planner alone is described in [judge-train.md](judge-train.md).

## Batch train (T-0522)

`factory judge --train T-a T-b [T-c T-d]` (2 to 4 ids, judge role, no `--dry-run`) needs the PRs' project to be in
local gate mode (non-empty `gate_cmd`, see [local-gate.md](local-gate.md)); otherwise it exits 2 and merges nothing.

1. **Plan.** The `--dry-run` planner runs unchanged: unready and conflicting PRs are `DROPPED`, the rest get prefix
   trees T_1..T_N of `origin/main` + PR1..PRk.
2. **Hold.** The merge freeze and the MAIN-RED hold (`red main: ...`) refuse the whole train before any gate run
   (exit 4).
3. **Local ref.** The tip commit T_N is written to `refs/factory-train/<ids>` in the local repo. It is never pushed.
4. **One gate run.** `gate_cmd` runs once on the tip in a detached worktree (same capacity, slot, shard and timeout rules
   as `factory gate run`, `gate_max_parallel` included). The `gate_runs` row has `kind=train`, `task_ids`, `tip_sha`,
   `main_sha`, `tree`. Cache key: (project, tip tree, `main_sha`, command); a hit is recorded as a pass with note `cached`
   and runs nothing.
5. **PASS.** The PRs merge in train order with the serial merge command (`gh pr merge --squash --delete-branch
   --match-head-commit <head>`, public-repo author/body rules, push policy, red main). After each merge the tree of the
   verified `origin/main` must equal T_k. A mismatch (main moved, or the merge produced another tree) stops the train;
   the remaining PRs are left for the serial judge. Each merged task gets a `merge` verdict whose summary cites the gate
   run id.
6. **FAIL.** Binary search over the prefixes: the next run is T_(lo + ceil((hi-lo)/2)). For N <= 4 that is at most 2 extra
   runs. The passing prefix is merged as in step 5. The first PR whose prefix fails gets a `REJECT candidate` message to
   its lead (or the director) with the failed run's log path. The train records no REJECT verdict itself; that PR and the
   ones after it go back to the serial judge.
7. **ERROR or timeout.** No merge. The train is released, the PRs fall back to the serial judge (exit 5). An error is not
   bisected.

Exit codes: 0 all planned PRs merged; 4 refused, stopped or part merged; 5 gate ERROR.

Not done yet: `factory gate run --train` as a standalone command, and the measured run on real PRs.

Tests: `tests/test_judge_train.py` (`JudgeTrainRunTests`).
