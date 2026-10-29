#!/usr/bin/env python3
"""Tiered testing: pick the test modules a diff can affect (T-0519).

  affected.py --base origin/main [--head HEAD] [--list|--run]

Reads `git diff --name-only --no-renames BASE...HEAD` and prints the module names to run, one per line, or the
word FULL with the rule that triggered it. --run executes the selection (all modules for FULL) with the same loader as ci/shard.py
(a selection above tests/_slot.py's full-run size takes a host-wide test slot). Rules, in order:
  1 tests/test_X.py -> X; tests/harness.py, tests/_slot.py, tests/stubs/** -> FULL.
  2 ci/X.py -> modules mentioning "ci/X" or "X.py", plus tests/test_X.py.
  3 bin/factory -> top-level defs whose source changed (ast, base vs head). SCHEMA, DEFAULT_CONFIG, main, db, an
    unnamed top-level statement or a def with >= 15 call sites -> FULL; else modules mentioning a changed def name
    (cmd_judge also "judge") as a whole word.
  4 roles/** -> modules mentioning "roles/" or the role file name; docs/** and *.md -> the smoke set.
  5 smoke set (config key affected_smoke, default below) is always added; over 40% of the modules -> FULL.
Quick mode (T-0642): `--quick` (or `--budget SECONDS`, default 120) is the role-facing form: one process, only the
affected modules, the elapsed time printed. A FULL selection is refused: it prints "FULL needed: rely on CI" and runs nothing
(CI is the full suite). Modules still unrun when the budget is spent are listed and the exit code is 4: rely on CI for them.
Any other changed path: modules mentioning its path, file name or dotted module path, FULL if none does. Stdlib only. The judge's full gate is the backstop for a missed dependency.
"""
import argparse
import ast
import os
import re
import sqlite3
import subprocess
import sys
import time
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TESTS = os.path.join(ROOT, "tests")
DEFAULT_SMOKE = "test_db_layout.py test_inbox.py test_judge.py"
CAP = 0.40
HUB_CALLS = 15
FULL_DEFS = {"SCHEMA", "DEFAULT_CONFIG", "main", "db"}
BIN = "bin/factory"


class AffectedError(Exception):
    """A git or parse failure; the message says what to do next."""


class Full(Exception):
    """The selection is the whole suite; str() is the triggering rule."""


def git(root, *args):
    r = subprocess.run(["git", "-C", root, "-c", "core.fsmonitor=false"] + list(args), capture_output=True, text=True)
    if r.returncode != 0:
        raise AffectedError("git %s failed (exit %d): %s; check that the base ref exists (git fetch origin)"
                            % (" ".join(args), r.returncode, r.stderr.strip()))
    return r.stdout


def changed_files(root, base, head):
    """(path, exists_at_head) per file in `git diff base...head`; renames show as delete + add."""
    out = git(root, "diff", "--name-status", "--no-renames", "%s...%s" % (base, head))
    res = []
    for line in out.splitlines():
        st, _, path = line.partition("\t")
        res.append((path, not st.startswith("D")))
    return res


def all_modules(tests_dir):
    return sorted(f for f in os.listdir(tests_dir) if f.startswith("test_") and f.endswith(".py"))


def smoke_set(modules):
    """config affected_smoke of the factory DB (read-only), else the default; unknown modules are dropped."""
    val = DEFAULT_SMOKE
    db = os.path.join(os.path.expanduser(os.environ.get("FACTORY_HOME") or "~/factory"), "db", "factory.db")
    if os.path.exists(db):
        try:
            con = sqlite3.connect("file:%s?mode=ro" % db, uri=True, timeout=2)
            try:
                row = con.execute("SELECT value FROM config WHERE key='affected_smoke'").fetchone()
            finally:
                con.close()
            if row and str(row[0]).strip():
                val = str(row[0])
        except sqlite3.Error:
            pass  # no config table or no key: the default
    return {m for m in val.split() if m in modules}


def mentions(tests_dir, modules, needle, word=False):
    """Modules whose source contains `needle` (as a whole word if `word`)."""
    pat = re.compile(r"(?<![A-Za-z0-9_])%s(?![A-Za-z0-9_])" % re.escape(needle)) if word else None
    hit = set()
    for m in modules:
        with open(os.path.join(tests_dir, m), encoding="utf-8", errors="replace") as f:
            text = f.read()
        if (pat.search(text) if pat else needle in text):
            hit.add(m)
    return hit


