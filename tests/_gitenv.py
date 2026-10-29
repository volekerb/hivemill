"""Hermetic git environment for tests (T-0637).

A test that runs `git -C <tmp> ...` still obeys GIT_DIR / GIT_WORK_TREE / GIT_COMMON_DIR / GIT_INDEX_FILE inherited from
its caller (a git hook, a worktree shell, the factory), so its `git init` / `git config` can land in the real repo
instead of the temp one. Importing this module refuses (raises GitEnvLeak) when a repo-pointing GIT_* variable is set.
Commit identity (GIT_AUTHOR_*, GIT_COMMITTER_*) passes, as the
repo-config allow-list requires tests to pass it through the environment (see harness.py).

User and system config are left alone by default: some tests point HOME at a temp user config on purpose (test_src).
scrub(environ, isolate_config=True) also sends GIT_CONFIG_GLOBAL / GIT_CONFIG_SYSTEM to os.devnull for a child that
must not read any. Import with `import _gitenv  # noqa: F401`.
"""
import os
import re
import tempfile
from pathlib import Path

KEEP_PREFIXES = ("GIT_AUTHOR_", "GIT_COMMITTER_")
# Harmless non-repo-pointing variables some callers (the factory pane) export; everything else is refused at import.
ALLOWED = frozenset({"GIT_EDITOR", "GIT_SEQUENCE_EDITOR", "GIT_PAGER", "GIT_TERMINAL_PROMPT"})
CONFIG_ISOLATION = frozenset({"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM"})  # harmless in a child env
URL_LIKE = re.compile(r"^[A-Za-z][A-Za-z0-9+.-]*://|^[^/\s]+@[^/\s]+:")  # scheme://... or user@host:path


class GitEnvLeak(RuntimeError):
    pass


class RemoteRefused(RuntimeError):
    pass


def leaked(environ):
    """GIT_* variables that would aim a test's git at a real repo: anything but identity and the harmless allowlist."""
    return sorted(k for k in environ if k.startswith("GIT_") and k not in ALLOWED and not k.startswith(KEEP_PREFIXES))


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
    GIT_DIR means the run is already unsafe (T-0637 incident)."""
    bad = leaked(os.environ)
    if bad:
        raise GitEnvLeak("refusing to run tests with %s set; unset them (env -u ...) and rerun" % ", ".join(bad))


install()
