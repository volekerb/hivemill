# Web UI performance, memory and tick impact (G-008 T-0088)

Measured on `main` @ `6122a40` (everything merged: T-0077..T-0087, T-0124, systemd unit), your-box 8 vCPU, load average 5-7 during
the runs (other agents were working, so these numbers are pessimistic). Data: a temp `FACTORY_HOME` built from `state/factory.sql`
(373 agents, 36 tasks, WAL mode) and the REAL `~/.claude/projects` (585 jsonl files, 444 MB). Server: `factory serve --bind 127.0.0.1
--port 18765`, started by hand and killed after each run. **Every target was met on the first run, so there is no "before/after" difference. The only
`web/` change is a correctness fix found by QA (`web/server.py`: the `/api/*.json` handler now closes its DB reader before writing the
response, like the HTML pages); it does not affect these numbers.**

## Setup (all commands from the repo root; `S` is any scratch dir)

```
T=$(mktemp -d); mkdir -p $T/factory/db
python3 -c "import sqlite3,sys;c=sqlite3.connect(sys.argv[1]);c.executescript(open(sys.argv[2]).read());c.execute('PRAGMA journal_mode=WAL');c.close()" $T/factory/db/factory.db ~/factory/state/factory.sql
FACTORY_HOME=$T/factory python3 bin/factory serve --bind 127.0.0.1 --port 18765 &
```

## Summary

| # | Measurement | Result | Target | Met |
|---|---|---|---|---|
| 1 | Cold start: first index pass ready | 1.73-2.12 s (5 runs: 1.87, 1.82, 1.95, 2.12, 1.73), RSS 78-81 MB (HWM 75-81 MB) | (none) | n/a |
| 2 | Page/API max, cold (index not ready) | 0.252 s (5 re-runs at load 6.8: overall max 0.190, 0.213, 0.252, 0.197, 0.141) | < 1.0 s | yes |
| 2 | Page/API max, warm | 0.072 s | < 1.0 s | yes |
| 3 | VmHWM after all pages x5, 40 s request loop, 10 min idle | 86.4 MB (VmRSS 86.4 MB) | < 150 MB | yes |
| 4 | Writer round p99, server idle / under page loop | 0.29 ms / 0.23 ms (max single round 0.89 ms) | < 100 ms | yes |
| 4 | Median-of-p50 slowdown under load | 0.18 ms -> 0.18 ms (0%) | < 10% | yes |
| 4 | `wal_checkpoint(PASSIVE)` under page loop | 5/5 runs `(0, 134, 134)`: log == checkpointed | not starved | yes |
| 5 | Idle CPU over 10 min | mean 0.03% (5-min means 0.03, 0.02); worst 1 s sample 2.0% | < 2% | yes |
| 5 | CPU during the first (full) index pass | 1.77 CPU-s in 1.73 s wall (about one niced core for under 2 s) | (none) | n/a |
| 6 | Token cross-check, `coder-t-0122`: parsing / prices | tokens equal / API $0.964826 = tokens x table; cost-state $0.9805172 is 1.6% higher (T-0134) | equal | yes / yes |

Open issues: none against the targets. Caveats: the cold window is only about 2 s on a warm page cache (the log files were
already cached), so a truly cold disk cache would lengthen the "index not ready" period, not the page latency (pages never wait
for the index). The 1 s idle-CPU sample peak of 2.0% is a refresh reading newly appended bytes of the live sessions.

## 1. Cold start

Command (5 times; `cold.py` starts the server, polls `GET /api/tokens.json` until `"ready": true`, prints the wall time since
launch and `VmRSS`/`VmHWM` of the server pid, then kills the server):

```
python3 cold.py "$T/factory" 18765
```

cold.py:

```
import http.client, json, os, subprocess, sys, time
home, port = sys.argv[1], sys.argv[2]
env = dict(os.environ, FACTORY_HOME=home)
t0 = time.perf_counter()
p = subprocess.Popen(["python3", "bin/factory", "serve", "--bind", "127.0.0.1", "--port", port], env=env,
                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
try:
    while True:
        try:
            c = http.client.HTTPConnection("127.0.0.1", int(port), timeout=5)
            c.request("GET", "/api/tokens.json", headers={"Host": f"127.0.0.1:{port}"})
            if json.loads(c.getresponse().read()).get("ready"):
                break
        except (OSError, ValueError):
            pass
        time.sleep(0.02)
    dt = time.perf_counter() - t0
    st = dict(l.split(":", 1) for l in open(f"/proc/{p.pid}/status") if l.startswith(("VmRSS", "VmHWM")))
    print(f"ready after {dt:.2f} s  VmRSS={st['VmRSS'].strip()}  VmHWM={st['VmHWM'].strip()}")
finally:
    p.kill(); p.wait()
```

