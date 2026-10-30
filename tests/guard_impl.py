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
import os
import re
import shutil
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


def _run(argv, payload, env, cwd, timeout):
    return subprocess.run(argv, input=payload, capture_output=True, text=True, timeout=timeout, env=env, cwd=cwd)


def hook_run(payload, argv_tail, env=None, cwd=None, timeout=30):
    """Run the hook with `payload` on stdin and `argv_tail` (e.g. ["--role", "coder"]) as its arguments."""
    mode = impl()
    py = [sys.executable, HOOK, *argv_tail]
    if mode == "python":
        return _run(py, payload, env, cwd, timeout)
    go = [go_bin(), *argv_tail, "--py-hook", HOOK]
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
