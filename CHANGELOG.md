# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

v0.1.0 summarises the changes merged during private development.

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

### All merged changes since v0.2.0

#### Added

- T-0894: add researcher to interp-inline NO_PUSH test set
- review_autospawn: R+Q for a new green head after an earlier round (T-0812)
- Tick issue intake: one director message per new public issue (flag issue_intake_repos, T-0797)
- docs: ISSUES.md labels, triage rule, milestones; templates add triage label (T-0796)

#### Changed

- T-1058: factory task note refuses an empty body
- T-1004: tick restarts the handed-off coder on a red head (coder_ci_handoff)
- T-1065: agent CI watchers poll every 60 s (--interval 60)
- T-1075: drift mail routes to the goal org; lead bare HQ address resolves in own org
- T-1070: unittest-shard timeout 20 to 30 min (shard 1 had no margin)
- T-0907: coder push pin: alias via send-pack, --config-env alias, xargs git config
- T-1055: judge --reject --no-code-change keeps the task in_review
- T-1054: read-only roles' test-count/merge-base rule text uses literal ids
- T-1048: review autospawn waits for coder/prcheck, restarts missing role, binds verdict head
- T-1018: Go mirror of uv run/uvx unwrap (uv_check), uv own-push pin, corpus and goldens
- T-1044: red_main alert names the real repo slug
- T-1025: behind_ok does not apply when main changed a file the PR also changes
- T-1014: cache the tick's PR probes, skip optional ones when GraphQL quota is low
- T-1010: stale-policy doc names the tests.yml exception and current reason
- T-1031: multi-angle-review rejects a non-numeric pr before any agent starts
- T-0863: deliver bare boss/director to legacy HQ rows under org_prefix_default
- T-1001: Go mirror of T-0805 tar/unzip extraction grammar
- T-1034: exited-pane-reap doc: mode on needs T-1033 and director approval, names shared-worktree limit
- T-1028: tick stops idle coders with a clean tree and pushed PR head
- T-1020: multi-angle-review stops on bad args before any agent starts
- T-1005: reap_exited_panes closes the host's pane id, not the row's stale one
- T-1029: judge waits for the main push run (flag judge_wait_main_run)
- T-0870: refuse cursor spawns when pane limit is recorded
- T-0987: no-push roles: gh api with a non-GET method is a gh write
- T-1021: refresh shard timings, oversize-module pack rule
- T-1007: verify the host pane's name before closing a row's pane_id
- T-1012: mech merger honours judge_hold_reason on the task
- T-1011: test_guard_impl_coverage knows test_lessons_flow
- T-1003: coder_ci_handoff flag (coder done at PR open, review at green)
- T-1000: behind_ok does not apply when tests.yml changed on main since the merge-base
- T-0991: judge merged-tree check via git -C task worktree
- T-0992: hook-only roles/ merges do not mark HQ stale
- T-0998: Go nopush inline gh-write reason matches Python (T-0881)
- T-0986: direct released-row test, pane_gone False/None after close
- T-0881: no-push roles: gh api body flags and gh release writes are gh writes
- T-0982: red-PR owner alert (red_pr_owner_alert, default off)
- T-0979: prcheck evidence fence opener/closer per CommonMark; 'failed in a row' not output
- T-0957: ~/.local/bin is a protected tree for workers
- T-0978: release_stale_pane keeps pane_id when the close is not confirmed
- T-0961: no-push roles refused before the public-text push scan; git timeout in public_text_guard
- T-0970: prcheck evidence: unclosed fence fails closed, exact heading, N failed prose
- T-0952: qa.md step 2 verifies PR head; Q1 merge-base in two steps
- T-0974: dispatch-refused mail is kind report (wakes parked lead); document it
- T-0976: live-run pending marker holds numbered/checkbox/bold forms; combined brief carries the done wording
- T-0972: tick closes the live pane of a worker row that is exited
- T-0973: scout: non-UTF-8 gh output raises GhError (one candidate fails, not the source)
- T-0950: live-run pending marker accepts bullet/quote/backtick prefix; combined agent brief gets Q7
- T-0967: tick alerts director once on a dirty tracked file in the live checkout
- T-0837: notify coder on judge reject with in_progress status
- T-0918: mech merger holds a PR with a QA live-run: pending line; qa.md Q7
- T-0966: dispatch_tick applies the qa no-PR/no-worktree refusal, once per task
- T-0954: prcheck evidence: heading mention, indented/~~~ fences, N failed marker
- T-0956: qa spawn needs a PR, worktree or cwd; qa done BLOCKED reopens the task
- T-0941: task update --role applies with checks and a history event
- T-0951: pr-description step 11, coder C9 and no-PR message point at factory pr create
- T-0946: risk tier: roles/*.md and merge-gate defs are sensitive
- T-0947: prcheck body check: Test evidence needs a command line plus an output line
- T-0937: task update --spec/--acceptance - refuses empty stdin
- T-0939: evidence review as fresh researcher; one heading list; sandbox docs name researcher
- T-0835: judge hold flag blocks judge autospawn
- T-0924: prcheck check 5 never runs ci/affected.py for gate_fast_projects; defers to green PR CI
- T-0864: daily digest grouped by org
- T-0923: done accepts merged task; clean keeps worktree of live-pane agent
- T-0893
- T-0922: deflake test_serve_mem_fn_honours_the_pin (mock live mem reader)
- T-0827: count Cursor agent processes in spawn gate and flagless audit
- T-0859: factory res per-org agent mix (G-028 SC5)
- T-0920: spawn --task derives agent goal_id/team_id from the task row
- T-0916: affected selects test_shard.py on deleted tests / shard_timings change
- T-0359: render settings before sandbox/worktree prep; cancel a queued spawn once on render failure
- T-0913: affected --quick hard wall-clock cap + QUICK_SKIP
- T-0130: re-queue keeps queued_at for queued/starting rows
- T-0874: researcher gh api only when method resolves to GET
- T-0144: role-file merge notice to director only
- T-0197: prepare_worktree cleanup on failed checkout, control test, docs
- T-0905: M16816 load gate on every local suite path
- T-0911: spawn --name help wording + prefix-off/cursor tests
- T-0304: project remote rule fails closed on NUL repo_path, duplicate rows, dot gh_repo parts
- [T-0872] spawn --name refused when the org rule would change it
- T-0799: coder push pin covers xargs git and git -c alias.X
- docs(T-0823): align runtimes and cursor-harness with multi-runtime main
- T-0902: throttle state-branch dumps to state_dump_interval_s
- T-0850: docs/research-team.md and index line
- T-0903: one local suite box-wide while load1 >= load cap (M16816)
- T-0834: disable Cursor commit/PR attribution in spawned cli.json
- T-0849: research flow skills, boss/_common/delivery lines, tests
- T-0205: ci/state_load.py builds a temp DB from state/ including events
- T-0833: isolate Cursor policy under run/cursor/<agent>
- T-0847: researcher always runs in the G-010 worker sandbox
- T-0202: tests.<module> from repo root; QA serve --port 0
- T-0860: auto-Enter after Cursor agent prompt
- T-0900: isolate workflow transcripts in web.adoption so per-role attribution matches pre-#272
- T-0857: reviewer kickoff carries the Gated PR line (multi-angle-review)
- [T-0853] skilleval positive control: marker plugin + factory skilleval control
- T-0856: count Workflow subagent tokens (subagents/workflows/<run>) in web.adoption
- [T-0848] research-skills plugin: six text-only researcher skills + tests
- T-0862: G6 cursor-runtime release gate and freshbox probe
- T-0817: cursor spawns skip Claude usage caps
- T-0895: drop stale test_delegate_search_skill.py from shard_timings
- T-0263: tick alerts director when installed factory-serve unit drifts from main
- [T-0851] Scout: pin to claude org, follow the spawned row, unknown tokens are NULL
- T-0878: Cursor limits and time spent in header, Overview, Resources
- [T-0855] Roll back T-0244 delegate-search coder skill (0/69 uses)
- T-0876: prcheck affected run takes a test slot and waits
- [T-0845] researcher role: prompt, settings, registration, research dir
- T-0868: overview lanes count active teams with parked leads
- T-0873: factory done accepts green CI on PR head instead of local affected run
- [T-0858] factory-serve Restart=always; reapers skip the unit's cgroup without systemctl
- T-0865
- red_main: re-check every tick while a repo is red (T-0841)
- [T-0869] usage(): parse five_hour/seven_day per field; weekly cap enforced when 5h is empty
- T-0819
- [T-0867] Lead wakes and dequeues skip only the CPU-load gate
- [T-0839] Prune judge_ci:<url> cache rows of closed or old PRs in the tick
- spawn: drop --resume when the runtime changed (legacy cursor lead respawned in the claude org) (G-028)
- Two orgs (claude + cursor) side by side, and the Cursor runtime on main (G-027 T-0819, G-028)
- cleaner: drop dead pane_reused term in pane_holds; rename stale leftover-pane test (T-0764)
- T-0829: web runtime chip (Claude/Cursor) + tests (G-027 Cursor dogfood)
- review autospawn: throttle the gh probe per dead task; transient gh failure logs a probe-error event (T-0816)
- review autospawn/re-run: skip dead or foreign PRs (T-0815)
- review_rerun_tick: send the fyi for the reviewer started when qa is gate-refused; docs for the re-run pass (T-0814)
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

#### Fixed

- T-1016: red_main alert names the stored fix task
- T-1024: uv-wrapped bare pytest stays refused; fix stale test/corpus/Go-test expectations
- T-0904: validate events.jsonl keys against table columns; fix live_homes doc wording

#### Security

- T-1064: worker-guard: restore real git config reads lost in #411
- T-1037: worker-guard: deny env -S, --split-string and every prefix for worker roles
- T-0882: worker-guard scans interpreter stdin code for no-push roles
- T-1035: worker-guard: git config value-taking options no longer hide a write
- T-1062: guard-diff base-tool archive carries systemd/install.sh
- T-0331: guard allows factory init / project add / install.sh --dry-run in scratch homes (coder, qa)
- T-1030: worker-guard: pin gh api -X forms, refuse X-HTTP-Method-Override write
- T-0999: docs/guard-diff.md names guard-equiv; manual-run is python-only
- T-0993: Go guard mirror of T-0881 (gh api body flags, gh release writes, no-push roles)
- T-0964: worker-guard env value options and check_scrub forms
- T-1039: guard-unittest in 3 test-id shards plus fan-in
- T-1008: guard-equiv path steps compare base and HEAD routed lists
- T-0984: run test_worker_guard_public_text.py under GUARD_IMPL=diff in CI
- T-0945: worker-guard treats -m zipfile.__main__/tarfile.__main__ as zipfile/tarfile for no-push extraction
- T-1009: guard-equiv routed-rest modules run in parallel
- T-0356: worker settings call the worker-guard shim (27 lines), HQ stays python3
- T-0989: guard-equiv path list covers guard test sources; all-skipped module not green
- T-0925: worker-guard protected trees: globs, writer flags, cd then write, Go trees, live go/bin/roles, Edit tools
- T-0435: guard refuses full pytest runs; -k allowed
- T-0880: worker-guard unwraps uv run / uvx before git/gh write checks
- T-1002: guard-change rule for Python-only guard PRs under guard-equiv
- T-0975: guard_diff hook field types, newline event name, Go section note
- T-0355: guard-equiv CI job (strict Go gate) + hook latency report
- T-0808: worker-guard: close T-0783 follow-up bypasses
- T-0981: raise real-corpus guard_diff test timeout 300s -> 600s (matches neighbours)
- T-0955: guard-diff doc heading, go-only note, ALLOWLIST entry
- T-0977: guard_impl seam seeds run/guard-export.json under go/diff
- T-0968: CI split guard-unittest and guard-diff jobs
- T-0826: worker-guard: env long-option prefixes and zsh precommand modifiers
- T-0965: guard_diff settings_rules fails closed on malformed shapes; automode key carried whole
- T-0354: build+stamp Go guard in factory up/tick; guard_impl config -> run/guard_impl
- T-0949: risk tier: Go guard paths and merge/verdict defs are sensitive
- T-0944: guard_diff settings compare covers ask, autoMode, hook timeout
- T-0948: README and settings-templates point at the CI guard-diff job
- T-0928: CI runs ci/guard_diff.py on guard-path PRs
- T-0327: guard_diff settings compare covers hook matcher/event, env, removed templates
- T-0353: sh shim worker-guard with guard_impl flag, stamp fallback and shadow mode
- T-0806: worker-guard: block factory flags like factory config
- T-0805: worker-guard: no-push roles may not extract archives holding .git or config+HEAD
- T-0887: port the public-text checker and rule to the Go guard
- T-0339: factory src: caller row from the live home, scrub GIT_DIR, TOCTOU documented
- T-0885: Go guard mirror of the no-push interpreter inline-code rule (#241)
- T-0914: prune 12 stale scrub tree-allowlist entries
- T-0883: protect run/guard-export.json from worker writes
- T-0320: factory_remote_url hardening: reject dot segments, config set validates shape, live_home docstring
- T-0560: scrub_scan summary says N hits not allowlisted, M allowlisted
- T-0912: load_slot_cap sys.path once, guard_diff parses args before load gate, restore FACTORY_LOADAVG
- T-0906: drop dead scrub allowlist entry; warn on unused tree-allowlist entries
- T-0871: per-entry env in guard_corpus.json (push-pin agent cases)
- T-0828: Cursor MCP hook + edit-tool guard parity
- T-0232: test harness hygiene: FactoryEnv ctor cleanup, live-home guard
- T-0558: define factory-root placeholder, drop unused scrub allowlist entry
- T-0875: guard_diff extracts base ci/*.py next to the base hook
- [T-0767] Go guard reads exported DB config + agent goals; no Python spawn for git/gh writes
- T-0559: scrub_scan high-entropy-id with bounded lookback
- T-0877: scrub_scan: templated systemd unit names are not emails
- [T-0846] worker-guard: researcher role (no-push, public clone into research dir, Edit/Write only there)
- T-0861: CI guard-diff job and cursor suite checks
- worker-guard: no-push roles may not run interpreter inline code that launches git push or gh writes (T-0789)
- T-0830: Go guard mirror of T-0788 zsh path + env unset rules
- [T-0844] worker-guard: coder push pin accepts the claude-/cursor- org prefix (G-010, G-028)
- guard_diff: scratch_fixture tolerates losing the linked/l symlink race (T-0825)
- worker-guard: no-push roles may not assign zsh path or unset/clear protected env (T-0788)
- Go guard: mirror T-0783 gitfile + archive-extract refusal for no-push roles (T-0809)
- GUARD_IMPL=python|go|diff for every guard test + coverage check (T-0352)
- release_check: freshbox reminder passes --repo/--ref, quoted cd; scrub_terms_file from config (T-0813)
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
