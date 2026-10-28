#!/usr/bin/env python3
"""CI test-count gate: fail a PR whose unittest count drops below its base.

  test_count_gate.py count <tree>
      print the number of tests discovered in <tree>/tests; exit 2 on load errors
  test_count_gate.py check --base N --pr M --body-file F
      exit 0 if M >= N or the PR body has a `tests-removed: <reason>` line, else 1

Stdlib only. The PR body is untrusted text: it is only ever read from a file.
"""
import argparse
import os
import re
import sys
import unittest

BYPASS = re.compile(r"(?im)^[ \t]*tests-removed:[ \t]*(\S.*)$")


class LoadError(Exception):
    """Test discovery hit import errors (each would count as one test)."""


def _walk(suite):
    for t in suite:
        if isinstance(t, unittest.TestSuite):
            yield from _walk(t)
        else:
            yield t


def count_tests(path):
    """Number of tests discovered in <path>/tests; raises LoadError on load errors."""
    tests_dir = os.path.join(os.path.abspath(path), "tests")
    if not os.path.isdir(tests_dir):
        return 0
    old_cwd, old_path, old_mods = os.getcwd(), list(sys.path), set(sys.modules)
    try:
        os.chdir(os.path.dirname(tests_dir))
        sys.path.insert(0, os.path.dirname(tests_dir))
        sys.path.insert(0, tests_dir)
        loader = unittest.TestLoader()
        suite = loader.discover(start_dir=tests_dir, top_level_dir=tests_dir)
        bad = [t.id() for t in _walk(suite) if isinstance(t, unittest.loader._FailedTest)]
        if loader.errors or bad:
            raise LoadError(
                "test discovery failed in %s: %s" % (tests_dir, ", ".join(bad) or "load errors")
            )
        return suite.countTestCases()
    finally:
        os.chdir(old_cwd)
        sys.path[:] = old_path
        for m in set(sys.modules) - old_mods:
            del sys.modules[m]


def decide(base, pr, body):
    """Return (ok, message) for a base/PR test count and the PR body."""
    head = "base=%d pr=%d" % (base, pr)
    if pr >= base:
        return True, head
    m = BYPASS.search(body or "")
    drop = "test count dropped from %d to %d" % (base, pr)
    if m:
        return True, "%s\n%s; allowed by tests-removed: %s" % (head, drop, m.group(1).strip())
    return False, "%s\n%s; add 'tests-removed: <reason>' to the PR body if intended" % (head, drop)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    c = sub.add_parser("count")
    c.add_argument("tree")
    k = sub.add_parser("check")
    k.add_argument("--base", type=int, required=True)
    k.add_argument("--pr", type=int, required=True)
    k.add_argument("--body-file", required=True)
    a = ap.parse_args(argv)
    if a.cmd == "count":
        try:
            print(count_tests(a.tree))
        except LoadError as e:
            print("error: %s" % e, file=sys.stderr)
            return 2
        return 0
    try:
        with open(a.body_file, encoding="utf-8", errors="replace") as f:
            body = f.read()
    except OSError:
        body = ""
    ok, msg = decide(a.base, a.pr, body)
    print(msg)
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
