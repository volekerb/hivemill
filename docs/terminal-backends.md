# Terminal backends (TUI vs herdr-core)

Hivemill talks to agent panes through one of two **terminal backends**:

| Backend | Binary | Role |
|--------|--------|------|
| **TUI** | `herdr` | Herdr 0.9.x terminal UI; screen-based idle/working/done detection |
| **core** | `hc` | [herdr-core](https://github.com/herdrdev/herdr-core) (herdr-core runtime); status from what the agent reports (e.g. OSC 7501), not from screen scraping |

Config `terminal_backend` selects which CLI factory runs:

- **`herdr`** — always use the TUI binary (`herdr`).
- **`hc`** — always use herdr-core (`hc`). The Hivemill **socket pin** stays `config herdr_socket` (same env var `HERDR_SOCKET_PATH` for subprocesses); do **not** point `herdr_socket` at a core-only socket unless you intend to — core and TUI servers are separate.
- **`auto`** (default) — if both `herdr` and `hc` are on `PATH`, prefer **`herdr`** (TUI). If only one exists, use that one. If neither exists, factory still invokes `herdr` so errors match the historical “herdr missing” behaviour.

`factory doctor` and `factory res` print the configured value and the **effective** backend (after `auto` resolution).

## Behaviour notes

- **Missing status is never idle**: if an agent row from `agent list` has no `agent_status` (or it is empty), factory treats it as **`unknown`**. That blocks nudges, idle-coder reap, and other logic that would treat **idle** or **done** as safe.
- **Known finding (G-031)**: `hc wait <task> --output <path>` printed the expected output but **did not return** in one on-box trial. Do not rely on `wait` for automation until that is understood; document any new observations in goal notes.
- **Default for production**: keep `terminal_backend` at **`herdr`** or **`auto`** while the live factory stays on the TUI. A separate pilot can set `hc` per org/role once worker-guard parity and interfaces are complete.

## CI and tests

Stub binaries live under `tests/stubs/herdr` and `tests/stubs/hc` (same JSON protocol for the harness). The test `PATH` places both stubs ahead of any real binaries.
