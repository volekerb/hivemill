#!/usr/bin/env python3
"""Read-only profiling baseline: python start, worker-guard latency, factory CLI startup, tick wall time.

  python3 ci/profile_baseline.py [--runs N] [--json PATH] [--hook-cmd 'ARGV...'] [--tick-runs N]

Every child process runs with a throwaway FACTORY_HOME. Nothing here writes the factory DB or runs a
mutating factory command; the tick is never run, its wall time is read from journald. Stdlib only.
"""
import argparse
import datetime as dt
import json
import os
import re
import shlex
import statistics
import subprocess
import sys
import tempfile
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GUARD = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
FACTORY = os.path.join(ROOT, "bin", "factory")
GUARD_ROLES = ["coder", "qa", "reviewer", "judge", "cleaner", "evaluator", "hq"]
# One allowed, one blocked, one heredoc command: the same set for every role/mode.
GUARD_COMMANDS = {
    "allowed": "git status --short",
    "blocked": "herdr server stop",
    "heredoc": "cat <<'EOF'\nhello\nEOF",
}
TICK_UNIT = "factory-tick.service"


def stats(samples_ms):
    s = sorted(samples_ms)
    p95 = s[min(len(s) - 1, int(round(0.95 * (len(s) - 1))))]
    return {"runs": len(s), "min": round(s[0], 2), "median": round(statistics.median(s), 2), "p95": round(p95, 2)}


def timed(argv, env, stdin=None):
    t0 = time.perf_counter()
    p = subprocess.run(argv, input=stdin, env=env, capture_output=True, text=True)
    return (time.perf_counter() - t0) * 1000, p


def measure(argv, env, runs, stdin=None):
    return stats([timed(argv, env, stdin)[0] for _ in range(runs)])


def git_sha():
    try:
        return subprocess.run(["git", "-C", ROOT, "rev-parse", "--short", "HEAD"], capture_output=True,
                              text=True, check=True).stdout.strip()
    except (OSError, subprocess.CalledProcessError) as e:
        return f"unknown ({type(e).__name__})"


def meta():
    return {"git_sha": git_sha(), "python": sys.version.split()[0], "nproc": os.cpu_count(),
            "loadavg": [round(x, 2) for x in os.getloadavg()],
            "date": dt.datetime.now().astimezone().isoformat(timespec="seconds")}


def bench_python_start(env, runs):
    return {"python3 -c pass": measure(["python3", "-c", "pass"], env, runs),
            "python3 -S -c pass": measure(["python3", "-S", "-c", "pass"], env, runs)}


def bench_guard(env, runs, hook_cmd):
    base = shlex.split(hook_cmd) if hook_cmd else [sys.executable, GUARD]
    out = {}
    for role in GUARD_ROLES:
        argv = base + (["--hq"] if role == "hq" else ["--role", role])
        samples, exits = [], {}
        for name, cmd in GUARD_COMMANDS.items():
            payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}})
            for _ in range(runs):
                ms, p = timed(argv, env, payload)
                samples.append(ms)
                exits[name] = p.returncode
        out[role] = dict(stats(samples), exit_codes=exits)
    return out


def parse_importtime(text):
    """`-X importtime` stderr -> [(cumulative_us, name, self_us)]."""
    rows = []
    for line in text.splitlines():
        m = re.match(r"import time:\s+(\d+)\s*\|\s*(\d+)\s*\|\s*(\S+)", line)
        if m:
            rows.append((int(m.group(2)), m.group(3), int(m.group(1))))
    return rows


def bench_cli(env, runs):
    out = {"help": measure([sys.executable, FACTORY, "--help"], env, runs)}
    _, p = timed([sys.executable, "-X", "importtime", FACTORY, "--help"], env)
    rows = parse_importtime(p.stderr)
    top = sorted(rows, reverse=True)[:15]
    scout = sorted(((us, n, own) for us, n, own in rows if n == "scout" or n.startswith("scout.")), reverse=True)
    out["importtime_top15_cumulative_us"] = [{"module": n, "us": us} for us, n, _ in top]
    # self times do not overlap, so their sum is the real total (cumulative times double-count nested imports)
    out["scout"] = {"modules": [{"module": n, "us": us} for us, n, _ in scout],
                    "total_us": sum(own for _, _, own in scout)}
    out["importtime_total_us"] = sum(own for _, _, own in rows)
    return out


