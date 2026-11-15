# ADR 0004: Several independent factories that share repos and one Claude account

## Status

Proposed (2026-11-15). Written at the owner's request ("plan this change and the transition, and plan validation of
everything"). It becomes Accepted only with the approving message id recorded here. No build task starts before
that, and section 7 lists the decisions the owner makes first.

## 1. Context

The owner wants a second factory on a second machine: today a Linux box runs the factory, and a Mac is to run one
too. Each factory has its own boss, director, database and caps, and they usually work on different goals. Sometimes
they work on one goal together. The owner manages each one on its own machine, or all of them from a thin client
(a laptop running the Herdr app, which reaches each machine over `hc`).

Hivemill assumes one factory per OS user, and in practice one per machine. Two factories on two machines are
already separate on the machines themselves: each has its own home, database, herdr session, locks and agent names.
They collide where they share something:

| Shared thing | What breaks today | Where |
|---|---|---|
| The GitHub repo | Each database hands out task ids as the highest local id plus one. Two factories create the same `T-1600`, push the same branch `factory/t-1600`, and title PRs with the same id. | `next_id`; branch `factory/<task>`; `factory pr create` |
| Remote branch cleanup | The "in use" check reads only the local database, and the stray-branch rule matches task ids regardless of case. One factory could judge the other's branch stale. | `cmd_clean_remote_branch`, `_rb_stray_pr`, `_rb_in_use` |
| Repo-wide duties | Both factories freeze on red main and file their own revert task, both message their director for each new issue (`issue_intake_repos`), and both would push one `refs/heads/state`. `frozen_repos` and `public_text_repos` are kept by hand in each database. | red main; issue intake; `state_push` |
| The Claude account | The 5-hour and 7-day percentages describe the whole account, but each factory gates at its own `max_*_pct` (92 by default) as if it were alone. The gate is open while `~/.claude/usage-limits.json` is missing or older than 3 hours, and the script that writes it is not in this repo. A new factory without it has no Claude cap. | `usage()`, `gate()` |
| The gh account | GraphQL allows 5000 points per hour per account. Two factories on one account share that limit, and `gh_quota_min` cannot tell who spent it. | docs/gh-quota.md |
| The thin client | No factory reports a name. The API has no hostname or id, so the dashboard can only trust its own `teams` list. The installer writes only the single `factory` entry. The dashboard takes usage from the first team that reports it, and its Open links default to the first team. | `/api/*.json`, install.py `dashboard_config`, data.js `usage()` / `webUrl` |

A Mac factory also cannot run at all today: gates read `/proc`, there are only systemd units, and nothing is built
for darwin. ADR 0003 plans that port and is itself still Proposed.

## 2. Decision

Each machine runs its own factory with its own database, director, boss and caps. Factories meet only in git and on
GitHub. They never share a database or a worker pool, and no director spawns agents on another machine.

### 2.1 Factory identity

- New config key `factory_name`. It is short, lower case, `[a-z][a-z0-9-]{0,15}`, and set once at install. It is
  empty on existing factories until the owner sets it.
- `/healthz`, `/api/home.json` and `/api/resources.json` return `factory: {name, host, os, version}`.
- A dashboard team may name the factory it expects (`"expect": "linux"`). If the URL answers with a different name,
  the dashboard raises an alert instead of showing the wrong factory's data under that label.

### 2.2 Namespaces on a shared repo

Task and goal ids stay local. Two factories may both have a `T-1600`. Everything that reaches GitHub carries the
factory name once `factory_name` is set:

- Branches: `factory/<name>/t-1600`. Without a name, a factory keeps `factory/t-1600`. The first factory can stay
  unnamed, so the existing branches and PRs do not change.
- PR titles: `<name> T-1600: ...`.
- `factory clean --remote-branch` deletes only branches in its own namespace: `factory/<name>/` for a named factory,
  and `factory/<task>` with no further `/` for the unnamed one. It refuses any other branch, whatever the PR state.
- The judge's existing check (`headRefName == t.branch`) stays. It now also proves which factory owns the PR.

