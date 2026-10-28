# Demo data

`ci/demo_seed.py` builds a self-contained, fictional `FACTORY_HOME` so the web UI can be screenshotted and shown in
documentation examples without exposing the owner's real goals, messages or token usage. The screenshots (G-012 T-I)
and the examples (G-012 T-L) are produced from it.

## Usage

```
python3 ci/demo_seed.py --home <dir> [--now <ISO timestamp>] [--force]
```

It runs `bin/factory init` of this checkout against `<dir>` (with an explicit environment: `FACTORY_HOME=<dir>`,
`HOME=<dir>/home`), then fills `<dir>/db/factory.db` (the file `init` created; found with the same `web/dbpath.py` resolution as the CLI and opened with `mode=rw`, so a wrong path is an error, never an empty new file) with Python's `sqlite3`. The schema comes from `init`; the rows go
straight into the demo DB because the `factory` CLI stamps the real clock and is role-gated, so it cannot produce
history. Fake Claude usage logs are written under `<dir>/home/.claude/` for the Tokens page. Nothing outside `<dir>` is
written, and the live DB is never opened.

To look at it:

```
HOME=<dir>/home FACTORY_HOME=<dir> factory serve --bind 127.0.0.1 --port 8799
```

**The `HOME=<dir>/home` prefix is mandatory.** The Tokens page reads `~/.claude/projects/**/*.jsonl` and
`~/.claude/usage-limits.json` of the current `HOME`, not the DB. Without the prefix it shows the real owner's live usage.
The seed prints this command, with this warning, when it finishes.

## What it contains

Four projects (`acme-web`, `factory`, `ledger-api`, `docs-site`) and twelve goals in mixed states: three done (`G-001`,
`G-005`, `G-008`), four active (`G-002`, `G-003`, `G-006`, `G-007`), two blocked (`G-004` on a design decision, `G-009`
parked while it waits for the owner's legal texts), one proposed (`G-010`, waiting for the owner's approval, with an
unread message to the owner) and two being planned (`G-011`, `G-012`). Nine teams (five active, one parked, three
closed), about 110 tasks across the roles `pm`, `coder`, `qa`, `reviewer`, `judge` and `cleaner` in every task
status, about 70 merged PRs (the exact numbers move a little with `--now`) under `https://github.com/example-org/<project>/pull/<n>`, reviewer / QA / judge verdicts
including failures and judge rejections (many tasks fail once and pass on the retry), about 185 messages (requests, reports,
decisions and escalations), 17 live agents (boss, director, five leads and ten workers of every role) next to done,
exited, queued, lost and stopped ones, events spread over 21 days (throughput and quality curves), team evaluations,
plans, and fake usage logs for the Tokens page: one session per agent that has a working pane, so the Tokens page and the
home usage panel add up to the agent list. All names are fictional. The seed takes well under a second and the DB is
about 0.5 MB.

The first four goals, their 24 tasks (`T-0001` to `T-0024`), their agents and messages are the original hand-written
core that [examples/](examples/README.md) and the README trial quote; they never change. Everything else is generated on
top of them from tables and rules in `ci/demo_seed.py` (`NEW_GOALS`, `FEATURES`, `INFLIGHT`, `world()`), with ids from
`T-0031` up (`T-0030` is left free: it is the name of a queued agent).

### The throughput rule

