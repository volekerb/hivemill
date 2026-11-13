# Terminal backends (TUI vs herdr-core)

Hivemill talks to agent panes through one of two **terminal backends**:

| Backend | Binary | Role |
|--------|--------|------|
| **TUI** | `herdr` | Herdr 0.9.x terminal UI; screen-based idle/working/done detection |
| **core** | `hc` | [herdr-core](https://github.com/herdrdev/herdr-core) (herdr-core runtime); status from what the agent reports (e.g. OSC 7501), not from screen scraping |

Config `terminal_backend` selects which CLI factory runs:

- **`herdr`** — always use the TUI binary (`herdr`).
- **`hc`** — always use herdr-core (`hc`). Factory maps TUI verbs to `hc ls` / `run` / `send` / `read` / `keys` and does **not** set `HERDR_SOCKET_PATH` on those subprocesses (core uses `HC_SOCKET`). Do **not** point `herdr_socket` at the core socket — the two servers are separate.
- **`auto`** (default) — if both `herdr` and `hc` are on `PATH`, prefer **`herdr`** (TUI). If only one exists, use that one. If neither exists, factory still invokes `herdr` so errors match the historical “herdr missing” behaviour.

`factory doctor` and `factory res` print the configured value and the **effective** backend (after `auto` resolution).

## Behaviour notes

- **Missing status is never idle**: if an agent row from `agent list` has no `agent_status` (or it is empty), factory treats it as **`unknown`**. That blocks nudges, idle-coder reap, and other logic that would treat **idle** or **done** as safe.
- **Known finding (G-031)**: `hc wait <task> --output <path>` printed the expected output but **did not return** in one on-box trial. Do not rely on `wait` for automation until that is understood; document any new observations in goal notes.
- **Spawn on `hc`**: `factory spawn` maps `herdr agent start` to `hc run --bg` via a spawn script under `run/<agent>.hc-spawn.sh`. The script uses `env -i` plus explicit exports so the child sees role settings (for example `FACTORY_ROLE`) but not the core messaging credential (`CLAUDE_CODE_MESSAGING_TOKEN`), `HC_SOCKET`, live `HERDR_SOCKET_PATH`, `HERDR_PANE_ID`, or `SSH_AUTH_SOCK`. No secret values appear on the hc argv. Legacy TUI rows (pane ids that are not the agent name) are left alone when `terminal_backend=hc`.
- **Default for production**: keep `terminal_backend` at **`herdr`** or **`auto`** while the live factory stays on the TUI. A separate pilot can set `hc` per org/role once worker-guard parity, interfaces, and spawn are complete.

## What `hc` 0.1.21 prints

Checked against a real core (read-only commands):

- `hc ls --json` is `{"hidden_finished": N, "machines": [...], "terminals": [...]}`. A row is `{"address": "t2", "agent": "claude-code", "cwd": "...", "name": "...", "state": "live", "status": "idle", "turn": 0, "where": "<space> / <tab>"}`. `status` is a plain string here. A row may have no `name` (unnamed terminal), no `status` (the agent has not reported yet; factory reads that as `unknown`) or no `where`.
- Rows from another machine (added with `hc machine add`) have `address` `<m>/t19`. A bare name does not reach them: `hc get <name>` is `not_found` (exit 3), `hc get <m>/<name>` works. Factory never runs with `--on`, so it drops these rows when it reads `ls`; otherwise a remote terminal with the same name as a local agent would stand in for it (status, nudges, kill).
- `agent` is `claude-code` for Claude Code. Factory maps it to `claude`, the name the TUI and the rest of factory use.
- `hc get --json <t>` has more fields (`labels`, `parent`, `pid`, `argv`, ...), and its `status` is an object, `{"app": "claude-code", "state": "idle"}`. `_hc_status_word` takes both forms.
- `hc send <t> <text>` prints `{"started": true, "status": "idle", "turn": 0}`. It waits while a person has an unfinished draft in that terminal (`draft_wait`, default 60 s) and does not type into a blocked program without `--force`. hc errors are JSON too, for example `{"error": {"kind": "not_found", ...}}`, with a non-zero exit (3 for `not_found`).
- `hc keys` takes lowercase key names: `enter`, `esc`, `tab`, `up`, `ctrl+c`.
- hc types notes about child terminals into their parent. `notes = false` in `~/.config/herdr-core/config.toml` turns that off.

## Moving a live TUI team to herdr-core and the Herdr app

Lessons from moving a running team of Claude Code agents (leads, workers, a coordinator) from the herdr 0.9 TUI to herdr-core plus the Herdr desktop app, one agent at a time, while the team kept working.

### Moving one agent

A running agent cannot be moved from one server to another. The move is: exit it in the old pane, then start `claude --resume <session id>` in a new hc terminal. This is the same resume that `factory relaunch` does ([updating-persistent-agents.md](updating-persistent-agents.md)).

1. Find the session id. `herdr agent list` gives it as `agent_session.value`. An agent started without `--resume` has no id in its argv, so do not look for it there.
2. Read the model and permission mode from the pane's status line before it exits, and pass both on the resume (`--model`, `--permission-mode`). The resume does not bring them back by itself.
3. Exit the agent (`/exit`). Two dialogs can stop the exit, and automation must answer them:
   - "Exit and stop tasks / Move to background", shown when the session has scheduled tasks. Option 1 (Enter) exits.
   - "You have 1 unsent feedback draft: Enter to review & send, Esc to discard and exit". Press Esc. Never send the draft.
4. Wait until the old process has actually ended (wait on its pid). If you resume before that, two processes write to one session.
5. Start the new terminal in the agent's original working directory. Claude Code stores sessions per project directory, so a resume from elsewhere does not find the session.
6. A directory can show Claude Code's "trust this folder" prompt in the new terminal even though the old pane ran there. Detect it and answer it ([auto-clear-dialogs.md](auto-clear-dialogs.md) covers the dialogs the tick already handles; this one is not among them).
7. Do not re-create the agent's scheduled prompts (`CronCreate`, `/loop`). `claude --resume` restores session-scoped crons. Ask the agent to check `CronList` instead, or you get each one twice.

### Starting the terminal so the app shows it

```
hc run --bg --keep --no-parent --name <name> --cwd <dir> \
  --label herdr.space=<space id> --label herdr.tab=<tab id> \
  -- zsh -lc 'export PATH=...; exec claude --resume <session id> --model <model> --permission-mode <mode>'
```

- The Herdr app shows an hc terminal only when it has both labels, `herdr.space=<id>` and `herdr.tab=<id>`. An id the app does not know creates a new space or tab. Name them with `herdr-app do space.rename` and `herdr-app do tab.rename`.
- `--no-parent` matters. Without it, a terminal started from inside another terminal is that terminal's child: it ends when the caller's program ends, and hc types notes about it into the caller.
- `--keep` replaces a lost program with a shell in place. That suits a person's long-lived agent terminal. It is not what factory spawn wants (see below).
- A login-shell wrapper (`zsh -lc '...; exec claude ...'`) is fine: hc still detects the agent as `claude-code` and reads its OSC 7501 status.

### Hazard: coordinators that still talk to the old server

During a split move, a coordinating agent (a lead whose `herdr agent list` still pointed at the TUI server) saw the agents already moved as exited and resumed them in the TUI. That left two processes on one session.

What prevents it:

- Move coordinators early, and tell them not to resume anything in the old server.
- Until the move is done, route the coordinators' `herdr` commands through both servers with a small shim: `herdr agent list` returns the old server's agents plus hc's named terminals, and per-agent verbs go to hc when the name lives there.

The shim's verb mapping, next to factory's own (`_hc_translate_args` in `bin/factory_cli.py`):

| TUI verb | live shim | factory with `terminal_backend=hc` |
|---|---|---|
| `agent list` | old list plus hc named terminals | `hc ls` |
| `agent prompt <a> <text>` | `hc send <a> <text>` | same |
| `agent read <a> --lines N` | `hc read <a> --lines N` | same |
| `agent send-keys <a> <keys>` | `hc keys <a> <keys>`, lowercased (`Escape` becomes `esc`) | `hc keys <a> <keys>`, passed as given |
| `agent wait <a>` | `hc wait <a>` | no hc call (see the G-031 note above) |
| `agent start <a> ...` | `hc run --bg --keep --no-parent --name <a>` | `hc run --bg --name <a> [--cwd <dir>]` via a spawn script |

The shim used `hc wait` for single turns without trouble. That does not settle the G-031 `wait --output` finding.

Open points for factory spawn on hc (G-031), not changed here:

- Spawn does not pass `--no-parent`. A `factory spawn` run from inside an hc terminal makes the new agent a child of the caller: it ends with the caller, and hc types notes into the caller.
- Spawn should not pass `--keep`. A lost agent would turn into a shell with the same name and no status, which looks live or unknown instead of gone.
- The `Enter` key sent after a Cursor prompt goes to `hc keys` as `Enter`. hc documents lowercase names; whether it accepts `Enter` is not verified.

### Machines and the old TUI

- `hc machine update <m>` upgrades a remote core in place, and its terminals keep running (seen from 0.1.18 to 0.1.21). Afterwards `hc machine ls` reported `keeps_running_after_logout: true`, with systemd linger on for the user.
- While the move runs, the app can show the old TUI session. Make a space on the remote machine whose terminal runs the `herdr` client: `herdr-app do space.new '{"machine": "<m>", "argv": ["herdr"], "name": "<name>"}'`.

## CI and tests

Stub binaries live under `tests/stubs/herdr` and `tests/stubs/hc` (same JSON protocol for the harness). The test `PATH` places both stubs ahead of any real binaries. The hc stub's `ls` prints the 0.1.21 row shape above; give a fake agent an `"address": "<m>/t7"` to get a row from another machine. `tests/test_hc_status.py` covers the status mapping, the `claude-code` name and remote rows.
