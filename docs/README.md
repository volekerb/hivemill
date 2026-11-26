# Documentation

Every page under `docs/`, grouped by what you want to do. Hivemill today is the runner: a Go binary, `hivemill`, that runs one Claude Code session per task, checks it with a gate, asks a fresh judge and merges mechanically. New here? Start with the runner page, listed first below.

Each group lists the runner's pages first. The pages under "Legacy pipeline (paused)" describe the chain of chat agents (boss, director, lead, pm, coder, reviewer, QA, judge and the helpers), the Python `factory` CLI and tick, the plugin installer, the web UI and the Herdr dashboard. The runner replaced that chain. It is paused, kept as the rollback, and will be removed in its own PR. Those pages stay accurate for the legacy code; they do not describe the runner.

## Use it

### Runner and shared

- [option-b-runner.md](option-b-runner.md): the runner (`hivemill`, Go): one Claude Code session per task, the spec gate, the Stop-hook gate, a fresh judge, mechanical merge, budgets, daemon, backup and deployment profiles. Start here.
- [hivemill-planner.md](hivemill-planner.md): the `hivemill` planner session (keeps ready queues full), the owner queue (`hivemill ask`), wakes, starvation alerts and the morning digest.
- [hivemill-observability.md](hivemill-observability.md): `hivemill obs` (read-only local page: portfolio, project, goal, task, session), `obs pull` from other machines, and the OpenTelemetry export of tasks as traces.
- [design/obs-ui.md](design/obs-ui.md): the design direction for the `hivemill obs` pages: tokens, per-page rules, states and accessibility.
- [hivemill-machines.md](hivemill-machines.md): `hivemill` task sessions on other hc machines (`session_machines`), forwarding a worker's `hivemill` to the coordinator, and running on macOS.
- [pipelines/web-sample-run.md](pipelines/web-sample-run.md): the recorded run of one planner-filed task on the hivemill-www sample web repo: steps, elapsed time, cost and the merged PR.
- [pr-descriptions.md](pr-descriptions.md): the template every pull request body follows.
- [token-discipline.md](token-discipline.md): what agents cost (context per turn, idle turns, spawns), the worker context limits and the boss exception, the rules, and `hm-spend`.

### Legacy pipeline (paused)

- [SETUP.md](SETUP.md): install the factory on a Linux box and bring up the first agents.
- [adopt/ADOPT.md](adopt/ADOPT.md): a prompt you hand to your own Claude Code so it performs the setup for you.
- [examples/README.md](examples/README.md): overview of three worked examples with real command output.
- [examples/01-goal-to-merged-pr.md](examples/01-goal-to-merged-pr.md): follows one owner request all the way to a merged PR.
- [examples/02-handling-a-rejection.md](examples/02-handling-a-rejection.md): what happens when review fails and the judge rejects.
- [examples/03-digest-and-scorecard.md](examples/03-digest-and-scorecard.md): the morning digest and the per-team scorecard.
- [web-ui.md](web-ui.md): the dashboard started by `factory serve` and what each panel shows.
- [herdr-app-dashboard.md](herdr-app-dashboard.md): the Hivemill status bar and dashboard tab for the Herdr app; install, settings, Claude/Cursor choice.
- [releases/v0.4.0-announcement.md](releases/v0.4.0-announcement.md): what's new in v0.4.0, in plain English, with two screenshots.
- [releases/v0.5.0-announcement.md](releases/v0.5.0-announcement.md): what's new in v0.5.0, in plain English.
- [releases/v0.5.0-linkedin.md](releases/v0.5.0-linkedin.md): draft LinkedIn post for v0.5.0, to publish after the release exists.
- [images/README.md](images/README.md): light and dark screenshots of the dashboard, built from fictional data.
- [demo-data.md](demo-data.md): how the fictional demo home used for screenshots and examples is generated.
- [film/README.md](film/README.md): storyboard for the customer film, with each claim, its source line and a re-check command.
- [film/herdr/CAPTURE.md](film/herdr/CAPTURE.md): provenance of the Herdr app panel stills used in film scene 7.
- [readme-media.md](readme-media.md): the marked image and video slots in the top-level README and how to swap them.
- [machine-sync.md](machine-sync.md): the owner-run tool that copies allow-listed secret files to a worker machine (chmod 600, never printed).
- [adoption-metrics.md](adoption-metrics.md): read-only measurement of token spend per merged PR.
- [pr-flow.md](pr-flow.md): read-only PR flow report: merges per day, open-to-merge time and stage hours per org.
- [diff-checklist.md](diff-checklist.md): the diff-checklist tool: match checklist items to a diff, render and verify the PR body block.
- [research-team.md](research-team.md): how to give the boss a research question and what report comes back.
- [researcher.md](researcher.md): the researcher role, its per-task research directory and where its findings go.
- [design/g-019/README.md](design/g-019/README.md): static mockups and screenshots for the web UI redesign.
- [design/g-023/README.md](design/g-023/README.md): three draft README layouts for owner preview.
- [design/g-023/readme-option-a.md](design/g-023/readme-option-a.md): README draft A, showcase.
- [design/g-023/readme-option-b.md](design/g-023/readme-option-b.md): README draft B, docs-first.
- [design/g-023/readme-option-c.md](design/g-023/readme-option-c.md): README draft C, the loop.
- [design/macos-linux-deps.md](design/macos-linux-deps.md): Linux-only dependency inventory for the macOS port.
- [design/macos-facts-2026-11-15.md](design/macos-facts-2026-11-15.md): redacted MacBook facts for ADR 0003 stage M0.

