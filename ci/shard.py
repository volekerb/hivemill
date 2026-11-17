#!/usr/bin/env python3
"""CI test sharding: pick, run and verify a deterministic slice of tests/test_*.py.

  shard.py list [--shard I --total N]     module file names, one per line (I is 1-based)
  shard.py run --shard I --total N --out F [--only L] [--jobs J]   run that slice (just the modules listed in file L), write its report (JSON) to F;
                                          J modules at a time, each in its own child process (default: one per CPU, at most 4; SHARD_JOBS overrides)
  shard.py verify --total N --dir D [--only L]   check the shard reports in D cover every module (of L) once
  shard.py timings --dir D [--out F]      merge the reports' per-module seconds into a timing file

Modules are packed greedily, longest first, onto the lightest shard (ties: lowest shard, then module
name), using the seconds in ci/shard_timings.json; a module missing there gets the median. A module in
``SPLIT_TEST_IDS_MODULES`` whose timing alone exceeds the usual 15% bound is excluded from the greedy pack,
then appended to every shard's module list (T-1168); ``run`` executes only that shard's test-id slice for it,
by round-robin on the sorted id list (same rule as guard-unittest T-1039). Other modules stay whole. Stdlib only.
"""
import argparse
import json
import os
import re
import statistics
import subprocess
import sys
import tempfile
import time
import unittest
from concurrent.futures import ThreadPoolExecutor

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TESTS = os.path.join(ROOT, "tests")
TIMINGS = os.path.join(ROOT, "ci", "shard_timings.json")

# One module can be so slow that greedy packing puts it alone on a shard and still exceeds the job cap
# (30 min). Its unittest ids are then split across all shards (T-1168); keep the file name in sync with
# ci/shard_timings.json and the real tests/ tree.
SPLIT_TEST_IDS_MODULES = frozenset({"test_guard_diff.py"})


# Scoping (owner 2026-11-17: a PR run must stay near 10 minutes). The deep run (workflow_dispatch, SHARD_DEEP=true)
# runs everything. Elsewhere:
#  - DEEP_ONLY_MODULES never run (the 13 min Python-vs-Go corpus comparison);
#  - in full mode (no --only list) the guard hook modules run only when the diff SHARD_BASE..HEAD touches a guard
#    path. An unreadable diff or no SHARD_BASE keeps them (fail safe). The affected selection (--only) is left alone.
DEEP_ONLY_MODULES = frozenset({"test_guard_diff.py"})
GUARD_MODULE_RE = re.compile(r"^test_(worker_guard|priv_guard|guard_)")
GUARD_PATH_RE = re.compile(
    r"^(roles/hooks/|roles/settings/|ci/guard_|ci/(public_text_guard|scrub_scan|shard)\.py$|\.github/workflows/|"
    r"go/internal/guard/|go/cmd/worker-guard/|go/internal/shlex/|"
    r"tests/(test_(worker_guard|priv_guard|guard_)|guard_impl\.py$|harness\.py$|_slot\.py$|stubs/))")


