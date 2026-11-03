#!/usr/bin/env python3
"""Build a temp factory DB from a state/ directory, events included.

Usage: FACTORY_HOME=<tmp> python3 ci/state_load.py <state-dir>

<state-dir> holds factory.sql (schema and rows, no events) and events.jsonl (one event per line), the files the export
writes to state/. The DB is written to $FACTORY_HOME/db/factory.db. Refuses: FACTORY_HOME unset, FACTORY_HOME is a
live factory home, a DB already exists there. Prints the row count per table. docs/state-load.md has the details.
"""
import json
import os
import pwd
import sqlite3
import sys
from pathlib import Path


def live_homes(uid=None, base=None):
    """The live factory homes, derived like bin/factory_cli.py derive_live_homes (which cannot be imported without
    side effects): <passwd home>/factory, plus the one line of <passwd home>/.config/factory/live_home if it is a single absolute
    line. Unlike the CLI it does not check the file's type, owner or permissions: a file the CLI would ignore still
    adds a live home here, which can only make this script refuse more."""
    if base is None:
        base = pwd.getpwuid(os.getuid() if uid is None else uid).pw_dir
    homes = {os.path.realpath(os.path.join(base, "factory"))}
    cfg = os.path.join(base, ".config", "factory", "live_home")
    try:
        lines = Path(cfg).read_text().splitlines()
    except (OSError, UnicodeDecodeError):
        return homes  # no file (the usual case): only the default is live
    if len(lines) == 1 and os.path.isabs(lines[0]) and "\0" not in lines[0]:
        homes.add(os.path.realpath(lines[0]))
    return homes


def check_home(env_home, homes):
    """Return (home, error). error is None when env_home may be loaded into."""
    if not env_home:
        return None, "FACTORY_HOME is unset or empty: set it to a fresh temp dir"
    home = Path(os.path.realpath(env_home))
    if str(home) in homes:
        return None, f"FACTORY_HOME {home} is a live factory home: use a fresh temp dir"
    for name in ("db/factory.db", "factory.db"):
        if (home / name).exists():
            return None, f"{home / name} already exists: refusing to overwrite; use a fresh FACTORY_HOME"
    return home, None


def load(home, state_dir):
    """Create home/db/factory.db from state_dir; return {table: row count}."""
    sql, jsonl = state_dir / "factory.sql", state_dir / "events.jsonl"
    for p in (sql, jsonl):
        if not p.is_file():
            raise FileNotFoundError(f"{p} not found: pass the state/ directory holding factory.sql and events.jsonl")
    events = [json.loads(line) for line in jsonl.read_text(encoding="utf-8").splitlines() if line.strip()]
    (home / "db").mkdir(parents=True, exist_ok=True)
    final, tmp = home / "db" / "factory.db", home / "db" / "factory.db.tmp"
    tmp.unlink(missing_ok=True)
    con = sqlite3.connect(tmp)
    try:
        con.executescript(sql.read_text(encoding="utf-8"))
        valid = {r[1] for r in con.execute("PRAGMA table_info(events)")}
        for n, e in enumerate(events, 1):
            cols = list(e)
            bad = [c for c in cols if c not in valid]
            if bad:
                raise ValueError(f"events.jsonl line {n}: unknown column {bad[0]!r}; "
                                 f"allowed: {', '.join(sorted(valid))}")
            con.execute(f"INSERT INTO events ({','.join(cols)}) VALUES ({','.join('?' * len(cols))})",
                        [e[c] for c in cols])
        con.commit()
        tables = [r[0] for r in con.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")]
        counts = {t: con.execute(f'SELECT COUNT(*) FROM "{t}"').fetchone()[0] for t in tables}
    except (sqlite3.Error, TypeError, AttributeError, ValueError) as e:
        con.close()
        tmp.unlink(missing_ok=True)
        raise RuntimeError(f"load failed ({e}); nothing was written") from e
    con.close()
    os.rename(tmp, final)
    return counts


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv
    if len(argv) != 1:
        print("usage: FACTORY_HOME=<tmp> python3 ci/state_load.py <state-dir>", file=sys.stderr)
        return 2
    home, err = check_home(os.environ.get("FACTORY_HOME"), live_homes())
    if err:
        print(f"state_load: {err}", file=sys.stderr)
        return 1
    try:
        counts = load(home, Path(argv[0]))
    except (OSError, RuntimeError, ValueError) as e:
        print(f"state_load: {e}", file=sys.stderr)
        return 1
    print(f"loaded {home / 'db' / 'factory.db'}")
    for t, n in counts.items():
        print(f"{t}\t{n}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
