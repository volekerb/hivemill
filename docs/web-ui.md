# Factory web UI

A small web page that shows what the factory is doing: goals, tasks, agents, resources, throughput, quality and token cost. It is started with `factory serve`.

## What it is

- **Read-only.** It only shows information. There are no buttons that act: you cannot spawn, stop, merge or change settings from it. It answers only GET and HEAD requests.
- **LAN-only.** It listens on one private LAN address. It refuses to start on `0.0.0.0`, on a wildcard, or on a public address.
- **No login.** There is no password and no TLS (see "Security" below).
- It never shows prompt text, message bodies, plan bodies or task specs. It shows titles, statuses, counts and metadata only.

## The URL

Open **http://<LAN IP>:8765/** in a browser on the home network, where `<LAN IP>` is the private address of the machine running the server (for example `192.0.2.10`, so **http://192.0.2.10:8765/**). The server prints the exact URL at start.

To reach it by a name instead, add that name to `serve_hostnames` (comma-separated, no port): `factory config serve_hostnames <your-host>` (for example `factory.example`), restart the server, and open **http://<your-host>:8765/**. The name has to resolve to the server's address in your own DNS or hosts file; the factory does not set that up.

How the address is chosen: unless you set `serve_bind`, the server uses the address your machine would use to reach the default route (it looks it up locally and sends no packet). The server answers only requests whose `Host` header is that address or one of the names in `serve_hostnames` (default empty: the bind address only), each with the port.

## Start it by hand

```
factory serve                  # uses the config below
factory serve --bind 127.0.0.1 --port 9000   # one-off override, e.g. for a local check
```

It prints the URL it is serving, runs until you press Ctrl-C and runs at low priority (`nice 10`).

## Start it at boot (optional, run by the owner)

The web UI is not installed by default. To enable it:

```
systemd/install.sh --with-serve     # links and enables factory-serve.service for the next boot
systemctl --user start factory-serve   # start it now (the installer never starts anything)
```

Run `install.sh` from the main checkout, not from a worktree. Preview what it would do with `systemd/install.sh --dry-run --with-serve`. Without `--with-serve` the installer behaves exactly as before and does not touch this unit. Stop or disable it with `systemctl --user stop factory-serve` and `systemctl --user disable factory-serve`.

The unit runs with `Nice=10`, `MemoryMax=200M`, `MemoryHigh=150M`, `CPUQuota=50%`, `NoNewPrivileges`, `ProtectSystem=strict` and `ProtectHome=read-only`, so it cannot slow the factory down or write outside `~/factory`. Inside `~/factory` the code and work directories (`bin`, `roles`, `systemd`, `web`, `worktrees`, `.git`, `tests`) are mounted read-only too, so only the database directory itself stays writable. That is needed only because sqlite must be able to create the `db/factory.db-shm` file when no other process has the database open; the server itself never writes (read-only connection, `query_only`). Logs: `journalctl --user -u factory-serve`. The server also reloads itself: about once a minute it compares `web/**/*.py`, `scout/**/*.py` and `bin/factory` with what it started with; after a change that still imports in a fresh interpreter it finishes the in-flight requests and re-executes itself (same PID, same address and port, no `systemctl` needed). A change that does not import keeps the old code serving and logs one `error: code changed but does not import` line to the journal; the next change is checked again.

## Configuration

| key | meaning | default |
|---|---|---|
| `serve_bind` | LAN address to listen on | empty = auto-detect |
| `serve_port` | TCP port | `8765` |
| `serve_hostnames` | extra Host names accepted, comma-separated | empty = the bind address only |