def changed_paths(base, root=ROOT):
    """Paths changed between `base` and HEAD (merge-base diff, else two-dot); None when git cannot tell."""
    for spec in ("%s...HEAD" % base, "%s..HEAD" % base):
        r = subprocess.run(["git", "-C", root, "diff", "--name-only", "--no-renames", spec],
                           stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
        if r.returncode == 0:
            return [l for l in r.stdout.splitlines() if l]
    return None


def scoped_out(modules, only=None, env=None, changed=None):
    """The modules of `modules` this run leaves to the deep run; see the comment above."""
    env = os.environ if env is None else env
    if env.get("SHARD_DEEP", "") == "true":
        return frozenset()
    out = {m for m in modules if m in DEEP_ONLY_MODULES}
    base = env.get("SHARD_BASE", "")
    if only is None and base and set(base) != {"0"}:
        paths = changed_paths(base) if changed is None else changed
        if paths is not None and not any(GUARD_PATH_RE.match(x) for x in paths):
            out |= {m for m in modules if GUARD_MODULE_RE.match(m)}
    return frozenset(out)


class ShardError(Exception):
    """Bad shard arguments or an inconsistent set of shard reports."""


def _pack_limit_seconds(modules, timings, total):
    """Max shard load under the 15% rule (see tests/test_shard.py assert_packed)."""
    w = weights(sorted(modules), timings)
    s = sum(w.values())
    return s / total * 1.15


def split_modules(modules, timings, total):
    """Module basenames whose timing exceeds the pack limit alone: run by test id, not as one discover()."""
    if total < 1:
        return frozenset()
    limit = _pack_limit_seconds(modules, timings, total)
    w = weights(sorted(modules), timings)
    return frozenset(m for m in modules if m in SPLIT_TEST_IDS_MODULES and w.get(m, 0) > limit)


def all_modules(tests_dir=TESTS):
    return sorted(f for f in os.listdir(tests_dir) if f.startswith("test_") and f.endswith(".py"))


def load_timings(path=TIMINGS):
    """{module: seconds} from the checked-in timing file; {} if there is none."""
    try:
        with open(path) as f:
            data = json.load(f)
    except FileNotFoundError:
        return {}
    except ValueError as e:
        raise ShardError("unreadable timing file %s: %r; regenerate it (see docs/tests-and-ci.md)" % (path, e))
    if not isinstance(data, dict) or not all(isinstance(v, (int, float)) for v in data.values()):
        raise ShardError("timing file %s must map module name to seconds" % path)
    return data


def weights(modules, timings):
    """{module: seconds}; modules without a timing get the median of the known ones (1.0 if none)."""
    known = [timings[m] for m in modules if m in timings]
    median = statistics.median(known) if known else 1.0
    return {m: timings.get(m, median) for m in modules}


def pack(modules, total, timings=None):
    """Greedy longest-processing-time packing: a list of `total` sorted module lists."""
    if total < 1:
        raise ShardError("total %d out of range; use total >= 1" % total)
    tmap = load_timings() if timings is None else timings
    mods_sorted = sorted(modules)
    sm = split_modules(mods_sorted, tmap, total)
    rest = [m for m in mods_sorted if m not in sm]
    w = weights(rest, tmap)
    bins = [[] for _ in range(total)]
    loads = [0.0] * total
    for m in sorted(w, key=lambda m: (-w[m], m)):
        i = min(range(total), key=lambda k: (loads[k], k))
        bins[i].append(m)
        loads[i] += w[m]
    for m in sorted(sm):
        for b in bins:
            b.append(m)
    return [sorted(b) for b in bins]


def select(modules, shard, total, timings=None):
    """The modules of 1-based shard `shard` out of `total`."""
    if total < 1 or not 1 <= shard <= total:
        raise ShardError("shard %d out of range for total %d; use 1 <= shard <= total" % (shard, total))
    return pack(modules, total, timings)[shard - 1]


def collect_test_ids(module, tests_dir=TESTS):
    """Sorted unittest ids for one module basename (e.g. test_x.py -> test_x.Class.test_y)."""
    loader = unittest.TestLoader()
    suite = loader.discover(tests_dir, pattern=module, top_level_dir=tests_dir)
    out = []

    def walk(st):
        for t in st:
            if isinstance(t, unittest.TestSuite):
                walk(t)
            else:
                out.append(t.id())

    walk(suite)
    return sorted(out)


def shard_of_test_ids(ids, shard, total):
    """Ids for shard `shard` (1-based) of `total`: round-robin on sorted position (T-1039 / T-1168)."""
    return [i for pos, i in enumerate(sorted(ids)) if pos % total == shard - 1]


def read_only(path):
    """Module names listed one per line in `path` (the affected selection of ci/ci_tier.py); None if no path."""
    if not path:
        return None
    with open(path) as f:
        return {l.strip() for l in f if l.strip()}


def default_jobs():
    """Modules run at a time: $SHARD_JOBS, else one per CPU, at most 4 (the hosted runner size)."""
    env = os.environ.get("SHARD_JOBS", "")
    if env.isdigit() and int(env) > 0:
        return int(env)
    return max(1, min(4, os.cpu_count() or 1))


def _run_module(module, tests_dir, ids=None):
    """Run one module (or just the test ids `ids`) in this process; return (testsRun, wasSuccessful)."""
    loader = unittest.TestLoader()
    runner = unittest.TextTestRunner(verbosity=2)
    if ids is not None:
        result = runner.run(unittest.TestSuite(loader.loadTestsFromName(i) for i in ids))
    else:
        result = runner.run(loader.discover(tests_dir, pattern=module, top_level_dir=tests_dir))
    return result.testsRun, result.wasSuccessful()


def _child(module, tests_dir, json_path, ids_path=None):
    """Child entry (`shard.py _one`): run one module, write {tests, ok} to json_path. rc 0 only when it passed."""
    root = os.path.dirname(os.path.abspath(tests_dir))
    os.chdir(root)
    sys.path.insert(0, root)
    sys.path.insert(0, tests_dir)
    ids = None
    if ids_path:
        with open(ids_path) as f:
            ids = json.load(f)
    tests, ok = _run_module(module, tests_dir, ids)
    with open(json_path, "w") as f:
        json.dump({"tests": tests, "ok": ok}, f)
    return 0 if ok else 1


def _spawn_module(module, i, tmp, tests_dir, ids):
    """Run one module in its own child process; return (output text, tests, ok, seconds)."""
    jpath = os.path.join(tmp, "%d.json" % i)
    cmd = [sys.executable, os.path.abspath(__file__), "_one", module, tests_dir, jpath]
    if ids is not None:
        ipath = os.path.join(tmp, "%d.ids.json" % i)
        with open(ipath, "w") as f:
            json.dump(ids, f)
        cmd.append(ipath)
    t0 = time.monotonic()
    r = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, errors="replace")
    secs = round(time.monotonic() - t0, 1)
    tests, ok = 0, False
    try:
        with open(jpath) as f:
            data = json.load(f)
        tests, ok = int(data["tests"]), bool(data["ok"]) and r.returncode == 0
    except (OSError, ValueError, KeyError, TypeError):
        pass  # no report: the child died early; counted as a failure with its output below
    return r.stdout or "", tests, ok, secs