## Operate it

### Runner and shared

- [risk-tiers.md](risk-tiers.md): the T0 to T3 risk classifier and its stored, raise-only tier.
- [red-main.md](red-main.md): red-main monitor and merge freeze (flag red_main_freeze).
- [ci-cost.md](ci-cost.md): the $0 CI budget, owner billing and fork-approval steps, the kill switch.
- [ISSUES.md](ISSUES.md): labels, the triage rule that turns public issues into factory tasks, and release milestones.
- [PUBLISHING.md](PUBLISHING.md): the one approved route to a public copy of the repository.
- [RELEASING.md](RELEASING.md): how a release is made, who does each step, and the feature-flag lifecycle.

### Legacy pipeline (paused)

- [operations.md](operations.md): recovery after a VM reboot and how an agent's identity is decided.
- [darwin-python-branches.md](darwin-python-branches.md): the macOS branches of the Python CLI (detached runs, serve pid file, memory/tmp/ps reads) and `tick --deadline`.
- [state-branch.md](state-branch.md): the generated `state` branch that export commits to.
- [state-load.md](state-load.md): build a temp DB, events included, from a `state/` directory with `ci/state_load.py`.
- [db-layout.md](db-layout.md): where the live database sits and why it has its own directory.
- [stopping-a-worker.md](stopping-a-worker.md): the `factory stop` command, who may use it and on whom.
- [unblocking-a-worker.md](unblocking-a-worker.md): getting an agent out of a stuck permission dialog.
- [auto-clear-dialogs.md](auto-clear-dialogs.md): the tick dismisses blocking dialogs so nobody has to click.
- [block-conditions.md](block-conditions.md): `block_conditions` flag (G-040), default off; while off the tick does not change blocked rows.
- [boss-mail.md](boss-mail.md): why the tick never prompts the boss pane directly.
- [CUTOVER.md](CUTOVER.md): the runbook that moves factory development to the public repository.
- [lead-wake.md](lead-wake.md): bringing a parked lead back when its team has work.
- [lead-parking.md](lead-parking.md): parking an idle lead, its wake triggers, caps and rollback.
- [lead-less-dispatch.md](lead-less-dispatch.md): the tick starts workers for ready tasks of parked teams and only mails the lead.
- [idle-watchdog.md](idle-watchdog.md): the tick nudges, relaunches, then alerts both directors about a live worker with no progress.
- [updating-persistent-agents.md](updating-persistent-agents.md): how role changes reach long-running agents.
- [clean-remote-branch.md](clean-remote-branch.md): deleting stale `factory/*` branches on origin safely.
- [refresh.md](refresh.md): fast-forwarding a clean but stale PR branch onto main.
- [refresh-lane.md](refresh-lane.md): `refresh_lane` flag, only the merge-queue head is refreshed.
- [lanes.md](lanes.md): `lane-check`, whether two branches or the open PR heads really conflict (git merge-tree), with exit codes.
- [retro.md](retro.md): how the daily retro loop runs and how to turn it off.
- [red-pr-owner-alert.md](red-pr-owner-alert.md): `red_pr_owner_alert` flag, the tick mails the lead once per red or conflicted head with no coder.
- [stuckpr.md](stuckpr.md): the tick unit that classifies open hivemill PRs and wakes the coder or escalates once when stale.
- [gh-quota.md](gh-quota.md): `pr_view_ttl_s` and `gh_quota_min`, the tick's PR probe cache and low GraphQL quota guard.
- [judge-ci-gate.md](judge-ci-gate.md): judge_ci_gate=none merges without reading PR checks; red-main bisect + revert task (T-1772).
- [merge-queue.md](merge-queue.md): the serial and batch-train merge path (`judge --train` without `--dry-run`).
- [judge-train.md](judge-train.md): `factory judge --train ... --dry-run`, the batch merge train planner (step 1: plan only).
- [mech-merge.md](mech-merge.md): the tick's mechanical merger for T0-T2 PRs (`mech_merge` off/shadow/on), same gate as the judge.
- [fast-lane.md](fast-lane.md): the G-070 fast lane (`--lane fast`, off by default, owner-only switch): mechanical escalation, one fast check, auto-revert and 1-in-10 audit.
- [spend-alert.md](spend-alert.md): the tick mails claude-boss when the last hour's token spend is over $20 or 2x the 24 h hourly average (one alert per hour, no LLM).
- [judge-hold-expiry.md](judge-hold-expiry.md): a manual judge hold on a reviewer+QA PASS PR expires after 2 h unless it names a boss decision (`judge_hold_expiry_min`).
- [prcheck.md](prcheck.md): `factory prcheck`, the coder's pre-PR check, and the `prcheck_required` gate on `factory done` (G-022).
- [local-gate.md](local-gate.md): `factory gate run/show/list`, the local merge gate runner and `gate_runs` table (step 1: not yet read by the judge).
- [model-routing.md](model-routing.md): which model each role starts on, and the `model_<role>` config keys (rollback per role).
- [freshness.md](freshness.md): the per-tick check that flags live agents running outdated role files, plugins or CLI.
- [staleness.md](staleness.md): what to do when main moves during review and QA.
- [verdict-carry-over.md](verdict-carry-over.md): when a PASS verdict survives a merge of main.
- [verdict-gates.md](verdict-gates.md): machine-checked commands that a verdict summary must show passing.
- [spawn-queue.md](spawn-queue.md): the order in which queued spawns start once the gate opens.
- [stale-policy.md](stale-policy.md): `stale_policy=behind_ok`, when judge and spawn pre-check skip refreshing a BEHIND PR.
- [exited-pane-reap.md](exited-pane-reap.md): the tick closes the pane of a worker row that is exited (config `exited_pane_reap_mode`).
- [finishing-coders.md](finishing-coders.md): what `factory done` checks in a coder's worktree.
- [goal-lifecycle.md](goal-lifecycle.md): how a goal's tickets are owned from creation to done.
- [lifecycle.md](lifecycle.md): the mechanical status transitions of goals and tasks.
- [lessons.md](lessons.md): where corrections and incidents are recorded and applied.
- [utilization.md](utilization.md): the live-agent count shown by `factory res`, the tick sample and the web home.
- [terminal-backends.md](terminal-backends.md): Herdr TUI vs herdr-core (`hc`), how to switch `terminal_backend`, caveats.
- [multi-machine.md](multi-machine.md): workers on a second machine (G-057); its `factory-forward` ssh forwarder was removed with the old chain, and the page keeps its description for reference.
- [utilization-alerts.md](utilization-alerts.md): tick alerts to the director and boss for low worker count, starved goals and owner-cap breaches.

