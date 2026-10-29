# Agent freshness

Every `factory tick` compares each live agent with the role files, CLI and plugins on main and flags the ones that
run something older (`tick_freshness` in `bin/factory`, runs after the tick's own state export and `sync_pull`, on
every tick, no throttle). A failure inside the check records a `freshness-failed` event and never stops the tick. The event fires once per new error text (throttle key `fresh-failed` holds the text); when a later tick succeeds, one `freshness-recovered` event clears it.

## What is checked

Per live agent (the `evaluator` role is skipped: it runs on sandbox copies). Reasons:

| reason | true when |
|---|---|
| `prompt` | `run/<name>.prompt.md` differs from what a spawn would write now (`roles/_common.md` + blank line + `roles/<role>.md`) |
| `settings` | `roles/settings/<role>.json.in` (the template) or `<role>.json` (legacy rendered copy) is newer than `agents.started` (+1 s: it is whole seconds) |
| `plugins` | a file under `roles/plugins/<role>/` (coders also `roles/plugins/goals/<goal>/`) is newer than `agents.started` |
| `unreadable` | `run/<name>.prompt.md` exists but cannot be read (not a file, bad encoding); only that agent is flagged, the others are still checked |
| `checkout` | global: `~/.local/bin/factory` does not resolve (`realpath`) to `$FACTORY_HOME/bin/factory`, or main's `HEAD` != the local `origin/main` ref. Marks every live agent. No PATH lookup (the systemd unit's PATH lacks `~/.local/bin`) and no extra fetch |

A prompt file that does not exist is not compared (the row was not started by `factory spawn`). With no
`origin/main` ref the HEAD half of `checkout` is skipped.

## What is NOT checked

- **Hooks** (`roles/hooks/*`): each hook runs from disk on every tool call, so a running agent already uses the
  current one.
- **User plugins**: G-014 strips them from spawned agents; only `roles/plugins/<role>/` and the goal dir are loaded.
- The running model, effort flag or MCP set: they are fixed at spawn and not derived from main.

## Output

- Event `stale-agent` `{name, role, reasons[]}` when an agent's sorted reason set changes; event `fresh-agent`
  `{name}` when it clears. Dedupe state: throttle key `fresh:<name>` holding the reason set.
- Persistent roles (boss, director, lead) with no relaunch queued: one message per new reason set to the director
  (the boss for the director), subject `stale agent: <name>`, body ending in `factory relaunch <name> --when-idle`.
  An agent with `relaunch_requested_at` set gets the event only (the judge's role-change notice already covered it).
  Workers get the event and the digest line only.
- `factory digest`: one line (also printed when there is no goal block) `Freshness: all current` or `Freshness: <name> (<reasons>); ...`, after the first
  `Drift:` line. It shows the state of the last tick, also for a past `--date`/`--hours` window.

## How to act

Persistent agent: `factory relaunch <name> --when-idle` (the director or the boss does this). A worker is short-lived;
ignore it unless it stays stale across a long task. For `checkout`: fix the launcher symlink or find why the main
checkout is not at `origin/main` (see the sync-failure messages from `sync_pull`); relaunches do not clear it.

## mtime false positives

`sync_pull` only fast-forwards (`merge --ff-only`), which rewrites just the files that changed, and
`render_legacy_settings` writes `roles/settings/<role>.json` only when its content differs. Neither touches unchanged
files under `roles/`; `tests/test_freshness.py::CheckoutTests::test_a_synced_export_does_not_touch_unchanged_role_files`
pins this.