The merges of the last 21 days (the UTC calendar days ending at `--now`'s date) follow one rule, written as a comment on
`merge_days()` in the seed. Each day gets a weight, trend x weekday x dip x jitter:

- trend: rises linearly from 2.4 (oldest day) to 5.6 (today), a mild upward trend;
- weekday: 1.0 Monday to Friday, 0.3 on Saturday and Sunday, so weekends are quiet;
- dip: 0.2 on one weekday about two weeks back that holds no hand-written merge, one visible dip;
- jitter: a seeded plus or minus 15 %.

The weights are scaled to about 64 merges (the 8 hand-written ones included), rounded to at least one a day, and a day
that equals its neighbour is bumped by one, so no two consecutive days match. Merges land between 09:00 and 16:00 UTC.
Each merge is a task with a full lifecycle (spawn, PR, verdicts, merge); about a fifth fail review once, a seventh fail
QA once, and one in seventeen is rejected by the judge. `tests/test_demo_seed.py` asserts the minimum counts, the
weekday-above-weekend rule, the dip and the no-equal-neighbours rule.

Every task has a short fictional spec (2-4 lines) and 2-4 numbered, testable acceptance items. Verdict summaries are
concrete: a FAIL or REJECT names a finding (`file:line`, expected vs actual) and a PASS names what was checked.

**Head SHAs.** There is one head SHA per (task, attempt): the reviewer, QA and judge rows of an attempt (including the
`merged` event) all carry the same SHA, and a new attempt (after a fail or reject and a new `in_review`) gets a new one.
This matches the rule in [verdict-carry-over.md](verdict-carry-over.md) and [staleness.md](staleness.md). The seed does
not model a pure main merge after a PASS, so no verdict is carried over to a different head.

Verdicts use the values the real CLI stores: `pass` / `fail` for the reviewer and QA, `merge` / `reject` for the judge.

## The "now" and why it floors to the hour

All timestamps are relative to `--now`. The default is the **current UTC time floored to the hour**, not a fixed date:
the web pages window on the real clock (`datetime.now` in `web/server.py`): throughput looks at the last 14 days (the demo data covers 21), the
Agents page shows agents ended in the last 24 hours, Resources shows 7 days, and `usage-limits.json` counts as fresh for
3 hours. A fixed past date would leave those pages empty. Seed shortly before you serve or screenshot.

`--now` takes any ISO timestamp (floored to the hour; no zone means UTC) for a reproducible dump.

## The guard

The seed refuses with exit code 3 and the message `refusing to seed the live factory home` when `<dir>`, after
resolving symlinks, is a protected root, anything inside one, or a parent of one. The roots are the account's `~/factory` (taken from the
passwd entry, so a temporary `$HOME` cannot hide it), the checkout the script lives in, the main checkout (parent of
the git common dir) and `$FACTORY_HOME` when set. It also refuses (exit 3) when `<dir>/db/factory.db` (or a legacy `<dir>/factory.db`) already exists, unless `--force` is
given; `--force` deletes only that DB (and its `-wal`/`-shm`) and `<dir>/home/.claude`, then rebuilds. Refusal happens before anything is
created. Other exit codes: 1 failure or a scrub hit, 2 usage error.

## Determinism

The same `--now` gives an identical `iterdump()` of the DB and identical log files. Ids are autoincrement values assigned
in time order, SHAs are derived from the task id and attempt, and the random token counts use a fixed seed. Nothing depends on the
machine, the time zone or the live data.

## Scrub check

After seeding, the seed dumps the DB as SQL text and runs `python3 ci/scrub_scan.py --path` over it (see
[scrub-scan.md](scrub-scan.md)). Any hit fails the seed with exit 1. Set `SCRUB_TERMS_FILE` to include the owner-specific
terms; they are never committed. The temporary dump is written inside `<dir>` and removed again.

## Config

`factory init` copies the default config into the demo DB (`serve_hostnames` defaults to empty). The seed sets the
fictional `demo.example` so the demo server accepts a name besides its bind address.

`factory serve --ttl <min>` exits by itself after that many minutes; on a non-live home (a demo/scratch/temp `FACTORY_HOME`) it defaults to 60 unless you pass `--ttl 0`, and the live home never gets a TTL. The tick also SIGTERMs (SIGKILL after 10 s) any stray `factory serve` whose `FACTORY_HOME` or cwd is in the temp dir of an ended agent, at most every 10 min (`tests/test_serve_reap.py`).
