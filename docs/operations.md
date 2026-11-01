# Operations

How the factory comes back after a VM reboot, and how an agent's identity is decided. For the first install see [SETUP.md](SETUP.md); this page is the reference you come back to once it runs.

The two sections below were moved here from the README (Boot and Identity) with only scrubs and link fixes.

## Boot

`systemd/` holds the user units that bring the factory back after a VM reboot:

- `herdr-server.service` runs `herdr server`, the documented headless server for service-style setups. An interactive `herdr` / `herdr --session default` (for example from a mosh login) attaches to the same default session socket, `~/.config/herdr/herdr.sock`.
- `factory-up.service` waits up to 120 s for `herdr workspace list` to succeed, then runs `factory up`. That resumes the director, boss and every active team's lead with `--resume <session_id>`. It skips agents herdr already runs (by name or session id, since herdr restores its own layout and resumes panes after a server restart), skips closed teams, and exits non-zero if herdr is unreachable.
- `factory-tick.service` / `factory-tick.timer` are the versioned heartbeat units; the tick now orders after `factory-up.service`.

`herdr-server.service` is `Type=simple` on the strength of the herdr docs, which describe `herdr server` as the explicit foreground/headless server for supervised setups. It was not verified live (no herdr experiments before merge); check `systemctl --user status herdr-server` after the first owner-approved install, and switch the type if the process forks.

The installed units are symlinks into the main checkout, so a unit change merged to main applies at the next `daemon-reload` or reboot without re-running `install.sh`. A broken unit merged to main would break the next boot.

Workers (coder, qa, ...) are not resumed; they show up as lost and the director respawns them.

Install (owner-approved step, run from the main checkout, not a worktree):

    bash systemd/install.sh --dry-run   # prints every action, changes nothing
    bash systemd/install.sh             # links units into ${XDG_CONFIG_HOME:-~/.config}/systemd/user, daemon-reload, enable

The script never uses `--now` and never stops or restarts herdr or the tick. Linger must be on (`loginctl show-user $USER -p Linger` prints `Linger=yes`; otherwise `loginctl enable-linger $USER`).

### Post-reboot role flags (flagless agents)

herdr resumes agent panes after a server restart as a bare `claude --resume <id>`: no `--settings`, `--model`, `--permission-mode` or `--append-system-prompt-file`. An agent in such a pane runs without role isolation (no worker guard, no dead socket, no role prompt). Safeguards:

- **Detection (always on).** `factory up` and the tick scan `/proc/*/cmdline` for the claude process running each agent's `--resume <session_id>`. If its command line lacks `--settings <run>/settings/<role>.json`, the agent is *flagless*: `factory up` adopts it as live anyway (it stays addressable: nudges, identity, spawn gate and listing are unchanged, `status` stays `live`), the row gets `flagless=1` and `flagless_since`, `factory agents` shows a `FLAGLESS` marker, the digest lists it under Blocked, a `flagless` event `{name, pane, pid}` is recorded, and the director gets one message (`flagless agents after restore: <names>. Relaunch needs owner approval: close pane + factory up`). The tick repeats the check at most once an hour per agent (throttle state lives in the `throttle` table) and clears the flag when the process later has its role flags. Nothing is killed or closed automatically. A session with no live process found is treated as unknown and adopted as before.
- **`factory relaunch <name> [--dry-run]` (owner/director).** Prints its plan, then closes the agent's pane (`herdr agent prompt /exit` is not a documented clean exit, so `herdr pane close` is used), waits for it to go, and starts the agent again with `--resume <session_id>` and the full role flags. It refuses names that are not factory agents and panes that host another session.
- **Opt-in: make `factory up` the only resume path.** `bash systemd/install.sh --disable-herdr-agent-resume [--dry-run]` sets `[session] resume_agents_on_restore = false` in `$XDG_CONFIG_HOME/herdr/config.toml` (default `~/.config/herdr/config.toml`; idempotent, backup `config.toml.bak-<timestamp>`). herdr then restores the layout with plain shells and `factory up` starts each agent in its own restored pane (matched by the pane id in the DB, if herdr lists it at a shell prompt; otherwise a new pane is placed) with `--resume` plus the full role flags. **Trade-off:** the setting is global, so herdr also stops resuming your own non-factory claude panes after a restart. It is not the default for that reason; it is your call. Without it, the detection above still catches flagless panes and you relaunch them.

