# Fast lane (G-070, T-1827)

The fast lane merges small, low-risk PRs without the full reviewer + QA + judge round. It reuses the CI tier rules
(`ci/ci_tier.py` `RISKY_PATHS`/`RISKY_DEFS`), the mechanical merger (`factory mech-merge`, `docs/mech-merge.md`) and the
`judge_review_gate=green` merge gate (`docs/judge-ci-gate.md`).

**It ships off.** Nothing changes until the owner runs `factory config fast_lane on`. Every other role gets exit 3. The
rollback is `factory config fast_lane off`: every task then takes the normal lane again, including tasks already
marked fast.

## Config keys

| key | default | meaning |
|---|---|---|
| `fast_lane` | `off` | `on` turns the lane on; owner only |
| `fast_lane_check_timeout_s` | `600` | a fast check with no PASS/FAIL after this many seconds is stopped and the task moves to the normal lane (minimum 60) |
| `fast_lane_audit_every` | `10` | every Nth fast-lane merge gets a post-merge normal review task; `0` turns audits off |

The registry entries are in `bin/flags.json`.

## Which tasks take the fast lane

A task gets its lane from one of three places, checked in this order:

1. **The task's own lane.** Set with `factory task add ... --lane fast|normal` or
   `factory task update T --lane fast|normal`.
2. **The goal's lane.** Set with `factory goal new ... --lane fast|normal`. Every task of the goal inherits it unless
   the task has a lane of its own.
3. **Docs-only diffs, automatically.** A task with no lane whose PR diff touches only docs files (`docs/**`, or `*.md`
   outside `roles/`) gets the lane `fast:docs`.

Who may set what:

- `--lane fast` is for the boss, director and owner. Every other role gets exit 3.
- `--lane normal` (opting out of the fast lane) is open to every role that may already create or update the task.

`factory task show T` prints the lane. The lane lives in the config row `lane:<id>`, and each change is logged as a
`lane-set` event.

## Escalation to the normal lane (mechanical)

The detached `factory mech-merge` pass classifies each new PR head of a task that is not in the normal lane:

1. `git fetch origin main` plus the PR branch.
2. Load **main's** `ci/ci_tier.py` and call its `lane(root, origin/main, head)`. The rules come from main, so a PR
   cannot relax the rules that judge it.

The same check is available on the command line:

```
python3 ci/ci_tier.py escalate --base origin/main --head <sha>
```

It prints one `escalate: <reason>` line per rule hit, then `lane=fast|docs|normal`. The exit code is 0 for fast or
docs, 1 for normal, and 2 when git cannot answer (the PR then leaves the fast lane).

A diff goes to the normal lane when it touches any of the following:

| what | rule |
|---|---|
| guard, hooks, role settings and prompts | `roles/*`, `go/internal/guard/*`, `go/cmd/worker-guard/*`, `go/internal/identity/*` |
| GitHub workflows | `.github/*` |
| CI gates | `ci/*` |
| secrets and tokens | `*secret*`, `*token*`, `*credential*`, `*.pem`, `*.key`, `.env*` |
| sandboxes | `go/internal/sandbox/*`, `scout/*box*.py`, `scout/sandbox*`, plus the scout sandbox files in `RISKY_PATHS` |
| merge and judge code | `go/internal/gate/*`, `go/internal/holdexpiry/*`; in `bin/factory` and `bin/factory_cli.py`, a changed def named in `RISKY_DEFS`, or starting with `judge_`, `gate`, `mech_`, `merge_`, `verdict`, `red_main`, `fast_lane`, `lane_`, and similar prefixes |
| DB schema and migrations | `db/*`, `*.sql`, `*migration*`, `go/internal/factorydb/*`, and the defs `SCHEMA`, `SCHEMA_VERSION`, `migrate*` |
| deleted tests | a removed `tests/test_*` or `*_test.go` |
| unreviewable diffs | binary or non-text files, or a new top-level entry |
| systemd units | `systemd/*` (from `RISKY_PATHS`) |

`MAX_LINES` (600) does **not** escalate a fast-lane PR. The fast lane relaxes the size limit.

