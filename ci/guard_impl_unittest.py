#!/usr/bin/env python3
"""GUARD_IMPL=diff unittest selection for CI (T-0861). Lists and runs guard hook test modules.

Every module that exercises the hook through tests/guard_impl.hook_run and is not marked
GUARD_IMPL_PYTHON_ONLY is included (see docs/guard-diff.md). Stdlib only except importing
tests/test_guard_impl_coverage.py for the authoritative module set.
"""
import argparse
import ast
import importlib.util
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TESTS = os.path.join(ROOT, "tests")
COVERAGE = os.path.join(TESTS, "test_guard_impl_coverage.py")
SPELLING = re.compile(r"worker-guard|worker_guard\.py")


def _load_coverage():
    spec = importlib.util.spec_from_file_location("test_guard_impl_coverage", COVERAGE)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def diff_module_names():
    """Basenames test_*.py to run under GUARD_IMPL=diff."""
    cov = _load_coverage()
    mods = cov.modules()
    names = []
    for m in sorted(cov.guard_modules(mods)):
        if m in cov.ALLOWLIST or cov.python_only(mods[m]):
            continue
        if not cov.uses_helper(mods[m]):
            continue
        names.append(m + ".py")
    required = {"test_worker_guard.py", "test_priv_guard.py", "test_guard_diff.py"}
    missing = required - set(names)
    if missing:
        raise RuntimeError("guard_impl_unittest: missing required modules %s" % sorted(missing))
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
