"""Read-only access to factory.db for the web UI.

Never import bin/factory from here: its db() migrates (writes). One short-lived
connection per request; nothing is cached across requests.
"""
import contextlib
import os
import sqlite3
from urllib.parse import quote


def connect(db_path):
    """Open db_path read-only (URI mode=ro, query_only). Missing file -> FileNotFoundError, nothing created."""
    path = os.path.abspath(str(db_path))
    if not os.path.isfile(path):
        raise FileNotFoundError(f"factory database not found: {path}")
    con = sqlite3.connect(f"file:{quote(path)}?mode=ro", uri=True, timeout=2, isolation_level=None)
    con.row_factory = sqlite3.Row
    con.execute("PRAGMA query_only=ON")
    con.execute("PRAGMA busy_timeout=2000")
    return con


@contextlib.contextmanager
def reader(db_path):
    """Open, yield, always close. Callers open one per request."""
    con = connect(db_path)
    try:
        yield con
    finally:
        con.close()


def has_column(con, table, col):
    """True if `table` has column `col` (tolerates older DBs)."""
    return any(r[1] == col for r in con.execute(f"PRAGMA table_info({table})"))
