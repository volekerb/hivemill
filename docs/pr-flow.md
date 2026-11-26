# PR flow controller (`hivemill` pass)

The algorithm that keeps runner PRs from getting stuck. It is deterministic: every step is code in the pass, every action is an event, and no session or agent polls a PR. The report tool of the same name is described in the second half of this file.

## 1. One poll per pass

At its start a pass reads every open runner PR with one batched GraphQL query (`gh api graphql`) and caches the answer for the pass:

- per PR: state, draft, mergeable, head SHA, title, body, merge commit, and the check runs with status, timestamps and job link;
- per repo and base branch: the base protection (strict or not) and the checks of the last 30 base commits (main's push runs, and which job names passed in the last 24 h).

The pass makes no other `gh` status call. A PR the query could not read counts as pending until the next pass. The only other `gh` calls are the actions below (re-run, cancel, failed-job logs, merge and its confirmation, revert). A pass with no open runner PR makes no call. Commands run by hand (`hivemill merge T`) read their one PR directly.

## 2. Flow states and budgets

Each run with a PR has a flow state, set from the run state and the poll. The state, its entry time and its budget are stored on the run. `hivemill run status` and the obs Flow page show `<state> <minutes>/<budget>`.

| State | Meaning | Budget |
|---|---|---|
| `ci_queued` | checks wait for a runner; none has started | `ci_queue_max_min` (40) |
| `ci_running` | a check of this head has started | `ci_timeout_s` (2700 s) |
| `judge` | the judge session runs or its verdict is being applied | `stale_pr_min` |
| `merge_ready` | green and judged; merges in its turn | `stale_pr_min` |
| `main_wait` | waits for main (red, or its push run is pending) | `stale_pr_min` |
| `refresh` | behind main and has to merge it first | `stale_pr_min` |
| `held_owner` | held for the owner | `ask_alert_h` |

A new state, or a new head in a CI state, is a new entry. `ci_running` does not fall back to `ci_queued` while the head stays the same. Each entry is recorded as a `pr.flow` event. A run that is working again (red CI, conflict, judge REJECT) has no flow state.

## 3. Actions when a budget runs out

Each happens at most once per state entry.

- `ci_queued`: nothing on GitHub (the queue cannot be hurried). Record `ci.queue_slow`. While any PR is queued over budget, no PR is refreshed, because a refresh adds CI runs to the same queue.
- `ci_running`: cancel the unfinished Actions runs and re-run them (`gh run rerun`, on the first pass that sees them stopped). A second overrun on the same head fails the run: `CI timed out`. Checks that are not Actions runs cannot be re-run; they raise one owner alert and the run stays.
- A red CI where every failed job passed on main in the last 24 h under the same name: re-run the failed jobs once per head (`gh run rerun --failed`, event `ci.rerun`). A second red resumes the task session with the log tails.
- `held_owner`: one owner alert. The delegate path answers held asks. The runner never waits on an LLM to merge a green PR.

## 4. Merging

- Runs ready to merge go in FIFO order by the time the judge gave its verdict.
- **Merge train.** While main's push run for the previous runner merge is pending, up to `merge_train` (3) more merges may go out, as long as no two merges of the train change the same file. The train empties when the push run of main's head is green. A pending head that is not a runner merge (someone else pushed) holds as before.
- A red push run of any commit of the train stops it. With `auto_revert=on` the culprit is reverted; merges that rode the train after it do not block that revert, since they share no file with it.

## 5. Stale PRs

A flow state that has not changed for `stale_pr_min` (90) emits one `pr.stale` event with the reason (the merge hold, else the state). The obs Flow page lists it under **Stuck PRs** until the state changes.

## 6. Rate

At most one GraphQL call per pass, plus the actions above. The runner counts the `gh` calls of its passes per hour; `hivemill digest` reports them.

## Events

| Event | Data |
|---|---|
| `pr.flow` | `state` (empty: left the flow), `budget_s`, `pr` |
| `pr.stale` | `state`, `reason`, `minutes`, `pr` |
| `ci.queue_slow` | `head`, `limit_s`, `pr` |
| `ci.rerun` | `head`, `reason` (`timeout` or `flaky`), `runs` |
| `pr.held_alert` | `limit_s`, `pr` |

---

# PR flow report (`go/cmd/pr-flow`)

Read-only measurement of how fast pull requests move through the pipeline: merges per day, open-to-merge time,
PRs open too long, and the hours spent in each stage, split by org. Built for G-058 SC3/SC5 so every pipeline
fix is judged by the same numbers.

```
cd go && go run ./cmd/pr-flow --repo <owner>/<repo> [--since 72h] [--events <path>] [--json] [--red-main-all]
```

`--since` takes `72h`, `90m` or `3d`. `--events` defaults to `$FACTORY_HOME/state/events.jsonl`. `--json` prints
the same numbers as one JSON object. Exit 0 on success, 2 on a usage error, a missing events file or a failed
`gh` call (the message says what to check).

## What it reads

- **GitHub, two calls per run, no per-PR call.** `gh pr list -R <repo> --state all --limit 500 --search
  "created:>=<window start>"` and the same with `"created:<<start> -closed:<<start>"` (PRs created before the
  window that merged inside it or are still open). Fields: `number,state,createdAt,mergedAt,headRefName,mergeStateStatus`.
  PRs closed without a merge are left out. A call that returns 500 rows adds a `warning:` line.
- **The event log**, streamed line by line and never loaded whole. Lines that are not JSON, have no valid `ts`
  or are longer than 4 MiB are skipped; the header line prints how many.

It writes nothing: no factory DB access, no `gh` write, no file.

## PR to task and org

1. the task whose `update` event detail contains `pr=<url ending /pull/N>`;
2. else the task of the `pr-create` event for the PR's head branch;
3. else a branch named `factory/t-NNNN` gives `T-NNNN`.

The org is the prefix (`claude-`, `cursor-`) of the task's latest coder agent. A PR with no task, or a task with
no such coder, is org `other`; with no events its whole life is `ci_wait`.

## Stage map

The PR starts in `ci_wait` at `createdAt`. Each mapped event of its task starts a stage that lasts until the next
mapped event, up to `mergedAt` (or now for an open PR). Events before the PR opened and unknown or renamed
actions are ignored, so a change in the CLI's action names never fails a run.

| event | stage |
|---|---|
| `pr-create`, `pr-ready`, coder agent `done` | `ci_wait` |
| `review-autospawn-wait` with `"kind": "ci"` / `"coder"` / `"gate"` | `ci_wait` / `coder_rework` / `capacity_gate` |
| `review-autospawn` | `review_qa` |
| reviewer and QA verdict of the round both in: both pass / otherwise | `judge` / `coder_rework` |
| coder agent `spawn`, `coder-ci-resume`, `judge-reject` | `coder_rework` |
| `red-pr-unowned` | `red_pr_unowned` |
| `judge-autospawn`, `mech-blocked`, `judge-hold-clear` | `judge` |
| `judge-hold-set`, `judge-hold` | `hold` |
| `judge-blocked` containing `STALE` or `head is now`, `refreshed`, `verdict-carried` | `refresh` |
| task `update` with `status=blocked` | `blocked` |

Time in `judge`, `hold` or `refresh` that falls inside a red-main window of the repo (`red-main` to
`red-main-clear`; a window with no clear lasts until now) is counted as `red_main` instead.

A review round starts at a `review-autospawn` for a new head sha (or at a verdict when none is open). The
`refreshes` count is the number of `refreshed` events.

## Output

In this order: the window; `merged per day` (UTC days, per org; the first day is partial); `open-to-merge hours`
for all, claude, cursor and other (n, median, p90, max, count over 6 h; percentiles interpolate linearly); `open
over 4 h` and `open over 6 h` with number, age, `mergeStateStatus`, current stage, org and task; `stage hours` per
org; reviewer FAIL and QA FAIL counts, PRs with 2+ review rounds and the refresh count; the 10 slowest PRs with
their largest stage. Stage hours and the review counts cover the whole life of every PR in the report (merged
in the window, or open), including hours before the window started. `mergeStateStatus` is what the list call
returns; GitHub often answers `UNKNOWN` there.

## Red main

The last section of the text output and the `red_main` object of `--json` count how often main was red in the
window (G-063 SC4: red-main events per day before and after the quarantine). Keys and lines are only ever added; the
lines above the section and the other JSON keys are unchanged.

Where it comes from: the tick hook `red_main_check` (in the factory CLI, flag `red_main_freeze`) writes the event
`red-main` (entity `redmain`, entity_id `<owner>/<name>`) once per failed `tests.yml` push run id on main, and
`red-main-clear` when a green push run ends the window. The DB events table is exported to
`$FACTORY_HOME/state/events.jsonl`, which is `--events`. `red-main-unknown` (a `gh` error) is not a red event and
is ignored, like unknown and malformed lines.

For the repo given in `--repo` and the `--since` window:

| JSON key | meaning |
|---|---|
| `events` | `red-main` events in the window |
| `events_per_day` | `events` / window days, 2 decimals |
| `windows` | red windows that overlap the window: the first `red-main` while none is open (later ones only add to `events`) until `red-main-clear` or now |
| `red_hours` | the lengths of those windows clipped to the window |
| `per_day` | list of `{"day", "events"}`, days with at least one event, oldest first |

The day is the date in the event's own `ts` offset (a `+01:00` ts is bucketed by its local date), not the machine
time zone. The windows are the ones the stage map uses for `red_main` stage hours (the same window data; the clipping is separate code), so a PR inside
a window spends exactly its time there as `red_main`. `--red-main-all` adds `red_main_repos` (JSON) and
`red main repo <name>` blocks (text) for every repo in the event log.

Before/after measurement:

```
cd go && go run ./cmd/pr-flow --repo volekerb/hivemill --since 7d --json
```

## Daily run (director)

Once a day, from the factory checkout:

```
cd go && go run ./cmd/pr-flow --repo <owner>/<repo> --since 24h > /tmp/pr-flow.txt
factory report --goal G-058 --to boss "PR flow <date>: <merged> merged, median <h> h, <n> open over 6 h" - < /tmp/pr-flow.txt
```

Compare the merged count, the open-to-merge median and the `open over 6 h` count with the day before, and name
the largest row of `stage hours` in the headline when it changed.
