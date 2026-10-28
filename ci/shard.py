#!/usr/bin/env python3
"""CI test sharding: pick, run and verify a deterministic slice of tests/test_*.py.

  shard.py list [--shard I --total N]     module file names, one per line (I is 1-based)
  shard.py run --shard I --total N --out F   run that slice, write its report (JSON) to F
  shard.py verify --total N --dir D       check the shard reports in D cover every module once
  shard.py timings --dir D [--out F]      merge the reports' per-module seconds into a timing file

Modules are packed greedily, longest first, onto the lightest shard (ties: lowest shard, then module
name), using the seconds in ci/shard_timings.json; a module missing there gets the median. Stdlib only.
"""
import argparse
import json
import os
import statistics
import sys
import time
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TESTS = os.path.join(ROOT, "tests")
TIMINGS = os.path.join(ROOT, "ci", "shard_timings.json")


class ShardError(Exception):
    """Bad shard arguments or an inconsistent set of shard reports."""


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
    w = weights(sorted(modules), load_timings() if timings is None else timings)
    bins = [[] for _ in range(total)]
    loads = [0.0] * total
    for m in sorted(w, key=lambda m: (-w[m], m)):
        i = min(range(total), key=lambda k: (loads[k], k))
        bins[i].append(m)
        loads[i] += w[m]
    return [sorted(b) for b in bins]


def select(modules, shard, total, timings=None):
    """The modules of 1-based shard `shard` out of `total`."""
    if total < 1 or not 1 <= shard <= total:
        raise ShardError("shard %d out of range for total %d; use 1 <= shard <= total" % (shard, total))
    return pack(modules, total, timings)[shard - 1]


def run_shard(shard, total, out, tests_dir=TESTS):
    """Run the shard's modules; write {shard,total,modules,tests} to `out`; return the exit code."""
    mods = select(all_modules(tests_dir), shard, total)
    root = os.path.dirname(os.path.abspath(tests_dir))
    os.chdir(root)
    # `python -m unittest` puts the cwd (repo root) on sys.path; tests import `web` from there.
    sys.path.insert(0, root)
    sys.path.insert(0, tests_dir)
    loader = unittest.TestLoader()
    runner = unittest.TextTestRunner(verbosity=2)
    tests, ok, seconds = 0, True, {}
    for m in mods:
        t0 = time.monotonic()
        result = runner.run(loader.discover(tests_dir, pattern=m, top_level_dir=tests_dir))
        seconds[m] = round(time.monotonic() - t0, 1)
        tests += result.testsRun
        ok = ok and result.wasSuccessful()
    with open(out, "w") as f:
        json.dump({"shard": shard, "total": total, "modules": mods, "tests": tests, "seconds": seconds}, f)
    if tests == 0 and mods:
        print("shard %d ran 0 tests from %d modules" % (shard, len(mods)), file=sys.stderr)
        return 1
    return 0 if ok else 1


def verify(reports_dir, total, tests_dir=TESTS):
    """Return (summary, tests) if the reports cover every module exactly once; else raise ShardError."""
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
    dup = sorted(m for m, s in seen.items() if len(s) > 1)
    absent = sorted(set(expected) - set(seen))
    extra = sorted(set(seen) - set(expected))
    if dup or absent or extra:
        raise ShardError("module coverage wrong: ran twice=%s never ran=%s unknown=%s" % (dup, absent, extra))
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
                out.update(json.load(f)["seconds"])
        except (ValueError, KeyError, TypeError) as e:
            raise ShardError("report %s has no per-module seconds: %r" % (name, e))
    return dict(sorted(out.items()))


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    ls = sub.add_parser("list")
    ls.add_argument("--shard", type=int)
    ls.add_argument("--total", type=int)
    rn = sub.add_parser("run")
    rn.add_argument("--shard", type=int, required=True)
    rn.add_argument("--total", type=int, required=True)
    rn.add_argument("--out", required=True)
    vf = sub.add_parser("verify")
    vf.add_argument("--total", type=int, required=True)
    vf.add_argument("--dir", required=True)
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
            return run_shard(a.shard, a.total, a.out)
        if a.cmd == "timings":
            data = merge_timings(a.dir)
            with open(a.out, "w") as f:
                json.dump(data, f, indent=1)
                f.write("\n")
            print("wrote %d module timings to %s" % (len(data), a.out))
            return 0
        summary, _ = verify(a.dir, a.total)
        print(summary)
        return 0
    except ShardError as e:
        print("shard: %s" % e, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