def top_units(src):
    """{unit name: (start, end, source text)} for each top-level statement; unnamed ones are keyed '<stmt@N>'."""
    lines = src.splitlines()
    units = {}
    for node in ast.parse(src).body:
        start = min([node.lineno] + [d.lineno for d in getattr(node, "decorator_list", [])])
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            name = node.name
        elif isinstance(node, ast.Assign) and all(isinstance(t, ast.Name) for t in node.targets):
            name = node.targets[0].id
        elif isinstance(node, ast.AnnAssign) and isinstance(node.target, ast.Name):
            name = node.target.id
        else:
            name = "<stmt@%d>" % node.lineno
        text = "\n".join(lines[start - 1:node.end_lineno])
        key = name
        while key in units:  # a name defined twice: later one gets its own key
            key += "'"
        units[key] = (start, node.end_lineno, text)
    return units


def call_sites(src):
    """{name: number of calls f(...) by bare name} over the whole module."""
    counts = {}
    for n in ast.walk(ast.parse(src)):
        if isinstance(n, ast.Call) and isinstance(n.func, ast.Name):
            counts[n.func.id] = counts.get(n.func.id, 0) + 1
    return counts


def changed_defs(base_src, head_src):
    """Names of top-level units whose source differs (added, removed or edited) between the two versions."""
    a, b = top_units(base_src) if base_src else {}, top_units(head_src) if head_src else {}
    names = set()
    for k in set(a) | set(b):
        if k not in a or k not in b or a[k][2] != b[k][2]:
            names.add(k)
    # unnamed statements are keyed by line number: an edit elsewhere shifts them without changing them
    ua = sorted(a[k][2] for k in a if k.startswith("<stmt@"))
    ub = sorted(b[k][2] for k in b if k.startswith("<stmt@"))
    names = {n for n in names if not n.startswith("<stmt@")}
    if ua != ub:
        names.add("<stmt>")
    return names


def select_bin_factory(root, base, head, tests_dir, modules):
    base_src = git(root, "show", "%s:%s" % (base, BIN)) if _exists(root, base, BIN) else ""
    head_src = git(root, "show", "%s:%s" % (head, BIN)) if _exists(root, head, BIN) else ""
    try:
        names = changed_defs(base_src, head_src)
        calls = call_sites(head_src) if head_src else {}
    except SyntaxError as e:
        raise Full("rule 3: %s does not parse (%s)" % (BIN, e.msg))
    sel = set()
    for n in sorted(names):
        if n == "<stmt>" or n in FULL_DEFS:
            raise Full("rule 3: %s changed (%s)" % (n if n != "<stmt>" else "a top-level statement", BIN))
        if calls.get(n, 0) >= HUB_CALLS:
            raise Full("rule 3: %s has %d call sites (>= %d)" % (n, calls[n], HUB_CALLS))
        sel |= mentions(tests_dir, modules, n, word=True)
        if n.startswith("cmd_") and len(n) > 4:
            sel |= mentions(tests_dir, modules, n[4:], word=True)
    return sel


def _exists(root, rev, path):
    return subprocess.run(["git", "-C", root, "cat-file", "-e", "%s:%s" % (rev, path)], capture_output=True).returncode == 0


def select(root, base, head, tests_dir=None):
    """(modules, rule note) or ('FULL', rule). Raises AffectedError on git failure."""
    tests_dir = tests_dir or os.path.join(root, "tests")
    modules = all_modules(tests_dir)
    try:
        sel, notes = _select(root, base, head, tests_dir, modules)
    except Full as f:
        return "FULL", str(f)
    sel |= smoke_set(modules)
    if len(sel) > CAP * len(modules):
        return "FULL", "rule 5: %d of %d modules selected (> %d%%)" % (len(sel), len(modules), int(CAP * 100))
    return sorted(sel), "; ".join(notes)


