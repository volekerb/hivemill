# The state branch

## What it holds

`factory export` (also run by the tick) writes generated files into the factory home and commits them to a local branch named `state`:

- `exports/`: readable Markdown views of goals, plans, tasks, verdicts and reports.
- `state/factory.sql`: a full dump of the database. This includes every private agent message, task text, config value and verdict.
- `state/events.jsonl`: the audit log.

The commits are made without touching your checked-out branch.

## Why it is private

The dump contains all agent mail and every spec and plan text. If the `origin` remote is public, pushing the branch publishes all of it. Never push the state branch to a public remote.

## Config keys

| key | default | meaning |
|---|---|---|
| `git_push` | `1` | master switch for pushes from `factory export` and the tick: the code push in sync, and the state branch. The code fetch and fast-forward merge in sync is not gated. `0` turns every push off. |
| `state_push` | `0` | also push the `state` branch to origin. Needs `git_push=1`. Off by default because the dump is private. |
| `public_text_repos` | empty | comma list of `OWNER/NAME` that are public. `factory export` and the tick never push the `state` branch when `origin` is one of them, or cannot be parsed while the list is set, whatever `state_push` says (G-021). |
| `frozen_repos` | empty | comma list of `OWNER/NAME` that nothing in bin/factory may push to or merge into; the state push is refused for a frozen `origin` too. |
| `push_interval_s` | `600` | the tick pushes the state branch at most this often. A manual `factory export` pushes at once. |

With `state_push=0` the local `state` branch still gets a commit on every change, so you keep a local backup. Set it with `factory config state_push 1` (director) only when `origin` is a private repository. An unpushed state commit is pushed at the next export once the key is on.

`factory export` prints `state push disabled (state_push=0)` when it skips the push. It prints `state push refused: origin <slug> is public (G-021)` when `origin` is listed in `public_text_repos` (or `... cannot be parsed ...` when the list is set and `origin` names no `OWNER/NAME`); the local `state` commit is still made.

## Restore

From a clone or the factory home, with the state branch available:

```
git show origin/state:state/factory.sql > /tmp/f.sql
sqlite3 db/factory.db < /tmp/f.sql
```

Use `refs/heads/state` instead of `origin/state` when restoring from the local backup. Restore into a new, empty `db/factory.db`.