Result: ready after 1.87, 1.82, 1.95, 2.12, 1.73 s; RSS at that point 78.6-81.0 MB, VmHWM 73.9-81.0 MB.

## 2. Page load, 5 requests each

Command (cold: start the server and run it at once; warm: after `"ready": true`):

```
for p in / /goals /goal/G-008 /tasks /task/T-0088 /agents /resources /throughput /quality /changelog /tokens \
  /api/home.json /api/goals.json /api/tasks.json /api/agents.json /api/resources.json /api/throughput.json \
  /api/quality.json /api/changelog.json /api/tokens.json /healthz /static/app.css; do
  for i in 1 2 3 4 5; do curl -s -o /dev/null -H 'Host: 127.0.0.1:18765' -w "$p %{http_code} %{time_total}\n" "http://127.0.0.1:18765$p"; done
done | sort -k3 -n | tail -3      # three slowest requests; QA compares the last line with the max below
```

The table below was taken with `pages.py` (one fresh `http.client` connection per request, 22 endpoints, N passes):

```
python3 pages.py 18765 5
```

pages.py:

```
import http.client, statistics, sys, time
port, passes = int(sys.argv[1]), int(sys.argv[2])
paths = ["/", "/goals", "/goal/G-008", "/tasks", "/task/T-0088", "/agents", "/resources", "/throughput", "/quality", "/changelog", "/tokens",
         "/api/home.json", "/api/goals.json", "/api/tasks.json", "/api/agents.json", "/api/resources.json", "/api/throughput.json",
         "/api/quality.json", "/api/changelog.json", "/api/tokens.json", "/healthz", "/static/app.css"]
def get(p):
    c = http.client.HTTPConnection("127.0.0.1", port, timeout=30)   # one fresh connection per request
    t = time.perf_counter(); c.request("GET", p, headers={"Host": f"127.0.0.1:{port}"})
    r = c.getresponse(); n = len(r.read()); dt = time.perf_counter() - t; c.close()
    return r.status, n, dt
res = {p: [] for p in paths}
for _ in range(passes):
    for p in paths:
        st, n, dt = get(p); assert st == 200, (p, st); res[p].append((n, dt))
for p in paths:
    ts = [dt for _, dt in res[p]]
    print(f"{p} bytes={res[p][-1][0]} median={statistics.median(ts):.3f} max={max(ts):.3f}")
print(f"overall max {max(dt for v in res.values() for _, dt in v):.3f} s over {sum(len(v) for v in res.values())} requests")
```

Cold = 5 separate server restarts, one pass each (`pages.py 18765 1`) started right after the launch, before the index was ready (it was
never ready by the end of a pass, i.e. all 22 requests were cold); warm = `pages.py 18765 5` after ready. The per-endpoint cold
max column below is from the first set of runs (overall cold max 0.104 s); the 5 re-runs of `pages.py 18765 1` after a 0.15 s
sleep gave overall cold max 0.190, 0.213, 0.252, 0.197, 0.141 s, which is the figure in the summary.

