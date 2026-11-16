# Multi-machine factory (G-057)

The factory can run some workers on a second machine (a worker machine, e.g. `workerhost`) while the single factory DB
stays on the HQ machine (`hqhost`). This page covers the forwarder (T-1582) that lets a worker on the worker machine
run `factory ...` against the HQ DB. Later G-057 tasks fill in the sections marked as such.

Nothing here is active until the owner installs it. With config key `worker_machines` empty (the default) no ssh call
is made, nothing is spawned on a worker machine, and no live code references `factory-forward`.

## Forwarder

`go/cmd/factory-forward` (library `go/internal/forward`) has two modes:

- **client** (worker machine): `factory-forward <factory args...>`. It stands in for `factory` on a worker's PATH. It
  runs `ssh -o BatchMode=yes -o ConnectTimeout=5 -- <hq_host> <remote_receiver> --serve` (an argv slice, never a
  shell) and sends the call over the ssh channel's stdin.
- **server** (HQ machine): `factory-forward --serve`. ssh runs it; it reads one call from stdin and runs the real
  `bin/factory` with it.

### Protocol

Client to server, on the ssh channel's stdin:

```
"FWD1" | uint32 big-endian length N | N bytes of JSON {"argv": [...], "env": {...}, "cwd": "..."} | the call's stdin
```

- `argv`: the factory arguments, unchanged. They never appear on the ssh command line, so no shell ever parses them.
- `env`: only these keys (exact allow-list): `FACTORY_AGENT`, `FACTORY_ROLE`, `FACTORY_GOAL`, `FACTORY_TASK`,
  `FACTORY_ORG`, `FACTORY_CLI_IMPL`, `FACTORY_CLI_TRACE`. The client sends nothing else.
- `cwd`: the client's absolute working directory.
- The header is at most 64 KiB (N <= 65536). Long text goes on stdin with `-`, as on one machine.
- After the header, the rest of the stream is the CLI's stdin, byte for byte. The client forwards its stdin only when
  it is a pipe, socket or regular file (a heredoc, `< file`); a terminal, `/dev/null` or a closed stdin sends
  nothing, so the remote CLI sees EOF at once.

Server to client:

- The server first writes `FWD1-OK\n` on stdout (the Ack); the client strips it. Then the CLI's stdout and stderr
  pass through unchanged, and the exit code is the CLI's (0..255).
- The client holds ssh's stderr back until the Ack. If ssh exits 255 with no Ack, the connection failed: the client
  prints one line `forward: ssh to <hq_host> failed: <ssh's message>` and exits 255. A CLI that itself exits 255
  after the Ack is passed through as 255 with its own stderr.

Server checks (each refusal exits 2 with one `forward: ...` line and runs nothing):

- header longer than 64 KiB (refused before its body is read), bad magic, bad or trailing JSON, unknown fields;
- an env key outside the allow-list: `forward: env key not allowed: <KEY>`. Values are never printed or logged;
- invalid UTF-8 or a NUL byte in argv, env or cwd; a relative cwd;
- a mapped cwd that is not a directory on the HQ machine.

The CLI's env on the HQ machine is: the server's own `PATH`, `HOME`, `USER`, `LOGNAME`, `LANG`, `LC_ALL`,
`LC_CTYPE`, `TZ`, `TMPDIR`, `FACTORY_HOME`; the allow-listed keys from the header; and `FACTORY_FORWARDED=1`. A
client can never set `FACTORY_FORWARDED` (it is not on the allow-list). The identity check that uses it is on the HQ
CLI side (T-C), not in the forwarder.

The cwd is mapped with `path_map`: the longest `[from, to]` pair whose `from` is the cwd or a parent of it replaces
that prefix; a cwd outside every mapping runs in the HQ user's `HOME`.

Known limit: if reading the client's stdin fails mid-stream (an I/O error, not EOF), the client kills ssh and exits
2; the remote CLI may already have seen a shortened stdin. Pipes and files do not fail this way in practice.

