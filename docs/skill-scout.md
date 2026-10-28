# Skill scout

The skill scout finds new or trending Claude Code skills, plugins, agents and hooks, triages them, and (later) evaluates the promising ones. Adopting any third-party skill needs an owner decision through the boss. This page grows with the G-009 tasks; the sections below cover what exists today.

## Database

Six tables, created by `factory init` and added to older DBs by `migrate()` (idempotent): `scout_candidates`, `scout_triage`, `scout_stars`, `scout_cursors`, `scout_runs`, `skill_trials`. The team-scoring `evaluations` table and `factory eval` are unrelated and untouched.

- Candidates are deduplicated on a normalised key: `gh:<owner>/<repo>[/<path>]` with owner and repo lowercased, a trailing `.git` and `/` removed. A second sighting updates the facts, bumps `seen_count` and merges the source name into `sources`.
- `stars_per_day` is recomputed from the first and latest star snapshot within 14 days of the latest one.
- The library is `scout/db.py` (stdlib only, no network).

## `factory scout`

| command | roles | what |
|---|---|---|
| `fetch [--dry-run]` | scout, director, owner | run all sources, upsert candidates, star snapshots, install risk; prints one summary line |
| `pending [--limit N]` | scout, director, lead, boss, owner | untriaged candidates with their facts, a README excerpt (≤ 4 KB, at the head sha, between `BEGIN/END UNTRUSTED README` markers) and our inventory of skills/plugins under `roles/plugins/**` |
| `triage C-xxxx --purpose … --roles … --overlap … --rec adopt\|watch\|skip --why … [--suggest-eval]` | scout, director, owner | store the judgement and mark the candidate `triaged` |
| `list [--new] [--rec R] [--risk R] [--since ISO]` | same as pending | one line per candidate |
| `show C-xxxx` | same as pending | all fields, star snapshots, triage records |
| `runs` | same as pending | scheduled scout runs |

Triage validation (exit 2, nothing written): `--rec` must be adopt, watch or skip; `--roles` is a comma list of factory roles or `none`; `--purpose`, `--overlap` and `--why` are required and at most 600 characters. The text is stored verbatim as data (bound SQL parameters), never executed. Workers other than the scout role are refused.

## Eval sandbox

Third-party skills are evaluated by an agent that must not reach our secrets or the open internet. `scout/nsbox.py` and `scout/egress.py` build that sandbox (design D, plan v3 §7.5/§7.6). They only add the wrapper; spawn wiring (T-0111) and the checker call (T-0116) use it.

