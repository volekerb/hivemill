# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

v0.1.0 summarises the changes merged during private development.

## [0.3.0] - 2026-10-30

Hivemill v0.3.0 is a pre-release: a versioned release process, a flag registry, opt-in GitHub issue intake and a release gate, plus more worker-guard hardening. The new automation is opt-in and off by default.

### Highlights

- **Versioned releases.** A `VERSION` file, `factory --version`, and a release workflow that refuses a tag that does not match `VERSION`. The process is in [docs/RELEASING.md](https://github.com/volekerb/hivemill/blob/main/docs/RELEASING.md).
- **Flag registry.** `bin/flags.json` lists every config key with its default and lifecycle state, and `factory flags` prints it. A test fails when a config key is missing from the registry.
- **Issue intake.** Opt-in (`issue_intake_repos`, empty and so off by default): the tick sends the director one message per new open public issue. See [docs/ISSUES.md](https://github.com/volekerb/hivemill/blob/main/docs/ISSUES.md).
- **Release gate.** `ci/release_check.py` checks a merge commit against the release rules and prints the commands that tag it. It matches the version exactly, checks for leftover placeholders more widely, and names the run directory in its reminder.

### New and changed

- **`factory --version`.** Prints the contents of `VERSION`.
- **`factory flags`.** Lists config keys with default, current value and state. See [docs/RELEASING.md](https://github.com/volekerb/hivemill/blob/main/docs/RELEASING.md).
- **Issue intake.** `issue_intake_repos` takes a comma list of `OWNER/NAME`. Each new open issue produces one message, and a failed send is retried on the next tick. Off by default.
- **Install and upgrade.** Setup installs from the latest release tag, and [docs/SETUP.md](https://github.com/volekerb/hivemill/blob/main/docs/SETUP.md) has an Upgrade section.
- **Release notes template.** `ci/release-notes/TEMPLATE.md` fixes the five sections every release uses.
- **Issue labels and triage.** [docs/ISSUES.md](https://github.com/volekerb/hivemill/blob/main/docs/ISSUES.md) describes the triage, accepted and needs-info labels, and the issue templates add the triage label.
- **Test harness.** Test children accept the harness's own git config isolation, and the hermetic check accepts the temp root a home was created under.
- **Worker sandbox docs.** [docs/worker-sandbox.md](https://github.com/volekerb/hivemill/blob/main/docs/worker-sandbox.md) lists the protection facts after cutover and the preflight flags.

### Security

- **Worker guard.** Coder `git` subcommands must be a literal word. Roles that may not push can no longer assign `HOME`, `PATH` or `GIT_*` through `read`, `printf -v`, `mapfile`, `for` or `getopts`, or reach git config through a `.git` gitfile or an archive extract.

### Known limits

- **Pre-release.** Interfaces and behaviour may change before 1.0.
- **Worker sandbox.** Opt-in and not a security boundary: it is a tripwire, not a boundary against a determined adversary, and it has no egress filter. Read [SECURITY.md](https://github.com/volekerb/hivemill/blob/main/SECURITY.md) first.
- **Go guard.** The Go port of the worker guard is partial and not used by default.
- **Single box.** It is built for one machine, and it needs herdr and Claude Code.

### Upgrade

- **Database.** No schema change since v0.2.0.
- **Config.** One key added: `issue_intake_repos` (default empty). No key renamed or removed.
- **Flags.** No default changed. The new `issue_intake_repos` is off by default; every opt-in feature of v0.2.0 stays off until you turn it on. See [docs/SETUP.md](https://github.com/volekerb/hivemill/blob/main/docs/SETUP.md).

### All merged changes since v0.2.0

#### Added

- Tick issue intake: one director message per new public issue (flag issue_intake_repos, T-0797)
- docs: ISSUES.md labels, triage rule, milestones; templates add triage label (T-0796)

#### Changed

- release_check: exact smoke version, spec placeholder regex, freshbox cwd (T-0811)
- tests/harness: assert_hermetic accepts the temp root the home was created under (T-0807)
- Flag registry bin/flags.json + factory flags + coverage test (T-0793)
- tests/_gitenv: normalise pane config/credential env instead of refusing it (T-0804)
- ci/release_check.py: release gate that prints the owner tag commands (T-0794)
- docs/worker-sandbox.md: post-cutover protection facts, preflight flags (T-0634)
- tests/_gitenv: accept the harness's own config-isolation env so unsandboxed gate children can import it (T-0801)
- docs/ISSUES.md: owner creates only triage/accepted/needs-info labels with descriptions (T-0802)
- VERSION file, factory --version, release.yml tag/VERSION check (T-0792)
- Install from the latest release tag; SETUP Upgrade section; release notes TEMPLATE (T-0795)
- docs: RELEASING.md release process and flag lifecycle (T-0791)

#### Security

- issue_intake_check: guard event() in except; write seen markers after send succeeds (T-0810)
- worker-guard: no-push roles may not use .git gitfile or archive extract to reach git config (T-0783)
- worker-guard: coder git subcommand must be a literal word (T-0775)
- worker-guard: no-push roles may not assign HOME/PATH/GIT_* via read, printf -v, mapfile, for, getopts (T-0771)

## [0.2.0] - 2026-10-30

Hivemill v0.2.0 is a pre-release: a redesigned web UI, a faster pull request loop and a much harder worker sandbox, on top of the v0.1.0 chain of Claude Code agents. Most new automation is opt-in and off by default.

### Highlights

- **New web UI.** A redesigned cockpit with an overview page, goal, task and agent pages, and new insight pages for quality, throughput, resources and token use. Self-hosted font, no external requests, and a phone layout. See [docs/web-ui.md](https://github.com/volekerb/hivemill/blob/main/docs/web-ui.md).
- **Faster pull request loop.** Review and QA can start the moment a PR opens (opt-in), the merge queue refreshes only its head, agents start without blocking the tick, and an opt-in mechanical merger can land low-risk PRs without a judge agent. A project can run its tests on your own machine through a local gate (set per project), and CI runs in tiers so a docs change does not run the full suite.
- **Opt-in worker sandbox.** Workers can run in an unprivileged user and mount namespace with an allow-list view of the home directory, a worker-only Claude login and sandboxed test and gate runners. It is off by default (`sandbox_roles` is empty). See [docs/worker-sandbox.md](https://github.com/volekerb/hivemill/blob/main/docs/worker-sandbox.md).
- **Go quick win.** The first pieces of the worker guard move to Go: a byte-exact port of Python's `shlex`, the guard front end and command analyser, and the git rules. The Go guard is built and tested but not wired in yet; Python stays the default.
- **A new README.** New layout, fresh screenshots and a social preview image.

### New and changed

- **Insight pages.** `/quality`, `/throughput`, `/resources` and `/tokens` show how the factory is doing. Every page renders on the demo database in the render-time benchmark. See [docs/web-ui.md](https://github.com/volekerb/hivemill/blob/main/docs/web-ui.md).
- **Review autospawn.** Opt-in (`review_autospawn`, off by default): the tick starts the reviewer and QA at PR open and reserves review slots. See [docs/review-autospawn.md](https://github.com/volekerb/hivemill/blob/main/docs/review-autospawn.md).
- **Mechanical merger.** Opt-in (`mech_merge`, default `off`, with a shadow mode first): the tick merges tier T0 to T2 PRs through the same gate checks the judge uses. See [docs/mech-merge.md](https://github.com/volekerb/hivemill/blob/main/docs/mech-merge.md).
- **Local gate.** `factory gate run` runs a project's gate command and records the result per PR head; it applies once a project sets `gate_cmd`. `factory test` runs tests in a sandboxed runner with resource caps (`test_sandbox`, default `0`). See [docs/local-gate.md](https://github.com/volekerb/hivemill/blob/main/docs/local-gate.md).
- **`factory prcheck`.** Catches the usual PR misses before review. `prcheck_required` (default `0`) makes `factory done` require it.
- **Red-main freeze.** Opt-in (`red_main_freeze`, default `0`): merges stop while `main` is red. See [docs/red-main.md](https://github.com/volekerb/hivemill/blob/main/docs/red-main.md).
- **Contributor flow.** How an outside PR is gated and why forks are safe. See [docs/contributor-flow.md](https://github.com/volekerb/hivemill/blob/main/docs/contributor-flow.md).

### Security

- **Opt-in worker sandbox.** Namespace allow-list view, worker-only login, masked `/run` and detection of sandbox escapes. Per-role GitHub token files are supported; scoping those tokens is part of your own setup, see [SECURITY.md](https://github.com/volekerb/hivemill/blob/main/SECURITY.md) and [docs/worker-sandbox.md](https://github.com/volekerb/hivemill/blob/main/docs/worker-sandbox.md). Design: [ADR 0002](https://github.com/volekerb/hivemill/blob/main/docs/adr/0002-sandbox-runtime-v3.md).
- **Public text guard.** Pull request text, commits and pushed diffs are scanned for private data, with a pre-push hook installed in task worktrees and a check again at merge.
- **Branch protection.** The publish script sets admin enforcement on the public `main`, and the tick alerts when it is missing.
- **Worker guard.** Coder pushes are pinned to the coder's own task branch, and more launcher and test-runner routes fail closed.

### Known limits

- **Pre-release.** Interfaces and behaviour may change before 1.0.
- **Worker sandbox.** Opt-in and not a security boundary: it is a tripwire, not a boundary against a determined adversary, and it has no egress filter. Read [SECURITY.md](https://github.com/volekerb/hivemill/blob/main/SECURITY.md) first.
- **Go guard.** The Go port of the worker guard is partial and not used by default.
- **Single box.** It is built for one machine, and it needs herdr and Claude Code.

### Upgrade

- **From v0.1.0.** Pull `main` and follow [docs/SETUP.md](https://github.com/volekerb/hivemill/blob/main/docs/SETUP.md). The opt-in features above (sandbox, review autospawn, mechanical merger, local gate, `prcheck_required`, red-main freeze) stay off until you turn them on. The guard and CI changes apply on upgrade: the pre-push public-text hook is installed on every spawn, coder pushes are pinned, CI tiers and the branch-protection alert are active, agent start is non-blocking and the merge queue refreshes head-only. The sandbox needs the prerequisites listed in that guide.

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

- **Setup and operations.** Install, boot units and reboot recovery are described in [docs/SETUP.md](https://github.com/volekerb/hivemill/blob/main/docs/SETUP.md) and [docs/operations.md](https://github.com/volekerb/hivemill/blob/main/docs/operations.md).
- **Verdict gates.** The rules for reviewer, QA and judge verdicts are in [docs/verdict-gates.md](https://github.com/volekerb/hivemill/blob/main/docs/verdict-gates.md).
- **Goal lifecycle.** A request becomes a goal with a plan and success criteria, see [docs/goal-lifecycle.md](https://github.com/volekerb/hivemill/blob/main/docs/goal-lifecycle.md).
- **Worker guard.** Hooks block actions outside a role's job, see [docs/worker-guard.md](https://github.com/volekerb/hivemill/blob/main/docs/worker-guard.md).
- **Web UI.** Pages for goals, tasks and agents are covered in [docs/web-ui.md](https://github.com/volekerb/hivemill/blob/main/docs/web-ui.md).
- **State branch.** Markdown exports and a database dump are pushed to a state branch, see [docs/state-branch.md](https://github.com/volekerb/hivemill/blob/main/docs/state-branch.md).

### Known limits

- **Pre-release.** Interfaces and behaviour may change before 1.0.
- **Worker sandbox.** The per-agent worker sandbox is unfinished. Agent isolation is a tripwire, not a sandbox: it stops honest mistakes and makes bypasses visible, but it does not contain a determined adversary. Read [SECURITY.md](https://github.com/volekerb/hivemill/blob/main/SECURITY.md) first.
- **On-demand agents.** Starting agents on demand is not done yet.
- **Single box.** It is built for one machine, and it needs herdr and Claude Code.

### Install

- **Requirements.** Python 3.11+, git, gh, herdr and Claude Code.
- **Steps.** Follow [docs/SETUP.md](https://github.com/volekerb/hivemill/blob/main/docs/SETUP.md) and the quickstart in the [README](https://github.com/volekerb/hivemill/blob/main/README.md). The command line tool is `factory`.
