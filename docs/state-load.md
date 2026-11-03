# Building a temp DB from `state/`

`ci/state_load.py` builds a throwaway factory DB, events included, from a `state/` directory (the generated `state` branch, see [state-branch.md](state-branch.md)). QA and anyone else who needs to run the CLI against a snapshot use it instead of loading events by hand.

```
FACTORY_HOME=<fresh temp dir> python3 ci/state_load.py <state-dir>
```

`<state-dir>` holds `factory.sql` and `events.jsonl`. The DB is written to `$FACTORY_HOME/db/factory.db` (the path the CLI opens, see [db-layout.md](db-layout.md)), and the script prints the row count per table.

## Why a script

The export leaves the `events`, `throttle` and `util_samples` rows out of `factory.sql` (they are large and change every minute, which would churn the state branch). Events are written separately to `events.jsonl`, one JSON object per line. The script runs `factory.sql`, then inserts each event line with its original `id`, so the `events` row count equals the line count of `events.jsonl`. `throttle` and `util_samples` stay empty.

## Refusals (exit 1, nothing written)

- `FACTORY_HOME` unset or empty.
- `FACTORY_HOME` resolves to a live factory home: `<passwd home>/factory`, or the one line in `<passwd home>/.config/factory/live_home`. Symlinks are resolved. This repeats the CLI's own rule (`derive_live_homes` in `bin/factory_cli.py`), which cannot be imported without side effects.
- `$FACTORY_HOME/db/factory.db` (or the legacy `$FACTORY_HOME/factory.db`) already exists.
- `factory.sql` or `events.jsonl` missing, or either fails to load. The DB is built as `factory.db.tmp` and renamed only on success, so a failed load leaves no DB.

Delete the temp home when done.
