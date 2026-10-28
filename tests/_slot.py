"""Host-wide test slots (T-0284): a full-suite run takes one of N flock slots under ~/.cache/factory-test-slots/, so
a handful of worktrees cannot saturate the CPU at once. Targeted runs (few modules) and CI take none. The local merge
gate (`factory gate run`) takes one with acquire(require=True) and sets FACTORY_TEST_SLOT_HELD for its child.
`factory res` counts the held slots with the same files (bin/factory test_slot_usage)."""
import fcntl
import os
import sqlite3
import sys
import time
from pathlib import Path

FULL_RUN_MODULES = 50  # more test modules than this loaded in one process is a full-suite run
DEFAULT_SLOTS = 2
WAIT_MAX_S = 30 * 60
WAIT_NOTICE_S = 30

_held = []  # open slot files; the flock is released when the process exits


def slot_dir():
    return Path(os.path.expanduser("~/.cache/factory-test-slots"))


def slot_count():
    """$FACTORY_TEST_SLOTS, else config test_slots of the factory DB (read-only), else 2."""
    env = os.environ.get("FACTORY_TEST_SLOTS")
    if env and env.isdigit() and int(env) > 0:
        return int(env)
    db = Path(os.environ.get("FACTORY_HOME") or "~/factory").expanduser() / "db" / "factory.db"
    try:
        con = sqlite3.connect(f"file:{db}?mode=ro", uri=True, timeout=2)
        try:
            row = con.execute("SELECT value FROM config WHERE key='test_slots'").fetchone()
        finally:
            con.close()
        if row and str(row[0]).strip().isdigit() and int(row[0]) > 0:
            return int(row[0])
    except sqlite3.Error:
        pass  # no DB or no such key: the default
    return DEFAULT_SLOTS


def try_take(directory, n):
    """Lock the first free slot file without blocking; returns its number or None."""
    directory.mkdir(parents=True, exist_ok=True)
    for i in range(n):
        f = open(directory / f"slot-{i}.lock", "a+")
        try:
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            f.close()
            continue
        f.seek(0)
        f.truncate()
        f.write(f"{os.getpid()}\n")
        f.flush()
        _held.append(f)
        return i
    return None


def acquire(directory=None, n=None, wait_max=WAIT_MAX_S, notice=WAIT_NOTICE_S, sleep=time.sleep, clock=time.monotonic,
            out=sys.stderr, require=False):
    """Take a slot, waiting up to wait_max seconds (a notice every `notice` s). Past that run anyway with a warning.
    Returns the slot number, or None when it ran without one. require=True (the local merge gate, T-0515) never runs
    slot-less: past wait_max it prints why and returns None, and the caller must treat None as "no slot, do not run"."""
    directory = directory or slot_dir()
    n = n or slot_count()
    start = last = clock()
    first = True
    while True:
        i = try_take(directory, n)
        if i is not None:
            return i
        now = clock()
        if now - start >= wait_max:
            msg = "not running without one" if require else "running without one"
            print(f"{'ERROR' if require else 'WARNING'}: no test slot free after {int(wait_max)} s; {msg}", file=out, flush=True)
            return None
        if first or now - last >= notice:
            print(f"waiting for a test slot (all {n} busy, see `factory res`; the full suite is CI's job)", file=out, flush=True)
            first, last = False, now
        sleep(min(1, notice))


def release():
    while _held:
        _held.pop().close()


_modules = set()
_taken = False


def note_module(name, env=os.environ, **kw):
    """Called for each test module that defines a FactoryTestCase. The module that makes it a full run takes the slot."""
    global _taken
    _modules.add(name)
    # FACTORY_TEST_SLOT_HELD: a parent (`factory gate run`) already holds a slot for this whole process tree
    if _taken or len(_modules) <= FULL_RUN_MODULES or env.get("CI", "").lower() == "true" or env.get("FACTORY_TEST_SLOT_HELD"):
        return
    _taken = True
    acquire(**kw)