- **Shim.** `write_shim(trial_dir, real_claude, …)` writes `trials/<id>/bin/claude` (0755), plus a read-only snapshot of the factory CLI (`bin/factory` and `scout/`) next to it. spawn puts `bin/` first on PATH; the shim reads the token, starts the proxy, and execs the namespace wrapper around the real claude. `real_claude` is an absolute path whose basename is `claude` (the `~/.local/bin/claude` symlink, not its resolved version path) so kind-claude pane detection still matches. Every `~` path comes from the passwd entry, never from `$HOME` (inside the pane `HOME` is the trial dir), and is baked into the shim as an absolute path.
- **Namespaces.** Stage 1 is `unshare -r -n -m --pid --fork --mount-proc --propagation private`, running as root-in-namespace: it brings `lo` up, starts the loopback forwarder, and hides things. Stage 2 is `unshare --map-user=<uid> --map-group=<gid>` then `setpriv` (no-new-privs, empty bounding, inheritable and ambient sets), then claude, so claude has the real uid, zero capabilities and cannot undo the mounts. The forwarder also runs with no capabilities, and it listens before anything is hidden. Pure stdlib plus util-linux; no bwrap, pasta or slirp.
- **Hidden** (tmpfs, so they list empty): `~/.claude`, `~/.config` (includes the multiplexer socket), `~/.ssh`, `~/.gnupg`, `~/factory` (live DB, worktrees, state), `$XDG_RUNTIME_DIR` (user bus, ssh-agent), and every sibling trial under the trials root. `~/.git-credentials` and `~/.netrc` are covered with `/dev/null`. The list is the constant `HIDE_DIRS`. Kept visible: the own trial dir, the claude install, an optional read-only vendor dir. `SSH_AUTH_SOCK` and `DBUS_SESSION_BUS_ADDRESS` are unset. The pid namespace has its own `/proc`, so host processes cannot be seen, read or signalled.
- **Egress rule.** The netns has loopback only. A forwarder on `127.0.0.1:3128` relays to `trials/<id>/proxy.sock`, served by a CONNECT proxy outside the namespace that allows exactly `api.anthropic.com:443` (host compared case-insensitively). Everything else, including any non-CONNECT request, gets 403 and no DNS lookup. The proxy exits when the shim's pid dies or after a max lifetime (default 20 min); `stop_proxy(trial_dir)` kills it and is idempotent. Data can still leave through the allowed host with an attacker-supplied key; that residual risk is accepted on record (plan §7.5).
- **Token file.** `~/.config/factory/eval-oauth-token` must be a regular file, mode 0600, owned by the uid. Otherwise the shim exits 3 with `owner decision D3 pending: no eval token`. The value reaches claude only as `CLAUDE_CODE_OAUTH_TOKEN` in its environment: never argv, never a file, never logged.
- **Contained runner.** `run_contained(trial_dir, argv, cwd, env, timeout, net=False)` runs a checker (check.py, hidden tests) with the same hiding and no proxy, forwarder, token or proxy env. It returns `Contained(returncode, output, timed_out)` and kills the whole process tree on timeout. `net=True` keeps the host network and is off by default. With `env=None` the child gets an allow-list only (`PATH`, `LANG`, `LC_*`, `TERM`, `TMPDIR`, `HOME` = the trial dir, plus `env_extra`), never the caller's whole environment.
- **Working directory.** After the mounts, setup re-resolves the cwd (`chdir`) so `..` and `/proc/<pid>/cwd` cannot reach the pre-mount tree: a visible cwd is kept, any other lands in the trial dir. Hide dirs are resolved with `realpath`, so a symlinked entry is hidden too.
- **Requirement and CI.** Unprivileged user namespaces are needed (`userns_available()` probes `unshare -r -n -m` and returns `(ok, reason)`). On your-box they work. `tests/test_skilleval_nsbox.py` has group A (proxy, shim, argv, lifetime; runs everywhere, including CI) and group B (real namespaces), which skips with the reason where namespaces are restricted; expect it to skip on `ubuntu-latest` and to run on your-box: `python -m unittest tests.test_skilleval_nsbox -v`.

## `factory scout fetch`

Code: `scout/sources.py` (sources, dedupe, cursors) and `scout/risk.py` (install-risk classification). Every GitHub call is `gh api` through one helper with a 30 s timeout; there is no clone and no shell download tool, and nothing fetched is executed or written to disk. A failing source is caught, listed in the summary and does not stop the others (exit 0). The last line reads `fetch: new N, updated M, per-source {…}, gh calls X (search Y), hn calls Z, errors […]`. `--dry-run` makes the same calls into a throwaway copy of the DB, so nothing is stored (its line starts with `dry-run`). `$FACTORY_NOW` fixes the clock (search date filters) and `$FACTORY_HN_URL` replaces the Hacker News base URL; both exist for tests.

