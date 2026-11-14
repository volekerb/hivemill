# block_conditions (G-040)

Config key `block_conditions`: `0` or `1`, default `0` (off). Turn off with `factory config block_conditions 0`. While off, the tick does not change blocked rows: no unblock, no `met` updates, no free-text expiry alert, and no lead message from this feature. Turning the flag on in the live factory is a director decision; do not set `1` in a coder PR.

The typed condition engine lives in Go (`go/internal/blockcond`, `go/cmd/block-eval`). There is no new Python file and no Python engine. **G-041** owns the port of existing Python; G-040 only adds the flag, registry, and this doc.

## Closed condition tokens

Only these tokens are accepted (`factory task block T --on <token>`). Unknown types are refused (exit 2).

| token | met when |
|---|---|
| `task:T-0123` | that task's status is exactly `done` |
| `pr:#554:merged` | that PR on the factory repo is merged |
| `pr:#554:closed` | that PR is closed (merged or not) |
| `file:bin/factory_cli.py` | no open PR on the factory repo touches that path |
| `main:green` | the existing main-green check reports success |
| `config:task_clones=1` | string equality with factory config |
| `owner:M31382` | a message with that id and `kind=decision` exists |
| `check:gh-worker-ro-token-mode-600` | allowlisted read-only probe passes |
| `check:gh-worker-rw-token-mode-600` | allowlisted read-only probe passes |
| `until:2026-11-19T00:00:00+01:00` | now is at or after that instant (RFC3339) |
| `goal:G-041:done` | goal status is exactly `done` |
| `goal:G-041:released` | goal is `done` and a release tag exists at or after goal close |

Several `--on` flags are AND. Free-text blocks are still allowed via `factory task update --status blocked --note ...`; after 24 h the tick alerts the lead to convert them to typed conditions or give a next step.

## Repo and dependencies

v1 has **no repo field** on conditions: PR, file, main, and release checks use the factory repo only (`factory_remote_url` or origin). Other projects stay free-text blocks.

**`depends_on` is not a condition.** It gates when a `todo` task may start; the tick does not read it to unblock a `blocked` row. Use `task:T-id` when the wait itself is the block.