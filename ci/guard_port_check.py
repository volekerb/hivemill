#!/usr/bin/env python3
"""Guard port case-map check: prove no guard case is lost when old tests move.

  guard_port_check.py --map MAP --old PATH [--old PATH ...] [--base REF]
      exit 0 when MAP covers exactly the old test names and every new name is
      a test added to go/internal/guard at HEAD; else print each failure, exit 1
  guard_port_check.py --self-test
      run the checks against a temp git repo; exit 0 when all behave

MAP lines are `OldTestName -> NewTestName` or
`OldTestName -> duplicate-of ExistingTestName`; blank lines and lines starting
with # are skipped. Test files are read from git, never the working tree: at
the merge base of REF and HEAD, and at HEAD. The map is read from disk.
TestMain is not a case. See docs/guard-port.md. Stdlib only.
"""
import argparse
import collections
import os
import re
import shutil
import subprocess
import sys
import tempfile

GUARD_DIR = "go/internal/guard"
# Go runs TestXxx where Xxx does not start with a lowercase letter; TestMain is setup.
TEST_FUNC = re.compile(r"^func\s+(Test(?![a-z])\w*)\s*\(", re.M)
NAME = re.compile(r"^Test\w*$")
DUP = "duplicate-of"


class GitError(Exception):
    pass


def git(cwd, *args):
    env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
    p = subprocess.run(["git", *args], cwd=cwd, env=env,
                       capture_output=True, text=True)
    if p.returncode != 0:
        raise GitError("git %s: %s" % (" ".join(args), p.stderr.strip()))
    return p.stdout


def test_names(cwd, ref, path):
    names = TEST_FUNC.findall(git(cwd, "show", "%s:%s" % (ref, path)))
    return [n for n in names if n != "TestMain"]


def guard_tests(cwd, ref):
    """Every `func Test...` name in go/internal/guard/*_test.go at ref (a list)."""
    out = git(cwd, "ls-tree", "-r", "-z", "--name-only", ref, "--", GUARD_DIR + "/")
    names = []
    for path in filter(None, out.split("\0")):
        if os.path.dirname(path) == GUARD_DIR and path.endswith("_test.go"):
            names.extend(test_names(cwd, ref, path))
    return names


def parse_map(text):
    """Return (entries, failures); entry = (lineno, old, new, is_dup)."""
    entries, failures = [], []
    for n, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        left, sep, right = line.partition("->")
        old, rhs = left.strip(), right.split()
        is_dup = len(rhs) == 2 and rhs[0] == DUP
        new = rhs[-1] if rhs else ""
        if (not sep or not NAME.match(old) or not NAME.match(new)
                or not (len(rhs) == 1 or is_dup)):
            failures.append("map line %d: malformed: %r" % (n, raw))
            continue
        entries.append((n, old, new, is_dup))
    return entries, failures


