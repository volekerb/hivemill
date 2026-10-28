# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

v0.1.0 summarises the changes merged during private development.

## [0.1.0] - Initial public release

First public release of the factory, published from a fresh repository.

### Added

- T-0253: record new/triaged counts on scout_runs when a run ends
- T-0063: CLI blocks: boss-only goal new, no spec edit under live worker, spec-by-msg warning, spawn needs DB spec
- T-0058: factory lesson add/list/show/edit + lessons schema
- Add factory digest and morning tick hook (T-0005)

### Changed

- T-0216: CONTRIBUTING, SECURITY, CODE_OF_CONDUCT, issue templates
- T-0279: judge done needs a recorded outcome; PASS verdict needs --sha
- T-0123: count each distinct path:line ref as a finding in review-diff check
- T-0112: skilleval queue + tick scheduler
- T-0276: pm acceptance-template skill (runnable command + expected result per item)
- T-0218: ci/demo_seed.py demo FACTORY_HOME
- T-0172: staleness pre-check at reviewer/qa/judge spawn (refresh clean-behind, refuse conflict/in-flight, fail open on errors)
- T-0267: scout_triage_per_run config key for `factory scout pending`
- T-0146: PR description standard (template, coder skill, reviewer/judge gate)
- T-0260: per-PR refresh lock; judge/refresh skip a push while another refresh is in flight
- T-0211: LICENSE (MIT, M2492) + THIRD_PARTY.md
- T-0162: sandbox_roles flag, fail-closed preflight, rollback golden test
- T-0235: factory serve re-executes itself when web/, scout/ or bin/factory change
- T-0250: disable the advisor tool for every role + advisor/fable audit
- T-0142: prescreen evasions + tree_problems read cap
- T-0248: web/adoption.py read-only adoption metrics
- T-0131: DST-safe julianday ordering in scout, aware star timestamps, --limit>=1, triage @path inside cwd, worker-branch test
- T-0183: queue order: aging cannot erase priority; merge-only refresh first
- T-0187: web.context per-pr (tokens per merged PR, HQ input per PR)
- T-0116: skilleval collect_trial, metrics, results
- T-0249: pre-pr-check step in coder flow + in_review acceptance-walk check
- T-0163: web/tokens.py indexes sandboxed workers' claude/projects (confined, path-keyed)
- T-0198: allow-mode binds opened component-wise with O_NOFOLLOW, symlink parent refused
- T-0230: judge --merge refreshes a stale-only clean head itself (max 2, STALE-LOOP); n1/n2; judge_ci_wait_s 2400
- T-0251: shard the unittest CI job (4 shards + aggregator 'unittest')
- T-0177: factory clean --remote-branch: safe, logged deletion of stale factory/* branches
- T-0108: skill scout report (per-run to director, digest section, factory scout report), role rules, docs
- T-0159: move factory.db to ~/factory/db/ (self-migrating, FACTORY_DB override)
- T-0138: risk classifier residual Bash scope gaps
- T-0141: trim role prompts: audit/close-goal procedures move to skills; T-0121 nits
- T-0181: Opus agents spawn with --effort medium
- T-0176: factory refresh - mechanical pure-merge refresh of a PR branch
- T-0227: res shows /tmp usage, gate at tmp_max_pct, tick alert + scratchpad auto-clean, qa.md temp-home rule
- T-0201: drop rejection_rate from quality data/JSON; negative task total becomes unknown
- T-0100: cmd_done only closes open tasks
- T-0111: evaluator spawn through the trial sandbox + factory skilleval try
- T-0107: tick-scheduled scout runs + budgets
- T-0150: pre-pr-check coder skill + duplicate-def test + coder.md pointer
- T-0196: CI unittest timeout 35 min + timing tool
- T-0152: judge waits for pending CI, retries base-modified merge, refuses stale base
- T-0161: scout/workerbox.py worker profile (allow-list view, shim, agent dir) + SC1 integration tests
- T-0109: skilleval vendor (allow-list, pinned sha), executable-exclusion check, exfil pre-screen
- T-0160: nsbox allow-list mode (rw/ro/masks, pid=False, keep_env)
- T-0157: agents page filters (status, role, goal) + status counts
- T-0151: plan-quality P4-P8 + worker-brief merge-condition and CI-pending steps
- T-0154: lifecycle drift alerts in the tick + digest Drift line
- T-0155: honest metric labels (bar counts, open tasks, quality KPIs)
- T-0153: auto goal active + task in_progress at spawn
- T-0134: explain token gap vs cost-state (unlogged API calls)
- T-0127: shim_main env allow-list; run_contained refuses hidden cwd
- T-0088: web perf/memory/tick-impact measurements + token cross-check
- T-0106: roles scout + evaluator
- T-0137: lesson lines L-022, L-024, L-025
- T-0095: judge verdict carry-over across pure origin/main merges
- T-0132: scout install-risk fail-closed edge cases
- T-0118: eval sandbox (design D): shim, unshare wrapper, CONNECT proxy, contained runner
- T-0125: AA contrast for .bar labels, restore summary disclosure marker
- T-0133: Sonnet 5.5 cache-read price 0.20 -> 0.10 /MTok; confirmed 5.5 price table
- T-0120: web: negative rework duration becomes unknown in _stages
- T-0105: factory scout fetch (sources, dedupe, stars, install risk)
- T-0086: opt-in factory-serve user unit + install.sh --with-serve + docs/web-ui.md
- T-0124: web review follow-ups (epoch overflow, timeline bound, goals sort, PR opened, link allowance)
- T-0122: L-021 permission dialogs: factory unblock, blocked alert to lead, rule, skill
- T-0087: web design pass (tokens, light/dark, laptop/phone)
- T-0057: block sudo/su/doas/pkexec/docker for all agents (PRIV rule, HQ mode)
- T-0064: tick audit of claude started outside factory spawn (G-006 A6)
- T-0083: web views: teams/agents + resources/gate/director timeline
- T-0085: tokens view (+ cost nits in web/tokens.py)
- T-0104: scout DB schema + factory scout core CLI
- T-0093: tick never prompts the boss; badge + UserPromptSubmit inbox hook
- T-0071: coder and common prompt rules (C1-C10, K1-K2)
- T-0084: web views: throughput, quality, changelog
- G-007 B3: reviewer, QA and judge prompt rules + marker test (T-0070)
- T-0066: role standing rules from lessons + director skills plugin
- G-006 A4: relaunch --when-idle (tick-run), boss may relaunch director, snapshot off on resume (T-0061)
- T-0082: web views: goals, tasks/PR cycle, CI cache
- T-0081: vendor ui-ux-pro-max-skill as G-008 goal plugin (text + CSV only)
- T-0098: judge check gate uses only the latest run per check
- T-0097: sync_pull explicit fetch + ff-merge of origin/main; no for-merge FETCH_HEAD; CI timeout 20
- factory done: coders refuse dirty worktree or unpushed HEAD (T-0069)
- T-0080: web HTTP server, router, escaping helpers, factory serve
- G-004 B4: pm audit section, lead/director lifecycle, pm settings, README, tests
- G-006 A2: lesson link/apply/verify/reopen, judge post-merge hook, digest Lessons section (T-0060)
- G-006 A7: skill-authoring guide + SKILL.md front-matter lint (T-0089)
- T-0079: goal-scoped --plugin-dir for coders (roles/plugins/goals/<goal>)
- G-004 B3: goal close gate + goal status gating (T-0045)
- T-0075: tick dequeue honours goal priority; factory dequeue cancels a queued spawn
- Digest: collapse goals closed >24h ago; drop 'agent lost' when its task finished (T-0050)
- T-0059: per-role --plugin-dir for any role + lead skills plugin
- G-004 B1b: refuse pm closing coder-role non-PR task (T-0055)
- factory verdict: QA PASS requires a machine-checked Gates section (T-0065)
- force_stop returns (result, note); atomic --now claim; FactoryNowError (T-0049)
- T-0077: read-only web data layer (web/db.py, web/queries.py) + tests
- T-0078: token log indexer, attribution, price table
- T-0053: untrack exports/ and state/ on main, ignore them
- CI: test-count gate vs base unless 'tests-removed:' (T-0051)
- stop --now escalates an already-stopping agent to the hard fallback (T-0048)
- T-0052: tick commits exports/+state/ to branch 'state', not main (self-migrating cutover)
- G-004 B2: goal audit + signoff records (T-0044)
- G-004 B1: task reopen, --follow-up-of, pm status narrowing (T-0043)
- tests: make digest tests clock-independent; harness passes TZ to the CLI
- T-0035: factory stop — real STOP for busy workers, hard fallback with confirmed close
- T-0033: CLAIM_TTL_S above place() worst case + staff-engineer length rule
- T-0036: close worker→live-herdr escape E2, identity belt for E1
- T-0031: herdr() role gate for workers
- T-0026: vendor staff-engineer skills + --plugin-dir for staff-engineer
- T-0030: total claude process cap + T-0029 follow-ups
- T-0025: staff-engineer go-live core (spawnable, isolated, reachable by msg)
- T-0028: post-reboot agents lose role flags
- T-0023: export/tick: surface sync failures, push idle state
- T-0009: batch tick state pushes, commit only exports/ and state/
- T-0018: factory done blocks on unread decision/request
- factory clean: safe removal of squash-merged worktrees and branches (T-0007)
- task update: reject dependency cycles (T-0020)
- task update: honour --after (replace depends_on), validate ids (T-0015)
- Identity: fail closed when FACTORY_AGENT is unset in an agent pane (T-0014)
- Digest: Blocked ignores stale judge-blocked on done tasks (T-0012)
- Digest: count tasks closed via factory done; throttle digest-failed (T-0011)
- Spawn gate counts only factory-registered agents (T-0004)
- CI workflow (pull_request only), judge required_checks and post-merge gh error handling (T-0003)
- tests: offline unittest suite for bin/factory (T-0002)

### Fixed

- T-0149: staleness protocol (S1, J4, fix-loop step 6)
- T-0188: terse HQ inbox, inbox --show, limit fix
- T-0184: web/context.py context baseline tool (fixed prefix per role, input per task, HQ context over time)
- T-0072: pm plan-quality and lead fix-loop skills (G-007 B5)
- T-0110: skilleval task suite (fix-bug, add-feature, review-diff)

### Security

- T-0178: worker-guard heredoc delimiter parsing matches bash; fail closed
- T-0225: workerbox nits: link wording, .claude.json cap, argv-secrets doc
- T-0266: deny rule 'advisor' -> 'Advisor' in roles/settings
- T-0229: scrub_scan hardening (per-hit allowlist, bounded EMAIL_RE, history stderr to tempfile, noreply exemption)
- T-0228: deny coder/qa writes to planned top-level OSS files
- T-0068: worker guard blocks mutating git in the shared ~/factory checkout
- T-0210: ci/scrub_scan.py own scrub scanner (tree + history)
- T-0175: deny coder/qa writes to live checkout top level
- T-0135: hardened git helper for all bin/factory git calls
- T-0148: ci/guard_diff.py main-vs-PR hook diff + coder guard-change skill
- T-0102: worker-guard heredoc data, R2 false positives, kill rule
- T-0128: worker-guard: goal audit/signoff are not broad reads
- T-0094: drain-first dequeue order with aging, cancelled-upsert guard, starting refusal, --priority default, audit.log ignore
- WIP T-0096: move README feature sections to docs/, add README section guard test
- T-0091: worker factory report defaults to lead:<team>; on-demand guard; T-0060 nits
- G-006 B2: worker guard blocks claude; Bash(claude:*) deny for HQ/leads (T-0067)
- tests: repo hygiene checks (conflict markers, secret scan, role-prompt budget)
- T-0047: worker guard blocks broad live-home factory reads
- T-0034: stop hardening, flagless marker, worker gate nits, digest clock
- T-0027: coder-task done guard keys on pr_url, not role alone
- T-0032: staff-engineer hardening: lead-only docs, msg-spawn vs tick claim, reuse/retry tests
- Worker guard: PreToolUse Bash hook blocks direct herdr and factory/herdr systemctl (T-0016)
- Boot: systemd user units, install script, cmd_up hardening (T-0006)