def parse_tick_journal(text, last):
    """`journalctl -o short-unix` lines -> wall seconds of the last `last` Starting->Finished pairs."""
    durations, start = [], None
    for line in text.splitlines():
        m = re.match(r"(\d+\.\d+)\s+\S+\s+\S+: (Starting|Finished|Failed)\b", line)
        if not m:
            continue
        ts, kind = float(m.group(1)), m.group(2)
        if kind == "Starting":
            start = ts
        elif start is not None:
            durations.append(ts - start)
            start = None
    return durations[-last:]


def bench_tick(last, journal_cmd=None):
    argv = shlex.split(journal_cmd) if journal_cmd else [
        "journalctl", "--user", "-u", TICK_UNIT, "-n", str(max(last * 8, 200)), "--no-pager", "-o", "short-unix"]
    try:
        p = subprocess.run(argv, capture_output=True, text=True, timeout=30)
    except (OSError, subprocess.TimeoutExpired) as e:
        return {"status": "no data", "reason": f"journalctl unavailable: {type(e).__name__}: {e}"}
    if p.returncode != 0:
        return {"status": "no data", "reason": f"journalctl exit {p.returncode}: {p.stderr.strip()[:200]}"}
    d = parse_tick_journal(p.stdout, last)
    if not d:
        return {"status": "no data", "reason": f"no Starting/Finished pairs for {TICK_UNIT} in journald"}
    return dict(stats([x * 1000 for x in d]), status="ok",
                source=f"journald {TICK_UNIT}, last {len(d)} runs, whole-second stamps")


def render(d):
    m = d["meta"]
    L = ["profile_baseline",
         f"meta: git_sha={m['git_sha']} python={m['python']} nproc={m['nproc']} loadavg={m['loadavg']} date={m['date']}"]
    f = "{:<30} {:>5} {:>9} {:>9} {:>9}"
    L.append("(ms)         " + f.format("", "runs", "min", "median", "p95"))
    for k, v in d["python_start"].items():
        L.append("python_start " + f.format(k, v["runs"], v["min"], v["median"], v["p95"]))
    for k, v in d["guard"].items():
        L.append("guard        " + f.format(k, v["runs"], v["min"], v["median"], v["p95"]) + f"  exit={v['exit_codes']}")
    c = d["cli"]
    h = c["help"]
    L.append("cli          " + f.format("bin/factory --help", h["runs"], h["min"], h["median"], h["p95"]))
    L.append(f"cli          import total: {c['importtime_total_us'] / 1000:.1f} ms; "
             f"scout.* total: {c['scout']['total_us'] / 1000:.1f} ms")
    L.append("cli          top 15 modules by cumulative import time:")
    for r in c["importtime_top15_cumulative_us"]:
        L.append(f"cli            {r['us'] / 1000:>8.1f} ms  {r['module']}")
    for r in c["scout"]["modules"]:
        L.append(f"cli            {r['us'] / 1000:>8.1f} ms  {r['module']}  (scout)")
    t = d["tick"]
    if t["status"] == "ok":
        L.append("tick         " + f.format("wall time", t["runs"], t["min"], t["median"], t["p95"]) + f"  [{t['source']}]")
    else:
        L.append(f"tick         no data: {t['reason']}")
    return "\n".join(L)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--runs", type=int, default=20, help="runs per measurement (default 20)")
    ap.add_argument("--json", metavar="PATH", help="also write the data as JSON")
    ap.add_argument("--hook-cmd", metavar="ARGV", help="guard command to measure instead of worker-guard.py; "
                    "--role <r> or --hq is appended (e.g. a Go binary or shim)")
    ap.add_argument("--tick-runs", type=int, default=10, help="journald tick runs to report (default 10)")
    ap.add_argument("--journal-cmd", metavar="ARGV", help=argparse.SUPPRESS)
    a = ap.parse_args(argv)
    if a.runs < 1:
        ap.error("--runs must be >= 1")
    with tempfile.TemporaryDirectory(prefix="profile-baseline-") as tmp:
        env = dict(os.environ, FACTORY_HOME=tmp)
        env.pop("FACTORY_ROLE", None)
        data = {"meta": meta(), "python_start": bench_python_start(env, a.runs),
                "guard": bench_guard(env, a.runs, a.hook_cmd), "cli": bench_cli(env, a.runs),
                "tick": bench_tick(a.tick_runs, a.journal_cmd)}
    print(render(data))
    if a.json:
        with open(a.json, "w") as fh:
            json.dump(data, fh, indent=2)
    return 0


if __name__ == "__main__":
    sys.exit(main())