### Config

One JSON file, `~/.config/hivemill/forward.json` (override: env `FORWARD_CONFIG`). Unknown keys are refused. There
are no defaults that name a host.

| key | side | meaning |
|---|---|---|
| `hq_host` | client | ssh destination of the HQ machine (`host`, `user@host` or an `~/.ssh/config` alias). Must not start with `-`; must not contain `factory` (G-057 R2). |
| `remote_receiver` | client | path of `factory-forward` on the HQ machine (plain path characters only, since ssh hands it to the remote shell). |
| `factory_bin` | server | absolute path of `bin/factory` on the HQ machine. |
| `path_map` | server | list of `[from, to]` absolute path pairs: worker-machine path -> HQ path. |

Example, worker machine:

```json
{"hq_host": "hqhost", "remote_receiver": "/home/user/.local/bin/factory-forward"}
```

Example, HQ machine:

```json
{"factory_bin": "/home/user/factory/bin/factory", "path_map": [["/home/user/factory", "/home/user/factory"]]}
```

## Install (owner)

The owner does these steps by hand; no factory agent changes sshd, users or `~/.ssh` on either machine.

1. Build on the HQ machine from a checkout of the merged commit:
   `cd go && GOTOOLCHAIN=local go build -o ~/.local/bin/factory-forward ./cmd/factory-forward`.
   Copy the same binary to the worker machine (same architecture): `scp ~/.local/bin/factory-forward workerhost:.local/bin/`.
2. HQ machine: write `~/.config/hivemill/forward.json` with `factory_bin` and `path_map` (example above);
   `chmod 600` it.
3. Worker machine: write `~/.config/hivemill/forward.json` with `hq_host` and `remote_receiver`; `chmod 600` it.
4. ssh key: on the worker machine create a dedicated key for this use only, without a passphrase so BatchMode works
   (`ssh-keygen -t ed25519 -f ~/.ssh/id_forward -N ''`), and point `hq_host` at an `~/.ssh/config` alias that uses it:

   ```
   Host hqhost
     HostName <HQ address>
     IdentityFile ~/.ssh/id_forward
     IdentitiesOnly yes
   ```

5. HQ machine: add the public key to `~/.ssh/authorized_keys` restricted to the receiver, so the key can do nothing
   else (sshd then ignores the requested command and always runs the receiver):

   ```
   command="/home/user/.local/bin/factory-forward --serve",restrict ssh-ed25519 AAAA... forward@workerhost
   ```

   `restrict` turns off port, agent and X11 forwarding and pty allocation. Optionally add `from="<worker address>"`.
6. Check from the worker machine: `FACTORY_AGENT=<an agent> factory-forward whoami` prints that agent as the HQ
   CLI sees it; `echo $?` is 0. Then `factory-forward inbox` with the same env.
7. Putting the forwarder first on a worker's PATH (as `factory`) is done by spawn (T-C) once `worker_machines` is set;
   do not do it by hand.

Rollback: remove the `authorized_keys` line and the two config files. Nothing else depends on them.

## Threat model

Assets: the HQ factory DB and the HQ user's shell. The forwarder adds a path into them; it must add no authority a
worker does not already have.

| risk | control |
|---|---|
| (r1) a worker machine spoofs or adds env (e.g. `GH_TOKEN`, `LD_PRELOAD`, `PATH`, `FACTORY_FORWARDED`) | the client sends only the seven allow-listed keys; the server refuses (exit 2) any other key, and builds the CLI env from its own base keys plus those seven plus `FACTORY_FORWARDED=1`. |
| (r2) exit code or stdin lost | exit codes pass unchanged (Ack distinguishes ssh failure from a CLI 255); stdin is passed as a raw byte stream after a length-prefixed header (1 MiB tested byte-exact). |
| (r3) hang on missing stdin | a terminal or `/dev/null` stdin is not forwarded; the client does not wait for its stdin copy once the remote exits. |
| (r4) shell injection via argv | argv travels in the JSON header, never on the ssh command line; the only remote command is the fixed `remote_receiver`, checked to plain path characters (and pinned by `command=` in `authorized_keys`); `hq_host` cannot start with `-`. Every exec is an argv slice. |
| (r5) behaviour change before install | no live code references `factory-forward`; spawn and the CLI are unchanged until T-C, behind `worker_machines`. |
| (r6) secrets in config or logs | the config holds no secret (host alias and paths only); env values are never printed, even in refusals. The ssh private key stays on the worker machine with mode 600. |
| a header bomb | the header is bounded at 64 KiB and refused before its body is read. |

