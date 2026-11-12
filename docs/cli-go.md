# Go CLI dispatch (G-041, stage S1)

`bin/factory` can hand a subcommand to the Go binary `factory-go` (built from `go/cmd/factory`). Python stays the default and the fallback; nothing changes until a director sets the flags below.

## Flags (director only, like `guard_impl`)

| key | values | meaning |
|---|---|---|
| `cli_impl` | `python` (default), `go`, `shadow` | `go`: listed commands run in Go. `shadow`: Python answers, then Go runs the same argv (2 s timeout) and a difference is logged. |
| `cli_go_cmds` | comma list, default empty | commands that may go to Go. S1 accepts `whoami`, `flags`, `agents`, `config`; anything else is refused with exit 2. |

`factory config cli_impl python` is the rollback and takes effect on the next call.

## How a call is routed

`factory config`, `factory up` and every tick write `$FACTORY_HOME/run/cli_impl` as `<mode> <srchash> <csv>` (or `python -`). `bin/factory` reads it before it imports `factory_cli`, with `os` and `sys` only. It execs `~/.local/lib/factory/factory-go` (argv[0] stays the `bin/factory` path, which Go uses to find the Python entry) only when all of these hold:

- the mode is `go` or `shadow` and the file parses;
- `argv[1]` is in the csv;
- `FACTORY_CLI_IMPL` is not `python`;
- `factory-go` is executable and `factory-go.stamp` equals the srchash (`git rev-parse HEAD:go`).

A missing or malformed file, a stale or missing binary or stamp, or a command not in the list means Python. `FACTORY_CLI_IMPL=python` forces Python; no value forces Go. Go itself hands any shape it cannot answer back to Python with `FACTORY_CLI_IMPL=python`, so there is no exec loop.

`FACTORY_CLI_TRACE=1` prints `impl=python|go|shadow` to stderr, once per dispatch.

## Shadow mode

Only read-only calls are shadowed (`whoami`, `agents`, `flags`, `config` with at most one argument); a write would run twice. Python's output and exit code are what the caller gets. When Go's exit code, stdout or stderr differ, one line goes to `$FACTORY_HOME/logs/cli-shadow.log`: timestamp, sha256 of the argv, both exit codes, sha256 of each output. Never the text. A shadowed read that Go delegates back to Python (a shape Go does not answer itself) can run Python twice; the output is still Python's.

## Build

The tick and `factory up` build `go/cmd/factory` into `~/.local/lib/factory/factory-go` (CGO off, same allowlisted environment as the guard build), rename it into place and write `factory-go.stamp` last. Builds happen only when `cli_impl` is not `python`. `factory up` also fetches the modules (two steps, 120 s each); with the guard build (300 s) and this build (300 s) the worst case exceeds the 600 s of factory-up.service. That is safe: the flags are written before each build, so a killed `up` leaves a stale or missing stamp and every command stays on Python.

## Protected from workers

`<live>/run/cli_impl` and everything under `~/.local/lib/factory` (so `factory-go` and its stamp) are protected like `run/guard_impl`, in the Python hook and in `go/internal/guard`. Reads stay allowed.

## Schema version

`SCHEMA_VERSION` in `bin/factory_cli.py` is written to `PRAGMA user_version` by `factory init` and by every `migrate()`. The Go side (`go/internal/factorydb` `SchemaVersion`) answers only when it reads the same number. Changing `SCHEMA` or `migrate()` requires bumping both constants; `tests/test_db_layout.py` pins a hash of the two and fails until the pin is renewed.

## Building and dependencies

`factory-go` has one external module (modernc.org/sqlite, pinned by `go/go.sum`); the worker-guard stays dependency-free
(`go list -deps ./cmd/worker-guard` lists only this module). Builds are always offline (`GOPROXY=off`, private
`GOMODCACHE=~/.local/lib/factory/go/mod`), so the tick never touches the network. `fetch_cli_go_deps` is the only network
step: `factory up` runs `go mod download` then `go mod verify` (`-mod=readonly`, checksum database on) when `cli_impl` is
not python. When the modules are missing the build fails with "deps not fetched: run factory up" and `bin/factory`
stays on Python. CI seeds a module cache and exports `GOMODCACHE_SEED`; the real-Go tests skip without it.
