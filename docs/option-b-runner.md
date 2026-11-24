# Option B runner: one session per task, a gate, a judge, a mechanical merge

The runner is a second lane next to the old chain of roles (lead, coder, reviewer, QA, judge). Each task gets one interactive Claude Code session that implements it, reviews it with `/code-review`, fixes the findings and opens the PR. A Stop hook checks the work before the session may stop. A separate judge session decides whether the PR should merge, and the mechanical merger merges it. The old roles stay in place and still run. Decision record: [ADR 0009](adr/0009-option-b-runner.md).

Code: the Go binary `hivemill` (`go/cmd/hivemill`, package `go/internal/hm`). It reuses `gitx` (hardened git), `guard` (public-text and scrub scans), `citools/risktier` and `kit/spend`. Prompts: `roles/hivemill/task.md` and `roles/hivemill/task-judge.md`. Settings: `roles/hivemill/task.json.in` and `roles/hivemill/task-judge.json.in`. Tests: `go test ./internal/hm/`, with the merge cases in `go/internal/hm/testdata/merge_cases.json`. Build and install: `cd go && go build -o ~/.local/bin/hivemill ./cmd/hivemill`.

## Flow

```
hivemill run T-1234
  spec gate ── refuse: a criterion names no proof
  clone factory/t-1234, .hivemill/ excluded from git
  task session (hc terminal, interactive claude, Sonnet 5.5 medium; Opus 5.5 for a risk-tier task)
     implement → affected tests → /code-review → fix → findings.json + evidence.json → PR → one CI wait → stop
  Stop hook gate ── block with reasons (at most gate_max_blocks per round), then FAIL for the owner
  runner pass (hivemill run): PR conflicts → back to the session; CI red → back to the session; CI green → judge
  judge session (fresh, read-only, same model rule) → verdict JSON → MERGE or REJECT
     REJECT → reasons sent to the same task session (warm), at most judge_rounds_max rounds, then FAIL
     MERGE → merge decision: tier ≤ T2, judge MERGE on this head, green and complete PR checks, main push run green,
             public text, noreply, then gh pr merge --squash --match-head-commit
  stale at merge time → clean merge of origin/main, the verdict is carried to the new head, CI re-runs
  merged → terminals closed, session costs recorded, goal check when it was the goal's last task
  main red on that merge (auto_revert=on) → revert PR, named the red-main fix task, merged on green
```

1. **Spec gate.** Every acceptance criterion names its proof. `hivemill task lint T-1234` checks this, and `hivemill run` refuses a task that fails it. A criterion is a numbered item (`1.`, `2)`, `AC3.`) and its proof is one of:
   - `test:<unittest id or tests/ file>`, for example `test:test_option_b_runner.CriteriaTests` or `test:tests/test_x.py`. Python unittest only; other projects write `cmd:` proofs.
   - `cmd:<command> => <expected>`, where the expectation is `exit N` (or just `N`), `empty`, or text the output must contain.
   - `flow:<flow>`: reported to the judge, not re-run.
   - `manual:<why only the owner can check>`: allowed; the report flags the task "needs owner check".
2. **Dispatch.** `hivemill run T-1234` starts that task. A plain `hivemill run` advances every active run and, when `projects` names projects, starts ready tasks from them (same order as lead-less dispatch: goal priority, task priority, id) up to `max_concurrent`. The task clone is a `--shared` clone of the project checkout under `worktrees/<goal>-<task>`, with origin set to the project's GitHub remote, the public-text pre-push hook installed and workspace trust pre-registered. The session starts in an hc terminal (`hc run --bg`, an `env -i` spawn script, never `claude -p`) with `--session-id <uuid>`; the id is written to the `agents` row (role `task`) and to `hm_sessions` before the first request. A SessionStart hook records any later id (`/clear`, resume), so every dollar of the session maps to the task.
3. **Task session.** `roles/task.md` is its whole system prompt (no `_common.md`: the lead, inbox and director rules do not apply). The worker guard runs with `--role coder`, with the coder's deny list. It opens its PR with `factory pr create`. It waits for CI with one blocking `gh pr checks --watch` and never polls in a loop. It writes two files under `.hivemill/` in the clone, which git ignores through the clone's own `.git/info/exclude`:
   - `findings.json`: the `/code-review` findings mapped to [schemas/review-findings.schema.json](schemas/review-findings.schema.json), each with a resolution. The session is the adapter that maps `/code-review` output into this schema.
   - `evidence.json`: each criterion with its proof, the command run and its exit code.
