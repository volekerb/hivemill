# Multi-factory UI: UX and tech design

A design for review. It changes no code. It covers three surfaces: each factory's own obs UI, a central view over all factories, and the overview in the Herdr app. The visual direction stays [obs-ui.md](obs-ui.md).

Words: a **factory** is one hivemill runner with its own database, planner and obs server. A factory can place sessions on several **machines**. Example labels in this document ("Workshop", "Pilot") are invented.

## What was looked at

Two running factories (one busy, one a two-task pilot), their obs pages and `/api/obs/*`, the Herdr mod and its settings, `obs-ui.md`, and the parked work (PRs #1054, #1055, #1045 and the spec of T-2092).

Findings that shape the design:

| # | Finding | Consequence |
|---|---|---|
| 1 | The API has no identity: no label, version or API version. `/api/obs` is a 404. | A client cannot name a factory or detect version skew. Add both. |
| 2 | Pages and API show the OS hostname as the machine name when `machine_name` is empty, and `/api/obs/now` returns the event log's absolute path, home directory included. | Names leak into every screenshot and the path leaks to the network. Fix before more clients read the API. |
| 3 | "Needs you" cannot be built from the API today. Open questions are a count per project with no text. Held rows carry no reason and no PR link (both exist, but only in the task trace). Failed runs have no end time, so "failed in the last 24 h" cannot be filtered. | A purpose-built summary endpoint, not more client-side joins. |
| 4 | Planner state in the API is the last event of the planner session (on the busy factory: "session.started", a day old). The real state (on or off, last wake, why it waits) is only in `hivemill planner status`. | Put planner status in the API. |
| 5 | Budgets (`weekly_budget_usd`, `project_daily_budget_usd`, `task_budget_usd`) are config only, not in the API. Claude usage percent is read from a file on the computer that runs the Herdr app. | Add a budget block. Where usage percent comes from is an open decision. |
| 6 | "Stuck" is one rule: more than 1 h in any stage but held. On the busy factory it lists 7 runs, all in `merge`. | A per-stage limit, or the list is noise. |
| 7 | `/api/obs/flow` returns every task of the window (209 on the busy factory). Each request rebuilds the model from the whole event log (about 0.3 s there). | Too heavy to poll for a card. The summary must be small and cached. |
| 8 | Task and goal ids are per factory (`T-0001` exists on the pilot; goal titles live in each factory's database). | A hub must never merge event logs of two factories. `obs pull` stays what it is: the session machines of one factory. |
| 9 | The page CSP is `default-src 'none'` and there is no script. | A browser page cannot fetch peers. The central view has to aggregate on the server. |
| 10 | The server refuses a `Host` that is a DNS name (rebinding guard) and answers IP literals when bound beyond loopback. | Links between factories use the URLs from config as written. A reverse proxy with a host name needs a later change. |
| 11 | The installer writes daemon, digest and health jobs, not obs. One factory runs obs from a hand-written systemd unit. | Add obs to the installed jobs. |
| 12 | The mod still reads the stopped old API and draws its last answer as if live: roles that no longer exist, and counts hours old. | The rewrite's first rule: no number without its age, and nothing drawn from a dead source. |
| 13 | The Herdr tab-naming helper holds machine labels in its source and fetches task titles over ssh from one host. Task rows in the API have no title. | Titles and labels come from the API. |

Small bugs seen on the way, to fix where the pages are touched: the pilot's Flow banner reads "dispatched holds 0 of 0 stage hours (85%)"; the stuck limit prints as `1h0m0s`; a "(no project)" row holds all planner spend.

## UX design

### Levels and drill-down

One path, top to bottom. Each level answers one question and links to the next. Nothing is shown at two levels with different numbers.

| Level | Where | Question | Shows | Goes to |
|---|---|---|---|---|
| 0 | Herdr status bar | Does anything need me? | one segment per factory, the "needs you" total, the most urgent item | level 1 |
| 1 | Herdr dashboard tab | Which factory, and what? | Needs you across all factories, then one card per factory | planner tab, worker tab, PR, or levels 2 to 5 in the browser |
| 2 | Hub: All factories | The same, in a browser | Needs you across all factories, then one row per factory | level 3 on that factory |
| 3 | Factory: Now | What needs me here, and what runs? | Needs you, Running, Planner, Projects, Machines (as `obs-ui.md`) | project, goal, task |
| 4 | Factory: goal | How far is this goal? | its tasks with outcome, stage, cost | task |
| 5 | Factory: task | What happened, what did it cost? | the trace timeline: dispatch, prompts, sessions, gate, CI, judge, hold, merge | session, PR |
| 6 | Factory: session | What did this session do? | its events and usage | (end) |

Levels 3 to 6 exist today and always belong to one factory, on that factory's own server. Levels 0 to 2 are new and hold only summaries. The hub never re-renders a factory's pages; it links to them.

Every page of a factory names the factory: its label sits beside the brand in the sidebar ("hivemill · Workshop") and in the page title. When the factory knows a hub (`obs_hub_url`), the sidebar has an "All factories" link back to level 2.

### "Needs you" is the top object

On every level it comes first, and it is one list, not four tables. An item is something only the owner can move. Each item is one row: kind, factory, task, a one-line reason, the age, and one link that is the place to act.

| Order | Kind | Rule | Reason shown | Link |
|---|---|---|---|---|
| 1 | question | an open owner question (`ask`) | the question's text | the question on the factory's Now page |
| 2 | held | a run held for the owner | the hold reason ("tier T3 is above T2; the owner merges") | the PR |
| 3 | failed | a run that failed in the last 24 h and was not re-filed | the failure reason | the task trace |
| 4 | stuck | a run over its stage's limit | stage and time over the limit | the PR, else the task trace |
| 5 | system | the factory itself: unreachable, planner off, budget reached, a machine down, idle with startable tasks | what, and since when | the factory's Now or Machines page |

Rules:

- Questions and held runs are "act": they wait for the owner and for nothing else. Failed, stuck and system are "look". The list is sorted by this order, then oldest first.
- The count in the bar, on a card and on a page is the number of "act" rows. "Look" rows are listed under them and are not counted, so the number never grows from noise. One definition, computed once, on the factory.
- A failed run that was re-filed is not listed: the re-file is the action. Without the re-file filter the list would repeat what the planner already handled.
- Stuck uses a limit per stage (`obs_stuck_<stage>_min`), with defaults that fit the stage: minutes for `gate` and `judge`, longer for `ci` and `merge`. A held run is never stuck; it is held.
- An empty list is one line: "Nothing needs you." It is never a table of dashes.

### Per-factory health

One line of facts, the same fields in the same order on the Herdr card, the hub row and the top of the factory's Now page:

| Field | Meaning | Source today |
|---|---|---|
| working | runs in `dispatched`, `gate` or `judge` | `now.running[].stage` |
| CI | runs waiting for CI | stage `ci` |
| merge | runs waiting to merge | stage `merge` |
| held | runs held for the owner | `now.held` |
| planner | on or off, last wake, and why it waits | missing (finding 4) |
| merged | today, and a 7-day row of bars | `flow.merged_per_day` |
| spend | today and 7 days; cost per merged PR | `now.projects`, `spend` |
| budget | spend against each cap that is set; Claude usage percent when known | missing (finding 5) |

The four run counts always add up to the open runs. "Waiting" as one number hides the difference between CI (the factory's problem) and merge or held (often the owner's), so the three stay apart.

### Machines

A factory lists its machines: label, coordinator or session machine, up or down, running sessions and free slots, available memory, newest event with its age. This is today's Machines page plus the age and the word "stale" from `obs-ui.md`.

The hub does not add a machines view. Its row per factory says "3 machines, 1 down" and links to that factory's page. The Herdr mod keeps its own probe of the machines it can reach through `hc` (memory, load, disk, swap), and tags each with the factory it belongs to.

### Naming

Nothing in the repo names a real machine, person or project. Names come from local config, in this order:

- **Factory label**: config key `obs_label` on the factory ("Workshop"). The factory serves it in its API, so the hub and the mod need only a URL. Without it, the label is "Factory" plus the port.
- **Machine labels**: config key `obs_machine_labels` on the factory, a map from machine name to display label. Display only: `machine_name` and task keys do not change, so history and links stay valid. The API returns both `name` and `label`; pages show the label.
- **A local override**: the mod's settings may give a factory another label on this computer.
- **Demo mode** stays for screenshots: generic labels ("Factory 1", "Machine 2") and no task titles.

The hand-written label map in the tab-naming helper goes; it reads the labels from the API.

### Screens

Herdr dashboard tab (level 1). Needs you first and across factories; then a card per factory with the health line; then machines and budget.

```
Needs you  3                                                    updated 12 s ago
  held      Workshop  T-2043  tier T3 is above T2; the owner merges    32m  PR ↗
  held      Workshop  T-2016  tier T3 is above T2; the owner merges    24m  PR ↗
  failed    Workshop  T-1902  stalled: idle without a passing gate      3h  trace ↗

Workshop                                              planner on · woke 3m ago
  11 working    4 CI    10 merge    2 held                       2 need you
  merged 64 today  ▁▁▁▁▂▂█      $367 today · $511 7d · $5.60 per merged PR
  budget: no cap set · Claude 7-day 52%               planner tab · open ↗

Pilot                                                 planner on · woke 9m ago
  2 working     0 CI    0 merge     0 held                Nothing needs you.
  merged 0 today   ▁▁▁▁▁▁▁      $2.43 today
  budget: no cap set                                  planner tab · open ↗

Archive                                    unreachable for 6 m · last seen 14:02
  (last known: 0 working, 0 held)                                     open ↗
```

Status bar (level 0): `Workshop 11 working · 14 waiting · 2 need you │ Pilot 2 working │ wk 52% · $369 today │ held: T-2043 32m`. Here alone, CI and merge fold into "waiting" to fit the bar.

Clicks in Herdr:

- A card's title or "planner tab": select that factory's planner space in the sidebar.
- A task id: its worker tab when one is open, else the task trace in the browser.
- "PR ↗", "trace ↗", "open ↗": the browser.

Hub page (level 2), in the obs shell and following `obs-ui.md`: the same Needs you list as a table, then one table with a row per factory and the health fields as columns. The factory's label links to its Now page; each count links to the matching section there. A stale or unreachable factory keeps its row, with its last numbers in the muted colour and the words "stale, 4 m" or "unreachable since 14:02". No cards, no tiles, no script.

### States

- **Fresh**: no mark. The list header carries one "updated N s ago".
- **Stale** (older than 90 s): the numbers stay, muted, with their age in words.
- **Unreachable** (no answer for 5 min): a system item in Needs you. The card keeps the last known counts, marked as last known.
- **Never reached**: the card says so and shows the configured URL's label only. No zeros.
- **Version skew**: a factory with an older API than the client needs shows "update this factory (API 1, needs 2)" in place of the fields it cannot fill.
- **No factory configured**: one line with the settings file to edit.

## Tech design

The owner session's recommendation is the base. Where this design differs, it says so under "Changes to the recommendation".

### Factory API, version 1

Routes under `/api/obs/v1/`. Today's `/api/obs/*` routes stay as they are and are frozen; new clients use `v1`. Every `v1` answer carries `"api": 1` and the `runner` block. A field is only ever added within a version; removing or renaming one makes version 2.

`GET /api/obs/v1/summary` is the one call a card needs:

```json
{
  "api": 1,
  "now": "2026-01-01T12:00:00Z",
  "runner": { "id": "r-7f3a…", "label": "Workshop", "version": "<build>", "projects": ["app", "site"], "hub": "" },
  "counts": { "working": 11, "ci": 4, "merge": 10, "held": 2 },
  "needs_you": [
    { "kind": "held", "task": "T-2043", "key": "…", "title": "…", "reason": "tier T3 is above T2; the owner merges",
      "age_sec": 1924, "pr": "https://…", "href": "/task?k=…" }
  ],
  "planner": { "on": true, "last_wake": "…", "pending": "debounce: next wake after …" },
  "merged": { "today": 64, "per_day": [0, 0, 0, 0, 8, 6, 64] },
  "spend": { "today_usd": 367.1, "week_usd": 511.1, "per_merged_pr_usd": 5.6, "cost_pending": 0 },
  "budget": { "weekly_cap_usd": null, "weekly_spent_usd": 511.1, "task_cap_usd": 8, "usage_7d_pct": null, "usage_5h_pct": null },
  "machines": [ { "name": "…", "label": "Bench 1", "coordinator": true, "down": false, "running": 16, "free_slots": 0,
                  "mem_available_mb": 5218, "last_seen": "…" } ]
}
```

- `runner.id` is derived from the machine's name and the factory's home folder (a short hash; neither is shown). Nothing is stored. It survives a label change and lets a hub see one factory listed twice; it changes when the factory moves to another machine or folder.
- `needs_you` is the list of the UX section, already filtered and sorted. `href` is a path on this factory; a client joins it to the factory's URL.
- The answer is cached in the server for 10 s, so many clients cost one rebuild.
- `GET /api/obs/v1/now`, `flow`, `spend`, `machines`, `task` are today's payloads plus `api` and `runner`, with two changes: no file paths (finding 2), and task rows gain `title` and `pr`.

New data this needs on the factory, all read-only: the open questions' text, the hold reason and PR of a held run (from its `run.held` event), a run's end time and its re-file link, planner status, and the budget caps.

### Hub

`hivemill obs --hub` serves levels 2 and its API. It holds no database and no event log.

- Peers: config `obs_peers`, a comma list of factory URLs. On a computer without a factory, `--peers URL,URL` or the same list in a small local file.
- It asks every peer for `/api/obs/v1/summary` in parallel: 3 s timeout each, a 15 s cache, at most one request in flight per peer. A failed fetch keeps the last good answer and marks it.
- `GET /api/obs/v1/factories`: `{ "api": 1, "factories": [ { "url", "fetched_at", "age_sec", "stale", "error", "summary" } ] }`. `summary` is the peer's answer, unchanged, or the last good one.
- `GET /`: the hub page. Links go to each factory's own pages, built from the peer URL in config.
- The same guards as obs today: GET and HEAD only, the CSP, the Host check, no script.
- Peer URLs are fixed by local config and never taken from a request, so the hub cannot be made to fetch an arbitrary address.

Any factory can also be the hub: when `obs_peers` is set on a factory, its obs serves the hub page at `/all` and lists itself first. One process, no extra service. The separate `--hub` mode is for a computer that runs no factory.

### Herdr mod

A rewrite of the data layer and the views; the lease, the polling loop, the theme helpers and the node test setup stay.

- **Sources**: `factories: [{ "url": …, "space": … }]` in the mod's settings, or `hub: URL`. Both give the same shape, a list of `{ url, age, stale, error, summary }`, so the views have one code path. With a hub the mod makes one request; without, one per factory. Requests stay `curl` with a hard timeout, optionally run on an `hc` machine as today.
- **Polling**: summary every 30 s. Usage file and machine probes every minute, as today.
- **Removed**: boss, director, leads, lanes, spawn gate, orgs and the Cursor block, "live / cap", the old API paths, and `teams` (read once as `factories` when its URL answers `v1`, else ignored with a settings alert).
- **Kept**: budget meters, machine probes, demo mode, notifications for new items. Open question below: screen-scan wait patterns and the per-project PR lists.
- **Alerts** are the factories' `needs_you` items plus what only this computer can see: a factory unreachable, a settings file that does not parse, local disk or swap. Every alert carries the time of the data it came from, and an alert whose source went stale is dropped after the unreachable alert replaces it.
- **Clicks**: `space.select` for the planner space named in settings; `agent.focus` for a worker tab; `open` for URLs.
- **Tests**: fixtures of `v1/summary` for a busy factory, an idle one, a stale one and an older API; node tests for the list merge, the states and the bar text.

### Security

- Read-only everywhere: GET and HEAD, no action that writes, on factories and the hub.
- Default bind stays loopback. LAN access is the explicit `obs_listen`, with today's warning.
- No secrets in any page or payload: no file paths, no environment, no tokens, no prompt text. The trace shows prompt file names and step summaries, as today; showing prompt text is a separate decision.
- Remote access later: `obs_token`. The API takes `Authorization: Bearer`; the hub sends each peer's token from its local config; pages take the token once and set a cookie. A reverse proxy also needs an allowed host name (finding 10). Not built now; nothing in version 1 blocks it.
- Hub links assume the browser can reach each factory directly. That holds on one LAN. Proxying a factory's pages through the hub is the remote-access design and is left out.

### Default install

- The installer adds an obs job beside the daemon: a systemd user unit on Linux and a launchd job on macOS, running `hivemill obs`, restarted on failure. Uninstall removes it.
- It binds loopback unless `obs_listen` is set. `hivemill init` prints the URL.
- The hub is never installed by default. Setting `obs_peers` turns it on.
- The hand-written unit on the existing factory is replaced by the installed one.

### Changes to the recommendation

1. **A summary endpoint instead of fan-out to four.** The recommendation has the hub fetch every `/api/obs/*` of every peer and merge. That puts the definition of "working", "stuck" and "needs you" in two more places (hub and mod), and pulls the 209-task flow payload every 15 s. Here each factory computes its own summary once, and the hub and the mod only lay summaries side by side.
2. **Labels live on the factory**, served by the API, with an optional local override. One place to set a name, and three clients that agree.
3. **Any factory can be the hub** (`obs_peers` plus `/all`), and `--hub` remains for a computer with no factory. The owner's usual setup then needs no third service.
4. **The mod reads one shape from either source**, so "hub or peers directly" is a settings choice and not two code paths.
5. **The hub is not built on `obs pull`.** Merged event logs collide on task and goal ids and cannot carry questions, planner state or titles (finding 8).

### Build order

Small PRs, each with tests and light and dark screenshots where it draws.

| # | PR | Content |
|---|---|---|
| 1 | Herdr mod | the rewrite on today's `/api/obs/*`, one card per factory, settings and installer flags, doc rewrite of `herdr-app-dashboard.md` |
| 2 | API v1 and hub | `runner` identity, `obs_label`, `obs_machine_labels`, `/api/obs/v1/summary` with `needs_you`, planner and budget; paths removed from payloads; `obs_peers`, `/all`, `--hub`, `/api/obs/v1/factories`, the hub page |
| 3 | Factory pages | label in the shell, the unified Needs you list on Now, the health line, per-stage stuck limits |
| 4 | Mod on v1 | the mod reads `v1/summary` (or the hub) and drops its own counting |
| 5 | Install | the obs job in the installer; the tab-naming helper on API labels and titles |

The owner chose the mod first, on today's API, because it is looked at daily. Until PR 2 the mod counts the stages itself, reads a held run's reason and PR from its task trace, shows open questions as a count, lists every failure of the day, and has no dollar caps. PR 4 removes that code.

Parked work: #1055 (Now) and #1054 (Flow) apply `obs-ui.md` and pass CI; PR 4 would build on them, so they are candidates to merge first, after a read of each diff. #1045 (trace timeline) is level 5 of this design and gets the same read. T-2092's spec is replaced by PR 2; its remove and keep lists are taken over above.

## Decisions

Taken by the owner with "go ahead with the whole plan": the recommendations below stand, except the order.

1. **Order**: the mod first on today's API, then API v1 with the hub, then the pages.
2. **Failed runs**: once the API carries the re-file link, list only failures that were not re-filed.
3. **Claude usage percent**: each factory reports it (`usage_file` on the factory). Until then the mod reads a local file.
4. **Screen scans in the mod**: wait patterns stay, per project space.
5. **Per-project PR lists via `gh`** in the mod: dropped. Merges and holds come from the factory.
6. **Stuck limits**, defaults per stage: `gate` 15 min, `judge` 15 min, `dispatched` 2 h, `ci` 45 min, `merge` 2 h.