## Contribute

### Runner and shared

- [../CONTRIBUTING.md](../CONTRIBUTING.md): how to propose and land a change.
- [STARTER_ISSUES.md](STARTER_ISSUES.md): draft good-first-issue write-ups the owner files once the public repository exists.
- [tests-and-ci.md](tests-and-ci.md): the offline test suite and what CI runs.
- [old-chain-deletion-map.md](old-chain-deletion-map.md): what depends on the old chain (`bin/factory`, web, scout, skilleval, retro, the Go port, systemd) and the order to delete it in.
- [go-toolchain.md](go-toolchain.md): how the Go code is built and tested.
- [scrub-scan.md](scrub-scan.md): the scanner that catches owner-specific or sensitive data.
- [adr/README.md](adr/README.md): index of architecture decision records.
- [adr/0001-language-policy.md](adr/0001-language-policy.md): the language policy (Go, Rust exceptions, Python).
- [adr/0002-sandbox-runtime-v3.md](adr/0002-sandbox-runtime-v3.md): sandbox runtime v3 (hardened nsbox testbox, no microVMs for tests).
- [adr/0003-macos-support.md](adr/0003-macos-support.md): macOS support (sandbox mechanism and port plan).
- [adr/0004-node-esm-hivemill-ios-release.md](adr/0004-node-esm-hivemill-ios-release.md): Node ESM for the hivemill ios-release script.
- [adr/0005-self-hosted-ci-runners.md](adr/0005-self-hosted-ci-runners.md): self-hosted CI runners for trusted PRs (proposed design, nothing installed).
- [adr/0006-worker-sandbox-assessment.md](adr/0006-worker-sandbox-assessment.md): worker sandbox assessment (why go test fails for QA; proposed, nothing installed).
- [adr/0007-hivemill-kit.md](adr/0007-hivemill-kit.md): Hivemill Kit design (standalone skills, three levels, evals and a catalog; proposed).
- [adr/0008-resource-hygiene.md](adr/0008-resource-hygiene.md): deterministic resource hygiene (hm-clean; proposed, nothing installed).
- [adr/0009-option-b-runner.md](adr/0009-option-b-runner.md): ADR 0009 (Accepted): why the runner replaced the chain of agents, with the pilot results and the judge fix.