4. **Stop-hook gate** (`hivemill gate hook`). It runs in the clone and checks:
   - the tree is committed and the PR is open, with its head equal to the local HEAD;
   - the prcheck checks: fresh against main, noreply identity, PR body per [pr-descriptions.md](pr-descriptions.md) including the test-count drop rule, public text and scrub terms, and the affected tests (skipped for a docs-only T0 diff);
   - `findings.json` is valid and every finding has a resolution;
   - `evidence.json` covers every criterion.

   It then re-runs every `test:` and `cmd:` proof itself, from the criteria in the DB and never from the evidence file. On a failure it blocks the stop with the list of problems. After `gate_max_blocks` blocks in one round it allows the stop and marks the task FAIL: status `blocked`, `waits=owner`, a `run-fail` event and a `task.failed` line. A gate that cannot decide is not counted as a failure. When there is no test slot or the box is over the load cap, it waits up to `gate_wait_s` and then leaves the affected tests to CI. A crash of the hook itself is logged to `state/hivemill/hook-errors.log` and allows the stop, so the hook never loops.
5. **Before the judge.** One gh read per run per pass. A conflicting PR or a red CI goes back to the same session with the failing names (at most `ci_rounds_max` red rounds). A pending CI waits for the next pass.
6. **Judge** (`roles/task-judge.md`). A fresh session that is never the author. It runs with guard role `reviewer` (no push, read-only git), and Edit and Write are denied. Its model is Sonnet 5.5, or Opus 5.5 when the task is risk-tier. Its input is `.hivemill/judge-packet-rN.md`, assembled deterministically: spec, criteria with the factory's re-run results, findings and resolutions, gate checks, CI state, risk tier, test counts before and after, diff stat and diff. It answers three questions: does the diff do what was asked, does it do anything that was not asked, and does each proof show its criterion. It ends with one JSON block per [schemas/judge-verdict.schema.json](schemas/judge-verdict.schema.json). Its Stop hook (`hivemill judge hook`) parses the block from the last message. A missing block is refused once; a second miss counts as REJECT. The hook writes a `verdicts` row (role `task-judge`, `pass`/`fail`, on the judged head). A stale base is never a reason to reject: the prompt says so, and the runner refreshes mechanically.
7. **Merge.** On MERGE the run moves to `merging` and each pass reads the PR, git and GitHub once and decides with `Decide`, a pure function. The checks are the old mechanical merger's: the PR is open and not a draft, the repo is not frozen, a task-judge MERGE exists on this exact head, there is no conflict, the head contains the base, the checks are green and the required ones present, the tier is at most T2 (tier rules read from the main checkout, never the PR), main is not red unless this task is the named fix task, main's push run for its head finished green, every commit is noreply, and the public-text guard finds nothing. Then `gh pr merge --squash --match-head-commit <sha>` runs under a merge lock, so a later push cannot slip in. A T3 or unclassifiable diff is never merged mechanically: the task goes to the owner. When the head is behind the base and a merge is clean, the runner merges `origin/<base>` (a merge commit, never a force-push), pushes, and carries the MERGE verdict to the new head with a `carried from …` row. CI must be green again before the merge. A conflict goes back to the session. A head that moved after the verdict is judged again. The edge cases of the old merger's tests are kept as fixtures in `go/internal/hm/testdata/merge_cases.json`.
8. **After the merge.** The terminals are closed (agents rows `exited`), each session's cost is recorded in `session.ended`, and when the task was the last open task of its goal the goal's `cmd:` checks (from the goal's success criteria, same proof format) run on the merged main. A goal without one is recorded as `goal.checked: skipped`.
9. **Revert** (`auto_revert=on`). The red-main monitor (`red_main_check`, [red-main.md](red-main.md)) records a failed push run. If its head is the merge commit of a runner PR, and no later runner merge exists in that repo, the runner opens a revert PR (`git revert`, never a direct push) as a new task. It names that task the red-main fix task, so it may merge during the freeze, and merges it on green CI. Anything else is left to the bisect task the monitor files.

## The old pipeline leaves runner tasks alone

A task with an `agents` row of role `task` or `task-judge` is "runner-managed" (`runner_managed()`). For such a task:
- reviewer, QA and judge autospawn skip it (`autospawn_block_reason`);
- coder CI resume, lead-less dispatch and the red-PR owner mail skip it;
- the tick's mechanical merger skips it (`mech_candidates`); only `hivemill` merges it;
- the tick's reap loop, idle nudges, flagless check and Claude audit skip the `task` and `task-judge` rows, because the runner owns those terminals.

None of these guards changes a config key.

## Config keys

`hivemill config` lists them, `hivemill config KEY VALUE` sets one. They live in the `hm_config` table of the factory DB, apart from the old pipeline's keys: `max_live_agents`, `max_total_claude` and `dispatch_max_per_tick` are never read. The runner also reads the factory keys `noreply_email`, `scrub_terms_file`, `required_checks`, `frozen_repos` and `red_main_freeze`.

| key | default | meaning |
|---|---|---|
| `max_concurrent` | `2` | active runs at one time |
| `projects` | empty | project ids a plain `hivemill run` may pick from; empty = explicit task ids only |
| `model` | `claude-sonnet-5-5` | task session and judge model |
| `risk_model` | `claude-opus-5-5` | model when the task names a `sensitive_paths` entry of `ci/tier_rules.json` or is T3 |
| `effort` | `medium` | `--effort` of every session |
| `gate_max_blocks` | `3` | gate blocks per round before FAIL |
| `gate_wait_s` | `900` | wait for a test slot before leaving affected tests to CI |
| `judge_rounds_max` | `2` | judge REJECT rounds before FAIL |
| `ci_rounds_max` | `3` | red-CI rounds before FAIL |
| `auto_revert` | `off` | `on` turns on the mechanical revert |

The settings files set the worker context caps from [token-discipline.md](token-discipline.md), `promptCacheTtl` and `subagentPromptCacheTtl` to `5m` (Claude Code 2.1.331 accepts `5m` or `1h`), and they disable the design, product-management, productivity, cowork and caveman plugins. They load no MCP servers. Fable is refused as a model.

## Running it

```
hivemill task lint T-1234           # spec gate
hivemill run T-1234                 # start one task (or advance it when it already runs)
hivemill run                        # one pass: advance every run, revert check, start ready tasks (config projects)
hivemill run status                 # one line per run
hivemill run stop T-1234            # close its terminals, back to todo
hivemill gate T-1234                # run the gate checks now, print them
hivemill judge packet T-1234        # print what the judge reads
hivemill merge T-1234 --dry-run     # print the merge input and decision
hivemill report [--json]            # scoreboard
```

Agents other than the director and the boss may only run `task lint` and `report`; hooks identify their session by `FACTORY_AGENT`. Each pass makes at most one gh read per active run. To run passes every minute, install the user timer `systemd/hivemill-run.timer` with its service (`systemctl --user enable --now hivemill-run.timer`).

## Event log and report

Each step appends one line to `state/hivemill/events.jsonl`. The events are `task.dispatched`, `session.started`, `session.ended`, `gate.blocked`, `gate.passed`, `review.findings`, `ci.red`, `pr.refreshed`, `judge.verdict`, `pr.merged`, `pr.reverted`, `task.failed`, `task.cancelled` and `goal.checked`. Each line has `ts`, `task_id`, `goal_id`, `project` and `harness` (`claude-code`). Where they apply, it also has `session_id`, `role` (task, judge or review), `model`, `effort` and `cost_usd`. Run records (state, rounds, gate results, verdicts) are in the `hm_runs` table, sessions in `hm_sessions`, and gate logs and verdicts in `state/hivemill/runs/<task>/`.

`hivemill report` reads the log, the sessions and each session's transcript. For each task it shows the outcome, $ in total and for the task and judge sessions, hours from dispatch to merge, gate blocks, judge verdicts, CI rounds, findings fixed out of found, criteria, criteria proven by a re-run, manual criteria, re-run failures and the session ids. The totals are $ per merged PR, the median hours to merge and the share of PRs with 2 or more rounds, next to the baseline ($3.56–14 per merged PR, 50% with 2+ rounds, a median of 3.4 h from open to merge). The cost of a session is summed from its transcript, subagent logs included (`kit/spend.SessionCosts`), at the API prices of `go/internal/tokens`.

## Why not `/steward`, `/ship` or `claude -p`

- `/steward` (Claude Code 2.1.330+) is a watch that fixes CI and answers review comments for up to 7 days. It makes its own gh calls. The runner already owns the CI rounds and the merge, so one blocking `gh pr checks --watch` per round is enough.
- `/ship` adds a review and that same watch. The task prompt runs `/code-review` directly instead.
- `claude -p` is not used. Sessions are interactive in hc terminals, because subscription billing for headless runs is not verified.

## Sandbox

The Python-era test sandbox (unshare, cgroup, subuid) is off and not ported. Claude Code's own sandbox setting is not turned on yet: on Linux it needs bubblewrap, and with it the session's `git push`, `gh` and `factory pr create` need network and path allowances that are still to be tested in an hc terminal. The worker guard stays the security policy (PreToolUse, coder rules for the task session, reviewer rules for the judge).

## Rollback

- Stop starting new runs: stop the timer and run no `hivemill run`. Close running sessions with `hivemill run stop T-1234`.
- The old pipeline is untouched and stays the rollback. A stopped task stays runner-managed while its `task` agents rows exist, so a director must delete those rows (or file a new task) to hand it back.
- Turn off the revert with `hivemill config auto_revert off`.
- Remove the lane by reverting its PR. The `hm_*` tables are only read by `hivemill`, and no old config key was changed.
