#!/usr/bin/env python3
"""GUARD_IMPL=diff unittest selection for CI (T-0861). Lists and runs guard hook test modules.

CI runs the module set named in docs/guard-diff.md (GUARD_IMPL=diff example), not every
test_*.py that imports hook_run — the wider set has known Python/Go stderr drift on main.
Stdlib only except importing tests/test_guard_impl_coverage.py to verify hook_run usage.
"""
import argparse
import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from concurrent.futures import ThreadPoolExecutor

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


def routed_rest_names():
    """Basenames of the other modules that route the hook through guard_impl (hook_run), minus the diff set.

    Derived from the sources at call time, so a new routed module joins the guard-equiv job without a YAML edit.
    """
    cov = _load_coverage()
    skip = set(DOC_DIFF_BASENAMES)
    return [m + ".py" for m, src in sorted(cov.modules().items())
            if m + ".py" not in skip and m != cov.SELF and cov.uses_helper(src) and not cov.python_only(src)]


def module_names(routed_rest=False):
    return routed_rest_names() if routed_rest else diff_module_names()


def unittest_modules(routed_rest=False):
    """Import paths for `python -m unittest`, e.g. tests.test_worker_guard."""
    return ["tests." + n[:-3] for n in module_names(routed_rest)]


def run_guard_diff_unittest(extra_env=None, routed_rest=False):
    env = dict(os.environ, GUARD_IMPL="diff", GOTOOLCHAIN="local", GOFLAGS="-mod=readonly", GOPROXY="off")
    if extra_env:
        env.update(extra_env)
    # discover -s tests puts tests/ on sys.path so `from harness import …` matches the shard suite.
    last = subprocess.CompletedProcess(args=[], returncode=0)
    for base in module_names(routed_rest):
        cmd = [sys.executable, "-m", "unittest", "discover", "-s", "tests", "-p", base]
        last = subprocess.run(cmd, cwd=ROOT, env=env)
        if last.returncode != 0:
            return last
    return last


class CountingResult(unittest.TextTestResult):
    """Counts tests that really executed: a whole-test skip (or a test whose every subTest skipped) is not one.

    Text output cannot tell a setUpClass skip beside a passing class from an all-skipped module (T-0989).
    """
    executed = 0

    def startTest(self, t):
        super().startTest(t)
        self._cur, self._whole, self._sub_skip, self._sub_ok = t, False, False, False

    def addSkip(self, t, reason):
        super().addSkip(t, reason)
        cur = getattr(self, "_cur", None)
        if t is cur:
            self._whole = True
        elif cur is not None and getattr(t, "test_case", None) is cur:
            self._sub_skip = True

    def addSubTest(self, t, sub, err):
        super().addSubTest(t, sub, err)
        self._sub_ok = True

    def stopTest(self, t):
        super().stopTest(t)
        if not self._whole and not (self._sub_skip and not self._sub_ok):
            CountingResult.executed += 1
        self._cur = None


def _count_child(base, tests_dir, json_path):
    """Run one module through CountingResult; write its counts to json_path. rc 0 only if wasSuccessful()."""
    sys.path[0] = os.getcwd()  # as `python -m unittest` does
    prog = unittest.main(module=None, argv=["unittest", "discover", "-s", tests_dir, "-p", base],
                         testRunner=unittest.TextTestRunner(resultclass=CountingResult), exit=False)
    res = prog.result
    with open(json_path, "w", encoding="utf-8") as f:
        json.dump({"testsRun": res.testsRun, "executed": CountingResult.executed, "skipped": len(res.skipped),
                   "failures": len(res.failures), "errors": len(res.errors)}, f)
    return 0 if res.wasSuccessful() else 1


def default_jobs():
    """Workers for the routed-rest step: one per CPU, at most 4 (modules are CPU-bound hook subprocess storms)."""
    return max(1, min(4, os.cpu_count() or 1))


def _run_one(base, i, tmp, env, tests_dir):
    """Run one module in its own child process; return (module output text, counts dict or None, returncode, seconds)."""
    jpath = os.path.join(tmp, "%d.json" % i)
    cmd = [sys.executable, os.path.abspath(__file__), "_count", base, tests_dir, jpath]
    t0 = time.monotonic()
    r = subprocess.run(cmd, cwd=ROOT, env=env, capture_output=True, text=True)
    secs = time.monotonic() - t0
    text = (r.stdout or "") + (r.stderr or "")
    counts = None
    if os.path.isfile(jpath):
        with open(jpath, encoding="utf-8") as f:
            counts = json.load(f)
    return text, counts, r.returncode, secs