### Legacy pipeline (paused)

- [contributor-flow.md](contributor-flow.md): how external pull requests are gated, and why fork code never runs before a read pass.
- [unittest-shard-split.md](unittest-shard-split.md): when one module fills a shard, split its test ids across shards by sorted round-robin.
- [testing.md](testing.md): local = fast checks, CI = full; the policy for coder, reviewer and QA.
- [test-cgroups.md](test-cgroups.md): per-run cgroup caps for `factory test` and the user unit the owner installs.
- [cli-layout.md](cli-layout.md): why `bin/factory` is a stub and the CLI code lives in `bin/factory_cli.py`.
- [coder-rules.md](coder-rules.md): the quality bar coders work to.
- [review-rules.md](review-rules.md): rules that keep reviewer, QA and judge verdicts evidence-based.
- [review-workflow.md](review-workflow.md): the multi-angle review used on risky PRs.
- [review-autospawn.md](review-autospawn.md): the tick starts reviewer and QA when a PR opens; reserved review slots.
- [self-review.md](self-review.md): the independent read a coder runs on its own diff.
- [role-rules.md](role-rules.md): how lessons become standing rules in each role prompt.
- [planning-rules.md](planning-rules.md): planning and retry rules carried by per-role skills.
- [test-first-pilot.md](test-first-pilot.md): the pilot where QA writes the failing test first.
- [guard-diff.md](guard-diff.md): compare the worker-guard hook against main before changing it.
- [guard-port.md](guard-port.md): the case map each guard port PR carries, its format and new-name convention, and the `ci/guard_port_check.py` map check.
- [guard-unittest-shard.md](guard-unittest-shard.md): how guard unittest ids are sharded in CI and when a shard step fails the list check.
- [size-classes.md](size-classes.md): the ci-size-class selector: small, medium and large classes, budgets and checks.
- [worker-guard-go.md](worker-guard-go.md): the Go port of the worker-guard hook (opt-in through `guard_impl`, Python stays the default; section status, golden data, lookaround map).
- [cli-go.md](cli-go.md): the Go CLI behind `bin/factory` (opt-in through `cli_impl`, Python stays the default; fast path, shadow mode, build and dependency fetch).
- [schedule.md](schedule.md): owner-defined recurring jobs (`factory schedule`), Go-only dispatch, per-run token caps and overlap with digest, retro and scout.
- [profiling.md](profiling.md): the baseline script for per-call cost measurements.

