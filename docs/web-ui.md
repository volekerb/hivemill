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

The tick alerts the director when the installed unit (`~/.config/systemd/user/factory-serve.service`) drifts from the one on main; the owner then runs `systemd/install.sh --with-serve`, `systemctl --user daemon-reload` and `systemctl --user restart factory-serve`.

The unit runs with `Nice=10`, `MemoryMax=200M`, `MemoryHigh=150M`, `CPUQuota=50%`, `NoNewPrivileges`, `ProtectSystem=strict` and `ProtectHome=read-only`, so it cannot slow the factory down or write outside `~/factory`. Inside `~/factory` the code and work directories (`bin`, `roles`, `systemd`, `web`, `worktrees`, `.git`, `tests`) are mounted read-only too, so only the database directory itself stays writable. That is needed only because sqlite must be able to create the `db/factory.db-shm` file when no other process has the database open; the server itself never writes (read-only connection, `query_only`). Logs: `journalctl --user -u factory-serve`. The server also reloads itself: about once a minute it compares `web/**/*.py`, `scout/**/*.py` and `bin/factory` with what it started with; after a change that still imports in a fresh interpreter it finishes the in-flight requests and re-executes itself (same PID, same address and port, no `systemctl` needed). A change that does not import keeps the old code serving and logs one `error: code changed but does not import` line to the journal; the next change is checked again.

## Configuration

| key | meaning | default |
|---|---|---|
| `serve_bind` | LAN address to listen on | empty = auto-detect |
| `serve_port` | TCP port | `8765` |
| `serve_hostnames` | extra Host names accepted, comma-separated | empty = the bind address only |

