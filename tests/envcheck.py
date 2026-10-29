"""Environment detection for tests that cannot run from a source export (no .git, tree on RAM tmpfs).

A skip fires only when the condition is detected, so in a real git worktree on disk every guarded test still runs.
Use the decorators on single tests, never on a module or class.
"""
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
# Same list the demo scripts refuse (ci/demo_record.py RAM_DIRS); kept here so the tests need no import of ci/.
RAM_DIRS = (Path("/tmp"), Path("/dev/shm"), Path("/var/tmp"))

NO_GIT_REASON = "no .git: source export, not a git worktree"
RAM_REASON = "work dir on RAM tmpfs: demo scripts refuse paths under /tmp, /dev/shm, /var/tmp"


def has_git(root=REPO):
    """True when root holds a .git entry (a directory in a clone, a file in a linked worktree)."""
    return (Path(root) / ".git").exists()


def on_ram(path=REPO, ram_dirs=RAM_DIRS):
    """True when path resolves to or under one of ram_dirs (the demo scripts' refusal rule)."""
    p = Path(path).resolve()
    return any(p == r.resolve() or r.resolve() in p.parents for r in ram_dirs)


def skip_without_git(root=REPO):
    return unittest.skipIf(not has_git(root), NO_GIT_REASON)


def skip_on_ram(path=REPO / "docs" / "demo" / "out", ram_dirs=RAM_DIRS):
    return unittest.skipIf(on_ram(path, ram_dirs), RAM_REASON)
