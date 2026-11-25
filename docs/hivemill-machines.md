# hivemill on several machines, and on macOS

The Option B runner (`hivemill`) runs on one machine, the **coordinator**. The coordinator holds the factory DB
(SQLite), runs the daemon and the hc core. It can start task sessions on other hc machines, called **workers**. A
worker is only a place to run a Claude Code session. Every `hivemill` call that a session or its hooks make on a
worker runs on the coordinator.

## Placement

Two config keys, both read when a task session is about to start:

| key | default | meaning |
|---|---|---|
| `session_machines` | empty | hc machine names in order, for example `worker1,local`. Empty: every session runs on the local core, as before. `local` (or the core's own name) means the coordinator itself. |
| `machine_mem_floor_mb` | `2500` | the MemAvailable a listed machine needs to take a new task session. |

The runner walks `session_machines` in order and takes the first machine that qualifies. If none qualifies, the
session runs on the local core. A worker qualifies only when one `hc exec --on <machine>` probe shows all of this:

- **Same factory home.** The runner writes a fresh random marker under `<home>/state/hivemill/placement/`. The
  worker must read the same token at the same path. Nothing about the filesystem is assumed.
- **Same user home path.** Settings, `~/.claude/projects` and the spawn script use absolute paths.
- **A forwarding `hivemill`.** `~/.local/bin/hivemill local coordinator` must print a coordinator (see below). The
  runner asks only after the marker matched, so a machine that runs its own hivemill is never touched.
- **Enough memory.** On Linux this is MemAvailable from `/proc/meminfo`. On macOS it is free, inactive and
  speculative pages from `vm_stat`, and 0 while the memory pressure level is not normal.

After the clone exists, the runner also checks that `git -C <clone> rev-parse HEAD` on the worker equals the local
value. The clone shares objects with the project checkout, which the home marker does not prove. A mismatch puts the
session on the local core.

Each skipped machine and its reason go into the `task.dispatched` event (`skipped`).

When `session_machines` is set, the dispatch memory cap (`mem_floor_mb`) checks the coordinator only when the
session would run there. A busy coordinator then does not hold dispatch while a worker has room.

### What is recorded

- The run records its machine, `Machine` in `hm_runs.data`. Empty means the local core.
- `session.started` and `task.dispatched` events carry `data.machine`: where the session runs (`local` for the local
  core). The top-level `machine` of an event is the coordinator that wrote it (`hivemill obs`).
- `hivemill run status` prints `on <machine>`.
- Reconcile relaunches a lost task session with `claude --resume` on the recorded machine. The transcript and the
  worker's trust entry are there.
- While hc lists that machine as disconnected, the session is not counted as lost. hc keeps the terminal and lists it
  again after reconnect.
- The judge and the planner always run on the local core.
- Before a session starts on a worker, the runner trusts the clone in the worker's own `~/.claude.json`, through
  `hivemill local trust <clone>`.

## Hook forwarding

On a worker, the factory DB is reachable only over the shared filesystem. SQLite in WAL mode is not safe across
hosts, so a worker's `hivemill` never opens the DB. Configure it with either of these:

- the file `~/.config/hivemill/coordinator`, one line: the coordinator's ssh name. This is what counts for
  sessions. They start under `env -i`, so the placement probe also asks under `env -i`. Keep this directory local to
  the worker: a shared `~/.config/hivemill` would make the coordinator forward to itself.
- the environment variable `HIVEMILL_COORDINATOR=<name>`. It only helps interactive use, and it overrides the file.

Every call, the session's own and its Stop and SessionStart hooks', then becomes:

```
ssh -o BatchMode=yes -- <coordinator> 'PATH="$HOME/.local/bin:$PATH"; export PATH; cd <cwd> || exit 1;
  exec env HIVEMILL_FORWARDED=1 FACTORY_...=... hivemill <args>'
```

- Every argument, the cwd and every `FACTORY_*` variable (`FACTORY_HOME`, `FACTORY_AGENT`, ...) are shell-quoted.
- stdin passes through, so hook JSON reaches the coordinator.
- The exit code is the remote one. 255 means ssh could not connect, and 1 means the cwd does not exist on the
  coordinator.
- `HIVEMILL_FORWARDED` stops a second forward, and `hivemill local ...` never forwards.
- `hivemill guard`, `hivemill check` and `hivemill affected` open no DB and run on the worker. These are the guard
  hook, the clone's pre-push check and the affected-test runner. `hivemill pr create` is forwarded and runs `gh` on
  the coordinator.
- The worker needs non-interactive ssh to the coordinator (`BatchMode`) and a `hivemill` in the coordinator's
  `~/.local/bin` or PATH.

Worker install:

```
go build -o ~/.local/bin/hivemill ./go/cmd/hivemill    # or copy the binary built for that OS
mkdir -p ~/.config/hivemill && echo <coordinator> > ~/.config/hivemill/coordinator
hivemill local coordinator    # prints <coordinator>
hivemill run status           # runs on the coordinator
```

Then, on the coordinator: `hivemill config session_machines worker1,local`.

### Known limits

- **Gate proof re-runs run on the coordinator.** The Stop hook (`hivemill gate hook`) is forwarded, so the gate
  re-runs the `test:` and `cmd:` proofs on the coordinator, in the same clone path over the shared home. This
  works, but the proofs use the coordinator's CPU and toolchain, not the worker's.
- **Plan usage.** The worker writes its own `~/.claude/usage-limits.json`, and only the coordinator's copy feeds the
  usage caps. Sessions on the coordinator (planner, judges) keep that copy fresh. With none running there, the reading ages, and the usage probe rule applies.
- **The worker guard runs on the worker,** through the guard shim or `hivemill guard`. Where it falls back to the
  Python guard, that guard reads the factory DB read-only over the shared filesystem, as it did for the old
  pipeline's workers.
- **The old `factory` CLI is not covered.** A worker that also runs old-pipeline sessions needs that CLI's own
  forwarder.

## Running on macOS

hivemill builds and runs natively on macOS (arm64). The following was checked on a Mac with Go 1.27.3, from a clean
clone, with a scrubbed PATH (no hc, no factory CLI) and a temporary `HOME`:

| check | result |
|---|---|
| `go vet ./...` | exit 0. Before this change it failed, because `internal/factorytest/sandbox2` tests use `syscall.Mount`. Those tests are now `//go:build linux`. |
| `go test ./internal/hm/... ./cmd/hivemill/... ./internal/kit/...` | all packages ok. 153 tests pass, none skipped or failed. Linux runs 157; the 4 extra are Linux-only `/proc` reader tests in `kit/clean/procx`. |
| memory reading | `MemAvailableMB` returned -1 on macOS: it read only `/proc/meminfo`. So `hivemill report` printed "mem -0.0 GB free" and the `mem_floor_mb` cap was silently skipped. It now uses `vm_stat` and the memory pressure level, and a reading that fails counts as 0 (the cap holds), as in the old pipeline. On the test Mac it read about 10 GB, matching `vm_stat` by hand. |
| placement probe | a Mac listed in `session_machines` that does not share the coordinator's home is refused at the marker check, and its own hivemill is never called. |

To run the daemon on a Mac, use `hivemill launchd daemon` (a launchd agent) in place of the systemd unit. A Mac can
also be a worker. It then needs the same factory home and user home paths as the coordinator, which usually means
the same user name and a shared mount.