def run_shard(shard, total, out, tests_dir=TESTS, only=None, jobs=None):
    """Run the shard's modules (only those in `only`, if given); write {shard,total,modules,tests} to `out`; return the exit code.

    jobs > 1: every module is its own child process, `jobs` at a time, slowest first; a module's output is printed whole
    when it finishes, in list order (never interleaved). A child with no report or a non-zero exit fails the shard.
    """
    mods = select(all_modules(tests_dir), shard, total)
    if only is not None:
        mods = [m for m in mods if m in only]
    dropped = scoped_out(mods, only)
    if dropped:
        print("shard %d: left to the deep run: %d modules (%s)" % (
            shard, len(dropped), "guard hook modules, no guard path changed" if len(dropped) > len(DEEP_ONLY_MODULES) else " ".join(sorted(dropped))))
        mods = [m for m in mods if m not in dropped]
    timings = load_timings()
    split = split_modules(all_modules(tests_dir), timings, total)
    split_here = [m for m in mods if m in split]
    id_subsets = {}
    for m in split_here:
        all_ids = collect_test_ids(m, tests_dir)
        id_subsets[m] = shard_of_test_ids(all_ids, shard, total)
    jobs = default_jobs() if jobs is None else max(1, jobs)
    tests, ok, seconds = 0, True, {}
    failed = []
    if jobs == 1:
        root = os.path.dirname(os.path.abspath(tests_dir))
        os.chdir(root)
        # `python -m unittest` puts the cwd (repo root) on sys.path; tests import `web` from there.
        sys.path.insert(0, root)
        sys.path.insert(0, tests_dir)
        for m in mods:
            t0 = time.monotonic()
            n, good = _run_module(m, tests_dir, id_subsets.get(m))
            seconds[m] = round(time.monotonic() - t0, 1)
            tests += n
            ok = ok and good
            if not good:
                failed.append(m)
    else:
        w = weights(mods, timings)
        order = sorted(range(len(mods)), key=lambda i: (-w[mods[i]], mods[i]))
        with tempfile.TemporaryDirectory() as tmp, ThreadPoolExecutor(max_workers=jobs) as ex:
            futs = {i: ex.submit(_spawn_module, mods[i], i, tmp, tests_dir, id_subsets.get(mods[i])) for i in order}
            for i, m in enumerate(mods):
                text, n, good, secs = futs[i].result()
                sys.stdout.write(text if text.endswith("\n") or not text else text + "\n")
                print("module %s: tests=%d ok=%s secs=%.1f" % (m, n, good, secs), flush=True)
                seconds[m] = secs
                tests += n
                ok = ok and good
                if not good:
                    failed.append(m)
    if failed:
        print("shard %d failed modules: %s" % (shard, " ".join(failed)), file=sys.stderr)
    report = {"shard": shard, "total": total, "modules": mods, "tests": tests, "seconds": seconds}
    if id_subsets:
        report["test_ids"] = {m: id_subsets[m] for m in sorted(id_subsets)}
    with open(out, "w") as f:
        json.dump(report, f)
    if tests == 0 and mods:
        print("shard %d ran 0 tests from %d modules" % (shard, len(mods)), file=sys.stderr)
        return 1
    return 0 if ok else 1


