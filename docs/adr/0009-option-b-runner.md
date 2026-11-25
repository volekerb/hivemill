# ADR 0009: Option B runner, one session per task with a gate and a judge

## Status

Accepted, 2026-11-25. The owner accepted it in chat on 2026-11-25, after the two pilots below; that approval has no factory message id. Proposed on 2026-11-24, when the owner chose Option B of the 2026-11-24 factory review and asked for a pilot on real tasks.

## Context

The chain of chat agents (lead, coder, reviewer, QA, judge) costs $3.56–14 per merged PR. Half of its PRs need two or more rounds, and the median time from open to merge is 3.4 h. More than half of all rejections come from the process (gate refusals, stale branches), not from wrong code. Each reviewer and QA session starts cold, at 38–43k tokens of context. Claude Code 2.1.331 does much of the chain natively: `/code-review` runs in fresh-context subagents, Stop hooks can refuse a stop with a reason, and the prompt cache TTL can be set to 5 minutes.

## Decision

- A deterministic scheduler (`hivemill run`, Go) starts one interactive Claude Code session per task, in an hc terminal. Sonnet 5.5 at medium effort is the default, and Opus 5.5 is used for risk-tier tasks. The session's id is recorded at spawn.
- The session implements the task, runs the affected tests, runs `/code-review`, fixes the findings and opens the PR.
- A Stop hook gate reuses the prcheck checks, validates the review findings and the evidence, and re-runs every `test:` and `cmd:` proof itself. It has a block cap, after which the task goes to the owner.
- Every acceptance criterion must name its proof before dispatch (the spec gate).
- A fresh, read-only judge session decides MERGE or REJECT from a deterministic packet. A REJECT goes back to the same warm session, for at most 2 rounds.
- `hivemill` merges with a fresh Go merge decision that keeps the old merger's safety checks (fixtures from its tests): judge MERGE on the exact head, green and complete checks, tier at most T2, red-main freeze, main push run green, noreply, public text, `--match-head-commit`.
- Optionally, a runner merge that turns main red is reverted mechanically through a PR.
- The lane is a fresh, small Go core (`go/cmd/hivemill`, `go/internal/hm`), written from this spec, not ported from Python. It reuses `gitx`, `guard`, `citools/risktier` and `kit/spend`. The byte-parity port (`go/internal/cli`, `tick`) is not extended. State is SQLite behind a `Store` interface (Postgres-ready, not built).

## Pilot results

| run | cost per merged PR | merged |
|---|---|---|
| pilot 1 | $1.33 | 4 of 5 tasks |
| pilot 2 | $4.64 | 3 of 6 tasks |
| old pipeline (for comparison) | $3.56–14 | |

Pilot 2 found a gap in the judge. It passed three side effects that no criterion asked for as "no unasked changes". Two fixes closed it:

- #904: the verdict lists every behaviour change in the diff and maps each to a word-for-word criterion or spec quote, or `null`. The runner turns a MERGE with a `null` change into a REJECT. A new verdict, HOLD, parks a plausibly fine unasked change for the owner. The judge packet also gets a mechanical scope check that flags changed files outside the task's declared scope.
- #905: the model is chosen from files, never from words in the task text. The files a task declares in scope and, for the judge, the files the diff changes are matched against `sensitive_paths`; an explicit T3 tier also routes to Opus.

The pilot 2 failures that were not judge verdicts were spec conflicts and a budget overrun. A criterion that contradicts another rule of the repo, such as a scope criterion that forbids a file the change needs, or an existing test the spec must change, has to be resolved in the spec before dispatch. Tasks are sized to fit `task_budget_usd`.

## Consequences

- Sessions per task fall from 4–6 to 2 (task and judge), and the review runs inside the warm session.
- The Python tick must skip runner tasks while it still runs. One `runner_managed()` check in seven places does this, and no config key changes. The old pipeline stays paused as the rollback and is deleted later in its own PR.
- `/code-review` and the judge are model judgement. The gate and the proof re-runs are the deterministic part, and a proof the judge doubts can still block a merge.
- T3 tasks cannot merge mechanically (`MECH_MAX_TIER`), so they wait for the owner.

## Alternatives

- **Option A**, trimming the chain step by step: weeks of factory time to rebuild by hand what Claude Code already provides.
- **`claude -p`, headless**: subscription billing for headless runs is not verified, and `--bare` may become its default.
- **`/ship` and `/steward`**: a watch that can last days and makes its own gh calls. The runner already owns the CI rounds and the merge.
- **No judge, mechanical merge only**: rejected by the owner. Context decides whether a PR should merge.