| source | what it does | cursor (`scout_cursors`) |
|---|---|---|
| `marketplace` | `anthropics/claude-plugins-official`: if `main` moved, reads `marketplace.json` at that sha and diffs entries (name + source) against the previous list. New or changed entries become candidates (`gh:<owner>/<repo>[/<path>]`, or `mkt:claude-plugins-official/<name>` when the source is not on GitHub) | `plugins-official`: sha, entry fingerprints, list hash per sha |
| `skills` | `anthropics/skills`: every new `skills/<name>/SKILL.md` at a new sha, key `gh:anthropics/skills/skills/<name>` | `anthropics-skills`: sha |
| `search` | GitHub repository search over six topics (`claude-code`, `claude-skills`, `agent-skills`, `claude-code-plugin`, `claude-code-skills`, `claude-code-subagents`) with `created:>=today-14d stars:>=25` and `pushed:>=today-2d stars:>=200`; at most 10 search calls a run (the 12 queries rotate across runs) | `gh-search`: query offset |
| `awesome` | links added to the READMEs of four awesome lists since the previous README | `awesome:<owner/repo>`: sha and processed links |
| `hn` | Hacker News (Algolia) stories with ≥ 20 points whose url is a GitHub repo; a known repo gets source `hn` and `seen_count`+1 | `hn`: newest `created_at_i` |

Rules shared by `search`, `awesome` and `hn`: an unknown repo must have a manifest (`.claude-plugin/plugin.json`, `SKILL.md`, `agents/*.md` or `commands/*.md`) in its tree or it is dropped and not stored; at most 15 new candidates a run in total, highest stars first; at most 40 repo inspections a run. A known repo only gets its source merged, `seen_count`+1 and (from search) a star snapshot. Links left over by those limits are picked up on a later run.

A new candidate costs: repo facts (`licence`, `last_commit` = `pushed_at`, `stars`, `default_branch`), the head commit sha, one `git/trees/<sha>?recursive=1` call and the blobs below. Each sighting with a star count adds a `scout_stars` snapshot.

### Install risk

`install_risk` is a comma list, or `prompt-only` when no flag applies. `risk_detail` is a JSON list of at most 20 `flag: path` strings. A check that cannot decide adds `unknown` (unreadable blob, truncated tree, nothing under the path, more markdown than the read limit), so `prompt-only` is never a guess.

| flag | when |
|---|---|
| `scripts` | a file outside the allow-list (`.md`, `.txt`, `.csv`, `plugin.json`, `marketplace.json`, data `.json` under `skills/<name>/`, extensionless `LICENSE*`/`NOTICE*`/`README*`), or mode 100755 |
| `hooks` | a `hooks/` directory, `hooks.json`, or a `hooks` key in `plugin.json` |
| `mcp` | `.mcp.json` or an `mcpServers` key in `plugin.json` |
| `symlinks` | mode 120000 or 160000 |
| `shell-in-markdown` | a `!`-backtick (or fenced `!`) line in a command or SKILL.md, or `Bash(…)` with a network tool in `allowed-tools` |
| `network` | `WebFetch`/`WebSearch` in `allowed-tools`, or a SKILL.md/command instructing to fetch a URL |

At most 10 `SKILL.md` / `commands/*.md` blobs and 3 `plugin.json` blobs are read per candidate (pinned by blob sha). Only `prompt-only` candidates may be evaluated later.

## Pages

- [Eval sandbox: environment and working directory rules](scout-nsbox.md)
- [Scout roles](scout-roles.md): the `scout` and `evaluator` worker roles, guards and known limits.
- [Scout scheduling](scout-tick.md): tick-scheduled runs, slots, budgets and the `scout_*` config keys.
- [Bash scopes that count as open](scout-risk.md): which `Bash(<cmd>:…)` scopes the install-risk classifier treats as bare `Bash`.
- [Vendoring and the exfil pre-screen](scout-vendor.md): `factory skilleval vendor`
- [Reports](scout-report.md): the "Skill scout" section, per-run message to the director, digest section, `factory scout report`.
- [Evaluator trials](scout-eval.md): `factory skilleval try`, the trial sandbox and the evaluator spawn rules
- [Timestamps and input rules](scout-timestamps.md): DST-safe ordering with julianday(), `--limit` and `@path` limits
- [Pending order](scout-pending.md): how `factory scout pending` ranks candidates within the per-run cap.