def verify(reports_dir, total, tests_dir=TESTS, only=None):
    """Return (summary, tests) if the reports cover every module (every module of `only`, if given) exactly once; else raise ShardError."""
    reports = {}
    for name in sorted(os.listdir(reports_dir)):
        path = os.path.join(reports_dir, name)
        if os.path.isdir(path):
            continue
        try:
            with open(path) as f:
                r = json.load(f)
            reports[r["shard"]] = r
            if r["total"] != total:
                raise ShardError("%s was made for %d shards, expected %d" % (name, r["total"], total))
        except (ValueError, KeyError, TypeError) as e:
            raise ShardError("unreadable shard report %s: %r" % (name, e))
    missing = [i for i in range(1, total + 1) if i not in reports]
    if missing:
        raise ShardError("missing shard reports: %s" % ", ".join(map(str, missing)))
    seen = {}
    for i, r in reports.items():
        for m in r["modules"]:
            seen.setdefault(m, []).append(i)
    expected = all_modules(tests_dir)
    if only is not None:
        expected = [m for m in expected if m in only]
    dropped = scoped_out(expected, only)
    expected = [m for m in expected if m not in dropped]
    timings = load_timings()
    split = split_modules(expected, timings, total)
    dup = sorted(m for m, s in seen.items() if m not in split and len(s) > 1)
    absent = sorted(set(expected) - set(seen) - split)
    extra = sorted(set(seen) - set(expected))
    if dup or absent or extra:
        raise ShardError("module coverage wrong: ran twice=%s never ran=%s unknown=%s" % (dup, absent, extra))
    for m in split:
        if m not in seen:
            raise ShardError("module coverage wrong: ran twice=[] never ran=%s unknown=[]" % ([m],))
        full = collect_test_ids(m, tests_dir)
        by_shard = {}
        for i in sorted(reports):
            r = reports[i]
            if m not in r["modules"]:
                continue
            ids = (r.get("test_ids") or {}).get(m)
            if not ids:
                raise ShardError("split module %s on shard %d: missing test_ids in report" % (m, i))
            for tid in ids:
                by_shard.setdefault(tid, []).append(i)
        dup_ids = sorted(t for t, s in by_shard.items() if len(s) > 1)
        ran = sorted(by_shard)
        if dup_ids or ran != full:
            raise ShardError(
                "split module %s: id coverage wrong: ran twice=%s never ran=%s"
                % (m, dup_ids, sorted(set(full) - set(ran))))
        for i in sorted(reports):
            r = reports[i]
            if m not in r["modules"]:
                continue
            got = (r.get("test_ids") or {}).get(m)
            exp = shard_of_test_ids(full, i, total)
            if got != exp:
                raise ShardError("split module %s shard %d: expected %d ids, got %d (or wrong ids)" % (
                    m, i, len(exp), len(got or ())))
    tests = sum(r["tests"] for r in reports.values())
    lines = ["%d modules, %d tests across %d shards" % (len(expected), tests, total)]
    lines += ["- shard %d: %d modules, %d tests, %s" % (
        i, len(reports[i]["modules"]), reports[i]["tests"],
        "%.0f s" % sum(reports[i]["seconds"].values()) if "seconds" in reports[i] else "no timing")
        for i in sorted(reports)]
    return "\n".join(lines), tests


def merge_timings(reports_dir):
    """{module: seconds} from the shard reports in `reports_dir`."""
    out = {}
    for name in sorted(os.listdir(reports_dir)):
        path = os.path.join(reports_dir, name)
        if os.path.isdir(path):
            continue
        try:
            with open(path) as f:
                r = json.load(f)
            sec = r["seconds"]
            for m, s in sec.items():
                out[m] = out.get(m, 0) + s
        except (ValueError, KeyError, TypeError) as e:
            raise ShardError("report %s has no per-module seconds: %r" % (name, e))
    return dict(sorted(out.items()))


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv
    if argv[:1] == ["_one"]:  # internal: one module in a child process (run --jobs)
        return _child(argv[1], argv[2], argv[3], argv[4] if len(argv) > 4 else None)
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    ls = sub.add_parser("list")
    ls.add_argument("--shard", type=int)
    ls.add_argument("--total", type=int)
    rn = sub.add_parser("run")
    rn.add_argument("--shard", type=int, required=True)
    rn.add_argument("--total", type=int, required=True)
    rn.add_argument("--out", required=True)
    rn.add_argument("--only", help="file of module names: run only those (affected mode)")
    rn.add_argument("--jobs", type=int, help="modules run at a time, each in a child process (default: per CPU, max 4)")
    vf = sub.add_parser("verify")
    vf.add_argument("--total", type=int, required=True)
    vf.add_argument("--dir", required=True)
    vf.add_argument("--only", help="file of module names: only those must have run (affected mode)")
    tm = sub.add_parser("timings")
    tm.add_argument("--dir", required=True)
    tm.add_argument("--out", default=TIMINGS)
    a = ap.parse_args(argv)
    try:
        if a.cmd == "list":
            mods = all_modules()
            if a.shard is not None:
                mods = select(mods, a.shard, a.total or 1)
            print("\n".join(mods))
            return 0
        if a.cmd == "run":
            return run_shard(a.shard, a.total, a.out, only=read_only(a.only), jobs=a.jobs)
        if a.cmd == "timings":
            data = merge_timings(a.dir)
            with open(a.out, "w") as f:
                json.dump(data, f, indent=1)
                f.write("\n")
            print("wrote %d module timings to %s" % (len(data), a.out))
            return 0
        summary, _ = verify(a.dir, a.total, only=read_only(a.only))
        print(summary)
        return 0
    except ShardError as e:
        print("shard: %s" % e, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