def run_routed_rest(extra_env=None, out=None, names=None, tests_dir="tests", jobs=None):
    """Run EVERY routed-rest module (never stop at the first failure), `jobs` at a time; then a summary.

    Each module is its own child process with its own temp dirs (tests use TemporaryDirectory per test; none shares a cwd,
    and the one port bind, tests/test_db_layout.py, uses an ephemeral port 0 that cannot collide), so modules do not share state. Output is written per module in LIST order once that module
    finished (a red module's text is never interleaved with another's), one flushed line per module.
    Returns 0 only if every module passed (director M20747: stays red on any failure, not just the last).
    Green = child rc 0 AND its counts file exists AND at least one test really executed (T-0989).
    """
    out = out or sys.stdout
    env = dict(os.environ, GUARD_IMPL="diff", GOTOOLCHAIN="local", GOFLAGS="-mod=readonly", GOPROXY="off")
    if extra_env:
        env.update(extra_env)
    failed_mods, tests_failed, total, skipped_total = [], 0, 0, 0
    names = module_names(True) if names is None else names
    jobs = default_jobs() if jobs is None else max(1, jobs)
    # largest test source first: the long modules start early, so the pool does not end on one straggler
    order = sorted(range(len(names)), key=lambda i: -_size(tests_dir, names[i]))
    with tempfile.TemporaryDirectory() as tmp, ThreadPoolExecutor(max_workers=jobs) as ex:
        futs = {i: ex.submit(_run_one, names[i], i, tmp, env, tests_dir) for i in order}
        for i, base in enumerate(names):
            text, counts, rc, secs = futs[i].result()
            ran_n = counts["testsRun"] if counts else 0
            fails = counts["failures"] if counts else 0
            errs = counts["errors"] if counts else 0
            skipped = counts["skipped"] if counts else 0
            executed = counts["executed"] if counts else 0
            green = rc == 0 and counts is not None and executed > 0
            if not green:
                out.write(text if text.endswith("\n") else text + "\n")  # the evidence for a red module
                if rc == 0:
                    why = "no counts file (child exited early)" if counts is None else \
                        "ALL %d tests skipped" % ran_n if ran_n else "no tests ran"
                    out.write("module %s: %s: not counted green; fix the skip condition\n" % (base, why))
                failed_mods.append(base)
                tests_failed += fails + errs
            total += ran_n
            skipped_total += skipped
            out.write("module %s: ran=%d failures=%d errors=%d skipped=%d rc=%d secs=%.0f\n" % (
                base, ran_n, fails, errs, skipped, rc, secs))
            out.flush()
    out.write("summary: modules_run=%d modules_failed=%d tests_run=%d tests_skipped=%d tests_failed=%d failed=%s\n" % (
        len(names), len(failed_mods), total, skipped_total, tests_failed, ",".join(failed_mods) or "-"))
    out.flush()
    return 1 if failed_mods else 0


def _size(tests_dir, base):
    try:
        return os.path.getsize(os.path.join(ROOT, tests_dir, base))
    except OSError:
        return 0


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    rest = "--routed-rest"
    rest_help = "the other hook_run-routed modules, not the guard-diff set (guard-equiv job)"
    sub.add_parser("list", help="module basenames, one per line").add_argument(rest, action="store_true", help=rest_help)
    sub.add_parser("modules", help="unittest module paths, one per line").add_argument(rest, action="store_true", help=rest_help)
    runp = sub.add_parser("run", help="run GUARD_IMPL=diff over the module set; exit code from unittest")
    runp.add_argument(rest, action="store_true", help=rest_help)
    runp.add_argument("--jobs", type=int, default=None, help="--routed-rest: modules in parallel (default: CPUs, max 4)")
    hidden = sub.add_parser("_count", help=argparse.SUPPRESS)
    for arg in ("base", "tests_dir", "json_path"):
        hidden.add_argument(arg)
    a = ap.parse_args(argv)
    if a.cmd == "_count":
        return _count_child(a.base, a.tests_dir, a.json_path)
    if a.cmd == "list":
        print("\n".join(module_names(a.routed_rest)))
        return 0
    if a.cmd == "modules":
        print("\n".join(unittest_modules(a.routed_rest)))
        return 0
    if a.routed_rest:
        return run_routed_rest(jobs=a.jobs)
    return run_guard_diff_unittest().returncode


if __name__ == "__main__":
    sys.exit(main())