`factory config` lists all keys with their values. Setting one needs the director role: `factory config serve_port 8766` (the director or boss does this on the owner's behalf). Restart the server afterwards. Only private addresses are accepted for `serve_bind`.

## Pages

- `/` Home: overview of active goals, running agents and recent activity.
- `/goals` and `/goal/<id>`: all goals with status, and one goal's tasks and progress.
- `/tasks` and `/task/<id>`: all tasks, and one task's status, PR, verdicts and history.
- `/agents`: teams and agents, who is alive, and what each works on.
- `/resources`: memory, swap, load, agent caps, queue, and whether the spawn gate is likely open or closed (derived, not live).
- `/throughput`: tasks merged over time and how long they take. "Open tasks now" counts tasks in progress or in review; "Live workers now" counts live agents with a task. Bars scaled to the largest row show their count (aria-label "n of max m"), not a percentage.
- `/quality`: merged PRs, merged first try, merged after at least one rejection, rejections (reviewer, QA and judge fail/reject verdicts) with rejections per merged PR, gate refusals (judge-blocked and merge-failed events, counted apart), a per-source table, rejections by cause (n/a until G-013), CI pass rate, retries and the per-team table (merged, rejections, rejections per merged PR, gate refusals; team-less tasks are added before the ratios are computed).
- `/changelog`: recently merged work.
- `/tokens`: estimated token usage and cost by model and day.
- `/healthz`: plain "ok" for health checks. `/api/<page>.json` returns a page's data as JSON.

## Security

No authentication. Anyone who can reach your-box's LAN address on the port can read every page. The boundary is: bind to a private LAN address only, refuse 0.0.0.0/public addresses, a Host-header allowlist, GET-only, no secrets or message text rendered. Do not port-forward it.

## What "token cost" means

Token cost on `/tokens` is an **estimate**. It is computed from the token counts in the Claude Code session logs, multiplied by **list prices** (the public per-token prices, with no discounts, plans or credits). The date the prices were last updated is shown on the page ("prices as of"). Your actual bill or plan usage can differ. Use it to compare days, models and teams, not as an invoice. The token totals come from the logged responses (deduplicated by message id and request id) and sit about 1.6% below Claude Code's own `cost-state` line. That line most likely also counts auxiliary or background calls that Claude Code bills but does not write to the session log, so the gap is expected and the page does not try to close it. For session 07576c04, `cost-state` reports 592 input and 3,673,932 cache-read tokens against 74 and 3,556,200 in the log (cost $0.9805 against $0.9648), and session 42039c02 shows the same pattern (540 input and 788,551 cache-read tokens against 22 and 715,553 in the log). Both sessions have an identical gap of +518 input and +7 output tokens, which points to a fixed per-session overhead rather than a random loss.

## Troubleshooting

- **"cannot listen on ...: Address already in use"**: another process uses the port. Either a second `factory serve` is running (`systemctl --user status factory-serve`), or something else holds 8765. Stop the other one, or pick another port: `factory config serve_port 8766` and restart.
- **"421 misdirected request"**: you opened the page with a name or address the server does not accept. Use the URL it prints at start. To allow another name, add it: `factory config serve_hostnames factory.example,other.example` and restart.
- **"refusing to bind ..."**: `serve_bind` is a wildcard or public address. Use a private LAN address or clear the key.
- **Page does not load from another machine**: check that you are on the LAN, that the IP is right (`ip -4 addr` on your-box), and that no firewall blocks the port.
- **Cannot open the database**: the factory home has no `db/factory.db` yet. Check `FACTORY_HOME`.

## Token index: sandboxed worker transcripts (T-0163)

`web.tokens.TokenIndex` scans `~/.claude/projects` and, when no explicit `root` is given, every
`<scout.workerbox.WORKERS_ROOT>/<agent>/claude/projects` that exists (sandboxed workers write there). Sessions are
attributed exactly as before. Files are keyed by full path, so the same basename in two roots is two entries.
Worker files are confined: each file's realpath must be a regular file inside that agent's own `claude/projects`;
symlinks pointing elsewhere are skipped. `TokenIndex(root, extra_roots=[...], workers_root=...)` adds roots without
changing the meaning of `root`.