| endpoint | bytes | cold median / max (s) | warm median / max (s) |
|---|---|---|---|
| / | 1608 | 0.066 / 0.077 | 0.004 / 0.029 |
| /goals | 5156 | 0.023 / 0.033 | 0.003 / 0.003 |
| /goal/G-008 | 12481 | 0.035 / 0.088 | 0.003 / 0.004 |
| /tasks | 47210 | 0.027 / 0.062 | 0.036 / 0.068 |
| /task/T-0088 | 1998 | 0.018 / 0.032 | 0.004 / 0.037 |
| /agents | 32134 | 0.055 / 0.102 | 0.010 / 0.040 |
| /resources | 15673 | 0.053 / 0.104 | 0.012 / 0.018 |
| /throughput | 2063 | 0.030 / 0.037 | 0.002 / 0.002 |
| /quality | 4063 | 0.017 / 0.064 | 0.004 / 0.005 |
| /changelog | 1079 | 0.014 / 0.048 | 0.002 / 0.006 |
| /tokens | 2219 cold, 15579 warm | 0.010 / 0.020 | 0.017 / 0.020 |
| /api/home.json | 198 | 0.034 / 0.062 | 0.003 / 0.051 |
| /api/goals.json | 3006 | 0.031 / 0.039 | 0.007 / 0.018 |
| /api/tasks.json | 29598 | 0.018 / 0.059 | 0.016 / 0.039 |
| /api/agents.json | 28129 | 0.039 / 0.083 | 0.006 / 0.007 |
| /api/resources.json | 8408 | 0.052 / 0.087 | 0.039 / 0.065 |
| /api/throughput.json | 1044 | 0.019 / 0.047 | 0.002 / 0.012 |
| /api/quality.json | 2027 | 0.021 / 0.034 | 0.004 / 0.006 |
| /api/changelog.json | 53 | 0.015 / 0.019 | 0.002 / 0.003 |
| /api/tokens.json | 424 cold, 13764 warm | 0.014 / 0.038 | 0.029 / 0.072 |
| /healthz | 22 | 0.008 / 0.019 | 0.001 / 0.001 |
| /static/app.css | 7246 | 0.011 / 0.045 | 0.001 / 0.007 |

Overall max: cold 0.104 s, warm 0.072 s. All responses were 200 (`pages.py` asserts it). The curl loop above was spot-checked on 55 warm
requests: slowest 0.026 s.

## 3. Memory

Same server process that served the warm 5x pass, then 40 s of continuous requests (5560 requests, all pages) during measurement 4,
then 10 min idle with the 60 s background index refresh running (`/proc/<pid>/status`):
`VmHWM 86392 kB`, `VmRSS 86392 kB` (84.4 MB). The systemd unit sets `MemoryHigh=150M`; peak is 58% of it.

## 4. Tick impact (simulated; `factory tick`/`export` never run)

The writer mimics a tick: per round `BEGIN IMMEDIATE`; 20 x `UPDATE agents`; 5 x `INSERT INTO events`; `COMMIT`; 20 ms pause; 60 rounds
per run, `busy_timeout=30000`, WAL, on the temp DB. Run 5 times idle and 5 times while a loop requested every page/API
(`perf` loop, 5560 requests in 40 s). After each run `PRAGMA wal_checkpoint(PASSIVE)` is read.

```
python3 writer.py "$T/factory/db/factory.db" 60 5        # idle server, then again with the page loop running
```

writer.py:

```
import sqlite3, sys, time, statistics
db, rounds, runs = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
c = sqlite3.connect(db, timeout=30, isolation_level=None)
c.execute("PRAGMA busy_timeout=30000"); assert c.execute("PRAGMA journal_mode").fetchone()[0] == "wal"
names = [r[0] for r in c.execute("SELECT name FROM agents LIMIT 20")]
allp50, allp99 = [], []
for run in range(runs):
    lat = []
    for i in range(rounds):
        t = time.perf_counter()
        c.execute("BEGIN IMMEDIATE")
        for n in names:
            c.execute("UPDATE agents SET flagless=0 WHERE name=?", (n,))
        for _ in range(5):
            c.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                      ("2026-10-25T03:00:00+02:00", "perf", "agent", names[0], "perf-sim", '{"n":%d}' % i))
        c.execute("COMMIT")
        lat.append((time.perf_counter() - t) * 1000)
        time.sleep(0.02)
    lat.sort()
    p50, p99 = statistics.median(lat), lat[int(len(lat) * 0.99 + 0.5) - 1]
    ck = c.execute("PRAGMA wal_checkpoint(PASSIVE)").fetchone()
    print(f"run{run+1} p50={p50:.2f}ms p99={p99:.2f}ms max={lat[-1]:.2f}ms checkpoint(busy,log,ckpt)={tuple(ck)} {'OK' if ck[0]==0 and ck[1]==ck[2] else 'STARVED'}")
    allp50.append(p50); allp99.append(p99)
print(f"median-of-p50={statistics.median(allp50):.2f}ms max-p99={max(allp99):.2f}ms")
```

