# Skill scout report

How the skill scout's results reach the director, the boss and the owner. The code is `scout/report.py`; `bin/factory` has `factory scout report`, the `build_digest` section and the `send` handed to the tick. See [skill-scout.md](skill-scout.md) for the whole feature.

## The section

`build_skill_scout_section(con, since, until)` returns markdown that starts with `## Skill scout`:

- **Finds**: up to 5 candidates triaged in the window (their newest triage), ordered `adopt` > `watch` > `skip`, then stars per day (highest first). Each shows the repo link, stars and +Δ/day, licence, last commit, purpose, target roles, overlap, install risk, the REC and its one-line why.
- **Eval results (indicative, n=1 per cell)**: for candidates with trials that ended in the window, one row per task: pass (`P`) or fail (`F`) with the skill and without it, median tokens (input + output + cache creation) and median wall seconds per condition, and whether the skill was invoked.
- **Needs owner decision: executable parts**: triaged `adopt` or `watch` candidates whose install risk is not `prompt-only` (or unknown), with their risk flags.
- Footer: "Adoption of any third-party skill needs owner approval through the boss."
- Nothing in the window: the heading and the line `No new finds.` and nothing else.

Candidate text is untrusted: every field is collapsed to one line, so it cannot add headings or list items.

## Window boundaries

Every window is `(since, until]`: the start is exclusive, the end inclusive. The section and the digest (`factory digest`, `--hours`) use the same rule, so an item stamped exactly on a boundary appears in exactly one of two adjacent reports.

## Report flow

| path | when | from → to |
|---|---|---|
| per run | the tick sees a run reach `done`, `stopped` or `failed` with `reported_msg` NULL | `tick` → director, kind `report`, subject `skill scout <date> <HH>` |
| daily digest | `build_digest` (morning digest and `factory digest`, not with `--goal`) | director → boss, the section for the digest window |

- The window of a per-run report is the end of the previous *reported* run to the end of this run (24 h before when there is none). A skipped run sends nothing and is never "reported", so the next report also covers its gap.
- Exactly once per run: the check, the message and `scout_runs.reported_msg` are one IMMEDIATE transaction. A report that fails is retried on the next tick (a `scout-report-failed` event is logged) and never blocks the next slot.
- No herdr prompt is sent for it. The director sees it through the normal unread-mail nudge.
- The director folds the latest report into the 18:00 owner report through the boss. Adopt and executable-part items go to the owner as decisions. Nobody adopts or installs a third-party skill without a recorded owner decision.

## CLI

`factory scout report [--slot S | --since ISO] [--send]`

- Prints the section. `--slot 2026-10-26T09` uses the window of that finished run (as the tick does); `--since` uses `(ISO, now]`. Without either, the window is the newest finished run's own window: it starts at the end of the run before it, so that run's finds are included, or 24 h before now when there is none.
- `--send` (director or owner only; lead, boss, scout and workers are refused with exit 3) sends the section to the director as kind `report`, subject `skill scout <date> <slot>` (the hour of now when no `--slot`). It does not set `reported_msg`.
- Reading is open to director, lead, boss, scout and owner.

## Related settings

`scout_enabled` (`factory config scout_enabled 0` stops new runs; reports of runs already finished are still sent), `scout_slots`, `scout_max_minutes`, `scout_max_tokens`, `scout_queue_max_h`, and `skilleval_root`. Details: [scout-tick.md](scout-tick.md) and [scout-vendor.md](scout-vendor.md).
