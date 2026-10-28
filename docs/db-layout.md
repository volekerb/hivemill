# DB layout (`~/factory/db/factory.db`)

The live database lives alone in its own directory, `~/factory/db/` (mode 0700), so a sandbox can bind that one directory read-write (SQLite WAL needs `-wal` and `-shm` next to the file) and the rest of `~/factory` read-only.

## Which file the CLI opens

`bin/factory` resolves the path in one place (`resolve_db()`), and `roles/hooks/worker-guard.py` and `factory serve` use the same order:

1. `$FACTORY_HOME/db/factory.db` (`FACTORY_HOME` defaults to `~/factory`), if it exists or the legacy file does not.
2. The legacy `$FACTORY_HOME/factory.db` (before the migration).

There is no override: no environment variable other than `FACTORY_HOME` picks the DB, so a different DB means a different home. The choice is made by one helper, `web/dbpath.py`, used by `bin/factory` and `web/context.py`. Known residual: a `FACTORY_HOME` whose `db/` is a symlink to `~/factory/db` still reaches the live DB (pre-existing; the sandbox covers it).

Only `factory init` creates a database (and the `db/` directory). Every other command opens the file with `mode=rw`, so a missing DB is an error (`no database; run factory init`), never an empty new file. If the missing file has a `factory.db.migrated-*` sibling (a stale script still pointing at the pre-migration path), the error is `factory.db moved to db/factory.db; rerun the command` instead.

## Self-migration

At the start of every `factory tick`, under the tick lock, when the legacy file exists and `db/factory.db` does not:

1. open the legacy DB, `BEGIN EXCLUSIVE`, checkpoint the WAL;
2. copy it with the SQLite backup API to `db/factory.db.tmp`, check the row counts, fsync, rename to `db/factory.db`, fsync `db/`;
3. in the legacy file add `BEFORE INSERT/UPDATE/DELETE` triggers (`factory_moved_*`) on every table that abort with `factory.db moved to db/factory.db; rerun the command`, then commit. A process that still has the old file open fails loudly instead of writing into a dead file;
4. rename the legacy file (and any `-wal`/`-shm`) to `factory.db.migrated-<UTC timestamp>`;
5. write event `db-migrated` with the per-table row counts.

A second tick does nothing. If any step fails, the partial copy is removed, the legacy DB stays authoritative, event `db-migrate-failed` is written, and the director gets one message per hour at most; the tick retries every minute. In WAL mode readers do not block `BEGIN EXCLUSIVE`, only other writers do; a writer that holds the lock for more than 30 s makes the attempt fail, which also ends in a failed attempt and a retry.

`factory serve` resolves the path on every request, so after the migration it reads `db/factory.db` and never the renamed `.migrated-*` file.

## Rollback

Run from a shell on your-box (`H=~/factory`):

```
systemctl --user stop factory-tick.timer factory-serve
python3 -c "import sqlite3,sys;c=sqlite3.connect(sys.argv[1]);print(c.execute('PRAGMA wal_checkpoint(TRUNCATE)').fetchone())" ~/factory/db/factory.db
mv ~/factory/db/factory.db ~/factory/factory.db
rm -f ~/factory/db/factory.db-wal ~/factory/db/factory.db-shm
```

The checkpoint folds the WAL into the main file, so the `-wal`/`-shm` leftovers are empty and safe to delete. Then revert the T-A PR (the older `bin/factory` reads `~/factory/factory.db`); without the revert the next tick migrates again. Restart:

```
systemctl --user start factory-tick.timer factory-serve
```

If you restored the `.migrated-*` file instead (it carries the abort triggers), drop them first; the command is harmless on a copy that has none:

```
python3 -c "import sqlite3,sys;c=sqlite3.connect(sys.argv[1],isolation_level=None);[c.execute('DROP TRIGGER \"%s\"' % n) for (n,) in c.execute(\"SELECT name FROM sqlite_master WHERE type='trigger' AND name LIKE 'factory_moved_%'\").fetchall()]" ~/factory/factory.db
```

`tests/test_db_layout.py` runs both rollback variants in a temp home.
