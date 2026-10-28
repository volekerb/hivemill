# Eval sandbox: environment and working directory rules

Follow-up to the eval sandbox in [skill-scout.md](skill-scout.md#eval-sandbox) (`scout/nsbox.py`).

## Shim environment allow-list

`shim_main` (what `trials/<id>/bin/claude` runs) no longer passes the evaluator pane's environment into stage 1 minus a scrub list. It builds the environment from an allow-list and execs stage 1 with only that:

| variable | source |
|---|---|
| `PATH`, `LANG`, `TERM`, `TMPDIR`, `LC_*` | the caller (`ENV_PASS`, the same list `run_contained` uses) |
| `CLAUDE_CODE_ENTRYPOINT`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`, `DISABLE_AUTOUPDATER` | the caller (`CLAUDE_ENV_PASS`, listed explicitly) |
| `HOME` | the trial dir, not the caller's value |
| `CLAUDE_CODE_OAUTH_TOKEN` | the token file (mode 0600), never the caller |
| `HTTPS_PROXY`, `https_proxy`, `NO_PROXY`, `no_proxy` | set by stage 1 for the forwarder |

Anything else, for example `CLAUDE_CODE_MESSAGING_TOKEN` or `FOO_SECRET`, is absent inside claude. To let another variable through, add it to `CLAUDE_ENV_PASS`.

## `run_contained` working directory

`run_contained(cwd=X)` binds X back in full, so a cwd that is equal to, above or inside a hide dir (or a sibling trial) would expose hidden content, for example `cwd=~/factory` would show the live `factory.db`. Such a cwd raises `ValueError` unless it is the trial dir or under it. `cwd=None` is unchanged: a cwd inside a hidden dir lands in the trial dir.

## Allow-list mode

`make_cfg` has an additive, keyword-only API for callers that want "hide everything, bind back only what I list" instead of the default deny-list (`mode="deny"`, unchanged). At their defaults the cfg dict is identical to before; the keys `mode`, `rw`, `ro`, `masks`, `pid`, `tmpfs_size`, `tmpfs_sizes` only appear when non-default, and `setup`/`compose_stage1` read them with `cfg.get`.

| kwarg | meaning |
|---|---|
| `mode="allow"` | use the allow-list view. `hide_dirs` then means "tmpfs over these whole trees" (pass the real home, the runtime dir, `/tmp`, `/dev/shm`); the sibling-trial scan of deny mode is not done |
| `rw=()` | paths bound back read-write |
| `ro=()` | paths bound back read-only (nosuid/nodev/noexec kept). With `claude` set, the link and its real target are added here |
| `masks=()` | dirs inside a bound-back dir that get an empty tmpfs; re-bind selected children via `rw`/`ro` |
| `pid=True` | `False` drops `--pid --fork --kill-child --mount-proc`: an exec-only chain, the target keeps the stage-1 pid. There is no pid namespace then, so the host `/proc` stays visible (other processes' files are still not reachable: no capabilities, other user namespace) |
| `tmpfs_size="8m"`, `tmpfs_sizes=None` | allow mode only (`ValueError` in deny mode, which stays `size=8m`). `tmpfs_size` is the `size=` of every hide/mask tmpfs, `tmpfs_sizes` maps a hidden tree or mask to its own (`8m`, `512k`, `1g`, or `N%` of RAM with N 1..100; the bytes must be > 0 and <= 16g, because the kernel wraps oversized values such as `17179869184g` to 0 = unlimited; anything else is refused). tmpfs is memory-backed and allocates only what is written, so a size is a cap, not a reservation |
| `keep_env=()` | names exempted from the scrub list in either mode, e.g. `CLAUDE_CODE_OAUTH_TOKEN` with `net=True, forward=False` |

`rw`, `ro` and `masks` need `mode="allow"` (otherwise `ValueError`).

**Nothing is implicitly writable.** `trial`, `keep` and `cwd` are not bound back in allow mode. Only paths listed in `rw` are writable; list the trial dir in `rw` if the target must write there. A cwd that is hidden falls back to the first `rw` path (or `/` if there is none).

**Ordering rule.** All bind sources are opened as `O_PATH` fds before any tmpfs is mounted. Then the hide trees get their tmpfs, and binds and masks are applied by path depth, shorter first (ties keep the caller's order), so an ro parent `P` is mounted before an rw child `P/c`, and a mask `P/m` after `P` and before its re-bound children `P/m/own`. The order of the lists does not matter. A symlink leaf stays a link.

**Fail closed.** A missing bind source or mask, a path listed in both `rw` and `ro`, or any failing mount makes stage 1 exit non-zero with a message naming the path, before the target is exec'd. There is never a partial view.

**Refused combinations (`ValueError` from `make_cfg`).** `pid=False` with `forward=True`: the egress forwarder is only killed by `--kill-child` of the pid namespace, so without one it would outlive the run. Use `pid=True`, or `net=True` without `forward` (the G-010 worker profile). `vendor_dir` and `keep` in allow mode: they are deny-mode args and would be silently dropped; list the paths in `ro`/`rw`. A symlink listed in `rw`/`ro` would bind a dangling link; list its realpath. (The `claude` link that `make_cfg` adds to `ro` itself is recreated as a link inside the namespace, not bound.) A symlink in any parent directory of an `rw`/`ro` path (or of the `claude` link) is refused too. Each bind source is opened component by component, `openat(O_PATH|O_NOFOLLOW)` from `/`, so a parent swapped for a symlink after `make_cfg` checked it exits non-zero naming the component before exec; the paths are not re-resolved with `realpath`. Mount targets, the directories created for them and masks are walked the same way (`openat(O_PATH|O_NOFOLLOW)`, missing directories via `mkdirat` on the chain fd) and mounted through `/proc/self/fd/<fd>`, so a parent swapped after the walk cannot redirect a mount; a symlink anywhere in a target or mask path (leaf included, except the recreated `claude` link) exits non-zero before exec.

**Writability and egress caveats.** `forward=True` puts the forwarder socket at `<trial>/proxy.sock`; if the trial is not listed in `rw`/`ro` and lies under a hide tree, the socket is hidden and egress fails closed. An unlisted trial that lies outside every hide tree is not hidden by tmpfs and stays host-writable: the "nothing is implicitly writable" rule (C1) is about what is bound back, not about paths the hide trees do not cover.

## Worker profile (`scout/workerbox.py`, G-010)

Security code. `prepare_agent(agent, role, cwd, factory_home, db_dir, real_claude, rw_cwd)` (run by HQ) writes `~/.local/state/factory/workers/<agent>/` with `bin/claude` (the shim, 0755, no secrets), `gitconfig` (user.name/user.email from the owner's global git config and a credential helper that hands git `$GH_TOKEN`; no other keys) and `claude/` (the persistent `CLAUDE_CONFIG_DIR`, 0700, seeded once with onboarding done and the cwd trusted; later calls only add a missing cwd entry). Sandboxable roles: reviewer, qa, coder (pm's cwd is `~/factory`, which the cwd rule refuses). The agent name must be a slug, because it becomes a path.

**Worker sizes.** `workerbox` passes `tmpfs_sizes={/tmp: 512m, home: 64m}` (`TMP_SIZE`, `HOME_SIZE`): the private `/tmp` holds the Claude scratchpad, test temp files and clones, and 8m ENOSPCs. Worst case per worker is 576 MB of RAM/swap on a ~5.7 GB box; the runtime dir and `/dev/shm` keep 8m.

**The view** (`nsbox.make_cfg(mode="allow", net=True, pid=False, keep_env=(CLAUDE_CODE_OAUTH_TOKEN, GH_TOKEN))`): empty tmpfs over the real home, `/run/user/<uid>`, `/tmp`, `/dev/shm`. Read-only: `factory_home` (code, roles, run, `.git`), the claude link and its install dir, `~/.local/bin/factory` (the link is recreated inside the writable home tmpfs, not bound, so `factory` resolves via PATH; the host link is untouched), `agent_dir/bin`, `agent_dir/gitconfig`. Read-write: `db_dir`, `agent_dir/claude`, and the cwd when `rw_cwd` (read-only otherwise). `factory_home/worktrees` is masked; only the cwd is bound back. Network stays on, no pid namespace (the chain is exec-only: claude keeps the shim's pid, process group and session). **Without a pid namespace, `/proc/<pid>/cmdline` of every owner process is readable inside: owner-side tools must never put secrets in argv.** `.claude.json` is read up to 16 MiB (`CLAUDE_JSON_MAX`); a larger file is refused with a `ValueError` and left untouched, never truncated and reseeded.

**cwd rule.** `ValueError` for a cwd equal to or above the home, `factory_home`, the agent dir or a hide tree, inside a hide tree, or inside `factory_home` but not strictly under `factory_home/worktrees/` (a writable cwd under `bin/`, `roles/` or `db/` would make code that HQ runs writable).

**Shim (outside, then exec).** Reads `worker-oauth-token` and the role's gh token (`gh-worker-ro-token` for reviewer and qa, `gh-worker-rw-token` for coder) with `nsbox.read_token`: missing, symlink, not 0600, foreign owner or empty gives exit 3 naming the path, and claude never starts. The env is built from an allow-list, `WORKER_ENV_PASS` plus `LC_*`, and the values the shim sets:

| variable | source |
|---|---|
| `PATH`, `LANG`, `TERM`, `LC_*`, `CLAUDE_CODE_ENTRYPOINT`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`, `FACTORY_AGENT`, `FACTORY_ROLE`, `FACTORY_HOME`, `HERDR_PANE_ID` | the caller (pane id only, for identity recovery in `bin/factory`; the sockets stay hidden) |
| `HOME` | the real home path (an empty tmpfs inside) |
| `CLAUDE_CONFIG_DIR`, `GIT_CONFIG_GLOBAL`, `DISABLE_AUTOUPDATER=1` | set by the shim |
| `CLAUDE_CODE_OAUTH_TOKEN`, `GH_TOKEN` | the token files, env only (never argv, the shim file or the cfg) |

Everything else is absent, in particular `SSH_AUTH_SOCK`, `DBUS_SESSION_BUS_ADDRESS` and `TMPDIR`.

**The shim is trusted code and the worker cannot influence it.** `agent_dir/bin` and `agent_dir/gitconfig` are read-only binds (a rename of `bin` fails with EBUSY), `trial_dir` is only `agent_dir/claude`, and every outside write into the agent dir is a temp file plus rename (`O_EXCL`), so a symlink planted in `claude/.claude.json` is replaced, never followed, and a directory there is refused.

Tests: `tests/test_workerbox.py` runs the real chain against a fake home (credentials, ssh key, gh config, listening sockets for the user bus, the multiplexer control socket and the agent proxy), a pty test, and read-only probes of the owner's real paths (each skipped if the path is absent; userns tests skip with the reason from `userns_available()`).