def check(cwd, map_text, olds, base, head="HEAD"):
    """Return the list of failure strings; empty means the map is good."""
    entries, failures = parse_map(map_text)
    try:
        base = git(cwd, "merge-base", base, head).strip()
        old_names = collections.Counter()
        for path in dict.fromkeys(olds):
            found = test_names(cwd, base, path)
            if not found:
                failures.append("a: old file %s has no test at %s" % (path, base))
            old_names.update(found)
        ref_guard = guard_tests(cwd, base)
        head_guard = guard_tests(cwd, head)
    except GitError as e:
        return failures + ["git: %s" % e]

    # a. old names in MAP are exactly the old test names
    mapped = collections.Counter(old for _, old, _, _ in entries)
    for name in sorted(set(old_names) | set(mapped)):
        want, got = old_names[name], mapped[name]
        if got == 0:
            failures.append("a: old test %s is missing from the map" % name)
        elif want == 0:
            failures.append("a: map names %s, which is not a test in the old files at %s"
                            % (name, base))
        elif got > want:
            failures.append("a: old test %s is in the map %d times" % (name, got))
        elif got < want:
            failures.append("a: old test %s is defined %d times but mapped %d times"
                            % (name, want, got))

    # b. each new name is added to go/internal/guard at HEAD, once
    ref_set, head_set = set(ref_guard), set(head_guard)
    news = collections.Counter(new for _, _, new, dup in entries if not dup)
    for n, old, new, dup in entries:
        if dup:
            continue
        if new not in head_set:
            failures.append("b: line %d: %s is not a test in %s at %s"
                            % (n, new, GUARD_DIR, head))
        elif new in ref_set:
            failures.append("b: line %d: %s already exists in %s at %s"
                            % (n, new, GUARD_DIR, base))
    for new, c in sorted(news.items()):
        if c > 1:
            failures.append("b: new test %s is in the map %d times" % (new, c))

    # c. each duplicate-of target exists at REF
    for n, old, new, dup in entries:
        if dup and new not in ref_set:
            failures.append("c: line %d: %s is not a test in %s at %s"
                            % (n, new, GUARD_DIR, base))
        elif dup and new not in head_set:
            failures.append("c: line %d: %s is gone from %s at %s"
                            % (n, new, GUARD_DIR, head))

    # d. guard test count grows by exactly the non-duplicate lines
    delta = len(head_guard) - len(ref_guard)
    nondup = sum(1 for e in entries if not e[3])
    if delta != nondup:
        failures.append("d: %s gained %d tests (%d at %s, %d at %s) but the map has "
                        "%d non-duplicate lines" % (GUARD_DIR, delta, len(ref_guard),
                                                    base, len(head_guard), head, nondup))
    # d, by name: no base guard test removed, no added test left unmapped
    for name in sorted(ref_set - head_set):
        failures.append("d: %s is in %s at %s but gone at %s"
                        % (name, GUARD_DIR, base, head))
    for name in sorted(head_set - ref_set - set(news)):
        failures.append("d: %s is added to %s at %s but not in the map"
                        % (name, GUARD_DIR, head))
    return failures


def repo_path(top, path):
    rel = os.path.relpath(os.path.realpath(path), os.path.realpath(top))
    return rel.replace(os.sep, "/")


# ---------------------------------------------------------------- self-test

def _write(root, rel, body):
    full = os.path.join(root, rel)
    os.makedirs(os.path.dirname(full), exist_ok=True)
    with open(full, "w") as f:
        f.write(body)


def _go(*names):
    return "package x\n\n" + "".join(
        "func %s(t *testing.T) {\n}\n\n" % n for n in names)


def _commit(root, msg):
    git(root, "add", "-A")
    git(root, "-c", "user.name=self-test", "-c", "user.email=self-test@example.invalid",
        "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
        "commit", "-q", "-m", msg)