## Internals

### Runner and shared

- [worker-guard.md](worker-guard.md): the hook that blocks bypasses of prefix-matched deny rules.
- [public-text-guard.md](public-text-guard.md): refuses private data in text that becomes public (commit/PR text, refs, pushed diffs).

### Legacy pipeline (paused)

- [worker-sandbox.md](worker-sandbox.md): the sandbox around workers: design, owner steps, rollout per role, rollback, residual risk.
- [git-hardening.md](git-hardening.md): how `bin/factory` defuses malicious settings in shared git config.
- [settings-templates.md](settings-templates.md): per-role settings that need absolute paths.
- [per-role-plugins.md](per-role-plugins.md): each role loads only its own skills.
- [context-policy.md](context-policy.md): keeping persistent sessions' context from growing unbounded.
- [opus-effort.md](opus-effort.md): the reasoning effort given to agents on Opus.
- [main-freeze.md](main-freeze.md): the tick freezes merging and syncing when public main gets a non-PR commit; only director/boss clears it.
- [spawn-context.md](spawn-context.md): what a spawned agent carries, including the advisor tool.
- [runtimes.md](runtimes.md): agent runtimes (Claude, Cursor) and the two-org model (claude-* and cursor-* side by side).
- [cursor-prompt.md](cursor-prompt.md): `cursor_prompt` v2, the short loop-until-done prompt and kickoff for Cursor workers; the audit behind it and the A/B table.
- [research/cursor-harness.md](research/cursor-harness.md): research behind the Cursor Agent CLI runtime.
- [research/cursor-e2e-issues.md](research/cursor-e2e-issues.md): issues found dogfooding Cursor coder, qa, reviewer and judge.
- [web-design.md](web-design.md): the design pass behind the web UI styling.
- [web-redesign-brief.md](web-redesign-brief.md): G-015 SC4 redesign brief: page audit from the before screenshots, decisions, F-27 tree, task split.
- [web-redesign-review.md](web-redesign-review.md): G-015 SC4 design review: before and after screenshots per page, phone layout, contrast, open visual findings.
- [web-perf.md](web-perf.md): measured cost of the web UI in time, memory and tick impact.
- [web-tokens.md](web-tokens.md): worker roots and read limits for token accounting.
- [skill-scout.md](skill-scout.md): how the scout finds and triages new Claude Code skills.
- [scout-roles.md](scout-roles.md): which roles may drive the scout.
- [scout-tick.md](scout-tick.md): twice-daily scheduling with time and token budgets.
- [scout-pending.md](scout-pending.md): the order in which untriaged candidates are listed.
- [scout-report.md](scout-report.md): how scout results reach director, boss and owner.
- [scout-risk.md](scout-risk.md): which Bash scopes the risk classifier treats as open.
- [scout-vendor.md](scout-vendor.md): vendoring a candidate and the exfiltration pre-screen.
- [scout-eval.md](scout-eval.md): running one sandboxed evaluator trial.
- [scout-nsbox.md](scout-nsbox.md): environment and working-directory rules in the eval sandbox.
- [scout-timestamps.md](scout-timestamps.md): sortable timestamps across DST switches.
- [kit/ADD-A-SKILL.md](kit/ADD-A-SKILL.md): adding a Hivemill Kit skill in one PR, validated by kit-check.
- [skills.md](skills.md): the registered UI/UX skills, how sessions use them and how they are installed.
