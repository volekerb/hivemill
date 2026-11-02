#!/usr/bin/env python3
"""GUARD_IMPL=diff unittest selection for CI (T-0861). Lists and runs guard hook test modules.

CI runs the module set named in docs/guard-diff.md (GUARD_IMPL=diff example), not every
test_*.py that imports hook_run — the wider set has known Python/Go stderr drift on main.
Stdlib only except importing tests/test_guard_impl_coverage.py to verify hook_run usage.
"""
import argparse
import importlib.util
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TESTS = os.path.join(ROOT, "tests")
COVERAGE = os.path.join(TESTS, "test_guard_impl_coverage.py")
# docs/guard-diff.md — GUARD_IMPL=diff python3 -m unittest …
DOC_DIFF_BASENAMES = (
    "test_worker_guard.py",
    "test_priv_guard.py",
    "test_guard_diff.py",
)


def _load_coverage():
    spec = importlib.util.spec_from_file_location("test_guard_impl_coverage", COVERAGE)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def diff_module_names():
    """Basenames test_*.py to run under GUARD_IMPL=diff (docs/guard-diff.md set)."""
    cov = _load_coverage()
    mods = cov.modules()
    names = list(DOC_DIFF_BASENAMES)
    missing = [n for n in names if n[:-3] not in mods]
    if missing:
        raise RuntimeError("guard_impl_unittest: missing test modules %s" % missing)
    for base in names:
        m = base[:-3]
        if not cov.uses_helper(mods[m]):
            raise RuntimeError("guard_impl_unittest: %s must import hook_run from guard_impl" % base)
        if cov.python_only(mods[m]):
            raise RuntimeError("guard_impl_unittest: %s is GUARD_IMPL_PYTHON_ONLY" % base)
    return names


def unittest_modules():
    """Import paths for `python -m unittest`, e.g. tests.test_worker_guard."""
    return ["tests." + n[:-3] for n in diff_module_names()]


def run_guard_diff_unittest(extra_env=None):
    env = dict(os.environ, GUARD_IMPL="diff", GOTOOLCHAIN="local", GOFLAGS="-mod=readonly", GOPROXY="off")
    if extra_env:
        env.update(extra_env)
    cmd = [sys.executable, "-m", "unittest", *unittest_modules()]
    return subprocess.run(cmd, cwd=ROOT, env=env)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("list", help="module basenames, one per line")
    sub.add_parser("modules", help="unittest module paths, one per line")
    run = sub.add_parser("run", help="run GUARD_IMPL=diff over the module set; exit code from unittest")
    a = ap.parse_args(argv)
    if a.cmd == "list":
        print("\n".join(diff_module_names()))
        return 0
    if a.cmd == "modules":
        print("\n".join(unittest_modules()))
        return 0
    return run_guard_diff_unittest().returncode


if __name__ == "__main__":
    sys.exit(main())