def self_test():
    tmp = tempfile.mkdtemp(prefix="guard-port-check-")
    old = "go/internal/factorytest/guard1/old_test.go"
    try:
        git(tmp, "init", "-q")
        _write(tmp, GUARD_DIR + "/a_test.go", _go("TestExisting"))
        _write(tmp, GUARD_DIR + "/sub/b_test.go", _go("TestSubdirIgnored"))
        _write(tmp, old, _go("TestFoo", "TestBar", "TestMain", "Testhelper"))
        _write(tmp, "go/internal/factorytest/guard1/helpers_test.go", _go("TestMain"))
        _commit(tmp, "base")
        git(tmp, "-c", "tag.gpgSign=false", "tag", "base")
        _write(tmp, GUARD_DIR + "/g1_test.go", _go("TestG1Foo"))
        _commit(tmp, "port")
        git(tmp, "-c", "tag.gpgSign=false", "tag", "port")
        _write(tmp, GUARD_DIR + "/extra_test.go", _go("TestG1Extra"))
        _commit(tmp, "extra")
        git(tmp, "-c", "tag.gpgSign=false", "tag", "extra")
        git(tmp, "checkout", "-q", "-b", "rename", "port")
        _write(tmp, GUARD_DIR + "/a_test.go", _go("TestExisting2"))
        _commit(tmp, "rename")
        git(tmp, "-c", "tag.gpgSign=false", "tag", "renamed")
        git(tmp, "checkout", "-q", "-b", "swapb", "port")
        os.remove(os.path.join(tmp, GUARD_DIR, "sub", "b_test.go"))
        _write(tmp, GUARD_DIR + "/a_test.go", _go("TestExisting", "TestOther"))
        _commit(tmp, "add other")
        git(tmp, "-c", "tag.gpgSign=false", "tag", "withother")
        _write(tmp, GUARD_DIR + "/a_test.go", _go("TestExisting", "TestJunk"))
        _commit(tmp, "swap")
        git(tmp, "-c", "tag.gpgSign=false", "tag", "swap")
        git(tmp, "checkout", "-q", "-b", "mainmoved", "base")
        _write(tmp, GUARD_DIR + "/other_test.go", _go("TestG2Other"))
        _commit(tmp, "other batch")
        git(tmp, "-c", "tag.gpgSign=false", "tag", "mainmoved")

        good = "# batch\n\nTestFoo -> TestG1Foo\nTestBar -> duplicate-of TestExisting\n"
        cases = [
            ("pass", good, "port", None),
            ("a fail: old test missing", "TestFoo -> TestG1Foo\n", "port", "a:"),
            ("a pass: all old tests mapped once", good, "port", "!a:"),
            ("a fail: old file with no test",
             "", "port", "a: old file go/internal/factorytest/guard1/helpers_test.go"),
            ("a fail: old test twice", good + "TestFoo -> duplicate-of TestExisting\n",
             "port", "a:"),
            ("b pass: new test added at HEAD", good, "port", "!b:"),
            ("b fail: new test already at REF",
             "TestFoo -> TestExisting\nTestBar -> duplicate-of TestExisting\n", "port", "b:"),
            ("b fail: new test not at HEAD",
             "TestFoo -> TestG1Nope\nTestBar -> duplicate-of TestExisting\n", "port", "b:"),
            ("b fail: new test mapped twice",
             "TestFoo -> TestG1Foo\nTestBar -> TestG1Foo\n", "port", "b:"),
            ("c pass: duplicate target at REF", good, "port", "!c:"),
            ("c fail: duplicate target gone at HEAD", good, "renamed", "c:"),
            ("c fail: duplicate target missing",
             "TestFoo -> TestG1Foo\nTestBar -> duplicate-of TestNope\n", "port", "c:"),
            ("d pass: count matches", good, "port", "!d:"),
            ("d fail: unmapped extra test at HEAD", good, "extra", "d:"),
            ("d fail: base test deleted, offset by an unmapped one", good, "swap", "d: TestOther is"),
            ("pass: base moved on after the branch point", good, "port", None),
            ("malformed line", good + "TestFoo => TestG1Foo\n", "port", "map line"),
        ]
        bad = 0
        for label, text, head, expect in cases:
            ref = ("mainmoved" if "base moved" in label
                   else "withother" if head == "swap" else "base")
            olds = [old, old]
            if "no test" in label:
                olds = ["go/internal/factorytest/guard1/helpers_test.go"]
            fails = check(tmp, text, olds, ref, head)
            joined = "\n".join(fails)
            if expect is None:
                ok = not fails
            elif expect.startswith("!"):
                ok = expect[1:] not in joined
            else:
                ok = any(f.startswith(expect) for f in fails)
            print("%s  %s" % ("ok  " if ok else "FAIL", label))
            if not ok:
                bad += 1
                for f in fails:
                    print("      " + f)
        print("self-test: %d cases, %d failed" % (len(cases), bad))
        return 1 if bad else 0
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--map", help="map file: `Old -> New` or `Old -> duplicate-of Existing`")
    ap.add_argument("--old", action="append", default=[], help="old test file (repeatable)")
    ap.add_argument("--base", default="origin/main", help="base ref (default origin/main)")
    ap.add_argument("--self-test", action="store_true", help="run the built-in self-test")
    args = ap.parse_args(argv)
    if args.self_test:
        return self_test()
    if not args.map or not args.old:
        ap.error("--map and at least one --old are required")
    try:
        top = git(os.getcwd(), "rev-parse", "--show-toplevel").strip()
    except GitError as e:
        print("guard_port_check: %s" % e, file=sys.stderr)
        return 1
    try:
        with open(args.map) as f:
            text = f.read()
    except OSError as e:
        print("guard_port_check: cannot read map: %s" % e, file=sys.stderr)
        return 1
    olds = [repo_path(top, p) for p in args.old]
    failures = check(top, text, olds, args.base)
    for f in failures:
        print(f)
    if failures:
        print("guard_port_check: %d failure(s)" % len(failures))
        return 1
    print("guard_port_check: ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