`factory config` lists all keys with their values. Setting one needs the director role: `factory config serve_port 8766` (the director or boss does this on the owner's behalf). Restart the server afterwards. Only private addresses are accepted for `serve_bind`.

## Pages

Shell. Every page shares one frame. From 1024 px wide a grouped sidebar lists Work (Overview, Goals, Tasks, Agents), Insights (Throughput, Quality, Tokens, Changelog) and System (Resources). Below 640 px it becomes a top bar, a tab bar with four tabs (Overview, Goals, Tasks, Agents) plus More, which opens every section and the theme control. The status header shows "Updated hh:mm", a health pill and a "Needs you" badge with the number of items waiting for the owner. The pill reads, worst first: "Memory low", "Usage cap reached", "Gate closed" (with the queued count), "Claude usage n%", "Weekly usage n%", "Usage unknown" or "All normal". The theme control offers Auto, Light and Dark (Auto follows the system setting). Pages refresh every 60 s and keep the scroll position and the open sections.

Orgs (G-028). The factory can run two orgs side by side, a Claude org and a Cursor org, each with its own boss, director, team leads and workers (`scout/orgs.py`). Every page shows which org a thing belongs to with an org chip, "Claude" (terracotta) or "Cursor" (teal), with light and dark colour tokens (`--org-claude-*`, `--org-cursor-*`). The org implies the runtime, so the old runtime chip only appears next to the org chip when an agent runs on a different runtime than its org (for example the Cursor org's evaluator, which runs on Claude). Where the org comes from:

- a goal: `goals.org` (a DB without that column: every goal is in the Claude org);
- a task or a team: its goal's org;
- an agent: `agents.org`, else its name prefix (`cursor-coder-t-0840`), else an explicit `spawn_args` runtime, else its goal's org, else Claude (legacy unprefixed names such as `boss` or `lead-g-005-a`).

The status header has a global org filter, a segmented control "All · Claude · Cursor", carried as `?org=claude` or `?org=cursor`. Any other value means All and is never echoed. The sidebar, tab bar and the in-page links (filters, sort, range, breakdown) keep `?org=`, so the filter follows you from page to page; switching org from the header drops the page's other filters. The box's capacity (agent cap, memory, load, the spawn gate, Claude usage) is shared by both orgs and is never filtered.

- `/` Overview. When both orgs have goals or agents, it starts with two org lanes side by side (stacked on a phone): per org the boss and director (running, queued or not running), active teams (an active team still counts while its lead is parked; the lane shows teams, leads awake, parked, waking, and workers), live workers, active goals and queued agents, each linking to the filtered page, and a health pill (worst first: "Boss not running", "Director not running", "n need you", "n queued", "Idle", "All normal"). With `?org=` only that org's lane shows, and the sections below show only that org's needs-you items, goals, agents, merges and bouncing tasks (System stays shared). Then, in this order: "Needs you" (blocked tasks, escalations and requests, merges waiting for an OK, blocked or proposed goals), "System" (gauges for agents against the cap, available memory, load and Claude usage, with a link to Resources), "Open goals", "Agents running", "Shipped" (recent merges, linking to the changelog) and "Quality" (headline tiles and what is bouncing now, linking to /quality).
- `/goals` and `/goal/<id>`: an Org facet (All, Claude, Cursor with goal counts; the same `?org=` as the header), open goals with status, priority, org chip and progress, sortable, with "Needs you" and a collapsed "Done" list; one goal shows its org chip next to the status, what is blocking it, its success criteria (met or open, linked to the tasks), its tasks grouped by status, its intent, and a side rail with teams, plan version and dates.
- `/tasks` and `/task/<id>`: all tasks as a table (status, ID, task, goal with the goal's org chip, assignee, PR with its CI state, rejections, updated), filterable by status and goal (with `?org=`, only that org's goals and tasks); one task shows its meta, verdicts, total time, rejections and attempts, the PR cycle and follow-ups.
- `/agents`: agents running (grouped by org, then team; the group header names the org when both orgs run agents), queued with the reason, teams with their org and lead state, then an "Ended" list; filter chips for status, org, role, goal and **runtime** (Claude Code vs Cursor Agent; shown only when some agent runs on another runtime than its org's, `?runtime=` still works). The summary line splits the running agents per org. Each agent shows its org chip; see [docs/runtimes.md](runtimes.md).
- `/resources`: the spawn gate (open or closed, derived, not live; its agents check reads "agents live 7/6 (Claude 3 · Cursor 4)"), capacity gauges (agents, memory, load, Claude usage), "Agents by org" (live, workers, leads, HQ, queued and runtime mix per org; only the filtered org with `?org=`), the checks behind the gate, spawns, queued, merges and escalations per day, recent events and the caps and config.
- `/throughput`: a range of 7, 14 or 30 days (default 14). Tiles for merges (a task counts once, on the day of its last merge), median and p90 time from spawn to merge, and agents running now (workers, leads, HQ); a note under the tiles, "n tasks open now (in progress or in review), m agents working on a task", where tasks open now counts tasks in progress or in review and agents working on a task counts live agents with a task; a column chart of merges per day; where the time goes (coding, review / QA wait, judge, rework); per goal; and the 10 slowest tasks. With `?org=claude` or `?org=cursor` every number on the page counts only that org. An unknown `?org=` means All.
- `/quality`: headline tiles with the change against the previous 14 days: rejections per merged PR, first-try merge rate, CI-blocked merges and median attempts. "Bouncing now" lists the tasks being rejected. Then, in this order: per team (merged, rejections, rejections per merged PR; team-less tasks are added before the ratios are computed), rejections by source (reviewer, QA, judge) with gate refusals (judge-blocked and merge-failed events, counted apart), rejections per week, and "More metrics": merged PRs, merged first try, merged after at least one rejection, rejections (reviewer, QA and judge fail/reject verdicts) with rejections per merged PR, gate refusals, rejections by cause (totals from recorded rejection_incidents when any exist, else “Not measured yet”), retries per merged task, CI pass rate and the latest team scores. With `?org=` the tiles, the bouncing list, the per-team table, rejections by source, the weekly chart and the 14-day deltas count only that org. An unknown `?org=` means All.
- `/changelog`: merged work, grouped by date or by goal, filterable by goal. With `?org=` the feed and the goal chips show only that org. An unknown `?org=` means All.
- `/tokens`: a range (today, 7 days, 30 days, all time) with Claude usage, a "Cursor runtime" table (see below), tiles for total tokens, cost estimate, cache read share and requests, a column chart by day, a breakdown by goal, task, agent, model or org, and the attribution quality of the sessions. A session's org is its attributed agent's org, else its goal's, else Claude. With `?org=` the token numbers are that org's only; `?org=cursor` hides the Claude plan gauges and shows the Claude Code usage of Cursor-org agents that ran on Claude (such as evaluators).
- `/healthz`: plain "ok" for health checks. `/api/<page>.json` returns a page's data as JSON.

## Cursor usage: what is and is not measured

Cursor Agent CLI leaves no token or usage counts on this machine. Checked (read-only, no message content read) on 2026-11-01: `~/.cursor/chats/<hash>/<id>/store.db` holds a `meta` row (agent id, name, mode, created time, last used model) and encrypted content blobs; `~/.cursor/projects/<dir>/agent-transcripts/*.jsonl` lines carry only role, message and tool calls; `~/.cursor/ai-tracking/ai-code-tracking.db` tracks AI-written code hashes, scored commits and conversation summaries per model; `agent-cli-state.json` and `cli-config.json` are settings. None has input, output or cache token counts. Headless runs (`scout/cursor_harness.py`, `--print`) use `--output-format text`, so the factory captures no usage from the CLI's output either.

So `/tokens` never shows tokens or $ for the Cursor runtime. Its "Cursor runtime" table lists, per org and model, the Cursor-runtime agents of the range from the agents table: sessions (agent rows), how many are live now, and agent-hours (started to ended, or to now while live, clipped to the range), with the note "token usage not reported by Cursor CLI". The "By org" breakdown puts the Claude-log tokens and cost of each org next to its Cursor sessions and agent-hours. `web/prices.py` has no prices for Grok or Composer models; they would show n/a.

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

## Visual QA

`python3 ci/visual_qa.py` captures every page (`/`, `/goals`, a goal, `/tasks`, a task, `/agents`, `/resources`, `/throughput`, `/quality`, `/changelog`, `/tokens`) in light and dark at 1280x860 and 390x844 (44 PNGs) and checks them. It is run by hand or by QA, not by unittest.

- **Overflow**: the page must not scroll sideways, and no element may reach past the viewport unless a horizontally scrolling ancestor (`overflow-x: auto/scroll`) contains it. The report names the selector and its right edge.
- **Blank**: on the overview at 1280 px, at most 35% of the content column (right of the sidebar) may be 8x8 px cells of one uniform colour equal to the page background. The share is printed with one decimal.
- **Contrast**: every text pair of the design tokens in `web/static/app.css` must reach 4.5 and every non-text pair 3.0, in both themes (same pairs and formula as `tests/test_web_design.py`).
- **Speed**: the median of 5 server-side renders (HTTP GET to the local server) per route must stay under 200 ms.

The thresholds are constants in the script and no option changes them. Options: `--routes /a,/b` (subset), `--out DIR` (keep the PNGs; default is a temp dir that is removed), `--browser PATH` (an installed chrome-headless-shell instead of the pinned download), `--json FILE` (machine-readable result). The last output line is `visual-qa: PASS` (exit 0) or `visual-qa: FAIL (<n> problems)` (exit 1); exit 2 means setup failed (no browser, seed failed, live home refused).

It never touches the live home: the demo DB is seeded into a temp `FACTORY_HOME`/`HOME`, served on 127.0.0.1 on a free port, and the server, browser and temp dir are removed when the run ends, also on Ctrl-C. The theme is chosen with the browser's emulated `prefers-color-scheme`; the server needs no QA route.