Verify: `systemd-analyze --user verify systemd/*.service systemd/*.timer`, `systemctl --user is-enabled factory-up.service herdr-server.service`, and after a reboot `journalctl --user -u factory-up -b` plus `factory agents` (boss, director and the lead live with unchanged session ids).

## Identity

`factory` takes the caller from `FACTORY_AGENT`. Fail closed: inside an agent context (`CLAUDECODE` or `HERDR_PANE_ID` set) an unset `FACTORY_AGENT` never means owner. The pane id may recover the live agent (a warning goes to stderr); otherwise the identity is `unknown`: read-only commands run, `inbox` prints nothing and marks nothing read, every write exits 3. `FACTORY_AGENT=owner` is honoured anywhere, except in a pane registered to an agent with status `live` or `done` (exit 3 for every command, reads included, plus an `identity-spoof` event attributed to `pane:<id>`). Outside an agent context the caller is the owner, as before.

This guards against accidental loss of the environment, not against an adversary: identity is still self-asserted through env vars (for example `env -u CLAUDECODE -u HERDR_PANE_ID` or a forged `FACTORY_AGENT` bypasses it).

## Tick phase timers

`factory config tick_phase_timers 1` makes every tick time its phases (default `0`, off: no output, no event, only a few integer counter increments). Log-only: it changes no behaviour, and a timer failure is swallowed.

With it on, each tick prints one line to stdout (so it lands in journald) and records one event row (`action` `tick-phases`, same text), also on the "herdr unreachable" early return:

    tick-phases total=<s> <phase>=<s> <phase>=<s> ... dequeue_spawns=<n> dequeue_spawn_s=<s> ci_wake_ls_remote=<n> ci_wake_gh=<n> export_git=<n>

Seconds have 2 decimals and phases come in run order (`migrate_db`, `render_settings`, `herdr_list`, `digest`, `tmp_alert`, `remote_url_alert`, `main_protection`, `tmp_clean`, `red_main`, `reap_serves`, `reap_orphans`, `judge_ci_prune`, `scout`, `skilleval`, `pending_nudges`, `reap_loop`, `drift`, `flagless`, `claude_audit`, `stop_fallback`, `gate`, `ci_wake`, `wake_leads`, `dequeue`, `dispatch`, `start_confirm`, `nudge`, `sidebar`, `export`, `freshness`, `recycle`, `relaunches`, `util`). A phase's time runs from the end of the previous one. Counters: `ci_wake_ls_remote` and `ci_wake_gh` (calls made by the CI-wake step), `export_git` (git subprocesses in `export`), `dequeue_spawns` and `dequeue_spawn_s` (spawns started by the dequeue loop and their summed wall time).

Read it with `journalctl --user -u factory-tick.service | grep tick-phases`. About 1440 lines a day while on; turn it off with `factory config tick_phase_timers 0` after a sample.

### Async agent start (`tick_async_start`)

`factory config tick_async_start 1` (default `0`, off: spawn and tick behave as before) makes `_spawn_start` call `herdr agent start --timeout 1` instead of `--timeout 90000` and skip the 2 s kickoff sleep. The kickoff message is still sent to the inbox at spawn time; the row gets `agents.start_pending` (spawn epoch) and `start_pending_info` (what to finish). The tick's `start_confirm` phase, before the nudge step, then looks at the herdr agent list: an agent listed `idle`, `done` or `working` gets the `relaunched-fresh` event (fresh spawns), the flagless clear, one kickoff `agent prompt` and `last_nudge`, and the pending mark is cleared. An agent not listed ready after 90 s gets the `start-uncertain` event, no prompt, and the mark is cleared. The nudge step skips agents still pending or confirmed in that tick. Sandboxed spawns and the evaluator always take the synchronous path, so the sandbox escape check is never delayed. Rollback: `factory config tick_async_start 0`; rows still pending finish on the next tick.
