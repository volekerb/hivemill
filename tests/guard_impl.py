"""Shared runner for the worker-guard tests (G-016 T-0352): one place decides which implementation of the hook runs.

GUARD_IMPL unset or "python": roles/hooks/worker-guard.py, exactly as before.
GUARD_IMPL=go: the Go binary (GUARD_GO_BIN, else built once per process from go/cmd/worker-guard). A build failure
raises, so a Go run never silently skips.
GUARD_IMPL=diff: both run; differing exit codes or stderr raise AssertionError naming the command; the Python result is
returned. Stderr must be identical except that Go's internal-error lines start "worker-guard(go):" where Python's start
"worker-guard:"; nothing else is normalized.

Not a test module (no test_ prefix). tests/test_guard_impl_coverage.py fails any module that runs the hook without it.
"""
import atexit
import datetime as dt
import json
import os
import re
import shutil
import sqlite3
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
_go_bin = []


def impl():
    mode = os.environ.get("GUARD_IMPL", "") or "python"
    if mode not in ("python", "go", "diff"):
        raise ValueError(f"GUARD_IMPL={mode!r} is not python, go or diff; fix the environment")
    return mode


def go_bin():
    """Path of the Go hook binary: GUARD_GO_BIN, else built once per process. Raises when it cannot be had."""
    if os.environ.get("GUARD_GO_BIN"):
        path = os.environ["GUARD_GO_BIN"]
        if not (os.path.isfile(path) and os.access(path, os.X_OK)):
            raise RuntimeError(f"GUARD_GO_BIN={path} is not an executable file; point it at a built worker-guard")
        return path
    if not _go_bin:
        go = shutil.which("go") or os.path.expanduser("~/.local/go/bin/go")
        if not os.access(go, os.X_OK):
            raise RuntimeError("go not found on PATH; install Go or set GUARD_GO_BIN (a Go guard run never skips)")
        tmp = tempfile.mkdtemp(prefix="guard-impl-go-")
        atexit.register(shutil.rmtree, tmp, True)
        out = os.path.join(tmp, "worker-guard")
        env = dict(os.environ, GOTOOLCHAIN="local", GOFLAGS="-mod=readonly", GOPROXY="off")
        p = subprocess.run([go, "build", "-o", out, "./cmd/worker-guard"], cwd=os.path.join(ROOT, "go"),
                           capture_output=True, text=True, timeout=600, env=env)
        if p.returncode != 0:
            raise RuntimeError(f"go build ./go/cmd/worker-guard failed: {p.stderr.strip()}; fix the Go build first")
        _go_bin.append(out)
    return _go_bin[0]


def normalize_go_stderr(text):
    """Go's internal-error prefix "worker-guard(go)" -> Python's "worker-guard"; the only allowed stderr difference."""
    return re.sub(r"(?m)^worker-guard\(go\)", "worker-guard", text)


# G-016 T-0977: the Go guard fails closed when <FACTORY_HOME>/run/guard-export.json is missing but the DB exists. Test
# fixtures have a temp FACTORY_HOME with a DB and no export, so under go/diff hook_run seeds one (a small copy of the
# CLI's write_guard_export; tests/test_guard_impl.py pins the constants to the real source).
EXPORT_VERSION = 1
EXPORT_DEFAULTS = {"public_text_repos": "", "frozen_repos": "", "scrub_terms_file": "", "test_sandbox": "0"}
_seeded = {}  # export path -> bytes the seam last wrote there; any other content belongs to the test


def _under_tmp(home):
    root = os.path.realpath(tempfile.gettempdir()).rstrip(os.sep)
    return os.path.realpath(home).startswith(root + os.sep)


def _db_path(home):
    """Go dbPath(): HOME/db/factory.db, else the legacy HOME/factory.db when only that exists."""
    new, legacy = os.path.join(home, "db", "factory.db"), os.path.join(home, "factory.db")
    return new if os.path.exists(new) or not os.path.exists(legacy) else legacy


def _rows(con, sql):
    try:
        return con.execute(sql).fetchall()
    except sqlite3.OperationalError as e:
        if "no such table" in str(e) or "no such column" in str(e):  # partial fixture schema: empty
            return []
        raise


def _build_export(db):
    uri = "file:" + db.replace("%", "%25").replace("?", "%3f").replace("#", "%23") + "?mode=ro"
    con = sqlite3.connect(uri, uri=True)
    try:
        config = dict(EXPORT_DEFAULTS)
        for k, v in _rows(con, "SELECT key, value FROM config"):
            if k in config:
                config[k] = v
        doc = {
            "version": EXPORT_VERSION,
            "generated_at": dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds"),
            "config": config,
            "agent_goals": {n: g for n, g in _rows(con, "SELECT name, goal_id FROM agents") if g},
            "task_worktrees": {i: w for i, w in _rows(con, "SELECT id, worktree FROM tasks") if w},
        }
    finally:
        con.close()
    return json.dumps(doc, sort_keys=True).encode()


def seed_guard_export(env=None):
    """Write <FACTORY_HOME>/run/guard-export.json for the Go guard; no-op unless GUARD_IMPL is go or diff, the home is
    set and under the temp dir, and its DB exists. An unreadable DB seeds nothing (Go's fail-closed shows as a diff)."""
    if impl() == "python":
        return
    home = (env if env is not None else os.environ).get("FACTORY_HOME", "")
    if not home or not _under_tmp(home):
        return
    db = _db_path(home)
    if not os.path.isfile(db):
        return
    path = os.path.join(home, "run", "guard-export.json")
    try:
        with open(path, "rb") as fh:
            current = fh.read()
    except FileNotFoundError:
        current = None
    except OSError:
        return
    if current is not None and _seeded.get(path) != current:
        return  # the test's own export (or one the seam never wrote): leave it
    try:
        data = _build_export(db)
    except (sqlite3.Error, OSError):
        if current is not None:
            try:
                os.unlink(path)  # our stale file must not mask the unreadable DB
            except OSError:
                pass
            _seeded.pop(path, None)
        return
    os.makedirs(os.path.dirname(path), exist_ok=True)
    tmp = f"{path}.tmp{os.getpid()}"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "wb") as fh:
        fh.write(data)
    os.chmod(tmp, 0o600)
    os.replace(tmp, path)
    _seeded[path] = data


def _run(argv, payload, env, cwd, timeout):
    return subprocess.run(argv, input=payload, capture_output=True, text=True, timeout=timeout, env=env, cwd=cwd)


def hook_run(payload, argv_tail, env=None, cwd=None, timeout=30):
    """Run the hook with `payload` on stdin and `argv_tail` (e.g. ["--role", "coder"]) as its arguments."""
    mode = impl()
    py = [sys.executable, HOOK, *argv_tail]
    if mode == "python":
        return _run(py, payload, env, cwd, timeout)
    go = [go_bin(), *argv_tail, "--py-hook", HOOK]
    seed_guard_export(env)
    if mode == "go":
        return _run(go, payload, env, cwd, timeout)
    a = _run(py, payload, env, cwd, timeout)
    b = _run(go, payload, env, cwd, timeout)
    what = f"{' '.join(argv_tail)} payload={payload!r}"
    if a.returncode != b.returncode:
        raise AssertionError(f"guard diff: exit python={a.returncode} go={b.returncode} for {what}")
    if a.stderr != normalize_go_stderr(b.stderr):
        raise AssertionError(f"guard diff: stderr differs for {what}\n  python: {a.stderr!r}\n  go:     {b.stderr!r}")
    return a