When a diff escalates, the merger:

1. Sets the task's lane to `normal`.
2. Logs a `lane-escalated` event with the reasons.
3. Labels the PR `lane:normal`. It creates the label if it is missing; a label failure is logged as
   `lane-label-failed`.
4. Sends the lead an FYI.

From then on the task follows the normal review flow. A head that cannot be read is never left in the fast lane:

- a task with no lane gets `normal`;
- a task already marked fast keeps its lane but is not merged until a head can be classified.

## Review: one fast check, or none

**Docs-only heads (`docs`)** get no agent at all. The mechanical merger merges them on green CI.

**Code heads (`fast`)** get exactly one combined fast check per head:

- It is a qa-role agent named `fastcheck-<task>-<sha8>`, running on the qa model (Sonnet 5.5 by default).
- The tick starts it once CI is green on the head.
- It replaces the reviewer, QA and judge. Its brief tells it to:
  1. read the diff and the acceptance;
  2. run only `python3 ci/affected.py --base origin/main --quick --run`;
  3. record one verdict with `factory verdict T --pass|--fail --sha <sha8>`.
- The verdict is stored with the role `fastcheck`.

Outcomes:

- **PASS:** the mechanical merger merges on green CI. The fast lane merges even while `mech_merge` is `off` or
  `shadow`, because `fast_lane=on` is the owner's switch for these tasks. Every other merge gate still applies: PR open,
  not a draft, merge-tree clean, freshness, `red_main_freeze`, judge hold, project `auto_merge`, and the classified
  head being the current head.
- **FAIL:** the coder gets one rework round, and the lead receives a request.
  - A second FAIL moves the task to the normal lane with the reason "2 fast-check FAILs".
- **No verdict within `fast_lane_check_timeout_s`:** the tick stops the agent and moves the task to the normal lane.

While a task is in the fast lane, the reviewer/QA autospawn, the review rerun and the judge autospawn all skip it.

## After the merge

- **Red main.** When `red_main_check` finds main red and the newest merge on main was a fast-lane merge, the tick does
  four things:
  1. Opens a GitHub revert PR of that merge (`revertPullRequest`).
  2. Attaches the revert PR to the red-main task. That task goes in the fast lane, so the revert merges on green CI.
  3. Moves the culprit task to the normal lane.
  4. Files `Redo <T> in the normal lane` as a P1 coder task.

  A failed revert is logged as `fast-lane-revert-failed`, and the red-main task stays with its coder.
- **Audit.** Every `fast_lane_audit_every`-th fast-lane merge files a reviewer task: `Post-merge review of fast-lane
  <T>`, a follow-up of `<T>`. The reviewer's findings become follow-up tasks.

## Metrics: daily digest

The global daily digest (`factory digest`) has a `## Lanes` section whenever `fast_lane=on`, or whenever the window
holds a fast-lane merge. It compares the fast and normal lanes over the merges in the window:

- merged count;
- median hours from task creation to merge;
- revert rate (`fast-lane-reverted` events);
- follow-up-fix rate (a coder task filed `--follow-up-of` the merged task);
- median agent-minutes per PR.

Agent-minutes stand in for spend: the DB records tokens per goal, not per task.

## Events

| event | when |
|---|---|
| `lane-set` | a lane was set by a flag or by the classifier |
| `lane-classified` | a head was classified (`docs` or `code`) |
| `lane-classify-skipped` | git could not fetch or read the diff |
| `lane-escalated` | a task moved to the normal lane |
| `lane-label-failed` | the `lane:normal` label could not be added |
| `fast-lane-merged` | a fast-lane merge happened |
| `fast-lane-audit` | an audit task was filed |
| `fast-lane-reverted` / `fast-lane-revert-failed` | the red-main revert succeeded or failed |

## Limits

- This is the Python path only. The Go tick, Go CLI and Go `ci-tier` do not implement lanes yet. A `--lane` call
  falls back to Python.
- `judge --train` and the other global `judge_review_green` callers keep the global gate. Fast-lane tasks never reach
  them, because the judge autospawn skips those tasks.