The page loop (`loop.py`, same 22 endpoints as `pages.py`, fresh connection per request, for 40 s) runs in another shell while the writer runs:

```
python3 loop.py 18765 40
```

loop.py:

```
import http.client, sys, time
port, secs = int(sys.argv[1]), int(sys.argv[2])
paths = ["/", "/goals", "/goal/G-008", "/tasks", "/task/T-0088", "/agents", "/resources", "/throughput", "/quality", "/changelog", "/tokens",
         "/api/home.json", "/api/goals.json", "/api/tasks.json", "/api/agents.json", "/api/resources.json", "/api/throughput.json",
         "/api/quality.json", "/api/changelog.json", "/api/tokens.json", "/healthz", "/static/app.css"]
end, n = time.monotonic() + secs, 0
while time.monotonic() < end:
    for p in paths:
        c = http.client.HTTPConnection("127.0.0.1", port, timeout=30)
        c.request("GET", p, headers={"Host": f"127.0.0.1:{port}"}); c.getresponse().read(); c.close(); n += 1
print(n, "requests in", secs, "s")
```

| condition | per-run p50 (ms) | per-run p99 (ms) | checkpoint (busy, log, checkpointed) |
|---|---|---|---|
| server idle | 0.20, 0.19, 0.18, 0.16, 0.16 | 0.29, 0.24, 0.23, 0.23, 0.23 | (0,132,132) (0,134,134) x4 |
| page loop running | 0.18, 0.16, 0.18, 0.18, 0.18 | 0.23, 0.22, 0.22, 0.23, 0.21 | (0,134,134) x5 |

The server's connections are read-only and short, so the writer never waited: no slowdown, checkpoint always complete.

## 5. CPU

`idle.py` samples `/proc/<pid>/stat` (utime+stime) every second while the server is idle (index refreshing every 60 s); here 610 s,
the pid from `ss -ltnp | grep 18765`:

```
python3 idle.py <pid> 610
```

idle.py:

```
import os, subprocess, sys, time
pid, secs = int(sys.argv[1]), int(sys.argv[2])
hz = os.sysconf("SC_CLK_TCK")
def cpu():
    f = open(f"/proc/{pid}/stat").read().rsplit(")", 1)[1].split()
    return (int(f[11]) + int(f[12])) / hz        # utime + stime, seconds
prev, t_prev, pct = cpu(), time.monotonic(), []
for _ in range(secs):
    time.sleep(1)
    now, t = cpu(), time.monotonic()
    pct.append(100 * (now - prev) / (t - t_prev)); prev, t_prev = now, t
half = len(pct) // 2
print(f"mean={sum(pct)/len(pct):.2f}% first-half={sum(pct[:half])/half:.2f}% second-half={sum(pct[half:])/(len(pct)-half):.2f}% max1s={max(pct):.1f}% samples>5%={sum(p>5 for p in pct)}")
print("ps -o %cpu (lifetime average):", subprocess.run(["ps", "-o", "%cpu=", "-p", str(pid)], capture_output=True, text=True).stdout.strip())
```

Result: mean 0.03%, 5-min means 0.03% and 0.02%, worst 1 s sample 2.0%, 0 samples above 5%. The first-pass CPU is the same `/proc/<pid>/stat` utime+stime read right after `cold.py` sees `"ready": true` (and `nice` from
`ps -o ni= -p <pid>`). During the first index pass the process used
1.77 CPU-s in 1.73 s wall (utime 1.67, stime 0.10) at nice 10.

## 6. Token cross-check (SC3)

Session `07576c04` (agent `coder-t-0122`, goal G-006, one main log, no subagent logs). Independent recount: dedupe by
`(message.id, requestId)`, sum `input_tokens`, `output_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`:

```
python3 recount.py 07576c04-9d04-4c35-b3c5-0c40b2efe607
```

recount.py:

