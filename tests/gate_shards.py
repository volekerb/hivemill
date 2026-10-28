#!/usr/bin/env python3
"""The sharded full suite for `factory gate run` (T-0515): N shard processes in parallel in the current tree, one test slot.

Meant to be part of a project's gate_cmd, run from the MAIN checkout so a PR cannot edit it:
    python3 $FACTORY_MAIN/tests/gate_shards.py
Without $FACTORY_TEST_SLOT_HELD it takes one tests/_slot.py slot itself (never slot-less). N comes from $GATE_SHARDS (the runner caps it by load), default 4. Each shard is `python3 ci/shard.py run` of the tree
under test, so ci/shard_timings.json packs the modules. After the shards: `ci/shard.py verify` (every module ran once),
the summed test count, wall and CPU seconds; the summary also goes to $GATE_REPORT_DIR/summary.json for the runner.
Exit 0 only when every shard passed, the coverage check passed and at least one test ran."""
import json
import os
import resource
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path


def main(env=os.environ, cwd=None):
    cwd = Path(cwd or os.getcwd())
    total = int(env.get("GATE_SHARDS") or 4)
    if total < 1:
        print(f"gate shards: GATE_SHARDS={total} must be >= 1", file=sys.stderr)
        return 2
    child_env = dict(env)
    if not env.get("FACTORY_TEST_SLOT_HELD"):  # run by hand: take the ONE slot for all shards, never run slot-less
        sys.path.insert(0, str(Path(__file__).resolve().parent))
        import _slot
        if _slot.acquire(require=True, wait_max=float(env.get("FACTORY_GATE_SLOT_WAIT_S") or _slot.WAIT_MAX_S)) is None:
            print("gate shards: no test slot; not running without one", file=sys.stderr)
            return 5
        child_env["FACTORY_TEST_SLOT_HELD"] = "1"
    out = Path(env.get("GATE_REPORT_DIR") or tempfile.mkdtemp(prefix="gate-shards-"))
    reports = out / "reports"
    shutil.rmtree(reports, ignore_errors=True)  # a reused report dir must not leak an earlier run's shard files
    reports.mkdir(parents=True, exist_ok=True)
    shard = cwd / "ci" / "shard.py"
    start = time.monotonic()
    procs = []
    for i in range(1, total + 1):
        log = open(out / f"shard-{i}.log", "w")
        p = subprocess.Popen([sys.executable, str(shard), "run", "--shard", str(i), "--total", str(total),
                              "--out", str(reports / f"shard-{i}.json")], cwd=cwd, env=child_env, stdout=log,
                             stderr=subprocess.STDOUT)
        procs.append((i, p, log))
    codes = {}
    for i, p, log in procs:
        codes[i] = p.wait()
        log.close()
    wall = time.monotonic() - start
    cpu = resource.getrusage(resource.RUSAGE_CHILDREN)
    for i, _, _ in procs:
        text = (out / f"shard-{i}.log").read_text(errors="replace")
        print(f"===== shard {i}/{total}: exit {codes[i]} =====")
        print(text if codes[i] else "\n".join(text.splitlines()[-4:]))
    v = subprocess.run([sys.executable, str(shard), "verify", "--total", str(total), "--dir", str(reports)],
                       cwd=cwd, capture_output=True, text=True)
    print(v.stdout + v.stderr, end="")
    tests = 0
    for f in reports.glob("shard-*.json"):
        tests += json.loads(f.read_text())["tests"]
    summary = {"shards": total, "tests": tests, "wall_s": round(wall), "cpu_s": round(cpu.ru_utime + cpu.ru_stime)}
    (out / "summary.json").write_text(json.dumps(summary))
    print(f"gate shards: {total} shards, {tests} tests, wall {summary['wall_s']} s, cpu {summary['cpu_s']} s")
    if any(codes.values()):
        print(f"gate shards: FAIL, shards with a non-zero exit: {[i for i, c in codes.items() if c]}", file=sys.stderr)
        return 1
    if v.returncode != 0 or tests == 0:
        print("gate shards: FAIL, coverage check failed or no test ran", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
