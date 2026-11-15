# Launch note

This is a launch draft for the factory as it stands on the current tag, v0.4.0. It is not a new release tag.

Workers run on Claude and on Cursor. The terminal stays on herdr, with one herdr-core pilot. The worker sandbox isolates a coder's credentials, and the coder step is still open. The Go worker-guard hook is faster on the published latency table. Python stays the default.

### Highlights

- Claude and Cursor. The factory runs workers on Claude and on Cursor.
- herdr-core pilot. One coder, T-1218, was spawned with --host-backend hc. terminal_backend stays herdr. hc 0.1.21 has no view slot, so there is no Hivemill UI plugin.
- Worker sandbox. It isolates a coder's credentials. The coder step T-0171 is still open. The 24-hour hold is not closed and is not shortened.
- Go worker-guard. The Latency table in docs/worker-guard-go.md (2026-11-04) measures about 8 ms in Go against about 69 ms in Python, about 8.5x to 9x, for the roles in that table. Python stays the default.

### New and changed

This draft records status. It changes no default.

- Runtimes. Workers run on Claude and on Cursor. See docs/runtimes.md.
- Terminal. terminal_backend stays herdr. The herdr-core pilot is one coder, T-1218, spawned with --host-backend hc. hc 0.1.21 has no view slot. There is no Hivemill UI plugin. See docs/terminal-backends.md.
- Sandbox. The sandbox isolates a coder's credentials: the worker uses a worker login and a per-role GitHub token, and does not use the owner's Claude login, ssh keys, or gh login. T-0171 is still open. The 24-hour hold is not closed and is not shortened. See docs/worker-sandbox.md.
- Go hook. The measured path is the worker-guard shim, not the factory command line. From the Latency section: Go is faster at the median for every role (about 8.5x to 9x): about 8 ms against about 69 ms, and the Go p95 (9.7 ms at most) is below the Python minimum (63 ms). Python stays the default (guard_impl=python). The factory CLI port is not finished.

### Security

The worker sandbox is the credential isolation above. It is opt-in. It is not a full security boundary, and it has no egress filter. The coder step T-0171 is still open.

### Known limits

- T-0171 is still open. The 24-hour hold is not closed and is not shortened. The sandbox stays on that open step.
- terminal_backend stays herdr. The hc use is one coder, T-1218. hc 0.1.21 has no view slot, and there is no Hivemill UI plugin.
- Python stays the default worker-guard. The latency numbers are the hook table only. The factory CLI port is not finished.
- The current tag is v0.4.0. This draft does not add a tag.

### Upgrade

- Database. No schema change in this draft.
- Config. No key added, renamed, or removed. terminal_backend stays herdr. The guard default stays python.
- Flags. No default changes in this draft.