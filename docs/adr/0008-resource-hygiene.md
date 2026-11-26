# ADR 0008: Deterministic resource hygiene (hm-clean, Kit skill clean)

## Status

Proposed, 2026-11-17. Goal: G-060 (owner request M39644). Owner decisions 1-3 in the goal are final and are inputs here, not open questions. This ADR becomes Accepted only with the owner's approving message id recorded in this section.

## Context (measured by the owner, 2026-11-17)
- Mac mini: 109/460 GB free. ~/.herdr/worktrees 81 GB (owned by the owner's other team), ~/.herdr/build8-evidence 16 GB, DerivedData 11 GB, CoreSimulator 5.4 GB, one project repo 60 worktrees / 193 branches, swap 1.9/3 GB.
- Linux dev host: 117/197 GB free. ~/factory/worktrees 95 worktrees 28 GB, ~/.claude/projects 8.5 GB, ~/.cache 5.3 GB, swap 2.1/4 GB.
- MacBook: 129/926 GB free (personal data, out of scope); swap 6.9/8 GB, so memory is the problem there.
- Today: `factory clean` (bin/factory_cli.py cmd_clean, line ~13797) handles only merged factory task worktrees and branches and runs only when the director spawns a cleaner. The tick's /tmp sweep (tmp_sweep, tmp_sweep_harness; extended by open PR 795 / T-1698) covers factory scratch in /tmp only. Everything is Python and bound to /proc. Nothing cleans Docker, Xcode, simulators, Go/npm caches, logs, transcripts or orphan worktree dirs. Nothing works on macOS or without a factory.

## Decision summary
One Go binary, `hm-clean`, with no factory DB, shipped as Hivemill Kit skill `clean` (plugin `hivemill-clean`, dir plugins/kit-clean/). Rules are data in one JSON config. Each run measures, writes a plan, then (in auto mode) applies the plan, re-checking every item just before it deletes it. Safety predicates are built into each rule kind and fail closed. A daily launchd job (macOS) or systemd user timer (Linux) runs it as the owner's user. Privileged kinds (Docker prune, remote branch delete, process kill) run only from that scheduled job and never from inside an agent. Signals (free disk, swap, reclaimable bytes) go to a status file that the factory owner-alert path, the factory Resources view and the Herdr app Resources card read. `factory clean` and the tick /tmp sweep stay as they are (coexist).

## A. Command shape
Binary: go/cmd/hm-clean. Code: go/internal/kit/clean/... (ADR 0007 section G layout). Language: Go (ADR 0001 default for new tools; the binary must run on macOS without Python). No new Go module dependency (go/go.mod stays without require). `go list -deps ./cmd/hm-clean` must not contain go/internal/factorydb or modernc.org/sqlite (ADR 0007 section C rule).

Global flags: `--config PATH` (default env HIVEMILL_CLEAN_CONFIG, then ~/.config/hivemill/clean.json), `--json`, `--version`. State dir: XDG_STATE_HOME/hivemill/clean, default ~/.local/state/hivemill/clean, on Linux and macOS. It holds logs/, plans/, status.json, status.env and the first-run latch confirmed.json.

Subcommands:
- `measure` - JSON by category: for every enabled rule: rule id, kind, bytes, count, oldest age (days), reclaimable bytes (items that pass every predicate now), skipped count with reasons; plus disk (path, total, free bytes, free %), swap (total, used, %), memory (available), and the totals. Read-only. Writes status.json/status.env.
- `plan` - every candidate item: rule, kind, path or ref, bytes, age, reason (which rule matched and why it is eligible), identity (see D), and every predicate result. Skipped items are listed with the failed or unknown predicate. Writes plans/<UTC timestamp>.json and prints its path. Read-only on the targets.
- `apply [--plan FILE]` - with no --plan it builds a fresh plan and writes it first (owner decision 2: every run writes its plan before it applies). It walks the plan in order, re-runs every predicate and compares identity for each item just before deleting it; a changed or now-failing item is skipped with a reason. Stops at the byte cap; the rest is "deferred (cap)". Writes logs/apply-<ts>.json with bytes before/after per item and disk free before/after.
- `confirm --plan FILE` - first-run latch: records that the owner reviewed this plan (sha256 of the file, time). Until it exists, `apply` and `run --mode auto` refuse with exit 3 and the line `hm-clean: first run is plan-only; review <plan> and run hm-clean confirm --plan <plan>`.
- `check` - evaluates the signal thresholds against the current host; exit 0 ok, 4 warn, 5 critical (1 error, 2 usage as in ADR 0007 F). Writes status.json/status.env.
- `run --scheduled [--mode plan|auto]` - the scheduled job's entry: measure -> plan -> (auto and confirmed: apply) -> check -> notify. Mode default comes from the config `mode` (shipped default `plan`).
- `schedule install|remove|status [--mode plan|auto] [--at HH:MM]` - writes/loads the launchd agent or systemd user timer (section G).
- `config validate` - strict load of the config with all kinds registered; exit 0 or 1 with one line per problem.

Exit codes: 0 ok, 1 error, 2 usage, 3 refused (root, first run unconfirmed, agent context for a privileged action, config newer than binary) - the fixed ADR 0007 F set - plus 4 warn and 5 critical for `check` and `run` only. 4 and 5 are an extension of ADR 0007 F, recorded here.

## B. Config schema (JSON, data not code)
One file, strict (unknown fields refused), loaded with go/internal/kit LoadConfig (G-059 T-1663: integer schemaVersion, refuses a newer one). Schema v1 top-level fields:
- schemaVersion: 1
- profile: linux-host | macmini | macbook-memory | custom
- mode: plan | auto (default plan)
- caps.runBytes: hard per-run byte cap, required, 1 MiB .. 500 GiB
- roots: list of {name, path}; path absolute after ~ expansion; "/" and the home dir itself refused
- signals.disk: path, warnFreeGB, warnFreePct, critFreeGB, critFreePct; signals.swap: warnUsedPct, critUsedPct
- notify_local: bool
- rules: list of rule objects:
  - id: unique, [a-z0-9-] 1..40 chars
  - kind: one of section C
  - enabled: bool
  - root: a name from roots (required for file kinds, absent for command/memory kinds)
  - selector: glob, depth, exclude list
  - minAgeDays: age = newest mtime inside the item (not the dir's own mtime)
  - capBytes: optional per-rule cap, <= caps.runBytes
  - action: delete | report | kill | shutdown | rotate | command (the kind decides which are legal)
  - predicates: ADDITIONAL predicates; the kind's built-in ones can never be removed
  - params: kind-specific object, validated strictly by the kind
Unknown kind, unknown predicate, root not listed, path outside its root, duplicate id, cap above caps.runBytes, an action the kind does not allow -> `config validate` error and every subcommand refuses (exit 1).
Profiles, embedded in the binary as go/internal/kit/clean/profiles/*.json and written to ~/.config/hivemill/clean.json by the skill on first install:
- linux-host (Lenovo, with factory): roots ~/factory/worktrees, ~/factory/logs, ~/.claude/projects, ~/.cache/go-build, ~/.npm/_cacache, /tmp (temp patterns only); evidence list empty; Docker rules on; remote-branch rule on for factory/* on the factory origin.
- macmini (macOS, no factory): roots ~/.herdr/worktrees, ~/.herdr/build8-evidence (evidence list), ~/Library/Developer/Xcode/DerivedData, ~/Library/Developer/Xcode/Archives, ~/Library/Developer/Xcode/iOS DeviceSupport, ~/Library/Developer/CoreSimulator/Caches, ~/Library/Caches/go-build, ~/.npm/_cacache, TMPDIR; remote-branch rule OFF (the other team's remotes are not ours).
- macbook-memory (macOS laptop): no delete rules; memory class only (report), signals on swap and disk.

## C. Rule kinds
Every kind has built-in predicates (cannot be disabled) and lists the actions it allows. "Holder check" = no process has a file open under the item and no process cwd is inside it (procx). Kinds register themselves (one file per kind, init-time Register), so kind tasks share no file.

- git-worktree: entries of `git worktree list --porcelain` of the repos under a root. Built-in: path inside root; not the main worktree; not locked; `git status --porcelain` empty (no uncommitted or untracked); HEAD is an ancestor of origin/<default> OR a PR for the branch is MERGED with headRefOid == HEAD; no OPEN PR for the branch (gh); no process cwd inside; age >= minAgeDays. Action delete = `git worktree remove` (never --force).
- orphan-worktree-dir: dirs directly under a worktree root that no `git worktree list` of the configured repos names. Built-in: not a worktree of any configured repo; if it holds its own .git, the same clean + merged checks; holder check; age. Action delete.
- local-branch: refs/heads/* of configured repos minus the default branch and any branch checked out in any worktree. Built-in: merged (ancestor of origin/<default> OR its PR MERGED with headRefOid == sha); no OPEN PR; not checked out anywhere. Action delete = `git update-ref -d refs/heads/<b> <sha>` (atomic, fails if the ref moved).
- remote-branch (PRIVILEGED): origin branches matching params.pattern (default ^factory/[A-Za-z0-9._/-]+$, max 200 chars). Built-in = Go port of bin/factory_cli.py _rb_check (lines ~13914-14035): name pattern + check-ref-format; ls-remote sha; no OPEN PR (same-repo `gh pr list --state all --head`); a PR whose headRefOid == sha is MERGED or CLOSED and refs/pull/N/head == sha (content recoverable), or the stray-branch rule (_rb_stray_pr: an ancestor of a MERGED PR head of the same task id); not checked out in any worktree. The DB-only checks of _rb_in_use (task not terminal, agent live) are replaced by: no worktree has it checked out and no process cwd is in a worktree of it. Port, do not call Python. Action delete = `git push --porcelain --force-with-lease=refs/heads/<b>:<sha> origin :refs/heads/<b>`; the local branch is deleted only if it equals or is an ancestor of sha (as _rb_delete_local).
- path-age: items matched by selector under a root (DerivedData, Archives, iOS DeviceSupport, CoreSimulator caches). Built-in: inside root; lstat only; age >= minAgeDays; holder check; params.keepNewest N (DeviceSupport keeps the newest 2). Action delete.
- evidence: like path-age, but only items listed by exact path in params.paths (the config list); anything not listed is never touched. Action delete.
- temp-pattern: files/dirs in a temp root matching params.patterns. Built-in: age >= minAgeDays; holder check (Linux /proc/*/fd and cwd; macOS lsof); owner uid == our uid; on a factory box the tick's own /tmp scratch patterns are excluded (they stay with tmp_sweep). Action delete.
- transcript: ~/.claude/projects/<proj>/*.jsonl and subagent dirs. Built-in: age >= minAgeDays (default 60; on the linux-host it must be >= the factory token-index window, consumer go/internal/tokens/index.go); never the newest file of its project dir; holder check. Action delete.
- cache-cap: a tool cache over params.maxBytes. Built-in: the tool's own clean command exists (`go clean -cache`, `npm cache clean --force`); size measured without following symlinks. Action command (the tool's own clean; never hand deletion inside a cache).
- log-rotate: files matching params.patterns under a root. Built-in: size >= params.maxBytes or age; rotation = copy to <name>.1.gz then truncate in place (copytruncate, so a writer holding it open keeps working); keeps params.keep rotations, deletes older ones. Action rotate.
- simctl-unavailable (macOS only): `xcrun simctl list -j devices` entries with isAvailable false. Built-in: xcrun present. Action command = `xcrun simctl delete unavailable`.
- docker-prune (PRIVILEGED): `docker system df --format json` for measure. Built-in: docker CLI present and `docker info` succeeds as our user (never sudo); params.until required (default 168h). Action command = `docker image prune -af --filter until=<u>`, `docker builder prune -af --filter until=<u>`, `docker container prune -f --filter until=<u>`.
- memory-sim-booted: booted simulators. Built-in: booted >= params.minHours; no xcodebuild/simctl client process using it. Action report (default) or shutdown (`xcrun simctl shutdown <udid>`).
- memory-stray-process: processes matching params.names (node, xcodebuild, go test binaries). Built-in: same uid; age >= params.minHours. Action report only.
- memory-ended-agent (kill is PRIVILEGED): processes that CLEARLY belong to an ended agent. Action report (default) or kill.

"Clearly belongs to an ended agent" = ALL of: (1) same uid as hm-clean; (2) re-parented to init/launchd or the systemd --user manager (PPID 1 or the user manager pid), so its starting shell/agent is gone; (3) its cwd is inside a configured agent-workspace root (e.g. ~/factory/worktrees/<x>, ~/.herdr/worktrees/<x>, /tmp/claude-<uid>/<x>); (4) no live agent process (comm/argv0 basename claude or cursor-agent, or argv containing @anthropic-ai/claude-code) has its cwd in the same workspace top dir; (5) its name matches params.names; (6) age >= params.minHours (default 2). Kill = SIGTERM, wait 10 s, re-read pid start time, SIGKILL only if pid and start time are unchanged. Anything failing one test is only reported.

## D. Safety model
- Configured roots only: every item path is resolved component by component with lstat inside its root (Go os.Root, openat-style; go 1.27). `..`, absolute escapes and symlinks are refused. A symlink is never followed: a symlink item inside the root is removed as a link only; its target is never touched. The walk does not cross filesystems (st_dev of the root) and skips mount points.
- Never root: euid 0 -> every subcommand exits 3 `hm-clean: refusing to run as root`.
- Fail closed: every predicate returns PASS, FAIL or UNKNOWN. UNKNOWN (tool missing, timeout 60 s, parse error, permission denied) skips the item with the reason. A missing tool (git, gh, lsof, docker, xcrun) skips the whole rule with one reason line. No predicate ever defaults to PASS.
- Hard per-run byte cap caps.runBytes plus optional per-rule capBytes: apply stops before an item that would cross the cap; the rest is deferred to the next run.
- Item identity in the plan: dev, inode, mtime_ns, size (files/dirs; size via lstat walk), sha (git refs), udid (simulators), pid + start time (processes). apply refuses an item whose identity changed.
- JSON logs: every plan and apply writes a file under the state dir with items, per-item bytes before/after, disk free before/after, totals, binary version, config sha256. Logs older than 30 days are deleted by the binary itself (fixed rule, its own state dir only).
- First run is plan-only (confirm latch, section A) on every machine (SC4).
- Privileged kinds (docker-prune, remote-branch, memory-ended-agent kill) run only when ALL hold: `run --scheduled` (or `apply --scheduled`); none of the env vars CLAUDECODE, FACTORY_AGENT, FACTORY_ROLE, CURSOR_AGENT is set; no ancestor process (walked to pid 1) has comm/argv0 basename claude, cursor-agent or herdr, or argv containing @anthropic-ai/claude-code; the ancestry read succeeded. Otherwise the rule is skipped with `privileged rule <id> runs only from the scheduled job`. With FACTORY_AGENT set, `apply` is refused entirely (exit 3): factory agents clean through `factory clean` and their roles, not through hm-clean.

## E. Docker and the agent guard (SC7)
- The scheduled job (launchd agent / systemd user unit) runs as the owner's user. It is not an agent, so owner decision 3 lets it run `docker ... prune`. It never uses sudo; if `docker info` fails for the user, the rule is skipped and reported.
- The agent PRIV guard is NOT changed: roles/hooks/worker-guard and worker-guard.py, go/internal/guard (PrivWords includes docker), go/cmd/worker-guard and the `Bash(docker:*)` deny entries in roles/settings/*.json.in stay byte-identical. A test-only task proves agents still cannot run docker prune through the real hook entry point.
- Second layer inside hm-clean: privileged kinds refuse in agent context (D), so an agent running `hm-clean apply` gains neither Docker, push nor kill rights.

## F. Signals, resources API and owner alerts (SC5)
- check/measure/run write status.json and status.env (key=value per line, hm_clean_ prefix: status ok|warn|critical, reasons, disk_free_gb, disk_free_pct, swap_used_pct, reclaimable_bytes, last_freed_bytes, at (unix), version).
- Factory owner alert path (linux-host): bin/factory_cli.py owner_attention(con) (line ~14362) is the one "needs the owner" selector; owner_alert_feed_tick (~14438) mirrors it into the owner_alert_feed table and owner_alert_send_tick (~14504) sends rows to the owner's phone over ntfy when config owner_alert_feed=1 and owner_alert_send=1. Add one source to owner_attention: kind `disk`, ref <hostname>, since = status time, link /resources, listed while status.env says warn or critical and is fresher than 36 h. Because both flags default to 0, a critical status also sends one boss escalation per host per 6 h (same pattern as owner_blocker_alerts), so it reaches the owner through the boss either way.
- Non-factory machines (mac mini, MacBook): `run` posts a local notification on a change to warn/critical (macOS osascript display notification; Linux notify-send if present, else nothing) and the Herdr app card shows it. Direct ntfy from hm-clean is open question 6 (default off).
- Herdr app Resources card: per-machine numbers come from the PROBE shell script and parseProbe() in plugins/hivemill/herdr-app/mods/hivemill/data.js (lines ~295-326), run locally or via `hc exec --on <machine>`; this works without a factory. PROBE appends the contents of status.env from the state dir (2>/dev/null, so a machine without hm-clean shows nothing); parseProbe exposes reclaimable_bytes, clean_status, clean_at; main.js shows "reclaimable N GB" and the status tone in the machine row.
- Factory resources API: web/views/resources.py (served as /api/resources.json by web/server.py _route) gets a `hygiene` block read from the local status.json, kept outside queries.resources_db so the Go port parity goldens (go/internal/web/data ResourcesDB) do not change.

## G. Schedule
- macOS: ~/Library/LaunchAgents/dev.hivemill.clean.plist, StartCalendarInterval daily (default 04:30), ProgramArguments <abs hm-clean> run --scheduled --mode <m>, StandardOutPath/StandardErrorPath in the state dir, loaded with `launchctl bootstrap gui/<uid>`; `launchctl kickstart` for "run now".
- Linux: ~/.config/systemd/user/hm-clean.service (Type=oneshot, Nice=10, IOSchedulingClass=idle) + hm-clean.timer (OnCalendar=*-*-* 04:30, Persistent=true, RandomizedDelaySec=15m), `systemctl --user enable --now hm-clean.timer`.
- Reuse: go/internal/launchd was deleted as dead code (it was fixed to the four factory labels and FactoryHome); go/internal/schedule is the factory DB schedules table. Neither fits as is. hm-clean renders its own two templates in go/internal/kit/clean/schedule.
- `schedule status` prints the next run and the last result from the state dir.

## H. Skill layout decision (SC6) and dependency on G-059
Decision: the Kit layout of ADR 0007 (G-059), not plugins/hivemill/skills/<name>. Reasons: ADR 0007 D keeps the `hivemill` plugin unchanged and ships one plugin per kit skill; ADR 0007 A already lists "Cleaner" as kit candidate 6 (SOLO); the owner asked that it ships in the Kit catalog; the Kit shim bin/hm-<name> is the same pinned-sha256 download/verify/cache/exec design as plugins/hivemill/scripts/install that the owner named. So:
- plugins/kit-clean/.claude-plugin/plugin.json (name hivemill-clean), skills/clean/SKILL.md, clean.example.json, bin/hm-clean (shim), eval/cases/*.json (>= 3), eval/fixtures/; kit/skills.json entry (name clean, level SOLO, plugin plugins/kit-clean, binary hm-clean); .claude-plugin/marketplace.json entry hivemill-clean.
- "Plugin-only user" = someone who installs only the hivemill-clean plugin (or `npx hivemill add clean`), no factory, no other plugin.
- SKILL.md flow: locate the binary (ADR 0007 D lookup: HIVEMILL_BIN_DIR, then CLAUDE_PLUGIN_ROOT/bin, then ~/.local/share/hivemill/bin) -> ask one question (machine type) and write ~/.config/hivemill/clean.json from the profile -> `hm-clean measure` -> `hm-clean plan`, show totals and the top items -> ask the user to confirm (first install) -> `hm-clean confirm --plan <file>` -> `hm-clean apply` (non-privileged kinds only, it runs inside Claude) -> `hm-clean schedule install --mode plan|auto` (the user chooses; privileged kinds run from that job). Never asks for a token.
- Depends on G-059 (cross-goal; --after not enforced, lead g-060-a holds, lead g-059-a orders): T-1660 (ADR 0007, PR 771); T-1663 (go/internal/kit LoadConfig, exit codes, kit-check, kit/template) before the config task; T-1664 (kit-eval, kit.yml) before the plugin task; the plugin task edits kit/skills.json and .claude-plugin/marketplace.json after T-1672 (last of G-059's chain T-1666 -> T-1670 -> T-1672 on those files) or rebases on it; real pins come from T-1668 (kit-pin) at the next release. Until then the shim has zero pins and refuses to run; the rollout uses a binary built from origin/main via HIVEMILL_BIN_DIR.
- If the owner rejects ADR 0007: fallback plugins/hivemill/skills/clean/SKILL.md with a shim modelled on plugins/hivemill/scripts/install; only the plugin task changes.

## I. Rollout (SC4)
Per machine, owner-gated tasks (blocked, waits=owner):
1. Build hm-clean from origin/main on the machine (linux-host locally; mac mini and MacBook via `hc exec --on <m>` in a scratch dir), install the profile config, run `hm-clean measure` and `hm-clean plan` only. The plan summary (totals per rule, top 20 items, every skipped-reason class) goes to the lead, who sends it up as a report to the owner.
2. The owner reviews. Linux host and MacBook: the owner's go (message id). Mac mini: the owner's EXPLICIT go for the first real apply; the mac mini plan must show every ~/.herdr/worktrees entry with uncommitted work, an open PR or a live process in the skipped list.
3. The owner (not an agent) runs `hm-clean confirm --plan <file>` and `hm-clean schedule install --mode auto` (exact lines in the task). The first scheduled apply log is then checked.
Until step 3, `schedule install --mode plan` may run daily plan-only on that machine.

## J. Relation to `factory clean` and the tick /tmp sweep: coexist
- Keep `factory clean` (task-state aware: task status, agent rows, events, push-policy freeze, cleaner role) and the tick sweeps (tmp_sweep, tmp_sweep_harness, open PR 795 / T-1698) unchanged. Replacing them needs a DB-coupled port with parity tests in the hottest file and the tick; wrapping adds a Python->Go call for no gain.
- hm-clean on the linux-host covers what those do not: Docker, caches, transcripts, logs, orphan worktree dirs, merged worktrees between cleaner runs, memory. The linux-host profile leaves the tick's /tmp scratch patterns (zz-test-factory-*, agent scratch dirs) to the tick.
- Overlap: merged task worktrees and factory/* branches can be removed by either tool. Both require merged + clean + no open PR + not in use; whichever runs first wins. Regression check in the linux-host rollout: `factory clean --dry-run` after the first hm-clean apply exits 0 with no error line.
- Later (not this goal): the cleaner role may call hm-clean for non-task classes.

## K. Risks and mitigations
- r1 deletes live work (dirty worktree, unmerged branch, process cwd, held temp file, young file, symlink out of root) -> built-in predicates, fail closed, six-negative e2e fixture task (SC2) on Linux and darwin.
- r2 agents gain Docker/push/kill via hm-clean -> privileged gating (D) tested in the docker task; guard unchanged + guard test task (SC7).
- r3 one run frees too much / wrong rule -> per-run cap, plan-only first run, every plan logged.
- r4 the owner's other team's worktrees on the mac mini -> explicit go, remote-branch off, predicates, plan reviewed first.
- r5 transcripts deleted before the factory token index read them -> transcript minAgeDays >= token window, consumer named, open question 3.
- r6 macOS differences (lsof, launchd, simctl) -> darwin tests on the Mac mini via hc exec (QA task).
- r7 shared files with other goals -> section L ordering.
- r8 G-059 slips -> only the config and plugin tasks depend on it; fallback in H.

## L. Cross-goal file conflicts and ordering
- docs/adr/README.md, docs/README.md: PR 771 (T-1660, G-059) and PR 706 (ADR 0004) edit them. ADR task starts after PR 771 merges, merges origin/main first, rows in number order.
- go/internal/kit (package kit): created by T-1663. Our code lives in go/internal/kit/clean/ (new dirs only); the config task waits for T-1663.
- kit/skills.json, .claude-plugin/marketplace.json: G-059 chain T-1666 -> T-1670 -> T-1672; our plugin task after T-1672 (or rebases on it). .github/workflows/kit.yml is not edited (it discovers plugins from the registry). Pins: T-1668.
- bin/factory_cli.py (owner_attention only): hot file; open PRs 795, 793, 786, 784, 780, 778, 775, 765, 763, 760, 751, 741, 739, 736, 735, 733, 690, 671 touch it. The task edits only owner_attention plus one new helper next to it and merges origin/main before the PR.
- plugins/hivemill/herdr-app/mods/hivemill/data.js and main.js: open PR 713 (dashboard-render-cache) edits both -> the resources-card task starts after PR 713 merges or closes. G-059 T-1671/T-1672 do not edit herdr-app (ADR 0007 C).
- web/views/resources.py: no open PR. A future Go port of the resources view must mirror the hygiene block (named consumer).
- go/internal/guard, roles/hooks, roles/settings: PRs 790, 716, 661, 772, 789 edit them; our guard task adds one new test file only.
- tick /tmp sweep: PR 795 (T-1698) edits tmp_sweep; we do not touch it.

## M. Open questions for the owner (each has the default we build)
1. Skill layout: Kit plugin hivemill-clean (plugins/kit-clean) instead of plugins/hivemill/skills/clean? Default: Kit.
2. Per-run byte cap: default 50 GiB on linux-host and mac mini, 5 GiB on the MacBook profile.
3. Ages: DerivedData 14 d, Archives 90 d, iOS DeviceSupport newest 2 kept + 60 d, CoreSimulator caches 7 d, evidence folders 30 d, Claude transcripts 60 d, temp leftovers 3 d, orphan worktree dirs 7 d, merged worktrees 2 d, Docker until=168h, logs rotate at 50 MB keep 5. Default: these.
4. Alert thresholds: disk warn < 30 GB or < 15 % free, critical < 15 GB or < 7 %; swap warn > 60 %, critical > 85 % used. Default: these.
5. Schedule: daily 04:30 local with catch-up after sleep. Default: this.
6. Non-factory alerts: local notification + Herdr card only, or also ntfy straight from hm-clean (needs an ntfy topic file on that machine)? Default: local only.
7. Memory class: report-only in all profiles; ended-agent kill switched on per machine only with the owner's go at rollout. Default: report-only.
8. Linux host remote factory/* branch deletion from the scheduled job (same checks as `factory clean --remote-branch`). Default: on, linux-host only.
9. Mac mini ~/.herdr/worktrees: apply with the built-in predicates after the explicit go, or report-only for that root? Default: apply after go.