What the forwarder does not protect against: anyone who can run commands as the worker machine's user can call the
HQ CLI with any allow-listed identity, exactly as a worker on the HQ machine can today. The identity and role checks
stay where they are, in the HQ CLI and worker-guard; the HQ-side check that a forwarded call claims an identity
spawned on that worker machine is T-C.

## Placement

Placement runs only when `worker_machines` is a non-empty comma list. With that key empty, `gate()` is the
single-machine gate: it reads this machine's MemAvailable and load, it makes no ssh call, and nothing is recorded
against a worker machine.

`place_machine` runs after the owner global caps (live agents, claude processes, /tmp, review reserve, usage, lead
caps). Those caps are unchanged and are checked first. Placement can refuse a spawn they allow; it cannot allow a
spawn they refuse.

Roles other than coder, qa, reviewer and cleaner are placed on `machine_hq` only. A machine is eligible when it is
reachable, `mem_mb - per_agent_mb >= machine_mem_floor_mb` (cursor uses `machine_mem_per_cursor_mb`, claude uses
`machine_mem_per_claude_mb`), `load1 <= gate_max_load_ratio x` that machine's nproc, and the workers already on it
are below its share.

`machine_hq` keeps every live or starting non-worker (the HQ, lead and judge slots it holds). The rest of
`max_live_agents` is the worker budget, split by nproc across the reachable machines:

```
hq_in_use     = live and starting agents whose role is not coder, qa, reviewer or cleaner
worker_budget = max(0, max_live_agents - hq_in_use)
raw_i         = worker_budget * nproc_i / sum(nproc of reachable machines)
share_i       = floor(raw_i)
```

The leftover seats (`worker_budget - sum(share_i)`) go one each to the machines with the largest fractional part
(`raw_i - floor(raw_i)`). A tie goes to the earlier machine (`machine_hq`, then `worker_machines` order). The shares
sum to `worker_budget`.

Worked example: `max_live_agents` 28 and 4 HQ live, so `worker_budget` is 24. hqhost has nproc 8 and workerhost has
nproc 16 (sum 24). hqhost raw = 24 * 8 / 24 = 8, workerhost raw = 24 * 16 / 24 = 16. Both fractions are 0, so the
shares are 8 and 16. They sum to 24, and the workerhost share is the larger one.

A role in `machine_cpu_heavy_roles` (default coder,qa) takes the first worker machine that passes. Any other worker
role takes the eligible machine with the most RAM headroom (`mem_mb - per_agent_mb - machine_mem_floor_mb`); a tie
keeps the earlier machine. If none pass, the refusal names each machine and the check it failed.

This task still starts every agent on `machine_hq`. The chosen machine is written on the spawn (`place`, and on the
spawn event when the start proceeds). When `machine_hq` itself fails its checks, the spawn is refused with
`would place on <machine> (remote start not enabled yet)` instead of starting on a machine that is out of room.

Facts: `machine_hq` reads local MemAvailable, load and nproc. Each other machine is one
`ssh -o BatchMode=yes -o ConnectTimeout=<machine_ssh_timeout_s> -- <machine> ...` argv (the name is an argument, not
a shell string), cached for `machine_facts_ttl_s` seconds. A failed ssh marks that machine unreachable and does not
raise; placement then uses the machines that remain.

## Remote spawn

(T-C)

## res

(T-D)

## Cleanup

(T-E)