```
import glob, json, os, sys
sid = sys.argv[1]
files = glob.glob(os.path.expanduser(f"~/.claude/projects/*/{sid}.jsonl")) + glob.glob(os.path.expanduser(f"~/.claude/projects/*/{sid}/subagents/*.jsonl"))
seen, t, main_cost = set(), dict(input=0, output=0, cache_read=0, cache_write=0), None
for f in files:
    for ln in open(f, errors="replace"):
        if '"cost-state"' in ln:
            d = json.loads(ln)
            if d.get("type") == "cost-state" and "/subagents/" not in f:
                main_cost = d.get("totalCostUSD")
        if '"usage"' not in ln:
            continue
        d = json.loads(ln); m = d.get("message")
        if d.get("type") != "assistant" or not isinstance(m, dict) or not isinstance(m.get("usage"), dict):
            continue
        k = (m.get("id"), d.get("requestId"))
        if k in seen:
            continue
        seen.add(k); u = m["usage"]
        t["input"] += u.get("input_tokens") or 0; t["output"] += u.get("output_tokens") or 0
        t["cache_read"] += u.get("cache_read_input_tokens") or 0; t["cache_write"] += u.get("cache_creation_input_tokens") or 0
print(f"session {sid[:8]} files={len(files)} requests={len(seen)}", t, "cost-state totalCostUSD(main)=", main_cost)
```

The API values come from (the server of the Setup section):

```
curl -s -H 'Host: 127.0.0.1:18765' 'http://127.0.0.1:18765/api/tokens.json?days=all&goal=G-006'
```

entry `by_agent[0][]` with `key == "coder-t-0122"`. Re-run after the price fix T-0133 (merged, `web/prices.py` Sonnet 5.5 cache_read is
now $0.10/M), on `main` merged into this branch:

| | input | output | cache_read | cache_write | requests | cost |
|---|---|---|---|---|---|---|
| independent recount | 74 | 27009 | 3556200 | 84742 | 37 | cost-state `totalCostUSD` = $0.9805172 |
| `/api/tokens.json` | 74 | 27009 | 3556200 | 84742 | 37 | $0.964826 |

Two separate checks:

1. **Parsing check** (tokens): equal on all four counters and the request count.
2. **Prices check** (cost-state tokens x price table = API cost): 74 x $2 + 27009 x $10 + 3556200 x $0.10 + 84742 x $4 (all cache writes are
   1 h) per million = $0.964826, exactly the API figure, so the table is applied correctly.

The API cost is $0.015691 (1.6%) below the cost-state total ($0.9805172). Cost-state is Claude Code's own running total; the remaining
~1.6% token-coverage gap is tracked in T-0134 and not investigated here. Before T-0133 the estimate was
$1.3204 (+35%) because the Sonnet 5.5 cache_read price was $0.20/M instead of $0.10/M.

## Guards

`tests/test_web_perf.py` (about 2 s, no wall-clock assertions): every page and the stylesheet render under 300 KB on the fixture;
a refresh with no changed files reads 0 bytes; the DB connection count returns to 0 after 20 requests (checked by wrapping
`sqlite3.connect`; with `web/db.py` `reader()` no longer closing, the test fails with `20 != 0`).

## Render time (G-019 SC4)

Per-page server render time on the demo DB the README screenshots use (`ci/demo_seed.py` through `ci/screenshots.py seed`, fictional data),
with `web.server` in-process on 127.0.0.1:0, one discarded warm-up request and 5 timed GETs per page. Exit 1 if any median is >= 200 ms.

```
python3 ci/web_render_bench.py [--runs 5] [--limit-ms 200]
```

Measured on your-box @ `712680ae` (load from other agents running):

| route | median ms | p95 ms | bytes |
|---|---|---|---|
| / | 10.6 | 13.3 | 29090 |
| /goals | 4.4 | 6.7 | 26753 |
| /goal/G-001 | 3.5 | 5.0 | 18831 |
| /tasks | 9.6 | 15.0 | 102151 |
| /task/T-0001 | 3.2 | 4.6 | 12604 |
| /agents | 5.8 | 6.0 | 38381 |
| /resources | 6.5 | 10.3 | 35887 |
| /throughput | 5.2 | 5.5 | 21518 |
| /quality | 5.3 | 5.4 | 24099 |
| /changelog | 5.7 | 6.7 | 62956 |
| /tokens | 5.6 | 6.0 | 19054 |

`tests/test_web_render_bench.py` runs the same code on the test DB with a 1000 ms bound (CI-safe), asserts it covers every page in
`PAGES`, and asserts no page, `app.css` or `theme.js` references a non-localhost host in `src`, `href`, `url()` or `@import`
(outbound GitHub PR links are the existing exception).
