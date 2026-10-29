"""Hermetic git environment for tests (T-0637).

A test that runs `git -C <tmp> ...` still obeys GIT_DIR / GIT_WORK_TREE / GIT_COMMON_DIR / GIT_INDEX_FILE inherited from
its caller (a git hook, a worktree shell, the factory), so its `git init` / `git config` can land in the real repo
instead of the temp one. Importing this module scrubs the process environment once, so every child git inherits it:
every GIT_* variable is dropped except the commit identity (GIT_AUTHOR_*, GIT_COMMITTER_*), which the repo-config
allow-list requires tests to pass through the environment (see harness.py).

User and system config are left alone by default: some tests point HOME at a temp user config on purpose (test_src).
scrub(environ, isolate_config=True) also sends GIT_CONFIG_GLOBAL / GIT_CONFIG_SYSTEM to os.devnull for a child that
must not read any. Import with `import _gitenv  # noqa: F401`.
"""
import os

KEEP_PREFIXES = ("GIT_AUTHOR_", "GIT_COMMITTER_")


def scrub(environ, isolate_config=False):
    """Return environ without inherited GIT_* state (identity kept); optionally with user/system config disabled."""
    out = {k: v for k, v in environ.items() if not k.startswith("GIT_") or k.startswith(KEEP_PREFIXES)}
    if isolate_config:
        out["GIT_CONFIG_GLOBAL"] = os.devnull
        out["GIT_CONFIG_SYSTEM"] = os.devnull
        out["GIT_CONFIG_NOSYSTEM"] = "1"
    return out


def install():
    clean = scrub(os.environ)
    for k in [k for k in os.environ if k not in clean]:
        del os.environ[k]


install()