Id ranges per factory were rejected (section 4): they need a coordinator and they still collide on a mistake.

### 2.3 One duty owner per repo

For each repo, exactly one factory owns the repo-wide duties. The new config key `repo_duties` holds a comma list of
`OWNER/NAME` slugs and is empty by default. On a factory whose `repo_duties` does not list the repo:

- Red main still blocks its merges (it reads the same CI run), but it files no revert or fix task and mails no one.
- Issue intake is off for that repo.
- `state_push` writes `refs/heads/state/<name>`, never `refs/heads/state`.

Merges need no shared lock. Each factory merges with `--match-head-commit`, so a merge by the other factory turns
the next PR BEHIND, and the existing refresh lane handles that. `frozen_repos` and `public_text_repos` stay in
each database. The setup check (section 3, P3) compares them across factories and reports any difference.

### 2.4 The shared Claude budget

The only usage signal is the account-wide percentage, so no factory can tell how much of it is its own. A fixed
split ("60/40") therefore cannot be enforced from that signal. This ADR uses two mechanisms that can be:

1. **Tiered ceilings on the shared total.** These use the existing owner-only keys `max_five_hour_pct` and
   `max_seven_day_pct`. The factory with lower priority stops first (for example 70/75). The other runs to its
   normal limit (for example 92/92). This is a priority order, not a split. With every factory busy, the low tier
   gets nothing beyond its ceiling.
2. **An optional weekly token budget per factory.** New key `weekly_token_budget` (0 = off) is checked against this
   factory's own spend from its local transcripts (the same index as `web/tokens.py`). It closes the gate at the
   budget. Use it when the owner wants a hard limit on what one factory spends. Turning tokens into account percent
   is a guess, so this limits one factory's spend and is not a share of the account.

Other people and teams that use the same account count against the shared percentage too. Whatever ceilings the
owner sets must leave room for them.

The usage signal must exist and be fresh:

- The usage writer moves into this repo (`roles/statusline-usage.sh`), and both installers install it.
- Once `factory_name` is set, a missing or stale usage file closes the gate for new non-HQ spawns, and the tick
  raises one alert an hour. Without a name, the gate keeps today's behaviour (open) and raises the same alert.

### 2.5 gh

Each factory should use its own gh account or token. If they share one, raise `gh_quota_min` on the lower-tier
factory so that it backs off first. `factory doctor` reports when two factories report the same gh login (P3).

### 2.6 Managing several factories

- **On each machine:** nothing changes. Each factory's boss and director run there.
- **From a thin client:**
  - `install.py --herdr-app --team NAME=URL[@MACHINE]` (repeatable) writes `teams`, with `expect` set to NAME.
  - The dashboard shows Claude usage once, as the account's, from the freshest reading of any team. It then shows
    each team's own ceilings, spend and cost per PR.
  - Open links go to the team they belong to.
- **Common goals:** a goal shared by two factories is created in each database. The goal states which factory owns
  it, and only that factory merges the final PR or ships the result. The other factory's part ends in PRs for the
  owner to review. Boss-to-boss notes go through the owner, or `hc send --on <machine>`, never a shared database.

### 2.7 The Mac factory

It follows ADR 0003: no `/proc`, launchd jobs, and its sandbox. It joins as a second factory only after the
ADR 0003 stages it needs have passed, and then through the same onboarding as P5. ADR 0003 also says that runs on
a Mac shared with another team need the owner's go each time. That rule stays.

## 3. Plan, with validation and rollback per phase

Every phase changes Python and its Go mirror together, with parity tests. Every phase merges only after its checks
pass, and each one can be rolled back on its own.