def _select(root, base, head, tests_dir, modules):
    files = changed_files(root, base, head)
    sel, notes = set(), []
    for path, exists in files:
        if path in ("tests/harness.py", "tests/_slot.py") or path.startswith("tests/stubs/"):
            raise Full("rule 1: %s changed" % path)
        m = re.fullmatch(r"tests/(test_[^/]+\.py)", path)
        if m:
            if exists and m.group(1) in modules:
                sel.add(m.group(1))
            continue
        c = re.fullmatch(r"ci/([^/]+)\.py", path)
        if c:
            x = c.group(1)
            sel |= mentions(tests_dir, modules, "ci/" + x) | mentions(tests_dir, modules, x + ".py")
            if "test_%s.py" % x in modules:
                sel.add("test_%s.py" % x)
            continue
        if path == BIN:
            sel |= select_bin_factory(root, base, head, tests_dir, modules)
            continue
        if path.startswith("roles/"):
            sel |= mentions(tests_dir, modules, "roles/") | mentions(tests_dir, modules, os.path.basename(path))
            continue
        if path.startswith("docs/") or path.endswith(".md"):
            notes.append("docs only: %s" % path)
            continue
        hit = set()  # any other file: modules that mention its path, its file name or its dotted module path
        stem = path[:-3] if path.endswith(".py") else path
        for needle in {path, os.path.basename(path), stem.replace("/", ".")}:
            hit |= mentions(tests_dir, modules, needle)
        if not hit:
            raise Full("no test module mentions %s; run the full suite" % path)
        sel |= hit
    return sel, notes


QUICK_BUDGET_S = 120
EXIT_BUDGET = 4


def run(modules, tests_dir, budget=None):
    """Run the modules through the shard.py loader; exit 1 on failure or on 0 tests. With a budget (seconds), stop
    starting modules once it is spent, list the rest and exit 4 (the unrun modules are CI's job)."""
    root = os.path.dirname(os.path.abspath(tests_dir))
    os.chdir(root)
    sys.path.insert(0, root)
    sys.path.insert(0, tests_dir)
    loader, runner = unittest.TestLoader(), unittest.TextTestRunner(verbosity=2)
    tests, ok, start, skipped = 0, True, time.monotonic(), []
    for m in modules:
        if budget is not None and time.monotonic() - start >= budget:
            skipped.append(m)
            continue
        t0 = time.monotonic()
        res = runner.run(loader.discover(tests_dir, pattern=m, top_level_dir=tests_dir))
        print("%s: %d tests, %.1f s" % (m, res.testsRun, time.monotonic() - t0), file=sys.stderr)
        tests += res.testsRun
        ok = ok and res.wasSuccessful()
    ran = len(modules) - len(skipped)
    print("affected: Ran %d tests in %d modules: %s" % (tests, ran, "OK" if ok else "FAILED"))
    if budget is not None:
        print("affected: elapsed %.1f s of %d s budget" % (time.monotonic() - start, budget))
    if tests == 0:
        print("affected: ran 0 tests; that is a failure", file=sys.stderr)
        return 1
    if ok and skipped:
        print("affected: budget spent, not run: %s; rely on CI for them" % " ".join(skipped), file=sys.stderr)
        return EXIT_BUDGET
    return 0 if ok else 1


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--base", required=True)
    ap.add_argument("--head", default="HEAD")
    g = ap.add_mutually_exclusive_group()
    g.add_argument("--list", action="store_true", help="print the selection (default)")
    g.add_argument("--run", action="store_true", help="run the selection")
    ap.add_argument("--quick", action="store_true", help="role mode: affected modules only, one process, never FULL (rely on CI)")
    ap.add_argument("--budget", type=int, metavar="SECONDS", help="--quick with this time budget (default %d)" % QUICK_BUDGET_S)
    ap.add_argument("--root", default=ROOT, help=argparse.SUPPRESS)
    a = ap.parse_args(argv)
    if a.budget is not None and a.budget < 1:
        ap.error("--budget must be at least 1 second")
    quick = a.quick or a.budget is not None
    budget = (a.budget or QUICK_BUDGET_S) if quick else None
    try:
        mods, note = select(a.root, a.base, a.head)
    except AffectedError as e:
        print("affected: %s" % e, file=sys.stderr)
        return 2
    if mods == "FULL":
        if quick:
            print("FULL needed: rely on CI (%s)" % note)
            return 0
        print("FULL (%s)" % note)
        if a.run:
            return run(all_modules(os.path.join(a.root, "tests")), os.path.join(a.root, "tests"))
        return 0
    print("\n".join(mods))
    if a.run:
        return run(mods, os.path.join(a.root, "tests"), budget)
    return 0


if __name__ == "__main__":
    sys.exit(main())
