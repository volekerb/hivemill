# Option B runner: one session per task, a gate, a judge, a mechanical merge

The runner replaced the old chain of roles (lead, coder, reviewer, QA, judge). Each task gets one interactive Claude Code session that implements it, reviews it with `/code-review`, fixes the findings and opens the PR. A Stop hook checks the work before the session may stop. A separate judge session decides whether the PR should merge, and the mechanical merger merges it. The old pipeline is paused and kept as the rollback ([Rollback](#rollback)). Decision record: [ADR 0009](adr/0009-option-b-runner.md).

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
  judge session (fresh, read-only, same model rule) → verdict JSON → MERGE, REJECT or HOLD
     HOLD → held for the owner (an unasked change that is plausibly fine)
     REJECT → reasons sent to the same task session (warm), at most judge_rounds_max rounds, then FAIL
     MERGE → merge decision: tier ≤ T2, judge MERGE on this head, green and complete PR checks, main push run green,
             public text, noreply, then gh pr merge --squash --match-head-commit
  stale at merge time → clean merge of origin/main, the verdict is carried to the new head, CI re-runs
  merged → terminals closed, session costs recorded, goal check when it was the goal's last task
  main red on that merge (auto_revert=on) → revert PR, named the red-main fix task, merged on green
```

1. **Spec gate.** Every acceptance criterion names its proof. `hivemill task lint T-1234` checks this, and `hivemill run` refuses a task that fails it. `task lint`, `add` and `update` also refuse a `cmd:` or `test:` proof that runs a whole Go module (`go test ./...`, with any flags): name the packages the task touches, CI runs the full suite. `./...` passes only below an `internal`, `cmd` or `pkg` directory (`cd`, `pushd`, `go -C`), so in another layout name the path from the module root (`./parser/...`). `hivemill task lint --all` lists the todo and draft tasks that fail; `run` does not apply this rule to tasks filed before it. A criterion is a numbered item (`1.`, `2)`, `AC3.`). A number at the start of a line always starts one; a number inside a line starts one only when it is the next in sequence, so prose such as `(exit 3) naming` stays in its criterion. Its proof is one of:
   - `test:<unittest id or tests/ file>`, for example `test:test_option_b_runner.CriteriaTests` or `test:tests/test_x.py`, runs Python unittest. A `test:` proof that contains a space is a shell command run in the clone that passes on exit 0, for example `test:pnpm --filter web test` (see Tests under [Standalone install](#standalone-install-no-old-factory-cli)).
   - `cmd:<command> => <expected>`, where the expectation is `exit N` (or `rc=N`) for the exit code, a bare number for the whole output (a count such as `grep -c`), `empty`, or text the output must contain.
   - `flow:<flow>`: reported to the judge, not re-run.
   - `manual:<why only the owner can check>`: allowed; the report flags the task "needs owner check".
2. **Dispatch.** `hivemill run T-1234` starts that task. A plain `hivemill run` advances every active run and, when `projects` names projects, starts ready tasks from them (same order as lead-less dispatch: goal priority, task priority, id) up to `max_concurrent` working runs (working, judging; the runner's proofs and `gate_cmd` run while a run is working). Waiting runs (gated for CI, judged, merging, held for the owner) take no such slot and count against `max_waiting`: when it is reached no new run starts, and one `dispatch.paused_waiting` event is written until the count has been below the cap again. A waiting run sent back to its session (red CI, a conflict, a judge reject) resumes at once, and a judge starts as soon as CI is green, so working runs can exceed `max_concurrent`; the cap holds new runs only. The task clone is a `--shared` clone of the project checkout under `worktrees/<goal>-<task>`, with origin set to the project's GitHub remote, the public-text pre-push hook installed and workspace trust pre-registered. The session is named `claude-coder-t-NNNN-a7`, because the unchanged worker guard derives the one branch a pane may push to from that name. It starts in an hc terminal pinned to the local core (`hc run --bg --on <core>`: an hc wrapper may otherwise place worker-named terminals on another machine, where the factory DB is not local), with an `env -i` spawn script and never `claude -p`, and with `--session-id <uuid>`; the id is written to the `agents` row (role `task`) and to `hm_sessions` before the first request. A SessionStart hook records any later id (`/clear`, resume), so every dollar of the session maps to the task.
3. **Task session.** `roles/task.md` is its whole system prompt (no `_common.md`: the lead, inbox and director rules do not apply). The worker guard runs with `--role coder`, with the coder's deny list. It opens its PR with `factory pr create`. It waits for CI with one blocking `gh pr checks --watch` and never polls in a loop. It writes two files under `.hivemill/` in the clone, which git ignores through the clone's own `.git/info/exclude`:
   - `findings.json`: the `/code-review` findings mapped to [schemas/review-findings.schema.json](schemas/review-findings.schema.json), each with a resolution. The session is the adapter that maps `/code-review` output into this schema.
   - `evidence.json`: each criterion with its proof, the command run and its exit code.
4. **Stop-hook gate** (`hivemill gate hook`). While no PR exists yet, a stop is allowed without a block and is not counted: the session is mid-work, often waiting for a background `/code-review` that wakes it again. A session that then stays idle for 15 minutes with no passing gate is nudged once, and after a second idle spell the run is FAIL `stalled`. Once a PR exists, the gate runs, except while no `findings.json` is written: that stop is mid-work too (a background `/code-review` records its run when it starts) (for example a takeover of an older branch whose PR already exists) and is deferred, not counted. It runs in the clone and checks:
   - the tree is committed and the PR is open, with its head equal to the local HEAD;
   - the prcheck checks: fresh against main, noreply identity, PR body per [pr-descriptions.md](pr-descriptions.md) including the test-count drop rule, public text and scrub terms, and the affected tests (skipped for a docs-only T0 diff);
   - `findings.json` is valid, every finding has a resolution, and its `head` is the current HEAD, or HEAD adds to it only merges of the base branch whose tree is git's own conflict-free merge (a conflict resolution, any other commit, a changed `.gitattributes`, or local merge rules need a new review);
   - a `/code-review` ran in this round. The signal is Claude Code's own record of a forked skill run, `<session>/subagents/agent-*.forked-skill.json` with `"skillName": "code-review"`, for one of the task's sessions and newer than the round start. An empty findings list is valid only with that record;
   - `evidence.json` covers every criterion.

   It then re-runs every `test:` and `cmd:` proof itself, from the criteria in the DB and never from the evidence file. On a failure it blocks the stop with the list of problems. A block counts toward the cap only when its problems repeat the last block's (fixing one problem and stopping again is progress); at most 3 x `gate_max_blocks` blocked stops per round are allowed in any case. After `gate_max_blocks` counted blocks in one round it allows the stop and marks the task FAIL: status `blocked`, `waits=owner`, a `run-fail` event and a `task.failed` line. A gate that cannot decide is not counted as a failure. When there is no test slot or the box is over the load cap, it waits up to `gate_wait_s` and then leaves the affected tests to CI. A crash of the hook itself is logged to `state/hivemill/hook-errors.log` and allows the stop, so the hook never loops.

   **Guard block.** Before it defers a stop (no PR yet, or no `findings.json`), the gate reads the last 256 KiB of the session transcript. When the last tool result is a worker-guard denial (`worker-guard: blocked …`), the run is FAIL at once with the reason `guard block: ` and that line (at most 300 characters), so the planner can re-file the task in minutes instead of after the idle watch. A denial followed by any later tool result defers as before. Without a readable transcript, only a `worker-guard: blocked` line in the last assistant message fails the run.
5. **Before the judge.** One gh read per run per pass. A conflicting PR or a red CI (a startup failure is red) goes back to the same session (at most `ci_rounds_max` red rounds); a session whose pane has closed is relaunched by its session id. The red message names each failed job and carries the last 80 lines of its failed log (`gh run view --log-failed`; at most 10 jobs' logs, bounded in size). A red CI whose run's machine is disconnected waits, and no round is counted. A pending CI waits for the next pass.
6. **Judge** (`roles/task-judge.md`). A fresh session that is never the author. It runs with guard role `reviewer` (no push, read-only git), and Edit and Write are denied. Its model is Sonnet 5.5, or Opus 5.5 when the task is risk-tier. Its input is `.hivemill/judge-packet-rN.md`, assembled deterministically: spec, criteria with the factory's re-run results, findings and resolutions, gate checks, CI state, risk tier, test counts before and after, a mechanical scope check, diff stat and diff. It answers three questions: does the diff do what was asked, does it do anything that was not asked, and does each proof show its criterion. It ends with one JSON block per [schemas/judge-verdict.schema.json](schemas/judge-verdict.schema.json). Its Stop hook (`hivemill judge hook`) parses the block from the last message. A missing block is refused once; a second miss counts as REJECT. The hook writes a `verdicts` row (role `task-judge`, `pass` for MERGE and `fail` for REJECT or HOLD, on the judged head).
   - **Changes.** The verdict has a `changes` list: every behaviour change in the diff (failure paths, defaults, limits, timers, shared selectors and functions included), each with a word-for-word quote of the criterion or spec requirement that asks for it, or `null`. The runner turns a MERGE with a `null` criterion into a REJECT naming those changes (the judge's own verdict is kept as `judge_verdict`). A MERGE with an empty list, or with a criterion quote that is not in the task or that quotes only a proof command, is refused once like a missing block. `HOLD` is for an unasked change that is plausibly fine: the run is held for the owner, as for a T3 merge.
   - **Scope check.** Before the judge starts, the packet lists the scope the task declares and flags every changed file outside it. A task declares scope with a `FILES:` line in the spec (paths, globs, directories; a clause that starts with "No" lists files out of scope and is skipped), or with a scope criterion: a `cmd:` proof `git diff --name-only … | grep -v[xE] '<allowed>' => empty` (allow-list) or `… | grep -E '<forbidden>' => empty` (deny-list). A pattern that does not compile is reported as unreadable, never guessed. A stale base is never a reason to reject: the prompt says so, and the runner refreshes mechanically.
7. **Merge.** On MERGE the run moves to `merging` and each pass reads the PR, git and GitHub once and decides with `Decide`, a pure function. The checks are the old mechanical merger's: the PR is open and not a draft, the repo is not frozen, a task-judge MERGE exists on this exact head, there is no conflict, the head contains the base, the checks are green and the required ones present, the tier is at most T2, or T3 under the hard limits below (tier rules read from the main checkout, never the PR), main is not red unless this task is the named fix task, main's push run for its head finished green, every commit is authored and committed as the configured `noreply_email`, and the public-text guard finds nothing. After the merge commit lands, its author must be `noreply_email` and its committer that address or GitHub's web-flow `noreply@github.com`; a violation writes a `noreply-violation` event and opens an owner alert (`hivemill-noreply`). Then `gh pr merge --squash --delete-branch --match-head-commit <sha>` (never `--admin` or `--auto`) runs under a merge lock, so a later push cannot slip in. An unclassifiable diff is never merged mechanically, and neither is a T3 diff when `t3_auto_merge` is `off` or the task has a `manual:` criterion: the run is **held** for the owner (state `held`, a `run.held` event, the task waits on the owner). A held run is not a failure; it takes no `max_concurrent` slot but counts against `max_waiting`; each pass reads its PR once, and a merge by the owner finishes the run as merged (a closed PR fails it). When the head is behind the base and a merge is clean, the runner merges `origin/<base>` (a merge commit, never a force-push), pushes, and carries the MERGE verdict to the new head with a `carried from …` row. CI must be green again before the merge. A conflict goes back to the session. A head that moved after the verdict is judged again. The edge cases of the old merger's tests are kept as fixtures in `go/internal/hm/testdata/merge_cases.json`.
   - **T3 hard limits** (`go/internal/hm/merguard.go`). A T3 PR with a judge MERGE merges like a T1 or T2 one unless the diff or the PR hits a limit; then the run is held, one owner question lists the limits and `alert_cmd` runs once. The limits: (a) a line leaves a `deny` or `ask` list of a settings file or template, a hook is removed or edited there, its `defaultMode` changes, or `disableAllHooks` is set or changed; (b) a guard test file is deleted, test functions or cases leave the guard tests or `ci/guard_corpus.json`, or a Go guard test file gains `t.Skip`, `t.Skipf`, `t.SkipNow` or a new `//go:build` line; (c) a workflow loses a job or a required check name, or gains `if: false` or `continue-on-error: true`; (d) an allow for `gh pr merge --admin` or `--auto` appears, a line of `roles/hooks/worker-guard*` is added or removed, or anything but tests and test data changes under `go/internal/guard/` or `go/cmd/worker-guard/`; (e) CI is red or the PR conflicts; (f) `merguard.go`, `merge.go`, the tier classification (`tierrules.go`, `assets/tier_rules.json`, `ci/risk_tier.py`, `ci/tier_rules.json`, `go/internal/citools/risktier/`) or the `t3_auto_merge` default in `config.go` changes. Limit (f) also holds a PR below T3, since those files are not sensitive paths. Pending CI and an undecided mergeability only wait, and so does a required check that has not reported, unless the diff hits limit (c). Every decision writes a `merge.guarded` event with the head and the limits (empty = none), and the digest lists these merges under "T3 merged without you".
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

`hivemill config` lists them, `hivemill config KEY VALUE` sets one. They live in the `hm_config` table of the factory DB, apart from the old pipeline's keys: `max_live_agents`, `max_total_claude` and `dispatch_max_per_tick` are never read. The runner also reads the factory keys `noreply_email`, `scrub_terms_file`, `required_checks`, `frozen_repos`, `public_text_repos` and `red_main_freeze`. `hivemill config` lists the first five too and sets them with the checks `factory config` applies (a `noreply_email` must end in `@users.noreply.github.com`; `scrub_terms_file` must be an absolute path to a readable file; repo lists must be `OWNER/NAME`).

| key | default | meaning |
|---|---|---|
| `max_concurrent` | `2` | working runs at one time (working, judging) |
| `max_waiting` | `20` | waiting runs (gated, judged, merging, held); reached = no new run |
| `projects` | empty | project ids a plain `hivemill run` may pick from; empty = explicit task ids only |
| `model` | `claude-sonnet-5-5` | task session and judge model |
| `risk_model` | `claude-opus-5-5` | model when a file the task declares in scope (`FILES:` line or scope criterion) or, for the judge, a file the diff changes matches a `sensitive_paths` entry of `ci/tier_rules.json`, or the task is T3. Words in the task text never route a task |
| `effort` | `medium` | `--effort` of every session |
| `task_workflows` | `large` | which task sessions get the owner's request to use a dynamic workflow (the Workflow tool) in their first prompt: `off`, `large` (4 or more acceptance criteria, 6 or more scope paths, or T3) or `all`. Scope paths are the `FILES:` entries or the alternatives of the allow-list regexes, whichever are more. Eval and judge sessions never get it |
| `task_workflow_agents` | `6` | the agent limit the request names. It is an instruction to the session; the enforced limit is `task_budget_usd` |
| `task_goal` | `1` | `1`: the first prompt of a claude-code task session starts with `/goal` and a condition built from the acceptance criteria, so the session keeps working until an evaluator judges them met. A `manual:` criterion noted in `evidence.json`, a written `.hivemill/blocked.md` or a reported impossible spec also ends it. A prompt over the 4,000 characters `/goal` accepts goes to `.hivemill/kickoff.md` in the clone and the prompt points to it. `0`: no prefix. Eval review-only and judge sessions never get it. Gate and judge rounds are unchanged |
| `task_goal_turns` | `60` | the turn limit the goal condition names |
| `gate_max_blocks` | `3` | gate blocks per round before FAIL |
| `gate_wait_s` | `900` | wait for a test slot before leaving affected tests to CI |
| `judge_rounds_max` | `2` | judge REJECT rounds before FAIL |
| `ci_rounds_max` | `3` | red-CI rounds before FAIL |
| `auto_revert` | `off` | `on` turns on the mechanical revert |
| `t3_auto_merge` | `on` | a T3 PR with a judge MERGE merges unless a hard limit holds it (step 7); `off` holds every T3 PR for the owner, and so does a config read that fails |
| `pass_interval_s` | `60` | daemon: seconds between passes |
| `ci_queue_max_min` | `40` | minutes a PR's checks may wait for a runner before `ci.queue_slow`; while any PR is over it, no other PR is refreshed (see [pr-flow.md](pr-flow.md)) |
| `merge_train` | `3` | merges that may go out while main's push run is pending, when no two change the same file |
| `stale_pr_min` | `90` | minutes without a flow state change before one `pr.stale` event |
| `resume_max` | `3` | lost sessions resumed per run before FAIL `session lost` |
| `run_max_working_min` | `180` | minutes a run may stay in `working` in one round without a passing gate. Then one resume; 30 minutes later FAIL `timed out: N minutes in working`. `0` or `off`: no limit |
| `health_max_age_s` | `300` | `hivemill health` fails when the last good pass is older |
| `alert_every_s` | `3600` | at most one dead-man alert per this many seconds |
| `backup_interval` | `off` | daemon snapshot schedule (`6h`, `30m`) |
| `backup_keep` | `7` | snapshots kept |
| `backup_dir` | empty | snapshot dir; empty = `state/hivemill/backups` |
| `backup_target` | empty | `file:///path`, `s3://…` or `sftp://…` (see Backup) |
| `standby_from` | empty | on a standby machine: where the primary's snapshots are, for the daemon's `standby check` ([standby.md](standby.md)); empty = no check |
| `standby_check_days` | `30` | days between those checks; `0` = off |
| `max_five_hour_pct` | `92` | a fresh (under 3 h old) plan 5-hour usage reading at or over this holds new runs; same file and meaning as the old pipeline (`~/.claude/usage-limits.json`) |
| `max_seven_day_pct` | `95` | the same for the 7-day usage |
| `mem_floor_mb` | `1024` | MemAvailable below this holds new runs |
| `usage_max_age_s` | `600` | a usage reading older than this is unknown and holds new runs |
| `usage_require_5h` | `on` | an empty 5-hour field is unknown and holds new runs; `off` accepts a plan that reports no 5-hour window |
| `task_budget_usd` | `5` | a run whose sessions cost more at API prices is stopped and marked FAIL `budget: ...`; `off` = no cap |
| `project_daily_budget_usd` | `off` | per project, sessions started today; once reached, no new run in that project and one `budget.reached` event |
| `weekly_budget_usd` | `off` | all runs, sessions started in the last 7 days; once reached, no new run and one `budget.reached` event |
| `alert_cmd` | empty | owner alerts run `sh -c "$alert_cmd" hivemill-alert TITLE TEXT` (also `HIVEMILL_ALERT_TITLE`/`_TEXT`); empty = the alert log only, plus a notification on macOS (see Health and alerts) |
| `planner_queue_min`, `planner_model`, `planner_effort`, `planner_wake_gap_s`, `idle_alert_h`, `ask_alert_h` | see [hivemill-planner.md](hivemill-planner.md) | the planner, its wakes and the owner alerts |
| `machine_name`, `obs_port`, `otel_endpoint` | see [hivemill-observability.md](hivemill-observability.md) | the `machine` of every event, the `hivemill obs` page, the OpenTelemetry export |

The usage, memory and budget caps only hold new runs: active runs finish, except a run over `task_budget_usd`, which is stopped. The usage gate fails closed: a missing reading, a reading older than `usage_max_age_s`, an empty 7-day field or an empty 5-hour field (with `usage_require_5h=on`) holds new runs and writes one `usage.unknown` event a day per reason. The reading is `~/.claude/usage-limits.json`, which the status line writes from Claude Code's `rate_limits`. Claude Code includes `rate_limits.five_hour` only while the API reports a 5-hour window, which is why that field can be empty. The reading is refreshed only while some Claude session renders its status line, so an idle box goes stale. To avoid that deadlock, a **probe** is allowed: when the reading is stale, no run is active and its last values were under the caps, exactly one run may start (the effective `max_concurrent` is 1 until a fresh reading arrives) and a `usage.probe` event is written. If the fresh reading is over a cap, the gate closes as usual. A missing reading, or a stale one whose last values were over a cap, stays closed and alerts the owner once (owner alert feed `hivemill-usage`). The report header shows `UNKNOWN: <why>; dispatch held` while dispatch is held. On a plan whose API reports no 5-hour window, set `usage_require_5h off`: the field only appears while the API reports such a window. `hivemill report` starts with one line that shows every cap's current state, for example `usage 5h 42% / 7d 61% (caps 92%/95%), today $3.10 (cap $20.00 per project), 7d $9.80, task cap $5, mem 2.1 GB free (floor 1.0 GB)`.

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
hivemill daemon [--take-over]       # the pass loop (systemd/launchd run it)
hivemill standby check --from HOST  # restore HOST's newest snapshot into a temp home and read it (standby.md)
hivemill health [--alert]           # dead-man check
hivemill drain [--status] | resume  # stop / resume new dispatch
hivemill backup | restore <snap> [--dry-run]
hivemill planner start|stop|status|wake  # the planner session: keeps ready queues full (hivemill-planner.md)
hivemill ask add|list|answer|withdraw   # the owner queue
hivemill digest [--date D] [--send]     # morning digest
```

Agents other than the director, the boss and the planner may only run `task lint`, `task list`, `task show`, `report` and `health` (the planner: see [hivemill-planner.md](hivemill-planner.md)), and a task session `pr create|ready` for its own task; hooks identify their session by `FACTORY_AGENT`. Each pass makes at most one gh read per active run.

## Standalone install (no old factory CLI)

The runner needs neither the Python `factory` CLI nor a repo checkout:

```
hivemill init --home ~/hivemill [--noreply-email EMAIL] [--scrub-terms FILE]
export FACTORY_HOME=~/hivemill
hivemill project add www --repo OWNER/NAME --path ~/src/www [--gate 'make test']...
hivemill goal new --project www --title '...' [--criteria-file F]
hivemill task add --goal G-001 --title '...' --spec-file spec.md --acceptance-file acc.md [--draft]
hivemill config projects www         # only now does a plain pass dispatch from it
```

- `init` creates `db/factory.db` with the tables the runner reads and writes (`projects`, `goals`, `tasks`, `config`, `agents`, `verdicts`, `events`, `owner_alert_feed`, with the factory schema's columns) and the `hm_*` tables, plus `state/hivemill/*`, `worktrees` and `logs`. Every statement is `CREATE ... IF NOT EXISTS`: on a factory home it changes nothing, and it never sets `user_version`. Run it again at any time.
- The role prompts, settings templates and the daemon plist are built into the binary (`go/internal/hm/assets`, a copy of `roles/hivemill/*` and `launchd/hivemill.daemon.plist.in`). A home that has its own `roles/hivemill/<file>` (a repo checkout) uses that file. After changing one of the repo files, run `cd go && go generate ./internal/hm`; `TestAssetsMatchTheRepo` fails until the copy matches.
- `task add` runs the spec gate and refuses criteria that fail it, printing why. `--draft` stores the task as status `draft` instead: it is never dispatched, and its criteria may still fail. `task update` re-runs the spec gate whenever it changes the criteria or the status of a task that can be dispatched, so promoting a draft (`--status todo`) needs passing criteria. `task cancel` and `task update --status` refuse a task with an active run (`hivemill run stop` first).
- `goal new` files the goal as `active` (the old CLI starts at `proposed`); `goal close` refuses a goal with open tasks unless `--force`.
- Sessions (`FACTORY_AGENT`) may only run `task lint|list|show`, `report` and `health`: a session never changes a task row, because the gate re-runs the proofs stored there. A task session also runs `hivemill pr create --title 'T-1234: ...' --body -` (body on stdin) and `hivemill pr ready`: like `factory pr`, the repo and base come from the task's project and the head from the session's own branch (`factory/<task>`), never from arguments, `GH_REPO` or `GH_HOST`; in a `public_text_repos` repo the title, body and branch must pass the public-text check. Raw `gh pr create` stays denied.
- `init` also writes an empty `ci/scrub_allowlist.txt` (no hit allowlisted) when the home has none; the guard, the pre-push hook and the gate's public-text check read it from there. It sets `noreply_email` from `git config --global user.email` when that is a noreply address and nothing else set it. The gate and the merge check owner terms (and home paths, emails, private addresses and hosts) only for a repo in `public_text_repos` (case-insensitive `OWNER/NAME`); every other repo gets the secret checks (tokens, keys) alone, so list each public repo there. `scrub_terms_file` has no default: until `hivemill config scrub_terms_file /abs/path` names the owner terms, the public-text check of a `public_text_repos` repo fails (it never passes without terms). Nothing reads `~/.config/factory`.
- **Worker guard.** The settings run `sh <home>/roles/hooks/worker-guard --role R` on every tool call. A repo checkout has that shim and renders exactly as before. For a home without it, the hook is `hivemill guard --home <home> --role R` (wrapped so that any exit but 0 or 2, a missing binary included, blocks): the Go worker guard in-process (`guard.HookMain`, the same code as `go/cmd/worker-guard`), with the home added to the guard's live homes, so its `run/`, `roles/hooks`, `bin/`, `go/` and DB stay protected even when `~/.config/factory/live_home` names another home. No rule is relaxed. The two decisions the Go guard hands to the Python hook (an archive member list, a DB value the export lacks) block, since a standalone home has no Python hook.
- **Guard export.** The guard decides a coder's push pin, own goal, test sandbox and public-text config from `run/guard-export.json` and blocks when it is missing or older than 15 minutes. In every home, factory checkout or standalone, the daemon writes it at start and every minute, and every runner pass and session start writes it too. `hivemill guard export` (owner, director or boss) writes it on demand. If the daemon stops for 15 minutes, sessions are blocked until it runs again.
- **Workspace trust.** Sessions run in `<home>/worktrees/...` with `--add-dir <home>`; a folder Claude Code does not trust shows the "trust this folder?" prompt and the session would wait there. Claude Code has no documented command or flag that grants trust, so `init` only reads `~/.claude.json` and prints, for an untrusted home or worktree root, what the owner runs once: `cd <home> && claude`, accept, quit (and the same in `<home>/worktrees`). While a session starts, the runner reads its screen (`hc read`); at the trust prompt it closes the terminal and fails the start with that instruction instead of waiting 90 s. Nothing in hivemill answers the prompt.
- **Pre-push hook.** Each task clone gets `hivemill check public-text --home <home>`, the Go port of `ci/pre_push_public_text.py` (same arguments, stdin and exit codes).
- **Tests.** The gate's tests check runs the project's gate command (`project add --gate CMD`, with `gate_timeout_s`) in the clone when it has one; else, in a repo with `ci/affected.py`, `hivemill affected --base origin/<base> --quick --run` (the Go port of `ci/affected.py`); else it is n/a. A `test:` proof that contains a space is a shell command run in the clone and passes on exit 0, so a non-Python project names its own command: `test:pnpm --filter web test`, `test:go test ./internal/x`. Without a space it is Python unittest as before (`test:tests/test_x.py`, `test:tests.test_x.T.test_y`). `cmd:<command> => <expect>` works for any project (`=> exit 0`, `=> empty`, a count, or text the output contains).
- **Tier rules.** The gate's tier and the merge decision use, first found: the project's rules file (`hivemill project profile ID --tier-rules /abs/file`), the home's `ci/tier_rules.json`, the built-in copy of the repo's `ci/tier_rules.json`. `hivemill project profile ID --sensitive GLOB...` replaces `sensitive_paths` for that project (`--clear` drops both). A file that exists but does not load leaves the tier undecided (the owner merges), never a silent fallback. The session model (`Risk`) still reads the home's file only.
- `hivemill config model|risk_model` refuses anything but a `claude-*` id, and every Fable model.

## Running unattended

The runner needs no open client. The owner's laptop can be closed, and the runner survives a reboot.

### Supervision

`hivemill daemon` runs the pass loop in-process: one pass every `pass_interval_s` (60), a heartbeat after each good pass, and a scheduled backup when one is due. Only one daemon runs per factory home (`state/hivemill/locks/daemon.lock` on one machine, the heartbeat file `state/hivemill/daemon.lock` across machines: [standby.md](standby.md#the-daemon-lock)), and each pass also takes `pass.lock`, so a manual `hivemill run` never overlaps it. On SIGTERM it finishes the current pass and exits.

- **Linux:** the user unit `systemd/hivemill.service` (`Type=notify`, `WatchdogSec=900`, `Restart=always`). The daemon sends `READY=1` at start and `WATCHDOG=1` after every pass, so a hung pass is restarted. Install it with `install -m 644 systemd/hivemill.service ~/.config/systemd/user/ && systemctl --user daemon-reload && systemctl --user enable --now hivemill.service`. User lingering must be on (`loginctl enable-linger`).
- **macOS:** the LaunchAgent `hivemill.daemon` (`RunAtLoad`, `KeepAlive`), from `launchd/hivemill.daemon.plist.in`, and the LaunchAgent `hivemill.health` (`hivemill health --alert` every 5 minutes, `StartInterval` 300, log `<home>/logs/hivemill.health.log`), from `launchd/hivemill.health.plist.in`: `hivemill launchd health --out ~/Library/LaunchAgents/hivemill.health.plist`, then `launchctl bootstrap` it the same way. `hivemill launchd daemon [--home DIR] [--out FILE]` renders it with `FACTORY_HOME`, a PATH that has `~/.local/bin` (claude, hc, hivemill), `/opt/homebrew/bin` (gh) and the system dirs, and the log at `<home>/logs/hivemill.daemon.log`. Install it with `hivemill launchd daemon --out ~/Library/LaunchAgents/hivemill.daemon.plist && launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/hivemill.daemon.plist`.
- **Sessions** are hc terminals started with `--no-parent` and pinned to the local hc core, which keeps running after logout. Nothing in a session depends on the client that started it, whether an ssh session or the Herdr app.

### Reconcile and resume

Every pass compares each active run with what is really there, repairs it, and writes a `run.reconciled` event:
- **Lost session.** The task or judge terminal is gone (reboot, OOM, crash). It is relaunched in a new hc terminal with `claude --resume <session id>`, with the same settings and model, at most `resume_max` (3) times. After that the run is FAIL `session lost`.
- **Missing clone.** It is recreated from the pushed branch.
- **PR merged or closed by hand.** The run is finished or failed.

Every side effect checks before it acts, so a pass that crashed half way can safely run again:
- a session whose terminal already runs is adopted, not started twice;
- the PR is found by branch before one is opened;
- a refresh happens only when the head is behind;
- a merged PR is `done`, never merged again;
- the revert task is looked up by title before one is filed.

`TestStepsAreIdempotent` and `TestReconcileResumesALostSessionThenFails` run these steps twice.

**Working time limit.** A session that waits in a loop costs almost nothing, so the budget never stops it. Each round (dispatch, or a resume for red CI or a judge REJECT) may stay in `working` for `run_max_working_min` minutes. The runner writes `run.long_working` at 50% and 100% of the limit. At the limit it sends the session one message ("You have worked N minutes without a passing gate. Stop any wait loop, finish or report what blocks you."). A run still in `working` 30 minutes later is FAIL `timed out: N minutes in working`.

**Dead dependencies.** A todo task whose `depends_on` names a cancelled or failed task can never start. It is a planner level reason, `dead_dep:<task>=<dependency>(cancelled|failed)` (see [hivemill-planner.md](hivemill-planner.md)).

### Health and alerts

`hivemill health` exits 0 when the last good pass is less than `health_max_age_s` (300) old, and 1 otherwise. It also prints the starvation and owner-queue checks, and `--alert` raises them ([hivemill-planner.md](hivemill-planner.md#alerts-hivemill-health---alert)). The timer `systemd/hivemill-health.timer` (with `hivemill-health.service`) on Linux, or the LaunchAgent `hivemill.health` on macOS, runs `hivemill health --alert` every 5 minutes. When the check fails, it opens one row in the factory's owner alert feed (`factory alerts`; sent to the phone only when `owner_alert_send=1`) and raises one owner alert: a line in `<home>/logs/alerts.log`, then `alert_cmd` when it is set, else a macOS notification (`osascript`, the text passed as arguments) on macOS and nothing more elsewhere. Both happen at most once per `alert_every_s` (3600). Every other owner alert that opens a feed row (`hivemill-idle`, `hivemill-asks`, `hivemill-planner`, `hivemill-digest`, `hivemill-standby`) goes out the same way. To keep the old message to the planner session on a factory checkout: `hivemill config alert_cmd 'factory msg --kind alert claude-boss "$1" "$2"'`.

### Backup

- **Built in, no extra binary.** `hivemill backup` writes a consistent snapshot with SQLite `VACUUM INTO` to `backup_dir` (default `state/hivemill/backups`), keeping `backup_keep` (7). The daemon does it every `backup_interval` (a Go duration such as `6h`; default off).
- **Restore.** `hivemill restore <snapshot> --dry-run` runs `integrity_check` and counts the rows. Without `--dry-run`, it keeps the current DB as `<db>.pre-restore-<ts>` and refuses while a daemon runs. Stop the daemon and the factory tick first.
- **Off-machine copies (`backup_target`).**
  - `file:///path` (a mounted disk or NAS share) gets a copy of each snapshot, with the same retention.
  - `s3://…` (AWS, MinIO, B2, R2) and `sftp://…` use Litestream for continuous replication when it is on PATH. Fill in `systemd/litestream.yml.in` as `~/.config/hivemill/litestream.yml`, with credentials in the environment, never in the repo, and enable `systemd/hivemill-litestream.service`. Without Litestream, those targets get no upload: snapshots stay local, and `hivemill backup` says so.
  - Litestream is never required.
- **Warm standby.** A second machine that can take over from these snapshots: [standby.md](standby.md).
- **Hosted (Postgres).** Backups are the database's job (managed point-in-time recovery). Nothing is built for it.

### Drain and upgrade

`hivemill drain` stops new dispatch while active runs finish. `hivemill drain --status` lists what still runs, and `hivemill resume` lifts drain. The report header shows `DRAIN on` while it is active.

To upgrade:
1. `hivemill drain`, and wait until `--status` shows no active run, or leave runs active, since reconcile resumes them.
2. `hivemill backup`.
3. `git pull` in the factory home.
4. `cd go && go build -o ~/.local/bin/hivemill ./cmd/hivemill`.
5. `systemctl --user restart hivemill.service`.
6. `hivemill resume`.

### Deployment profiles

| profile | supervision | backup | what keeps it awake |
|---|---|---|---|
| laptop | launchd agent (`KeepAlive`) or a terminal running `hivemill daemon` | built-in snapshots (`backup_interval`, `backup_target` file:// to an external disk) | the laptop itself; runs pause while it sleeps and resume on wake (reconcile) |
| homelab | systemd user unit with lingering, health timer | snapshots to a NAS share (`file://`) or Litestream to MinIO/sftp | an always-on box; no client needed |
| company | systemd (or the company's service manager) on a server | Stage 2: the managed Postgres's PITR | the server; Stage 2 adds workers that pull work |

### Stage 2 (hosted), design notes only

- Postgres behind the same `Store` interface, with a tenant id on every row and no SQL outside `store`.
- Runs are claimed through leases with an expiry. A worker that dies loses its lease, and another worker reconciles the run.
- One leader lock (an advisory lock) for the scheduling pass.
- Workers pull work: each machine runs a worker that claims runs and hosts their sessions, instead of one host pushing work out.

## Eval mode

`hivemill eval T-1234 [--judge-only]` measures how well the layers catch a known defect. It runs on a task whose branch is already pushed with the change and has an open **draft** PR (the operator prepares both), and it never merges:
- an eval run ends at its first verdict (state `evaluated`) and never enters `merging`; `Merge` refuses it, and a PR merged outside the runner fails the run;
- the judge's verdict is kept in the run and the event log only, never as a `verdicts` row, so no other merger can read it;
- `--judge-only` starts no task session. The runner gates the prepared head itself (the gate checks without the session's findings, review and evidence duties, and every `test:`/`cmd:` proof re-run), waits for CI to finish, green or red, and starts a fresh judge on it. It leaves the PR open and `.hivemill/` empty for a full run.
- without `--judge-only`, a task session starts on the prepared head with a kickoff that reads like a resumed task: the change is committed, continue with the review. Then the usual gate, CI rounds and judge follow. At the end the PR gets the `eval` label, is closed and its branch deleted.

- `--review-only` starts a task session that only runs `/code-review` on the prepared head and writes `findings.json` without changing code; the run ends when the file exists. It measures the review on its own, because in a full run the session usually fixes a defect it notices before `/code-review` runs.

Each run writes one `eval.done` event (mode, outcome, gate problems, review findings, verdict and reasons, CI rounds) and `eval.gate` for a judge-only gate. `hivemill report` lists eval runs apart and leaves them out of the totals. The expected answer for a case is kept outside the factory home, because every session can read the factory home.

## Event log and report

Each step appends one line to `state/hivemill/events.jsonl`. The events are `task.dispatched`, `session.started`, `session.ended`, `session.closed`, `gate.blocked`, `gate.passed`, `review.findings`, `ci.red`, `pr.refreshed`, `pr.flow`, `pr.stale`, `ci.queue_slow`, `ci.rerun`, `pr.held_alert` (the last five: [pr-flow.md](pr-flow.md)), `judge.verdict`, `merge.guarded`, `pr.merged`, `pr.reverted`, `task.depends_repointed`, `task.failed`, `task.cancelled` and `goal.checked`. Each line has `ts`, `task_id`, `goal_id`, `project`, `harness` (`claude-code`) and `machine` (config `machine_name`, else the hostname). Where they apply, it also has `session_id`, `role` (task, judge or review), `model`, `effort` and `cost_usd`. `session.ended` (written when a run is merged, fails or is cancelled) carries the session's cost and tokens. When a run is merged, fails, is cancelled or is evaluated, the runner also ends its task and judge sessions on the machine they run on, and writes `session.closed` (with `data.agent` and `data.machine`) for each one that was still live. The worktree stays. `hivemill obs` and `hivemill otel export` read this log: see [hivemill-observability.md](hivemill-observability.md). Run records (state, rounds, gate results, verdicts) are in the `hm_runs` table, sessions in `hm_sessions`, and gate logs and verdicts in `state/hivemill/runs/<task>/`.

`GET /api/obs/task?k=<machine>/<task>` on the `hivemill obs` server is one task's timeline as JSON: `task`, `goal`, `project`, `total_usd` (its sessions' costs summed) and `steps` in time order (dispatch, sessions, gate, review findings, red CI, judge verdicts, held, merged or failed, and `prompt.sent` with kind and path only). Each step has `ts`, `kind`, `role`, `summary`, `cost_usd` when known and `links` (the PR URL and the `/session` page). It takes the same `machine` and `harness` filters as the pages; an unknown key answers 404 with a JSON error, any method but GET 405.

Every prompt the runner sends to a task or judge session is saved as `state/hivemill/prompts/<task>/<NNN>-<kind>.txt` (`NNN` counts per task; file mode 0600, directory 0700) and logged as `prompt.sent` with `data.kind`, `data.path` (relative to `state/hivemill`), `data.chars` and `data.sha256`. The kinds are `first` (the kickoff of a new session), `resume` (a conflict or merge resume, or the relaunch of a lost or closed session, whatever the reason), `ci` (red CI), `judge` (a judge rejection to the task session, or the judge packet, told apart by `role`) and `nudge` (an idle session); `gate`, `answer` and `other` are reserved. Gate findings are answers of the Stop hook, not prompts, and are not recorded here. Planner prompts are not recorded. A write error never stops the send: it is logged as `prompt.record_failed`.

`hivemill report` reads the log, the sessions and each session's transcript. For each task it shows the outcome, $ in total and for the task and judge sessions, hours from dispatch to merge, gate blocks, judge verdicts, CI rounds, findings fixed out of found, criteria, criteria proven by a re-run, manual criteria, re-run failures and the session ids. The totals are $ per merged PR, the median hours to merge and the share of PRs with 2 or more rounds, next to the baseline ($3.56–14 per merged PR, 50% with 2+ rounds, a median of 3.4 h from open to merge). The cost of a session is summed from its transcript, subagent and Workflow agent logs included (`kit/spend.SessionCosts`), at the API prices of `go/internal/tokens`. A second table compares the dispatched tasks that ran workflow agents with those that did not: tasks, how many were asked to use a workflow, merged, $ per merged task (the side's whole spend over its merged tasks), rounds per task (judge verdicts plus red CI rounds, over all its tasks) and median hours to merge. The `wf` column is the number of Workflow agents a task's sessions ran. The workflow split is a plain count, not a controlled test: its two sides hold different tasks, open tasks count too, and any workflow counts.

## Why not `/steward`, `/ship` or `claude -p`

- `/steward` (Claude Code 2.1.330+) is a watch that fixes CI and answers review comments for up to 7 days. It makes its own gh calls. The runner already owns the CI rounds and the merge, so one blocking `gh pr checks --watch` per round is enough.
- `/ship` adds a review and that same watch. The task prompt runs `/code-review` directly instead.
- `claude -p` is not used. Sessions are interactive in hc terminals, because subscription billing for headless runs is not verified.

## Sandbox

The Python-era test sandbox (unshare, cgroup, subuid) is off and not ported. Claude Code's own sandbox setting is not turned on yet: on Linux it needs bubblewrap (`bwrap`), which the factory host does not have (installing it is a machine change for the owner), and with it the session's `git push`, `gh` and `factory pr create` need network and path allowances that are still to be tested in an hc terminal. The worker guard stays the security policy (PreToolUse, coder rules for the task session, reviewer rules for the judge).

## Rollback

- Stop starting new runs: stop the timer and run no `hivemill run`. Close running sessions with `hivemill run stop T-1234`.
- The old pipeline is untouched and stays the rollback. A stopped task stays runner-managed while its `task` agents rows exist, so a director must delete those rows (or file a new task) to hand it back.
- Turn off the revert with `hivemill config auto_revert off`.
- Remove the lane by reverting its PR. The `hm_*` tables are only read by `hivemill`, and no old config key was changed.
