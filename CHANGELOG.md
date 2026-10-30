# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

v0.1.0 summarises the changes merged during private development.

## [0.2.0] - Unreleased

Hivemill v0.2.0 is a pre-release: a redesigned web UI, a faster pull request loop and a much harder worker sandbox, on top of the v0.1.0 chain of Claude Code agents.

<!-- VIDEO-PLACEHOLDER: replace the next line with the user-attachments player link once the owner approves the v2 demo video. -->
Demo video: _link added when the v2 recording is approved._

### Highlights

- **New web UI.** A redesigned cockpit with an overview page, goal, task and agent pages, and new insight pages for quality, throughput, resources and token use. Self-hosted font, no external requests, and a phone layout. See [docs/web-ui.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/web-ui.md).
- **Faster pull request loop.** Review and QA can start the moment a PR opens, the merge queue refreshes only its head, agents start without blocking the tick, and a mechanical merger can land low-risk PRs without a judge agent. Tests run on your own machine through a local gate, with CI tiers so a docs change does not run the full suite.
- **Sandbox hardening.** Workers run in an unprivileged user and mount namespace with an allow-list view of the home directory, a scoped GitHub credential, an egress allow-list and sandboxed test and gate runners. See [docs/worker-sandbox.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/worker-sandbox.md).
- **Go quick win.** The first pieces of the worker guard move to Go: a byte-exact port of Python's `shlex`, the guard front end and command analyser, and the git rules. The Go guard is built and tested but not wired in yet; Python stays the default.
- **A new README.** New layout, fresh screenshots and a social preview image.

### New and changed

- **Insight pages.** `/quality`, `/throughput`, `/resources` and `/tokens` show how the factory is doing. Every page renders on the demo database in the render-time benchmark. See [docs/web-ui.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/web-ui.md).
- **Review autospawn.** Opt-in: the tick starts the reviewer and QA at PR open and reserves review slots. See [docs/review-autospawn.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/review-autospawn.md).
- **Mechanical merger.** Opt-in, off by default, with shadow mode first: the tick merges tier T0 to T2 PRs through the same gate checks the judge uses. See [docs/mech-merge.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/mech-merge.md).
- **Local gate.** `factory gate run` records test results per PR head, and `factory test` runs tests in a per-run sandbox with resource caps. See [docs/local-gate.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/local-gate.md).
- **`factory prcheck`.** Catches the usual PR misses before review, and `factory done` requires it.
- **Contributor flow.** How an outside PR is gated and why forks are safe. See [docs/contributor-flow.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/contributor-flow.md).

### Security

