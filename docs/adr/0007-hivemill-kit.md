# ADR 0007: Hivemill Kit: standalone skills (marketplace + npx), three levels, evals and a catalog

## Status

Proposed, 2026-11-17. Goal: G-059. Task: T-1658. It becomes Accepted only with the owner's approving message id recorded here. Accepting this ADR also accepts the Node ESM exception in section D (the npx front end), the same kind of exception ADR 0004 made for the iOS release script.

## Context

Owner, 2026-11-17: Hivemill must install properly in 1-2 commands, and its pieces must be shareable with people who have no server: "modularize my setup ... provide skills for each piece like: pipeline for coding with review, skill for monitoring resources, skill and plugin for herdr core to get same dashboard with a wizard to configure it with claude, skill for clean up resources ... each piece ... as a skill with a go script provided and commands so people can download it and use it through npx", with evals, more skills later, and a website (hivemill.dev) with free open-source things and a consulting contact.

Today every factory piece reads or writes the factory DB (db/factory.db) through bin/factory or go/internal/factorydb. Only one piece is already standalone: the iOS TestFlight release skill (plugins/hivemill/skills/ios-testflight-release, ADR 0004). The marketplace (.claude-plugin/marketplace.json) lists one plugin, `hivemill`, whose setup skill runs a sha256-pinned installer shim (plugins/hivemill/scripts/install -> hivemill-install release asset).

Measured on origin/main 5e981a0b8 with `go list -deps`: go/internal/gate, herdr, install, prflow, guard, holdexpiry and lanecheck import neither go/internal/factorydb nor modernc.org/sqlite. go/internal/skilleval does. So the resource gate, the herdr client and the guard can be reused by standalone binaries as they are.

## Decision

Hivemill is shared as a **Kit**: standalone skills, each a SKILL.md, one Go binary and a small JSON config, with no factory DB. Skills are grouped in three levels. Each skill installs through the Claude Code plugin marketplace and through `npx hivemill add <skill>`; both routes fetch the same pinned, sha256-checked release binary. Every skill ships an eval suite that CI runs on linux and darwin (amd64 and arm64); a release is cut only when every suite passes, and the catalog on hivemill.dev is generated from the release.

### A. Module inventory

Ranked by value x shareability (H=3, M=2, L=1). "DB today" = needs the factory DB now. "Standalone needs" = what a no-DB version must replace.

