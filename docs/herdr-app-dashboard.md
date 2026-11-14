# Hivemill dashboard in the Herdr app

If you use the [Herdr app](https://herdr.dev) (the desktop client of herdr-core), Hivemill can live inside it:

- **A status bar** across the bottom of the window. It shows each project's open PRs and last merge, the factory's live agents, what needs you, Claude weekly usage, cost per merged PR and today's spend, and on the right the most urgent alert. Clicking it opens the dashboard.
- **A dashboard tab** with these cards:
  - **Alerts:** only what needs a person.
  - **Budget & cost:** the Claude 7-day and 5-hour usage against the factory's caps, and the Cursor plan state. It also shows spend per day and per org, cost per merged PR (median, p90 and average, with time to merge), and the most expensive tasks.
  - **Resources:** the factory machine's memory, swap and load, the spawn gate, and merges per day.
  - **One card per project:** its open PRs, oldest first, and its recent merges.
  - **The factory:** lanes, what needs you, active goals, the factory's agents, and shipped work.

The app's own agents list already shows every agent on this machine, so the dashboard does not repeat it. An agent appears on the dashboard only in an alert, for example when its context window is almost full.

New alerts also appear as notifications. Clicking one opens the dashboard.

This is a Herdr app mod: JavaScript the app runs natively. It is not a web view. The factory's web UI ([web-ui.md](web-ui.md)) stays as it is, and the dashboard reads the same JSON API (`/api/home.json`, `resources.json`, `throughput.json`, `tokens.json`).

## Install

Run this on the machine where you use the Herdr app. That can be your laptop rather than the factory's box. Open the app once first.

```sh
python3 plugins/hivemill/scripts/install.py --herdr-app --dry-run   # show the plan
python3 plugins/hivemill/scripts/install.py --herdr-app --yes
```

It changes three things and nothing else:

| Path | What |
|---|---|
| `~/.config/herdr-app/mods/hivemill/` | the mod (`main.js`, `data.js`, `mod.json`), copied from `plugins/hivemill/herdr-app/mods/hivemill/` |
| `~/.config/herdr-app/layouts/hivemill.json` | a copy of the app's `main` layout, with its status bar replaced by the Hivemill bar |
| `~/.config/hivemill/dashboard.json` | the settings below. A re-run changes only the fields you pass and keeps your projects. |

When the app is running, the installer then switches it to the Hivemill layout. `ctrl-shift-l` switches layouts, so you can go back to Herdr's own at any time. To uninstall, delete the first two paths.

A re-run updates the mod and regenerates `hivemill.json` from the app's current `main` layout. Make your own layout changes in a separate copy.

Exit codes:

- `0`: done, with a last line of `RESULT PASS herdr-app ...`.
- `2`: a usage error.
- `3`: the Herdr app is not set up on this machine.
- `1`: a step failed, with a last line of `RESULT FAIL step=herdr-app-<step>`.

## Where the factory is

The factory's web UI listens on `127.0.0.1:8765` of the factory's box by default ([SETUP.md](SETUP.md)). Pick one of these:

| Your setup | Flags |
|---|---|
| The app runs on the factory's box | none: the default `http://127.0.0.1:8765` |
| The factory's box is an `hc` machine in the app (`hc machine ls`) | `--factory-machine NAME`. The dashboard asks the factory from that machine, so no port is opened. |
| The factory's UI is reachable on your network | `--factory-url http://HOST:8765` |

`--factory-web-url URL` is the address your browser uses for the dashboard's "Open" links. It defaults to the factory URL when no machine is set.

## Claude, Cursor or both

A factory has two orgs, `claude` and `cursor` ([`scout/orgs.py`](../scout/orgs.py)). Most installs run one of them. With `"orgs": "auto"` (the default), the dashboard shows an org only while it has a live boss or director, goals or agents:

- Claude budget meters and Claude usage alerts appear only when `claude` is shown.
- The Cursor plan row and the "Cursor tasks unpriced" note appear only when `cursor` is shown.
- Lanes, needs, goals and agents are filtered to the orgs shown. Lanes appear only when more than one org is shown.

To fix the choice, pass `--orgs claude`, `--orgs cursor` or `--orgs claude,cursor`. `--orgs auto` goes back to automatic.

## Settings: `~/.config/hivemill/dashboard.json`

The dashboard rereads this file every minute. A file it cannot parse shows up as an alert. The full example is in [`plugins/hivemill/herdr-app/dashboard.example.json`](../plugins/hivemill/herdr-app/dashboard.example.json).

| Key | Default | Meaning |
|---|---|---|
| `factory.url` | `http://127.0.0.1:8765` | the factory web UI, as seen from `factory.machine`, or from here when no machine is set |
| `factory.machine` | `null` | an `hc machine ls` name. When set, the requests run there (`curl` on that machine). |
| `factory.web_url` | `null` | the address the Open links use |
| `orgs` | `"auto"` | `"auto"` or a list: `["claude"]`, `["cursor"]`, `["claude", "cursor"]` |
| `projects` | `[]` | projects whose agents run in this app; see below |
| `usage_file` | `null` | optional JSON with `five_hour_pct`, `seven_day_pct` and `updated_at`, for this machine's own Claude usage. Without it, the factory's figures are used. |
| `alerts.context_below_pct` | `10` | alert when an agent's context left is under this |
| `alerts.stall_minutes` | `60` | alert when a project has ready PRs and has not merged for this long |
| `alerts.usage_warn_pct` / `usage_danger_pct` | `80` / `90` | Claude weekly usage alerts |
| `alerts.factory_down_minutes` | `5` | alert when the factory has been unreachable this long |
| `context_pattern` | `ctx (\d+)% left` | a regex with one group: the context percent, as your agents' status line prints it |

### Projects

A project is a repository whose agents run in this Herdr app (for example a team you run beside the factory):

```json
{ "name": "My app", "github": "OWNER/NAME", "space": "my-app",
  "wait_patterns": [{ "match": "NEEDS REVIEW", "clear": "REVIEWED", "label": "waits for a review", "after_min": 15 }] }
```

- `github`: the repository whose open and recently merged PRs get a card and a segment in the bar. This uses `gh`, logged in on this machine.
- `space`: the Herdr space (id or name) that holds the project's agents. Its tab is where the dashboard tab opens first.
- `wait_patterns`: text on an agent's screen that means it is waiting on a person. An idle agent showing `match` (and not `clear`) for `after_min` minutes raises an alert, for example a team that asks for a shared resource and waits for a go.

## How it works

- One copy of the mod at a time does the polling, using a short lease in the app's shared state. Every copy reads the same data. The bar and the tab never poll twice.
- The polling intervals: factory home every 45 s, resources every minute, cost every 5 minutes, project PRs every 2 minutes, and agent screens every 2 minutes (`hc read <t> --lines 30`, this machine's agents only).
- `herdr-app check ~/.config/herdr-app/mods/hivemill` loads the mod once and reports errors.

## Relation to G-031

G-031 SC8 asks how herdr-core hosts a view. T-1392 and T-1420 found that `hc` has no view slot, and that is still true: herdr-core draws nothing. There is a slot one layer up, in the Herdr app. Its mods (`~/.config/herdr-app/mods/<name>/`) are panels, tabs and bars that the app draws, and they read `hc` through the app's `hc()` and `exec()`. This dashboard is one working example of that slot. Whether it settles SC8 is for that goal's own spec to decide.

## Limits

- The factory reports today's spend, spend per day, cost per merged PR, merges per day and the most expensive tasks for all orgs together. When only one org is shown, the Budget card says so.
- The Cursor CLI reports no token counts, so Cursor work is unpriced (see [web-ui.md](web-ui.md)).
- The status bar replaces Herdr's own status bar in the Hivemill layout. It keeps where you are, the branch and the commands hint, but not the agent count, which the agents list shows.
