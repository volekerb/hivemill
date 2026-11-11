# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

v0.1.0 summarises the changes merged during private development.

## [0.4.0] - 2026-11-11

Hivemill v0.4.0 is a pre-release. It adds a daily retro report on why work was rejected, an experimental second terminal backend, a one-prompt installer and Claude Code plugin, a per-goal token limit, a redesigned web UI, an optional phone alert feed and a large round of worker-guard hardening, including more of the Go guard. Everything new that acts on its own is off by default or advisory only; the Upgrade section lists the few changes that are active after the upgrade.

### Highlights

- **Daily retro loop.** Once a day the tick starts one advisory agent that reads the day's rejections and gate refusals and files a report with the top causes, evidence and one or two proposed fixes. The report reaches the owner in the daily digest under a `## Retro` heading. The agent cannot merge, push, edit role files or change a gate. It is on by default (`retro_enabled`, `1`); set it to `0` to turn it off. See [docs/retro.md](https://github.com/volekerb/hivemill/blob/main/docs/retro.md).
- **Terminal backend choice.** `terminal_backend` (`auto`, `herdr` or `hc`) selects whether factory drives the herdr terminal UI or the herdr-core `hc` binary. The `hc` backend is experimental and off by default: it is never chosen on its own while `herdr` is installed, `auto` prefers `herdr` when both are on `PATH`. A pane with no reported status is treated as unknown, never idle (the `hc` status signal is mapped, #551). See [docs/terminal-backends.md](https://github.com/volekerb/hivemill/blob/main/docs/terminal-backends.md).
- **One-prompt install.** A Claude Code plugin (`/plugin marketplace add`, `/plugin install hivemill`, `/hivemill:setup`) and a non-interactive installer (`plugins/hivemill/scripts/install.py`) replace the long manual steps; the README install section is three commands. A CI job runs the installer in clean containers. See [docs/SETUP.md](https://github.com/volekerb/hivemill/blob/main/docs/SETUP.md).
- **Go worker guard closer to parity.** The Go guard (opt-in through `guard_impl`; Python stays the default) now mirrors many more rules of the Python guard, and a guard-diff check compares the two. See [docs/worker-guard-go.md](https://github.com/volekerb/hivemill/blob/main/docs/worker-guard-go.md).
- **Per-goal token limit.** `factory goal edit` with `--token-limit N` sets a limit; the tick tallies tokens per goal (`goal_token_tally_s`, `0` = off), warns once at 80% and refuses new coder and pm spawns on a goal that has reached its limit. The limit applies only to goals in the Claude org. See [docs/goal-lifecycle.md](https://github.com/volekerb/hivemill/blob/main/docs/goal-lifecycle.md).
- **Web UI redesign.** Phone-first layouts, a goal-and-team tree with a "needs you" strip on the Overview, cost per merged PR and unattended share on the throughput page, a per-task cost line, and a changelog page (#460, #465, #472, #474, #486, #440, #441, #443).
- **Cost per merged PR.** Before, the web UI showed one average cost per merged PR and a cost line per task. Now the task page shows a lifecycle by stage, tokens and estimated cost per agent and a rework line (sessions of agents that started after the first rejection), a goal page has a "Cost and time" card with task cost and goal overhead apart, and `/throughput` shows the median and p90 cost and tokens per merged PR, and `/tasks` can sort by cost, tokens and wall time (#534-#540). All costs are estimates at list prices from `web/prices.py`, not a bill. See [docs/web-ui.md](https://github.com/volekerb/hivemill/blob/main/docs/web-ui.md).

### New and changed

- **Retro.** New `retro` role and `factory retro` commands (packet, file); every rejection now stores a cause, and `--cause` is required (see Upgrade). Config keys `retro_enabled` (default `1`), `retro_hour` (default `6`, local hour from which the tick may claim the day's retro) and `retro_max_minutes` (default `20`). A missed day is not backfilled (#515, #516, #521, #523).
- **Terminal backends.** `factory doctor` and `factory res` print the configured and the effective backend. On `hc`, `factory spawn` starts agents through a generated spawn script that clears the environment and passes only what the role needs (#485, #489, #495). On `hc` the tick also closes terminals of finished agents. Reviewer and qa rule text now posts with `gh pr comment --body-file` (#418).
- **Install.** The installer takes `--check`, `--dry-run`, `--yes`, `--ref`, `--project-id`/`--project-repo`, `--no-systemd` and `--resume`, never prompts and refuses to run as root. A plugin marketplace manifest and the `/hivemill:setup` skill ship in the repository (#438, #457, #458, #494).
- **Owner alerts.** `owner_alert_feed` (default `0`) records the owner's needs-you list as a deduplicated feed read with `factory alerts`; nothing is sent. `owner_alert_send` (default `0`, needs the feed on) sends each open row over ntfy with fixed fields only; the topic is read from a private file and is never stored in the database. See [docs/boss-mail.md](https://github.com/volekerb/hivemill/blob/main/docs/boss-mail.md) (#439, #442, #453).
- **Boss wake.** The tick wakes the boss when an escalation or request has been unread for `boss_wake_after_s` (default `900`), at most once per `boss_wake_cooldown_s` (default `1800`). Reports never wake it. Alerts about a goal go to that goal's org lead, else its director (#428, #513).
- **Spend and limits.** `goal_token_tally_s` (default `0`), the goal token limit above, `factory goal tokens`, a per-goal token tally on the web UI and a limit-aware dispatch throttle (#445, #446, #447, #497).
- **Pull request loop.** The tick caches PR probes and uses REST check-runs instead of GraphQL (#505, #510), a judge is not auto-started on a head without current verdicts or while the red-main freeze refuses merges (#452, #483), the judge refuses to merge when the agent is stopped or the task is on hold (#500), the hold event is logged once per hold (#451), and coder CI resume gets a role filter, a dropped-resume record and a probe throttle (#437). The mechanical merger waits until the latest push test run on main is green and matches main's current commit (#503), and skips tasks under a status-only judge hold (#499). A judge is also not started on a head that already has current verdicts (#504).
- **Tick resilience.** A failed start of one queued row, or an exception raised while starting one, no longer ends the tick (#455, #502). The tick sweeps stale CI leftovers in `/tmp` (#463). `factory stop` closes leftover shells and the reaper checks panes that share a working directory (#484).
- **Research and review skills.** Research skills require hypotheses, counter-evidence and a verification gate; the multi-angle review skill records the workflow run id; the reviewer skill reports the quick run on the PR head (#436, #480, #464).
- **Other CLI.** `factory inbox --show` lets the sender read its own message (#476). `factory task note` refuses a bad or duplicate task id (#507). Starting a researcher on the Cursor runtime is refused, and start diagnostics are saved when it is unclear whether a start worked (#514). Utilization counts the researcher as a worker (#430). A temp-home tick no longer touches the live checkout (#512).
- **Cursor runtime.** Cursor policy files now live outside every git work tree, so a worker pane at the repository root loads its policy hooks; the adapter logs early-deny paths. A script audits Cursor session logs for tool calls that ran unguarded. A failed Cursor start exits 3, marks the agent exited and saves a redacted scrollback in the start log, and a relaunch is proved by its own pane session (#545, #546, #544, #547, #543, #552).
- **Agent start and identity.** `factory spawn` keeps the bare HQ role names and allows one live boss and one live director per org (#555). The git credential config honours the owner's `includeIf` identity, and the worker sandbox resolves the owner identity in the pane's working directory with `--includes` (#554, #559). When the Claude weekly gate is closed, the boss is offered a move of the goal to the Cursor org (#532). Claude workspace trust is pre-registered for per-task clones under `worktrees/` only (#525).
- **Clean-up.** `factory clean` checks a clone's own tips inside the clone and ignores `refs/replace`, grafts and the commit-graph when it checks clone ancestry, so it cannot delete unmerged work (#548, #549, #550).
- **Config.** `factory config --changed` lists the keys whose live value differs from the default, and `factory config --check` validates every live value and exits 2 if any is bad (read-only; the set path is unchanged).
- **Worker plugins.** Account plugins are disabled on roles other than the boss through `enabledPlugins`, and coder and qa may not edit the live checkout's `plugins/` or the top-level `.claude-plugin` (#491, #450, #461).
- **CI.** The guard-diff test is split across shards, the guard-diff and guard-equiv jobs share a 30 minute cap, binary diffs and new top-level paths count as risky and run the full suite, clone-trust refusals gain tests (#528), and GitHub Actions are updated (checkout, setup-python, setup-go, upload and download artifact) (#481, #471, #479, #467, #487, #488, #490, #522).
- **Docs.** The README describes both runtimes, the user docs and screenshots match this release (#556), the stale boss-wake and goal-token refusal docs are fixed (#526), `docs/worker-guard-go.md` documents the `guard_impl` rollback and `docs/worker-sandbox.md` the live `worker_gh_roles` rollout (#542, #558), and the coder, qa and reviewer guidance asks for no new Python files (#557). New [docs/terminal-backends.md](https://github.com/volekerb/hivemill/blob/main/docs/terminal-backends.md), [docs/retro.md](https://github.com/volekerb/hivemill/blob/main/docs/retro.md), [docs/web-redesign-brief.md](https://github.com/volekerb/hivemill/blob/main/docs/web-redesign-brief.md), [docs/guard-diff.md](https://github.com/volekerb/hivemill/blob/main/docs/guard-diff.md) updates and a documented milestone step in the release steps (#449, #466).

### Security

- **Worker guard (Python).** Restricted roles can no longer: pass `git config` arguments built from a shell expansion, or put a value option before `--get` to get past the key check; close `~user` targets, copy into a parent directory, run `find -delete` or `git init` in a protected tree; run `git push` through `git submodule foreach`, `rebase -x`, `bisect run` or `difftool -x`. Roles that may not push also lose `gh issue`, `gh workflow` and `gh pr` writes (reviewer and qa keep `gh pr comment`), `gh pr/issue/release new`, and `gh repo`, `label`, `secret` and `variable` writes. Hook entries now carry a 120 second timeout and block on failure (#429, #448, #395, #454, #462, #470, #473, #342).
- **Live checkout.** The tick's own git helper refuses `git checkout`, `switch` and most `reset` on the live factory checkout, so a sync path cannot move it off main (#508). The Python guard also refuses live-checkout `git remote` and repo-config writes (including alias, include and push-URL overrides, also when they follow verbose flags), and `send-pack` for the judge and cleaner (#511). The lead's goal-only command runs the same guard descent, closing a trap and git alias hole (#408).
- **Go guard.** Mirrors of the live-checkout remote guard and the retro role refusals (#541, #530, #553), and of the `gh api` non-GET refusal, `env -S`, the git config read rules and expansion refusal, the coder push pin and `xargs`, the `gh` no-push writes, owner-terms brace handling and the earlier flag and archive rules (#433, #427, #432, #496, #498, #519, #509, #518, #390, #520, #517, #501, #493, #468, #434).
- **Owner-terms check.** The brace check ignores words that appear only inside single-quoted `grep`, `rg`, `jq` or `gh --jq` arguments, and handles `cd` before `gh --jq` (#475, #506).
- **Sandbox and alerts.** `owner_alert_send` sends fixed fields only and reads its topic from a private file (#453). Account plugins are off for workers (#491).

### Known limits

- **Pre-release.** Interfaces and behaviour may change before 1.0.
- **hc backend.** Experimental and off by default. In one trial `hc wait` printed its output but did not return, so automation must not rely on it. Worker-guard parity and the full spawn path are not complete for `hc`; keep `terminal_backend` at `herdr` or `auto`.
- **Worker sandbox.** Opt-in through `sandbox_roles` (empty, so off) and unchanged in this release. It is a tripwire, not a boundary against a determined adversary, and has no egress filter. Read [SECURITY.md](https://github.com/volekerb/hivemill/blob/main/SECURITY.md) first.
- **Go guard.** Python stays the default guard; the Go guard is opt-in through `guard_impl` and its parity is still being completed.
- **Retro.** The retro agent runs on the Cursor runtime; a host without Cursor set up gets no daily report. A real Cursor session needs `agent login` or `CURSOR_API_KEY`.
- **Cursor runtime.** Policy hooks for the boss, director and evaluator roles under Cursor are covered by live pane checks of the HQ panes plus configuration and documentation evidence, not by a full session sweep.
- **Single box.** It is built for one machine, and it needs herdr and either Claude Code or Cursor.

### Upgrade

- **Database.** New tables `rejection_incidents`, `retro_runs`, `retro_reports`, `owner_alert_feed` and `goal_token_tally`, and a new column `goals.token_limit` (empty means no limit). The CLI creates them itself on the first run and the migration is idempotent. No table or column is removed or renamed.
- **Breaking: `--cause`.** `factory verdict --fail` and `factory judge --reject` now exit 2 without `--cause`, and record nothing. Allowed values: `unclear_spec`, `bug`, `missing_test`, `stale_or_conflicting_branch`, `wrong_review`, `gate_refusal`, `other`. Update any script or role text that calls them without it (#492).
- **Config.** 9 keys added since v0.3.0, none renamed or removed: `boss_wake_after_s`, `boss_wake_cooldown_s`, `goal_token_tally_s`, `owner_alert_feed`, `owner_alert_send`, `retro_enabled`, `retro_hour`, `retro_max_minutes` and `terminal_backend`. The registry is `bin/flags.json`; run `factory flags` to see all keys.
- **Flags.** No default of an existing key changed. Off by default: `owner_alert_feed`, `owner_alert_send`, `goal_token_tally_s` (`0`) and every `hc` use (`terminal_backend` is `auto`, which keeps `herdr` when it is installed). Active after the upgrade: `retro_enabled` (`1`; `factory config retro_enabled 0` turns the daily retro off), and the boss wake (`boss_wake_after_s` `900`, `boss_wake_cooldown_s` `1800`).
- **Steps.** A live factory needs no checkout: the tick fast-forwards `~/factory` main to the tag commit within a minute (after `git -C ~/factory fetch --tags`, check that main contains the tag: `git -C ~/factory merge-base --is-ancestor v0.4.0 main && echo ok` prints ok); `git checkout v0.4.0` is only for a fresh, non-live install. Then run any `factory` command once so the migration runs, and run `factory flags` to review the keys. To use the daily retro, make sure the Cursor runtime is set up (see [docs/runtimes.md](https://github.com/volekerb/hivemill/blob/main/docs/runtimes.md)); otherwise set `retro_enabled` to `0`. See [docs/SETUP.md](https://github.com/volekerb/hivemill/blob/main/docs/SETUP.md).

### All merged changes since v0.3.0

#### Added

- T-1270: no-new-Python guidance for coder, qa, reviewer (G-041 SC4)
- test: drop dead home continue in HQ add-dir loop
- T-1145: treat new top-level paths as risky full CI

#### Changed

- T-1284: workerbox owner_identity resolves in pane cwd with --includes
- T-1265: spawn keeps bare HQ role names, one live boss/director per org
- T-1259: worker-sandbox.md states the live worker_gh_roles rollout
- T-1262
- T-1264: ghcred gitconfig honours the owner's includeIf identity
- T-1268: v0.4.0 docs: README for two runtimes, user docs pass, regenerated screenshots
- T-1261: relaunch cursor-agent by-pane session proof
- T-1214: map hc OSC 7501 status and never treat missing as idle
- T-1258: factory clean clone ancestry ignores grafts and commit-graph
- T-1257: factory clean ignores refs/replace in clone ancestry checks
- T-1256: factory clean checks a clone's own tips inside the clone
- T-1255: cursor_session_audit follow-ups (since padding, field escaping, outcome labels)
- T-1253: Cursor session-log audit script
- T-1252: Cursor policy dir outside every git work tree
- T-1247
- T-0357
- T-1213
- T-1229: offer claude-boss a goal move to cursor when the Claude weekly gate is closed
- T-1228: factory config --changed and --check
- T-1244: usage follow-ups: Cursor by agents.org, parsed-time row order, goal_usage single scan
- T-1219
- T-1242: /tasks sortable Cost, Tokens and Wall time columns
- T-1243: per-PR metrics lines in docs/web-ui.md and v0.4.0 release note
- T-1241: goal Cost and time card, median/p90 cost and tokens per merged PR on /throughput
- T-1240: /task/<id> lifecycle, cost-and-tokens and rework panel
- T-1239: task_usage()/goal_usage() per-agent tokens, cost, rework share, Cursor rows
- T-1238: task_lifecycle() stages for one task
- T-1235: pin TZ in clock-dependent schedule and blocker tests
- T-1234: FactoryEnv defaults retro_enabled=0 so tick tests do not depend on the clock
- T-1232: tests for clone-trust foreign-uid, race-retry and clean-side refusals
- T-1231: make test_db_layout independent of the wall clock (retro schedule)
- T-1223: draft v0.4.0 release notes and changelog entry
- T-1220: pre-register Claude workspace trust for per-task clones under worktrees/ only
- T-1226: link retro.md from docs index under Operate it
- T-1209: document daily retro loop and config keys
- T-1142
- T-1207
- T-1040: Go mirror of T-0907 coder push pin and xargs
- T-1167: Go mirror of terms_brace_view and dash rule
- T-0990
- T-1206
- T-1200
- T-1199
- T-1198
- T-1155: mirror T-0995/T-1154/T-1156 gh no-push writes in Go
- T-1197
- T-1078
- T-1194: cache tick PR probes with REST check-runs
- T-0836: let terms_brace_view handle cd before gh --jq
- T-0988
- T-0969
- T-0917
- T-1184: keep cmd_tick alive when dequeue spawn raises Exception
- T-1085: refuse judge merge when agent is stopped or task is on hold
- T-1137: Go mirror of T-1097 git config parsed-arg checks
- T-1125
- T-1103: Go mirror of T-1063 git config expansion refusal
- T-1188
- T-1093: README install = 3 commands + link; SETUP documents the installer
- T-1175
- T-0644: disable account plugins via enabledPlugins on non-boss roles
- T-1043
- T-1189
- T-1084: terminal backends (herdr/hc), doctor/res, missing status
- T-1187
- T-1181: bump actions/setup-python to v7 in CI workflows
- T-1172
- T-1180: bump actions/setup-go to v7 in tests workflow
- T-1179: use actions/checkout@v7 in GitHub workflows
- T-1178: actions/download-artifact v4 to v8 in tests workflow
- T-1177: bump actions/upload-artifact to v7 in tests workflow
- T-1153: tick does not autospawn a judge while the red-main freeze refuses merges
- T-1157: multi-angle-review skill: record workflow run id; empty result is not clean-diff evidence
- T-1168: split test_guard_diff across unittest shards by test id
- T-0822
- ci: classify binary numstat diffs as risky tier (T-1166)
- T-1147: reviewer skill S1 reports quick run on PR head, merged-tree check left to CI/judge
- T-1165: allowlist goal-light-phone.png random-byte email false positive
- T-1162: inbox --show lets the sender read its own message
- T-1160: R4 redesign: chart labels, quality/resources phone cards, throughput grid, changelog, width pass
- T-1159: R3 phone cards, task title block, goal Intent and Teams first
- T-1148: R2 F-27 goal-and-team tree and Needs-you strip on Overview, same goal row on /goals
- T-1150: tmp_sweep_harness passes the closed comm list to _proc_holds
- T-0995: no-push roles: gh issue, workflow and pr writes refused; reviewer and qa keep gh pr comment
- T-1146: tick sweeps stale CI leftovers in /tmp
- T-1152
- T-1083: Go mirror git config exact-table read rules (T-1035/T-1064)
- T-1141: R1 shell redesign: stacked phone header, no-wrap switches, Filters disclosure, More on demand, Over cap row
- T-0908: coder push pin covers git submodule foreach, rebase -x, bisect run, difftool -x
- T-1135: a failed start of one queued row no longer ends the tick
- T-1092: hivemill plugin marketplace manifest and /hivemill:setup skill
- T-1091: CI job runs the installer in clean containers
- T-1139: test Host.download uses HttpsOnlyRedirect; curl test independent of tmpdir name
- T-1136: ntfy sender for the needs-you feed (flag off), closed-feed pruning
- T-1090: non-interactive installer plugins/hivemill/scripts/install.py
- T-1098: judge autospawn needs verdicts on the current head; tick resumes count as attempts
- T-1097: git config value option before --get no longer passes the glob check
- T-1117: refuse coder and pm at the goal's token limit (F-74, L3)
- T-1130: judge-hold event once per hold, not every tick
- T-1104: coder_ci_resume_tick follow-ups (role filter, resume-dropped, probe throttle, Cursor test)
- T-1128: milestone step in release steps; setup label in owner commands
- T-1105: research skills: hypotheses, counter-evidence, verification gate
- T-1116: per-goal token limit, one warning at 80% (F-74, L2)
- T-1120: test cmd_tick calls owner_alert_feed_tick (F-50)
- T-1114: per-goal token tally in the tick, factory goal tokens (F-74, L1)
- T-1111: dark needs-you feed and factory alerts (F-50)
- T-1115: Cost per merged PR on /throughput (F-06)
- T-1113: per-task list-price cost line on the task page
- T-1112: Unattended share tile on /throughput (F-06)
- T-1110: owner_attention(con), one narrow needs-you selector
- T-1073
- T-1067: utilization counts researcher as a worker (UTIL_WORKER_ROLES)
- T-1057: reviewer/qa rule text posts with gh pr comment --body-file <path>
- T-1080: tick alerts about a goal reach the goal org's lead, else its director

#### Fixed

- T-1254: log adapter early-deny paths; fix policy_workspace docstring
- T-1230: fix stale boss-wake and goal-token refusal docs
- Fix tick check-runs gh api to use GET with -f query params.
- T-1176: Web UI SC4 visual fixes (G-015)
- T-1164: R6 redesign evidence: after screenshots, review doc, screenshot tool fixes
- T-1144: deny coder/qa edits to top-level .claude-plugin (fixes red main)
- T-1140: R0 redesign brief, 44 before screenshots, screenshots --evidence, web-design.md fix

#### Security

- Skip retro in guard-equiv --roles all until T-1219.
- T-1208: retro role guard (SC3)
- T-1134: limit-aware dispatch throttle, owner-alert hardening, token limit cleanup
- T-0983: guard closes ~user targets, copy into a parent, find -delete, git init for every protected tree
- T-0985: Go guard mirror of T-0808 (python flags, ar @file, python -c extract, inline writer)
- T-1190: split hc guard parity from PR #485
- T-1038: Go mirror gh api non-GET method guard (T-0987/T-1030)
- T-1118: reap same-cwd guard; stop closes leftover shells
- T-0960: worker-guard hook entries: timeout 120 and onFailure block
- T-1071: Go mirror env -S/--split-string deny (T-1037)
- T-1121: worker-guard: owner-terms brace check ignores single-quoted grep/rg/jq/gh --jq words
- T-1156: worker-guard: gh repo/label/secret/variable writes for no-push roles; -R-first frozen gap
- T-1154: worker-guard: gh pr/issue/release new count as create
- T-1158: guard-diff job cap 20 to 30 min, same as guard-equiv
- T-1149: docs/guard-diff.md: guard_diff ignores modes; untagged runs every role plus hq
- T-1132: deny Edit/Write on live checkout plugins/ for coder and qa
- T-1063: worker-guard refuses git config args built from an expansion

## [0.3.0] - 2026-11-05

Hivemill v0.3.0 is a pre-release. It adds a versioned release process and flag registry, a second agent runtime (Cursor) with support for several orgs side by side, a tighter pull request loop and a lot more worker-guard hardening. Most new automations are opt-in and off by default; a few small housekeeping changes are active after the upgrade, and the Upgrade section lists them and how to turn them off.

### Highlights

- **Cursor runtime and two orgs.** Roles can run on Cursor instead of Claude Code, and two orgs (`claude` and `cursor`) can run side by side with their own boss and director. Both are off by default: `agent_runtimes` is empty and `orgs_enabled` is `claude`. See [docs/runtimes.md](https://github.com/volekerb/hivemill/blob/main/docs/runtimes.md) (#242, #255, #269).
- **Pull request loop.** A coder can hand a pull request to review while CI is still running (`coder_ci_handoff`), the judge can wait for the main push run (`judge_wait_main_run`), reviewers and QA can start automatically for a new green head (`review_autospawn`, still off by default), and the tick skips optional GitHub probes when the API quota is low. The two merge and handoff switches are off by default; the quota guard (`gh_quota_min`, `300`) is active (#379, #400, #229, #384).
- **Worker-guard hardening.** Many more ways for a restricted worker to reach `git push`, `gh` writes, git config, protected trees or archive extraction are refused, and a Go version of the guard can run behind a shim (`guard_impl`, default `python`). See [docs/worker-guard.md](https://github.com/volekerb/hivemill/blob/main/docs/worker-guard.md) and [docs/worker-guard-go.md](https://github.com/volekerb/hivemill/blob/main/docs/worker-guard-go.md) (#234, #241, #359, #358, #319).
- **Versioned releases.** A `VERSION` file, `factory --version`, and a release workflow that refuses a tag that does not match `VERSION`. The process is in [docs/RELEASING.md](https://github.com/volekerb/hivemill/blob/main/docs/RELEASING.md) (#213, #215).
- **Flag registry and release gate.** `bin/flags.json` lists every config key with its default and state, and `factory flags` prints it. `ci/release_check.py` checks a merge commit against the release rules and prints the owner's tag commands (#219, #220, #269).
- **Issue intake.** Opt-in (`issue_intake_repos`, empty and so off by default): the tick sends the director one message per new open public issue. See [docs/ISSUES.md](https://github.com/volekerb/hivemill/blob/main/docs/ISSUES.md) (#225).

### New and changed

- **Cursor runtime.** `agent_runtimes` takes `role=runtime` pairs or `*=cursor`; `cursor_pilot_roles` is the older alias. Cursor agents get their own isolated policy, count in the spawn gate and `factory res`, skip the Claude usage caps, and show as a runtime chip in the web UI. Spawns are refused when a pane limit is recorded, and the `evaluator` role always stays on Claude (#242, #255, #236, #262, #308, #315, #397).
- **Orgs.** `orgs_enabled` (default `claude`) lists the orgs whose boss and director `factory up` keeps running; `org_prefix_default` (default `0`) gives the default org prefixed agent names too. Each goal belongs to one org, the daily digest is grouped by org, and mail to a bare `boss` or `director` goes to the sender's own org (#242, #317, #403, #426).
- **Researcher role.** A new `researcher` role answers one research question from public sources in a sandbox, with six research skills; docs in [docs/researcher.md](https://github.com/volekerb/hivemill/blob/main/docs/researcher.md) and [docs/research-team.md](https://github.com/volekerb/hivemill/blob/main/docs/research-team.md) (#247, #250, #263, #284).
- **Pull request loop.** `coder_ci_handoff` (default `off`; `resume` or `fresh`, and it needs `review_autospawn` on) lets a coder finish at PR open and the tick starts review at green; `coder_ci_resume_max` (default `2`) limits restarts on a red head. `judge_wait_main_run` (default `0`) makes merges wait for a green main push run. `red_pr_owner_alert` (default `0`) mails the lead about a red or conflicted PR with no coder. `idle_coder_reap` (default `off`, also `dry-run` and `on`) stops an idle coder with a clean tree and a pushed head. `pr_view_ttl_s` (default `120`) caches the tick's PR probes and `gh_quota_min` (default `300`) skips optional probes when the GraphQL quota is low. A lead or director can hold a task from the judge. A mechanical merge is held while QA has a pending live run. `behind_ok` no longer applies when main changed a file the PR also changes (#379, #400, #363, #398, #384, #393, #321, #386, #324).
- **Tick and housekeeping.** `exited_pane_reap_mode` (default `dry-run`, so it only records what it would close; `on` closes the pane of a worker that has exited), `state_dump_interval_s` (default `3600`) throttles state-branch exports, and the red-main alert names the real repo and re-checks a red repo every tick. A dirty tracked file in the live checkout alerts the director once (#349, #280, #245, #346).
- **Install and upgrade.** Setup installs from the latest release tag, and [docs/SETUP.md](https://github.com/volekerb/hivemill/blob/main/docs/SETUP.md) has an Upgrade section (#214).
- **Issues and releases.** `ci/release_check.py` also checks the Cursor runtime (#269). [docs/ISSUES.md](https://github.com/volekerb/hivemill/blob/main/docs/ISSUES.md) describes the triage labels (#216, #218).
- **CLI.** `factory task note` refuses an empty body, `factory task update --spec` and `--acceptance` refuse empty input, `factory task update --role` changes a task's role, and `factory spawn --task` takes the goal and team from the task (#419, #327, #330, #314).
- **CI.** The guard-diff job runs on guard-path pull requests, and the guard unit tests run in three shards (#328, #407).

### Security

- **Worker guard.** Restricted roles can no longer assign `HOME`, `PATH` or `GIT_*` through shell builtins, reach git config through a `.git` gitfile or an archive extract, run interpreter inline code or stdin that launches `git push` or `gh` writes, or use `env -S`. Coders must use a literal git subcommand and may push only their own branch. `gh api` write methods, `gh release` writes and `~/.local/bin` are protected, and worker settings now call one guard shim (#210, #222, #212, #241, #413, #416, #359, #340, #391).
- **Researcher sandbox.** The researcher always runs in the worker sandbox, and may use `gh api` only for GET (#266, #292).
- **Cursor.** Cursor commit and PR attribution is switched off, and Cursor's MCP and edit tools follow the same guard rules (#279, #291).

### Known limits

- **Pre-release.** Interfaces and behaviour may change before 1.0.
- **Worker sandbox.** Opt-in and not a security boundary: it is a tripwire, not a boundary against a determined adversary, and it has no egress filter. Read [SECURITY.md](https://github.com/volekerb/hivemill/blob/main/SECURITY.md) first.
- **Go guard.** Python stays the default guard; the Go guard is opt-in through `guard_impl`.
- **Cursor.** A real Cursor session needs `agent login` or `CURSOR_API_KEY` on the host, and the Claude process cap counts Claude processes only.
- **Single box.** It is built for one machine, and it needs herdr and either Claude Code or Cursor.

### Upgrade

- **Database.** Three columns are added, and the CLI adds them itself on the first run (the migration is idempotent): `goals.org` (default `claude`, so existing goals stay in the default org), `agents.org` and `tasks.judge_hold_reason`. Nothing is removed or renamed.
- **Config.** 16 keys added since v0.2.0, none renamed or removed: `issue_intake_repos` and `agent_runtimes`, `coder_ci_handoff`, `coder_ci_resume_max`, `cursor_pilot_roles`, `exited_pane_reap_mode`, `gh_quota_min`, `guard_impl`, `idle_coder_reap`, `idle_coder_reap_s`, `judge_wait_main_run`, `org_prefix_default`, `orgs_enabled`, `pr_view_ttl_s`, `red_pr_owner_alert`, `state_dump_interval_s`. The registry is `bin/flags.json`.
- **Flags.** No default of an existing key changed. The new flags (`agent_runtimes`, `cursor_pilot_roles`, `coder_ci_handoff`, `idle_coder_reap`, `judge_wait_main_run`, `red_pr_owner_alert`, `guard_impl`) are off, or keep the old behaviour, by default. Active after the upgrade: `gh_quota_min` (`300`; set `0` to never read the quota), `pr_view_ttl_s` (`120`; `0` turns the cache off), `state_dump_interval_s` (`3600`; `0` exports every tick), `exited_pane_reap_mode` (`dry-run`; `off` stops the log entries) and the live-checkout dirty-file alert to the director (no setting). The only wording change in the registry is the summary of `red_main_interval_s`; its default is still `120`. Run `factory flags` to see the full list. See [docs/SETUP.md](https://github.com/volekerb/hivemill/blob/main/docs/SETUP.md).

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