| Phase | Change | Pass check | Rollback |
|---|---|---|---|
| P0 | Owner decisions (section 7) | Recorded in this ADR's Status | none |
| P1 | `factory_name`; `factory` in `/healthz` and the API; dashboard `expect` and mismatch alert | Unit tests for name validation and API fields. On the live factory, `/healthz` shows the name. A dashboard team with a wrong `expect` alerts within one poll. | Unset `factory_name`; the API field is additive |
| P2 | Usage writer in the repo; fail closed when named; `weekly_token_budget` | Fixture usage files at 69/70/71 % and 91/92/93 % against ceilings of 70 and 92. Each closes exactly the right gate. A missing file and a 3 h 1 m old file close the gate on a named factory and only alert on an unnamed one. A fixture spend at budget minus one and at budget. | Unset the name or the budget; caps are config |
| P3 | Branch and PR namespace; cleanup limited to its own namespace; `repo_duties`; state ref per name; a doctor check that compares frozen and public repos and the gh login across teams | Below | Unset `factory_name` and `repo_duties` |
| P4 | Installer `--team`; dashboard account usage and Open links per team | Installer tests (two teams, rerun keeps them, a bad spec gives exit 2). Dashboard on a laptop with two teams, one of them P5's rehearsal factory. | Restore `dashboard.json` from its backup |
| P5 | Rehearsal on Linux (below), then onboarding of the real second factory | Below | Stop the second factory's units; its namespace keeps its branches apart |
| P6 | Mac factory per ADR 0003, onboarded like P5 | ADR 0003's own stage checks, then P5's checklist | As in ADR 0003 |

### P3 checks

These run on two factories under separate OS users on one Linux box, against a scratch repo. Separate users keep
homes, herdr servers and `~/.claude` apart.

1. Both factories create the same task id and push. The two branches differ, and both PRs open.
2. Factory A's cleanup, run on every branch, deletes none of B's branches, including after B's PR merged.
3. Red main on the scratch repo: both stop merging, and only the duty owner files a fix task.
4. A new issue reaches only the duty owner's director.
5. A merges first: B's next PR shows BEHIND, is refreshed and merges. Nothing merges twice.
6. Legacy unnamed branches still clean as before on the unnamed factory.

### P5: rehearsal, then onboarding

1. **Read only.** The second factory runs with `max_live_agents 0`. The dashboard shows both, with correct names
   and no mismatch alerts.
2. **One goal on the scratch repo.** It sets its lower-tier ceilings and checks that it stops at them while the
   first factory keeps working.
3. **One real goal** on a shared repo, with `max_live_agents 2`, for 24 hours. Pass: no collisions in branch,
   cleanup or duties; the first factory's throughput (merges a day) is not below its 7-day median; there are no
   usage alerts.
4. Only then does it get its normal caps.

## 4. Alternatives

- **One database for both, reached over the network.** This was rejected. SQLite over a network is not safe, and
  one machine's outage would stop both. A full cap on one machine would also look like an idle cap on the other.
- **One director that spawns on both machines.** This was rejected. Caps are per machine (processes, memory, load),
  and a remote spawn crosses each machine's sandbox boundary.
- **Id ranges per factory (T-1xxxx, T-2xxxx).** This was rejected. It needs a coordinator, older tools parse ids
  with a fixed width, and one wrong start value brings the collisions back without any sign.
- **A fixed budget split from the account percentage.** This was rejected because the signal cannot attribute usage
  to a factory (2.4).

## 5. Consequences

- A named factory's PRs and branches look different (`factory/<name>/...`, `<name> T-...`). The first factory can
  stay unnamed, so nothing it already has changes.
- A named factory stops spawning when its usage reading is missing or stale. That is deliberate.
- Several factories are only as safe as their config. The doctor check in P3 and the dashboard's `expect` turn
  config drift into alerts instead of silent damage.

## 6. Not decided here

The macOS port and its sandbox (ADR 0003); moving work between factories; any shared view beyond the Herdr app
dashboard.

## 7. Decisions for the owner before P1

1. The factory names (for example `linux` and `mac`), and whether the first factory stays unnamed.
2. Does the Mac factory replace the other team that already uses that machine, or run beside it? If beside it,
   both share that machine's memory and the Claude account.
3. The budget tiers: which factory has priority, its ceilings, and whether to set `weekly_token_budget`.
4. One gh account or token per factory, or a shared one.
5. Which factory owns the repo-wide duties for each shared repo.
