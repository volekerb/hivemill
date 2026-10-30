"""Hermetic git environment for tests (T-0637).

A test that runs `git -C <tmp> ...` still obeys GIT_DIR / GIT_WORK_TREE / GIT_COMMON_DIR / GIT_INDEX_FILE inherited from
its caller (a git hook, a worktree shell, the factory), so its `git init` / `git config` can land in the real repo
instead of the temp one. Importing this module refuses (raises GitEnvLeak) when a repo-pointing GIT_* variable is set.
Commit identity (GIT_AUTHOR_*, GIT_COMMITTER_*) passes, as the
repo-config allow-list requires tests to pass it through the environment (see harness.py).

Config and credential injection (GIT_CONFIG_*, GIT_ASKPASS) is not a leak: import removes it from os.environ and
points GIT_CONFIG_GLOBAL / GIT_CONFIG_SYSTEM at os.devnull (NOSYSTEM=1). A test that needs a user config sets its own
GIT_CONFIG_GLOBAL in the child env. scrub(environ, isolate_config=True) does the same for a child env. Import with `import _gitenv  # noqa: F401`.
"""
import os
import re
import tempfile
from pathlib import Path

KEEP_PREFIXES = ("GIT_AUTHOR_", "GIT_COMMITTER_")
# Harmless non-repo-pointing variables some callers (the factory pane) export; everything else is refused at import.
ALLOWED = frozenset({"GIT_EDITOR", "GIT_SEQUENCE_EDITOR", "GIT_PAGER", "GIT_TERMINAL_PROMPT"})
CONFIG_ISOLATION = frozenset({"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM"})  # harmless in a child env
ISOLATION_VALUES = {"GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_SYSTEM": os.devnull, "GIT_CONFIG_NOSYSTEM": "1"}
# Config / credential injection a worker pane exports (a real GIT_CONFIG_GLOBAL file, COUNT/KEY_n/VALUE_n, PARAMETERS,
# ASKPASS). They never aim git at a repo, so install() removes them instead of refusing (T-0804).
INJECTION = re.compile(r"^GIT_(CONFIG(_GLOBAL|_SYSTEM|_NOSYSTEM|_COUNT|_PARAMETERS|_KEY_\d+|_VALUE_\d+)?|ASKPASS)$")
URL_LIKE = re.compile(r"^[A-Za-z][A-Za-z0-9+.-]*://|^[^/\s]+@[^/\s]+:")  # scheme://... or user@host:path


class GitEnvLeak(RuntimeError):
    pass


class RemoteRefused(RuntimeError):
    pass


def _is_isolation(key, value):
    """scrub(isolate_config=True)'s own output (config pointed at /dev/null, or NOSYSTEM=1): an unsandboxed gate child of
    the harness inherits it and re-imports this module, so it must not count as a leak (T-0801)."""
    return ISOLATION_VALUES.get(key) == value


def leaked(environ):
    """GIT_* variables that would aim a test's git at a real repo: anything but identity and the harmless allowlist."""
    return sorted(k for k, v in environ.items() if k.startswith("GIT_") and k not in ALLOWED
                  and not k.startswith(KEEP_PREFIXES) and not _is_isolation(k, v))


def assert_local_remote(url, root=None):
    """Refuse a remote that is not a local path under the test tmp dir (network URLs, the real repo, anything else).
    Without `root`, the two standard temp roots (tempfile.gettempdir() and /var/tmp) are accepted."""
    roots = [Path(root).resolve()] if root else [Path(tempfile.gettempdir()).resolve(), Path("/var/tmp").resolve()]
    root = roots[0]
    if URL_LIKE.match(url) and not url.startswith("file://"):
        raise RemoteRefused("test git remote %r is a network URL; use a bare repo under %s" % (url, root))
    path = Path(url[len("file://"):] if url.startswith("file://") else url)
    if not path.is_absolute() or not any(path.resolve() == r or r in path.resolve().parents for r in roots):
        raise RemoteRefused("test git remote %r is not a path under %s; create it in the test's temp dir" % (url, root))


def scrub(environ, isolate_config=False):
    """Return environ without inherited GIT_* state (identity kept); optionally with user/system config disabled."""
    out = {k: v for k, v in environ.items() if not k.startswith("GIT_") or k.startswith(KEEP_PREFIXES)}
    if isolate_config:
        out["GIT_CONFIG_GLOBAL"] = os.devnull
        out["GIT_CONFIG_SYSTEM"] = os.devnull
        out["GIT_CONFIG_NOSYSTEM"] = "1"
    return out


def install():
    """Refuse (raise) when the caller exported repo-pointing GIT_* state, instead of silently scrubbing it: a leaked
    GIT_DIR means the run is already unsafe (T-0637 incident). Config/credential injection vars (INJECTION) are removed
    from os.environ and replaced by the isolation values, so no user, system or pane config reaches a test (T-0804)."""
    bad = [k for k in leaked(os.environ) if not INJECTION.match(k)]
    if bad:
        raise GitEnvLeak("refusing to run tests with %s set; unset them (env -u ...) and rerun" % ", ".join(bad))
    for k in [k for k in os.environ if INJECTION.match(k)]:
        del os.environ[k]
    os.environ.update(ISOLATION_VALUES)


install()