| # | Piece | Where today | Value | Share | DB today | Standalone needs | Level |
|---|---|---|---|---|---|---|---|
| 1 | Review pipeline: coder -> reviewer -> QA -> judge merge gate | roles/coder.md, reviewer.md, qa.md, judge.md; `factory verdict`/`factory judge` in bin/factory_cli.py | H | H | yes (tasks, verdicts) | verdicts as marked PR comments at a head SHA; `hm-review gate` decides from gh data | SOLO (skill 1) |
| 2 | Resource monitor + spawn gate | go/internal/gate (Decide is pure), `factory res` | H | H | yes (config table, agents rows) | JSON config; live agents counted from the host process scan | HOMELAB (skill 2) |
| 3 | Herdr dashboard + setup wizard | plugins/hivemill/herdr-app (JS mod), go/internal/install/herdrapp.go | H | H | yes (reads factory serve /api) | `hm-dash snapshot` from herdr + host readers; wizard writes a validated config | HOMELAB (skill 3) |
| 4 | Worker guard hook | roles/hooks/worker-guard.py, go/cmd/worker-guard, go/internal/guard | H | H | partly (guard export) | static rules file instead of the export | SOLO (next) |
| 5 | Public-text guard | ci/public_text_guard.py, ci/pre_push_public_text.py, go/cmd/ci-public-text-guard | H | H | no | owner terms file per user | SOLO (next) |
| 6 | Cleaner (merged worktrees, branches, temp files) | roles/cleaner.md, factory cleanup commands | M | H | yes (task/worktree state) | merged state from gh + git worktree list | SOLO (next) |
| 7 | Usage / budget cap guard | go/internal/tokens, gate MaxFiveHourPct/MaxSevenDayPct | H | M | yes | reads Claude Code usage files directly | HOMELAB (next) |
| 8 | PR flow metrics | go/cmd/pr-flow, go/internal/prflow | M | H | no (events file) | events from gh only | HOMELAB (next) |
| 9 | Stuck-PR watchdog | tick red-pr-unowned / review-autospawn-wait (go/internal/tick) | M | M | yes | gh poll + notify, no tick | HOMELAB (next) |
| 10 | Multi-angle review workflow | roles/workflows/multi-angle-review.js | M | H | no | none; becomes an hm-review reviewer mode | SOLO (later) |
| 11 | Role checklists (acceptance template, review, audit) | roles/plugins/*-skills | M | H | no | none; text reused inside skill 1 | SOLO (content) |
| 12 | iOS TestFlight release | plugins/hivemill/skills/ios-testflight-release | M | M | no | already standalone (ADR 0004) | SOLO (exists) |
| 13 | Lane check (file overlap of parallel work) | go/cmd/lane-check, go/internal/lanecheck | M | M | no | input from git diffs of open PRs | HOMELAB (later) |
| 14 | Installer + verifier shim | go/cmd/hivemill-install, plugins/hivemill/scripts/install | H | M | no (installs it) | n/a: installs FULL | FULL |
| 15 | Web pipeline skill | plugins/hivemill/skills/web (G-052 T-1557, PR 705) | M | M | yes (files a goal) | its .hivemill/web.json schema is reused by skill 1 | FULL |
| 16 | Skill eval harness | scout/skilleval.py, go/internal/skilleval, skilleval/ | M | L | yes | used internally for model evals only | FULL (internal) |
| 17 | Scout (third-party skill triage) | scout/*.py | M | L | yes | Linux namespaces sandbox | FULL |
| 18 | Sandbox (nsbox, workerbox, testbox) | scout/nsbox.py, go/internal/sandbox | H | L | yes | Linux only, root setup | FULL |
| 19 | Tick, staffing, lead/director/boss org | go/internal/tick, bin/factory_cli.py, roles/*.md | H | L | yes | is the factory | FULL |
| 20 | Web UI (factory serve) | web/, bin/factory_cli.py | M | L | yes | is the factory | FULL |
| 21 | Retro (daily rejection report) | go/internal/retro | L | L | yes | is the factory | FULL |
| 22 | CI tools (risk tier, test-count gate, shard, affected, changelog, scrub) | ci/*, go/cmd/ci-* | M | L | mostly no | factory-repo specific | FULL (internal) |
| 23 | macOS port tools, demo/film tools | go/cmd/macbox-setup, macos-facts, film-render, ci/demo_* | L | L | no | internal | FULL (internal) |

Next candidates after the first three, in order: 4, 5, 6, 7, 8, 9. Each one later is one PR through section G.

### B. Levels

Each level is a superset of the one before.

- **SOLO**: a laptop (Linux or macOS) with Claude Code and gh, no server. Skill 1 (review pipeline), then worker guard, public-text guard, cleaner; the existing iOS release skill.
- **HOMELAB**: one always-on box. SOLO + skill 2 (resource monitor + gate) and skill 3 (herdr dashboard + wizard), then usage cap guard, PR flow metrics, stuck-PR watchdog, lane check. Kit skills run on Linux and macOS.
- **FULL FACTORY**: today's setup on Debian or Ubuntu: factory DB, tick, org of agents, web UI, sandbox, scout, pipelines. Installed by the two commands in section D. The full factory does not run on macOS yet (ADR 0003 tracks that port).

A skill never assumes a higher level. A skill of a lower level works unchanged when a higher level is installed (no conflicting hooks, separate config files).

### C. Standalone boundary of the first three skills

Shared rules: one binary per skill under go/cmd/hm-<name>, code under go/internal/kit/<name>; the binary imports nothing that pulls in go/internal/factorydb or modernc.org/sqlite (checked by `go list -deps` in CI); no new Go module dependency; config is one JSON file with `schemaVersion`; no network except gh and the paths named below; no secret is read, stored or printed.

1. **hivemill-review** (binary hm-review), SOLO. One GitHub repo, the user's own gh login.
   - In the skill: the SKILL.md runs coder -> reviewer -> QA -> judge as separate subagents. The coder writes in a git worktree and opens a PR. The reviewer reads the diff only. QA runs the repo checks. Neither the reviewer nor QA edits code; the agent that wrote the change never records a verdict on it.
   - In the binary: `hm-review checks run` runs the argv list from `.hivemill/web.json` (the exact schema of G-052 T-1557: schemaVersion, defaultBranch, checks). `hm-review verdict --role reviewer|qa --pr N --verdict PASS|FAIL` posts a PR comment carrying a machine marker with role, verdict and the PR head SHA. `hm-review gate --pr N` passes only if: a reviewer PASS and a QA PASS exist for the current head SHA, posted by the authenticated gh user, every CI check on the head is success (or the repo has none), the PR is open, not draft and mergeable. `hm-review merge --pr N` runs the gate and merges only when it passes.
   - Not included: issue trackers, other forges, more than one repo, a server, a DB.
2. **hivemill-gate** (binary hm-gate), HOMELAB (works on a laptop too).
   - `hm-gate status [--json]`: memory available, load per CPU, /tmp use, live Claude agent processes, from the existing host readers (go/internal/gate proc_linux/proc_darwin). `hm-gate check`: exit 0 allow, exit 3 refuse with one reason line. `hm-gate hook`: a Claude Code PreToolUse hook for the Agent/Task tool; a refusal exits 2 with the reason (blocks that subagent start).
   - It reuses gate.Decide through an adapter; go/internal/gate itself is not edited, so the factory's `factory res` keeps its behaviour.
   - Not included: the factory's lead/review slot reserves, herdr pane counting, the usage percentages (later: candidate 7).
3. **hivemill-dash** (binary hm-dash + a herdr mod), HOMELAB.
   - `hm-dash snapshot --json`: herdr panes/agents (through go/internal/herdr or the herdr CLI) plus the host numbers of hm-gate's readers. `hm-dash config validate|write`: the wizard's answers in, a validated config out. `hm-dash install-mod`: copies the skill's herdr mod into herdr's user mods directory, under the name hm-dash only (the same directory go/internal/install/herdrapp.go uses for the factory mod).
   - The mod (JavaScript, because herdr mods are JavaScript, as plugins/hivemill/herdr-app is today) shows the panels that exist without a factory: agents/panes, context left, stalled panes, host resources. The factory dashboard (plugins/hivemill/herdr-app) stays the FULL view and is not edited.
   - The wizard is the SKILL.md: Claude asks one question at a time (which panes/projects, wait patterns, alert thresholds), then runs `hm-dash config write` and `hm-dash install-mod`. It never asks for a token.

### D. Packaging and distribution

- **Marketplace (primary).** `.claude-plugin/marketplace.json` in volekerb/hivemill lists one plugin per kit skill (hivemill-review, hivemill-gate, hivemill-dash) beside the existing `hivemill` plugin, which is unchanged. Plugin dirs are plugins/kit-<name>/. Each plugin carries a verifier shim `bin/hm-<name>`, the same design as plugins/hivemill/scripts/install: it downloads `hm-<name>-<os>-<arch>` from the GitHub release of its pinned version, checks the sha256 pinned in the shim, caches it, and execs it. A mismatch, a non-https URL or an unknown platform exits non-zero before anything runs.
- **npx (second route).** npm/hivemill/ is a tiny Node ESM front end with zero dependencies and no install scripts: `npx hivemill add <skill> [--user|--project] [--dry-run]`, `npx hivemill list`. It carries manifest.json (version, sha256 per platform per binary, written by the release process), downloads the binary from the same GitHub release, verifies it, puts it in ~/.local/share/hivemill/bin, and copies the skill directory into ~/.claude/skills/ (or ./.claude/skills with --project). Node is needed because npx is Node; this is a language-policy exception (ADR 0001), recorded here, limited to npm/hivemill/.
- **Binary lookup in SKILL.md**: `$HIVEMILL_BIN_DIR`, then `${CLAUDE_PLUGIN_ROOT}/bin`, then `~/.local/share/hivemill/bin`.
- **Platforms**: linux-amd64, linux-arm64, darwin-amd64, darwin-arm64, built with CGO_ENABLED=0 -trimpath -buildvcs=false -ldflags='-s -w -buildid=' so the build is reproducible and the pins can be committed before the tag (the hivemill-install pattern).
- **Where it lives**: binaries, SHA256SUMS, per-skill eval results and catalog.json are assets of the GitHub release of volekerb/hivemill (existing release.yml). The npm package name and the `npm publish` are owner decisions; until then the package is `"private": true` and CI proves it from an `npm pack` tarball.
- **Signing**: sha256 pins in the shim and the npm manifest are the integrity root (they come from the tagged repo). Recommended on top: GitHub artifact attestations (build provenance, keyless, no key to hold), verifiable with `gh attestation verify`. Alternative: a minisign/cosign key held by the owner. Owner decision; not built before it.
- **Full factory, two commands** (Debian/Ubuntu shell, after the gh and Claude logins that only the user can do): `git clone --depth 1 --branch vX.Y.Z https://github.com/volekerb/hivemill ~/hivemill` then `~/hivemill/plugins/hivemill/scripts/install --yes`. The installer already installs herdr and Claude Code and starts the units. Inside Claude Code the three plugin lines stay as the guided route. Proven by ci/freshbox_test.py in a two-command mode.

### E. Evals and the catalog

- Each plugin has eval/cases/*.json: argv, stdin, env, a fixture dir (fake `gh`/`herdr` on PATH), expected exit code and expected stdout/stderr substrings. `go/cmd/kit-eval` runs every case against the built binary and writes eval-results-<skill>.json (skill, version, commit, os, arch, passed, failed, case names).
- CI (.github/workflows/kit.yml) runs kit-check, the kit Go tests and kit-eval for every plugin on ubuntu-latest, ubuntu-24.04-arm, macos-latest (arm64) and an Intel macOS runner (amd64; macos-15-intel at plan time). A red suite fails the PR.
- release.yml runs the same suites on the release build; any failure stops the release. So every released skill version passed its evals; the results files are release assets.
- Model-graded evals (does Claude follow the SKILL.md?) run locally with the factory skilleval harness, or `claude plugin eval` if adopted; they need a model key in CI and spend, so moving them into CI is an owner decision.
- `go/cmd/kit-catalog` builds catalog.json from kit/skills.json plus the eval results: name, level, description, version, platforms, eval status per platform, install lines. hivemill.dev's skills page renders that file; a workflow in volekerb/hivemill-www fetches it from the latest tagged release, checks its sha256 against SHA256SUMS, and opens a PR.

### F. Versioning and compatibility

- One version line: the repo tag vX.Y.Z (VERSION file). Every kit plugin.json, binary `--version`, npm package and manifest carry that version. Nothing downloads "latest"; every download is a pinned version with pinned hashes.
- Config files carry an integer `schemaVersion`. A binary accepts every schemaVersion up to its own and refuses a higher one with a message naming the needed version. A change that breaks an old config raises schemaVersion and is listed under "Kit" in CHANGELOG.md.
- Before 1.0 a minor version may break a CLI flag; it is listed in CHANGELOG.md. Exit codes are fixed: 0 ok, 1 error, 2 usage or blocked (hooks), 3 refused by a gate.
- The site shows only tagged versions.

### G. Repo layout and the "add a new skill" path

- plugins/kit-<name>/: .claude-plugin/plugin.json, skills/<name>/SKILL.md, <name>.example.json, bin/hm-<name> (shim), eval/cases/*.json, eval/fixtures/.
- go/cmd/hm-<name>/ and go/internal/kit/<name>/; shared code in go/internal/kit/.
- kit/skills.json: the registry (name, level, plugin dir, binary, description). kit/template/: the copyable skeleton. docs/kit/ADD-A-SKILL.md: the steps.
- npm/hivemill/: the npx front end. Tools: go/cmd/kit-check, kit-eval, kit-pin, kit-catalog.
- **Adding a skill is one PR**: copy kit/template to plugins/kit-<name>, write go/cmd/hm-<name> and go/internal/kit/<name>, add eval cases, add one entry to kit/skills.json and one to .claude-plugin/marketplace.json. `kit-check` validates all of it; kit.yml discovers the plugin from the registry, so no workflow edit is needed; release and catalog pick it up from the registry.

### H. Security

- Checksums and pins: every binary is sha256-pinned in its shim and in the npm manifest; release.yml refuses to publish when a built hash differs from the committed pin.
- Downloads: https only (file:// for tests), redirects only to https, to a temp file, verified before chmod and exec; never piped into a shell.
- No secrets: binaries never read, store or print tokens; gh uses the user's own login. kit-check rejects an example config or eval fixture with a key or value that looks like a token, password or private key. SKILL.md text forbids asking for or repeating a token (as the setup skill does).
- No owner data: the public-text guard and scrub scan cover plugins/kit-*, kit/, npm/ and docs/kit/ like the rest of the repo. Examples use placeholders (OWNER/NAME, example hosts).
- npm package: no dependencies, no postinstall or other install scripts, a `files` whitelist.
- Hooks fail closed: an internal error in `hm-gate hook` blocks (exit 2), as the factory worker guard does.

## Consequences

- Seven new Go commands (hm-review, hm-gate, hm-dash, kit-check, kit-eval, kit-pin, kit-catalog), one npm package, three plugin dirs, one CI workflow. No change to the factory DB, the tick, go/internal/gate or the existing hivemill plugin.
- Two install routes must stay byte-equal on the binary: both read the same release asset and the same pinned hash.
- Releases now also carry the kit; a release fails if any kit eval fails or a pin is stale.
- The factory's own pieces stay DB-based; a standalone skill is a sibling, not a replacement. Drift between, for example, the factory judge gate and `hm-review gate` is possible and accepted.

## Alternatives

- **One big "hivemill-kit" plugin with all skills**: rejected; users install what fits their level, and evals and versions per skill read better in the catalog.
- **Python binaries (stdlib) instead of Go**: rejected; Go gives one static file per platform with no runtime, and ADR 0001 makes Go the default for new tools.
- **npx only, no marketplace**: rejected; the owner named the marketplace primary, and it needs no npm account.
- **`curl | sh` one-liner for the full factory**: rejected; it executes an unverified script. The two-command clone + pinned shim keeps the verification.
- **Standalone skills that talk to a small local DB**: rejected; "no factory DB" is the requirement, and PR comments plus config files are enough for one repo.
- **A contact form on the site**: rejected unless the owner wants a new external service; an email link is used.