- **Worker sandbox.** Namespace allow-list view, per-role GitHub tokens, masked `/run`, an egress proxy host allow-list, and detection of sandbox escapes. See [docs/worker-sandbox.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/worker-sandbox.md) and the [ADR](https://github.com/__PUBLIC_REPO__/blob/main/docs/adr/0002-sandbox-runtime-v3.md).
- **Public text guard.** Pull request text, commits and pushed diffs are scanned for private data, in every task worktree before push and again at merge.
- **Branch protection.** Admin enforcement on the public `main` and a merge freeze when `main` goes red.
- **Worker guard.** Coder pushes are pinned to the coder's own task branch, and more launcher and test-runner routes fail closed.

### Known limits

- **Pre-release.** Interfaces and behaviour may change before 1.0.
- **Worker sandbox.** It is a tripwire, not a boundary against a determined adversary. Read [SECURITY.md](https://github.com/__PUBLIC_REPO__/blob/main/SECURITY.md) first.
- **Go guard.** The Go port of the worker guard is partial and not used by default.
- **Single box.** It is built for one machine, and it needs herdr and Claude Code.

### Upgrade

- **From v0.1.0.** Pull `main` and follow [docs/SETUP.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/SETUP.md). The new features above are off or opt-in by default; the sandbox needs the prerequisites listed there.

### All merged changes since v0.1.0

#### Added

- task add warns on missing [SCn] tag; lead/pm rules (T-0671)
- README: drop hero.webp loop under the player, add Download the MP4 line
- Add visual QA automation for the web UI
- web: show the Needs-you count once on phone; add Memory low health state

#### Changed

- Demo polish: healthy seed, tile icon, hero, screenshots v2 (T-0727)
- T-0332: cleaner live-agent pane binding and closed-but-landed PRs
- T-0329: drive auto-clear Settings Warning test with an injected clock
- T-0522: judge --train runs the batch train on the local gate
- tests: restore process tz after test_auto_clear; director_row re-reads tz (T-0760)
- ci: refresh shard timings from a sharded run on main (T-0541)
- T-0289: bind pending nudge to pane_id and agent start
- T-0757: relaunch finds the host pane of an agent herdr lists without agent_session
- Review autospawn at PR open and reserved review slots (T-0629)
- T-0749: regression test, sandbox launch line is the only claude command path
- tick: sweep stale zz-test-factory-* harness homes (T-0233)
- Review workflow: mark errored slots, role-neutral RULES, self-review PR-open note
- T-0746: sandboxed pane PATH re-export; fail closed on shim shadowing
- Deflake TestPerformance: relative scaling bound instead of 1.0 s wall clock (T-0750)
- T-0739: freeze clock in test_web_insights
- factory prcheck + prcheck_required gate on coder done (T-0625)
- Run the review Workflow by name without the approval dialog (T-0693)
- Run the cleaner on Haiku 5.5 with a config rollback
- tick_async_start: timeout 3001, classify start errors, stub range check (T-0744)
- vendor: bind --rescreen to per-file sha256 in VENDOR.json (T-0736)
- tick: flagless-after-restore check skips evaluators (T-0747)
- T-0358: byte-exact Go port of CPython shlex (G-016)
- Mechanical merger in the tick: mech_merge off|shadow|on (T-0627)
- tick_wake_leads: clear wakedb key when wake_lead spawn is refused (T-0740)
- Pre-screen: scoped allowances + skilleval vendor --rescreen (T-0728)
- refresh_lane: release lane early on red CI/DIRTY owner; clean up lane throttle rows (T-0741)
- tick_async_start: non-blocking agent start, no kickoff sleep (T-0734)
- refresh_lane: only the merge-queue head is refreshed (T-0628)
- tick_wake_leads: lead_wake_debounce_s mail-wake debounce (T-0733)
- T-0738: reap runners under the ended agent's own exiting claude; dedupe TERM/KILL loop
- README screenshots v0.2 phase A: goal page, alt text (T-0480)
- tick: sweep /tmp scratch of finished agents over tmp_sweep_pct (T-0606)
- skilleval: void a terminal trial with --reason infra (T-0729)
- raw_git_violations: cover non-literal git argv forms (T-0694)
- tick: reap_orphans gets its own phase lap (T-0725)
- Tests: run CLI calls in-process for the group A modules (T-0593)
- T-0611: night owner-only blocker prompts the boss pane once
- factory pr create/ready: accept the task's recorded factory/<id>-vN branch (T-0717)
- README: remove headline h1 (T-0730)
- Reap ended agent's test runners on done/stop; alert on old ownerless test runs (T-0615)
- sync_pull: one git fetch of origin/main per call (T-0724)
- test_git_env_hermetic: allowlist test_skilleval_guard_copy for inert remote (T-0732)
- tick_ci_wake: gh probes in a bounded thread pool (T-0723)
- Judge public-text refusal names hit source and category, never the text (T-0710)
- factory test: per-run cgroup caps, unit file draft (T-0681)
- README v2: option A layout, option C headline (T-0712)
- Start-log safety tests: drop redundant p.kill, assert no leftover pgid after ForwardedSignalTests (T-0714)
- Route pre-push and push-pin tests' git through harness.git (T-0721)
- Tests: hermetic git env, local-only remotes, no git wrappers (T-0637)
- README layout drafts: 3 options for owner preview (T-0707)
- Extract judge_gate_and_merge from cmd_judge (re-land T-0624, pure refactor)
- Bundle Inter v4.1 self-hosted font (T-0711)
- Start-log tests: bound fake unshare loop, kill own pgid (T-0701)
- tick: per-phase wall timers behind tick_phase_timers (T-0664)
- factory test: subordinate uid via newuidmap (T-0682)
- nsbox: /run keep-list from real resolv.conf path (T-0697)
- Tick reaps orphan processes of ended workers (dry-run by default) (T-0580)
- gate_sandbox on/rollback/on test in temp FACTORY_HOME (T-0705)
- T-0604: refresh merges use a noreply identity on public repos
- Import scout modules lazily in bin/factory
- visual_qa: blank check counts only --surface-0 (T-0706)
- T-0433: cap evaluator-spawn-retry on persistent spawn failure
- skilleval: comment-only rc in trial HOME (T-0699)
- docs: worker GitHub credential (R-c) in worker-sandbox.md (T-0654)
- docs/web-ui.md: Pages section for the redesigned UI (T-0508)
- nsbox shim: stop before proxy start; forward QUIT/USR1/USR2
- Render-time bench for every web page on the demo DB + no-external-URL test (T-0678)
- factory test: sandboxed test runner, test_sandbox flag (T-0674)
- workerbox: gh token config and scoped ghcred helper in the sandbox (T-0653)
- Tick trailer check: normalise line separators; read noreply_email once (T-0659)
- Web UI: insights pages /quality /throughput /resources /tokens (T-0598)
- Make TestPerformance robust to shard neighbours (T-0691)
- scout: start-log strips U+FFFD/format chars; schedule constant and scout-slot bound
- docs/adr: ADR 0002 sandbox runtime v3 (T-0688)
- G-021 T-0532: contributor flow doc (external PR gate, fork safety)
- bin/factory: stub + factory_cli.py for bytecode caching (T-0660)
- nsbox: mask /run in allow mode (T-0673)
- CI policy: close FORK_TEXT format()/bracket gaps, check uses: pins and owners
- Egress proxy host-set allow-list for worker panes + PDEATHSIG spike notes (T-0676)
- skilleval start-log display: strip C0/DEL/C1 before token redaction, redact only shown lines
- Scout risk: command keys in escaped JSON, toml and yaml configs (T-0407)
- scout: report run with unparsable ended; bound agent-start lookup (T-0690)
- tests: skip env-only export failures with a named reason (T-0540)
- nsbox: run_contained uses the allow-list view
- Trim roles/coder.md under the 10800-byte role budget (T-0692)
- Language policy in role prompts and Go skills (T-0595)
- Allow-list for routine factory commands (T-0613)
- Evaluator start diagnostics: start.log in the trial dir
- factory clean: delete stray branches that are ancestors of a merged PR head (T-0578)
- T-0609: owner-only blockers at night ping the boss at once
- gate: gate_sandbox flag runs gate_cmd under gatebox, fail closed (T-0652)
- Scout report: half-open (since, until] windows, default window includes newest run (T-0579)
- Define test-count rule as merge-tree count vs origin/main (T-0563)
- docs: ADR 0001 language policy and profiling report
- review-diff check: accept spaced 'path: line' refs
- scout: close residual prescreen evasions
- factory clean: remove merged per-task clones and stale sandbox dirs (T-0166)
- scout: count window starts at the agent start; never-spawned runs keep NULL (T-0575)
- scout: close residual MCP gaps in risk.py; flag scripts by executability
- affected: select tests that import a changed module; dirty-worktree regression test (T-0665)
- Drop the Generated with Claude Code footer from PR bodies (T-0643)
- Freshness check nits: per-agent unreadable, failed-event dedupe, digest line without goal, export assert (T-0670)
- web: goals list, goal and task detail pages (T-0597)
- merged-externally: run after_merge(), refuse workers via caller_is_worker (T-0645)
- Risk tier classifier (T0-T3) and factory task tier, escalate-only (T-0622)
- Worker gh credential spawn wiring: worker_gh_roles, fail closed (T-0651)
- qa/coder sandbox on per-task clones: tests and docs (T-0167)
- Freeze merging and syncing on non-PR pushes to public main (T-0639)
- Web UI: overview cockpit and SVG chart primitives (T-0596)
- R-a: enforce_admins on public main, agents cannot change protection (T-0638)
- Prune sandbox-escaped throttle rows of dead processes and older than 7 days (T-0406)
- tick_ci_wake: one batched git ls-remote per tick (T-0663)
- task update --goal: move a task between goals (director/owner); refuse empty updates
- tests: TestNoTickBlocking checks lock wait, not a 0.1s wall-clock bound
- T-0608: dry-run owner-action commands (director R8, boss reject)
- web/tokens: reject hardlinked worker files, cap reads at 64 MiB, skip over-long line tails (T-0573)
- factory done: match the task branch case-insensitively (T-0577)
- Tick freshness check: flag live agents running outdated prompts, settings, plugins or CLI
- ci: refresh shard timings from a green main run (T-0626)
- sandbox_required task flag: spawn refuses without --sandbox, auto-start forces sandbox (T-0511)
- scout/ghcred.py: scoped git credential helper + gh shim (T-0649)
- gatebox: allow-mode gate sandbox profile (T-0650)
- Red-main monitor and merge freeze (T-0623)
- stale_policy=behind_ok: stop refreshing clean BEHIND PRs (T-0630, folds T-0488)
- docs: commit screenshots, hero and social preview (T-0555)
- CI tiers: docs quick pass, affected for normal PRs, full for risky and main (T-0658)
- Judges bypass the load spawn gate (T-0612)
- docs: SETUP.md worker sandbox prereqs, section and troubleshooting (T-0633)
- public_text_guard push: skip owner-term scan in verified image pixel data (T-0617)
- Web UI: lists, tables and filters for /tasks, /agents, /changelog
- Local = fast checks, CI = full (T-0642)
- judge: public squash body drops non-noreply Co-authored-by trailers (T-0616)
- Cutover follow-ups: publish_repo frozen_repos, remote_url tick check, CUTOVER C3/C4 (T-0603)
- spawn pre-check: skip the main refresh for reviewer/qa when the PR merges cleanly (T-0584)
- sandbox: cancel queued spawns that preflight refuses and tell the lead once (T-0590)
- T-0605: task update --merged-externally close path
- docs: fyi mail does not wake a parked lead (T-0621)
- Per-task git clone --shared behind task_clones (T-0569)
- ci: run tests.yml on push to main (T-0614)
- Key the sandbox-escape alert by pid and start time; filter spawn-time check to claude
- docs: worker sandbox design, owner steps, rollout, rollback, residual risk (T-0601)
- nsbox allow mode: mount hide dirs and hide_files by fd (T-0585)
- gate: exempt cleaner from the /tmp spawn gate (T-0610)
- Compact task show / goal show for workers; --full restores the full view
- release notes: absolute __PUBLIC_REPO__ links + test (T-0556)
- docs: web UI redesign mockups and screenshots (T-0572)
- screenshots: 2x capture, content-fit crop, blank-rows check, hero and social preview (T-0554)
- v0.1.0 pre-release notes, release workflow, changelog modes (T-0553)
- judge: noreply author on public merges + tick alert for non-noreply public main (T-0602)
- G-021 T-0550: project update --gh/--repo; allow repo-local user.name and noreply user.email; CUTOVER C3-C5

#### Fixed

- T-0745: worker-guard coverage/uvx/go --C routing + CorpusSanity env fix
- factory pr create|ready wrapper with fixed repo (T-0687)
- Fix test_web_insights: collect tile(definition=...) constants from main's quality view (T-0702)
- Red-main: file revert task under goal working in repo; add factory redmain fix (T-0669)
- Fix red main: --sandbox-required empty-update refusal; don't cancel push CI (T-0667)
- tests: fix 6 baseline failures on main (T-0599)

#### Security

- guard (Go): port the git rules, section 3 (T-0350)
- workerbox: bind the owner scrub-terms file read-only into the worker sandbox (T-0761)
- T-0758: remove one-off qa allow line Workflow(qa-skills:guard-probe)
- worker-guard: fail-closed uvx / uv tool run scan (T-0748)
- worker-guard Go port s2: command analyser (T-0349)
- Add guard-probe saved workflow (qa-only, probe only)
- tests: hermetic FACTORY_HOME for guard tests, guard_diff import path (T-0636)
- guard_diff: --impl go, --roles all, --strict (T-0348)
- worker-guard Go port s1: I/O contract, front end, lookaround matchers (T-0347)
- worker-guard: coder push pin resolves own worktree from task DB (T-0743)
- T-0735: worker-guard unittest_full walker + route go -C/coverage/uv/tox
- worker-guard: pin coder push -C to own worktree (T-0719)
- Harness: silence in-process spawn output, guard temp home/stub (T-0647)
- T-0675: worker-guard routes bare test runners to factory test (test_sandbox=1)
- Evaluator guard copy also carries ci/public_text_guard.py and scrub_scan.py (T-0720)
- Evaluator hook runs from a guard copy inside the trial (T-0718)
- worker-guard: pin coder git push to HEAD:factory/<own task id> (T-0689)
- Pre-push public-text guard in every task worktree (T-0696)
- spawn: export SCRUB_TERMS_FILE to worker roles; config <key> (T-0600)
- evaluator guard: treat blank FACTORY_HOME as unset, document known limits (T-0574)

## [0.1.0] - Initial public release

First public release of the factory, published from a fresh repository.

Hivemill v0.1.0 is a pre-release: the first public cut of an AI factory that runs a chain of Claude Code agents on one box.

### Highlights

- **Chain of command.** An owner talks to a boss, which hands goals to a director, team leads and workers (pm, coder, qa, reviewer, judge, cleaner).
- **Merge gate.** Only the judge merges, and only when the reviewer and QA verdicts pass.
- **One SQLite DB and one CLI.** Every goal, plan, task, message and verdict lives in a single database that you read and write through `factory`.
- **Web UI.** Read-only pages show goals, tasks, agents, throughput and quality.
- **Resource-aware spawning.** Agents start only when the box has the memory and CPU for them.

### Features

- **Setup and operations.** Install, boot units and reboot recovery are described in [docs/SETUP.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/SETUP.md) and [docs/operations.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/operations.md).
- **Verdict gates.** The rules for reviewer, QA and judge verdicts are in [docs/verdict-gates.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/verdict-gates.md).
- **Goal lifecycle.** A request becomes a goal with a plan and success criteria, see [docs/goal-lifecycle.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/goal-lifecycle.md).
- **Worker guard.** Hooks block actions outside a role's job, see [docs/worker-guard.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/worker-guard.md).
- **Web UI.** Pages for goals, tasks and agents are covered in [docs/web-ui.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/web-ui.md).
- **State branch.** Markdown exports and a database dump are pushed to a state branch, see [docs/state-branch.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/state-branch.md).

### Known limits

- **Pre-release.** Interfaces and behaviour may change before 1.0.
- **Worker sandbox.** The per-agent worker sandbox is unfinished. Agent isolation is a tripwire, not a sandbox: it stops honest mistakes and makes bypasses visible, but it does not contain a determined adversary. Read [SECURITY.md](https://github.com/__PUBLIC_REPO__/blob/main/SECURITY.md) first.
- **On-demand agents.** Starting agents on demand is not done yet.
- **Single box.** It is built for one machine, and it needs herdr and Claude Code.

### Install

- **Requirements.** Python 3.11+, git, gh, herdr and Claude Code.
- **Steps.** Follow [docs/SETUP.md](https://github.com/__PUBLIC_REPO__/blob/main/docs/SETUP.md) and the quickstart in the [README](https://github.com/__PUBLIC_REPO__/blob/main/README.md). The command line tool is `factory`.
