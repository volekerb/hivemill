# Observability store and the Spend and Flow pages (G-069)

`hm-obs` (Go, `go/cmd/hm-obs`, package `go/internal/kit/obs`) keeps `state/obs.db` and answers the web UI's
**Spend** (`/spend`) and **Flow** (`/flow`) pages. The old `/tokens` and `/throughput` URLs answer 301 to the new
pages and keep their query string.

## How the data gets there

- **Ingest.** The tick runs `hm-obs ingest` every 5 minutes. Each run:
  - Reads every Claude Code transcript under `~/.claude/projects` from where the last run stopped.
  - Keeps 90 days of requests and tool calls.
  - Joins each session to its agent, task, goal, project and PR from `db/factory.db`, which it opens read-only.
  - Refreshes the pr-flow snapshots when the last one is older than 15 minutes.
  - Takes a lock, so a second run at the same time does nothing.
- **Privacy.** Ingest stores token counts, prices, model names and tool names. Tool names are checked against a
  name pattern. A Bash call is reduced to an allowlisted command head such as `git push`. It also stores sizes and
  ids. It never stores message text, prompts, tool input or output, file contents or paths. The Go test
  `TestPrivacyNoContentIsStored` feeds secrets through every field and checks that none reaches `obs.db`.
- **API.** The pages call `hm-obs api spend|flow` with a fixed argv. Only validated values reach it: a known range,
  a known group, and filter values that match an id pattern. The same JSON is served at `/api/obs/spend` and
  `/api/obs/flow`.

```
hm-obs ingest [--home DIR] [--root DIR] [--now RFC3339] [--flow-every DUR]
hm-obs api spend [--home DIR] [--hours 24|72|168|720] [--group role|model|goal|project|agent|session]
                 [--role V] [--model V] [--goal V] [--project V] [--agent V] [--session V]
hm-obs api flow  [--home DIR] [--hours 24|72|168|720] [--project V]
```

Until the tick has built the binary and run one ingest, both pages say so in a `!` banner.

## How this is counted

### Spend

- **Cost.** Every request is priced at API list prices, with the same price table and rules as `hm-spend`. Each
  request's cost falls into one of three buckets:
  - **spawn:** the cache write of a transcript's first request.
  - **polling:** a turn that produced fewer than 300 output tokens.
  - **work:** everything else.
- **Strip.** The strip shows five figures for the selected range:
  - spend, with the not-attributed share;
  - cost per merged PR;
  - polling share;
  - cache-write share;
  - the Claude plan's weekly limit, which is a plan percentage, not dollars.

  The first four figures show their change against the range before it.
- **Chart.** The chart stacks work, polling and spawn per hour (24 h and 3 d ranges) or per day.
- **Group-by table.** The table groups spend by role, model, goal, project, agent or session.
  - Clicking a row drills in. A role row opens its models, a model row opens its sessions, and so on.
  - **Not attributed** is the spend of sessions that could not be tied to the field being grouped by.
  - A polling cell turns amber above 20% of its row. A spawn cell turns amber above 15%.
- **Parity.** Spend matches `hm-spend` for the same window within 1%, because both use the same prices and rules.

### Flow

All Flow figures come from the stored pr-flow snapshots (see [pr-flow.md](pr-flow.md)) for the window.

- **Strip.** The strip shows:
  - merged PRs;
  - median and p90 hours from open to merge;
  - the share of PRs that needed 2 or more review rounds, as "n of m";
  - PRs that have been open for more than 6 hours.
- **Stage map.** The map shows each pr-flow stage's hours and its share of all stage hours. The main lane runs
  through CI, review and QA, and the judge. Rework and the side queues are shown apart.
- **Stuck now.** This table lists open PRs older than 6 hours, with their task, stage and age.

### Rules (the `!` banners)

| Rule | Page | Fires when |
|---|---|---|
| 1 | Spend | Polling is more than 20% of spend. |
| 4 | Flow | More than 30% of PRs needed 2 or more review rounds. |
| 6 | Flow | One stage holds more than 30% of all stage hours. |

The thresholds are constants in `go/internal/kit/obs/api.go`.

## Page conventions

- **Header selectors.** The header holds the factory (org), project and range selectors (24 h, 3 d, 7 d and 30 d;
  7 d is the default), all kept in the URL.
- **Unknown values.** An unknown value shows "–", never 0.
- **Warnings.** Every warning starts with `!`, so color is never the only signal.
- **Not shown.** There are no status pills, org badges or KPI tiles. Box health and a Cursor plan on its usage
  limit show as `!` banners at the top of every page.
