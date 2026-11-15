# Terminal backends (TUI vs herdr-core)

Hivemill talks to agent panes through one of two terminal backends. Core (`hc`) is the default backend when it is on PATH (`terminal_backend=auto`). The classic herdr TUI is still supported and is opt-in with `factory config terminal_backend herdr`. `factory spawn --host-backend herdr|hc` overrides the backend per agent.

## TUI versus core

| Backend | Binary | Status source |
|---------|--------|---------------|
| TUI | `herdr` (Herdr 0.9.x) | Screen detection: the TUI reads the screen and reports a status. |
| core | `hc` ([herdr-core](https://github.com/herdrdev/herdr-core)) | Only what the program reports over OSC 7501. |

- The TUI is the herdr binary. Its status is derived from the screen.
- Core is `hc` (herdr-core). Its status is the OSC 7501 `status.state`: `idle`, `working`, `done`, `blocked` or `error`. When the program has not reported, the status is `unknown`.
- Terminal state (`live` or `finished`) describes the pane, not the agent. It is not an agent status.
- Factory maps its TUI verbs to `hc ls`, `run`, `send`, `read` and `keys`, and does not set `HERDR_SOCKET_PATH` on those subprocesses (core uses `HC_SOCKET`).
- `factory spawn` on core maps the TUI agent start to `hc run --bg` through `run/<agent>.hc-spawn.sh`. The script uses `env -i` plus explicit exports, so the child sees role settings such as `FACTORY_ROLE` but not `CLAUDE_CODE_MESSAGING_TOKEN`, `HC_SOCKET`, the live `HERDR_SOCKET_PATH`, `HERDR_PANE_ID` or `SSH_AUTH_SOCK`.

## How to switch

Config `terminal_backend` is one of `herdr`, `hc` or `auto`:

- `herdr`: always use the TUI binary.
- `hc`: always use herdr-core.
- `auto` is the default. It prefers `hc` whenever it is on PATH, even when `herdr` is too. With only `herdr` it uses `herdr`. With neither, factory still invokes `herdr`, so errors match the historical "herdr missing" behaviour. `terminal_backend=herdr` keeps the TUI.

`factory doctor` and `factory res` print the configured value and the effective backend after `auto` is resolved.

To put one agent on core and leave everyone else on the TUI, spawn it with `factory spawn --host-backend hc`. The flag is stored as `spawn_args.host_backend`, and `factory agents` shows it in the backend column. `factory spawn --host-backend herdr|hc` still overrides the config per agent. Omit the flag to follow the config. To move one worker only, use factory spawn --host-backend; the config moves every new spawn.

## Upgrade

Installs that have both herdr and hc on PATH and terminal_backend at its default (auto) move to herdr-core on upgrade: new spawns run on hc. Agents already running stay on the TUI until they exit. To stay on the TUI, run factory config terminal_backend herdr. Installs without hc keep the old behaviour.

## Caveats

A missing status is never idle.
Do not point herdr_socket at the core socket.
Do not replace ~/.local/bin/herdr.
hc wait --output did not return in the on-box proof.
The two servers do not share agents.

- A missing status maps to `unknown`. That blocks nudges, idle-coder reap, start-confirm and recycle, which would otherwise treat idle or done as safe.
- Factory does not call `hc wait` and does not treat a quiet screen as idle.
- Workers cannot run `hc`. The worker hook blocks it the same way it blocks `herdr`.
- HQ hook mode allows `hc server stop` because it allows `herdr server stop`.
- Legacy TUI rows (pane ids that are not the agent name) are left alone when `terminal_backend` is `hc`.
- Keep `terminal_backend` at `herdr` or `auto` while the live factory stays on the TUI.

## CI and tests

Stub binaries live under `tests/stubs/herdr` and `tests/stubs/hc`. The test `PATH` places both stubs ahead of any real binaries.
