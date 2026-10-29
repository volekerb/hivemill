# Documentation

Every page under `docs/`, grouped by what you want to do. New to the factory? Start with the setup guide, then read the walkthroughs, both listed first below.

## Use it

- [SETUP.md](SETUP.md): install the factory on a Linux box and bring up the first agents.
- [adopt/ADOPT.md](adopt/ADOPT.md): a prompt you hand to your own Claude Code so it performs the setup for you.
- [examples/README.md](examples/README.md): overview of three worked examples with real command output.
- [examples/01-goal-to-merged-pr.md](examples/01-goal-to-merged-pr.md): follows one owner request all the way to a merged PR.
- [examples/02-handling-a-rejection.md](examples/02-handling-a-rejection.md): what happens when review fails and the judge rejects.
- [examples/03-digest-and-scorecard.md](examples/03-digest-and-scorecard.md): the morning digest and the per-team scorecard.
- [web-ui.md](web-ui.md): the dashboard started by `factory serve` and what each panel shows.
- [images/README.md](images/README.md): light and dark screenshots of the dashboard, built from fictional data.
- [demo-data.md](demo-data.md): how the fictional demo home used for screenshots and examples is generated.
- [adoption-metrics.md](adoption-metrics.md): read-only measurement of token spend per merged PR.
- [pr-descriptions.md](pr-descriptions.md): the template every pull request body follows.
- [design/g-019/README.md](design/g-019/README.md): static mockups and screenshots for the web UI redesign.

## Operate it

- [operations.md](operations.md): recovery after a VM reboot and how an agent's identity is decided.
- [state-branch.md](state-branch.md): the generated `state` branch that export commits to.
- [db-layout.md](db-layout.md): where the live database sits and why it has its own directory.
- [stopping-a-worker.md](stopping-a-worker.md): the `factory stop` command, who may use it and on whom.
- [unblocking-a-worker.md](unblocking-a-worker.md): getting an agent out of a stuck permission dialog.
- [auto-clear-dialogs.md](auto-clear-dialogs.md): the tick dismisses blocking dialogs so nobody has to click.
- [boss-mail.md](boss-mail.md): why the tick never prompts the boss pane directly.
- [ci-cost.md](ci-cost.md): the $0 CI budget, owner billing and fork-approval steps, the kill switch.
- [CUTOVER.md](CUTOVER.md): the runbook that moves factory development to the public repository.
- [lead-wake.md](lead-wake.md): bringing a parked lead back when its team has work.
- [lead-parking.md](lead-parking.md): parking an idle lead, its wake triggers, caps and rollback.
- [lead-less-dispatch.md](lead-less-dispatch.md): the tick starts workers for ready tasks of parked teams and only mails the lead.
- [updating-persistent-agents.md](updating-persistent-agents.md): how role changes reach long-running agents.
- [clean-remote-branch.md](clean-remote-branch.md): deleting stale `factory/*` branches on origin safely.
- [refresh.md](refresh.md): fast-forwarding a clean but stale PR branch onto main.
- [judge-train.md](judge-train.md): `factory judge --train ... --dry-run`, the batch merge train planner (step 1: plan only).
- [local-gate.md](local-gate.md): `factory gate run/show/list`, the local merge gate runner and `gate_runs` table (step 1: not yet read by the judge).
- [freshness.md](freshness.md): the per-tick check that flags live agents running outdated role files, plugins or CLI.
- [staleness.md](staleness.md): what to do when main moves during review and QA.
- [verdict-carry-over.md](verdict-carry-over.md): when a PASS verdict survives a merge of main.
- [verdict-gates.md](verdict-gates.md): machine-checked commands that a verdict summary must show passing.
- [spawn-queue.md](spawn-queue.md): the order in which queued spawns start once the gate opens.
- [finishing-coders.md](finishing-coders.md): what `factory done` checks in a coder's worktree.
- [goal-lifecycle.md](goal-lifecycle.md): how a goal's tickets are owned from creation to done.
- [lifecycle.md](lifecycle.md): the mechanical status transitions of goals and tasks.
- [lessons.md](lessons.md): where corrections and incidents are recorded and applied.
- [utilization.md](utilization.md): the live-agent count shown by `factory res`, the tick sample and the web home.
- [utilization-alerts.md](utilization-alerts.md): tick alerts to the director and boss for low worker count, starved goals and owner-cap breaches.
- [PUBLISHING.md](PUBLISHING.md): the one approved route to a public copy of the repository.

## Contribute

- [../CONTRIBUTING.md](../CONTRIBUTING.md): how to propose and land a change.
- [STARTER_ISSUES.md](STARTER_ISSUES.md): draft good-first-issue write-ups the owner files once the public repository exists.
- [tests-and-ci.md](tests-and-ci.md): the offline test suite and what CI runs.
- [coder-rules.md](coder-rules.md): the quality bar coders work to.
- [review-rules.md](review-rules.md): rules that keep reviewer, QA and judge verdicts evidence-based.
- [review-workflow.md](review-workflow.md): the multi-angle review used on risky PRs.
- [self-review.md](self-review.md): the independent read a coder runs on its own diff.
- [role-rules.md](role-rules.md): how lessons become standing rules in each role prompt.
- [planning-rules.md](planning-rules.md): planning and retry rules carried by per-role skills.
- [test-first-pilot.md](test-first-pilot.md): the pilot where QA writes the failing test first.
- [guard-diff.md](guard-diff.md): compare the worker-guard hook against main before changing it.
- [scrub-scan.md](scrub-scan.md): the scanner that catches owner-specific or sensitive data.
- [go-toolchain.md](go-toolchain.md): how the Go code is built and tested.
- [profiling.md](profiling.md): the baseline script for per-call cost measurements.

## Internals

- [worker-guard.md](worker-guard.md): the hook that blocks bypasses of prefix-matched deny rules.
- [worker-sandbox.md](worker-sandbox.md): the sandbox around workers (status: stub).
- [git-hardening.md](git-hardening.md): how `bin/factory` defuses malicious settings in shared git config.
- [settings-templates.md](settings-templates.md): per-role settings that need absolute paths.
- [per-role-plugins.md](per-role-plugins.md): each role loads only its own skills.
- [context-policy.md](context-policy.md): keeping persistent sessions' context from growing unbounded.
- [opus-effort.md](opus-effort.md): the reasoning effort given to agents on Opus.
- [public-text-guard.md](public-text-guard.md): refuses private data in text that becomes public (commit/PR text, refs, pushed diffs).
- [spawn-context.md](spawn-context.md): what a spawned agent carries, including the advisor tool.
- [web-design.md](web-design.md): the design pass behind the web UI styling.
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
