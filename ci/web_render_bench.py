#!/usr/bin/env python3
"""Per-page server render time of the web UI on the demo DB (G-019 SC4).

  python3 ci/web_render_bench.py [--runs 5] [--limit-ms 200]

Seeds the demo home the README screenshots use (ci/screenshots.py seed -> ci/demo_seed.py) into a temp dir, starts web.server
in-process on 127.0.0.1:0, requests every page route of web.views.ROUTES `--runs` times after one warm-up request, and prints
route, median ms, p95 ms and bytes. Exit 0 when every median is under --limit-ms, 1 when one is not, 2 on a setup error.
The live home is never touched. Stdlib only.
"""
import argparse
import contextlib
import http.client
import io
import math
import shutil
import sqlite3
import statistics
import sys
import tempfile
import threading
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "ci"))

from web import server  # noqa: E402
from web.tokens import TokenIndex  # noqa: E402
from web.views import ROUTES  # noqa: E402

RUNS = 5
LIMIT_MS = 200


class SetupError(Exception):
    pass


def page_routes(db_path):
    """Every page route of web.views.ROUTES with `<id>` filled from the first goal / task of the DB (read-only)."""
    try:
        con = sqlite3.connect("file:%s?mode=ro" % db_path, uri=True)
        try:
            ids = {"goal": con.execute("SELECT id FROM goals ORDER BY id LIMIT 1").fetchone(),
                   "task": con.execute("SELECT id FROM tasks ORDER BY id LIMIT 1").fetchone()}
        finally:
            con.close()
    except sqlite3.Error as e:
        raise SetupError("cannot read goal/task ids from %s: %s" % (db_path, e))
    out = []
    for route in ROUTES:
        if "<id>" in route:
            row = ids[route.split("/")[1]]
            if not row:
                raise SetupError("%s has no row to open %s with." % (db_path, route))
            route = route.replace("<id>", row[0])
        out.append(route)
    return out


def fetch(port, path):
    c = http.client.HTTPConnection("127.0.0.1", port, timeout=30)
    try:
        t0 = time.perf_counter()
        c.request("GET", path, headers={"Host": "127.0.0.1:%d" % port})
        r = c.getresponse()
        body = r.read()
        ms = (time.perf_counter() - t0) * 1000
        if r.status != 200:
            raise SetupError("GET %s answered %d, expected 200." % (path, r.status))
        return ms, len(body)
    finally:
        c.close()


def p95(times):
    s = sorted(times)
    return s[max(0, math.ceil(0.95 * len(s)) - 1)]


def bench(port, routes, runs=RUNS):
    """[(route, median ms, p95 ms, bytes)] after one discarded warm-up request per route."""
    rows = []
    for r in routes:
        fetch(port, r)
        got = [fetch(port, r) for _ in range(runs)]
        times = [ms for ms, _ in got]
        rows.append((r, statistics.median(times), p95(times), got[-1][1]))
    return rows


def table(rows):
    w = max(len(r[0]) for r in rows + [("route",)])
    lines = ["%-*s  %10s  %8s  %8s" % (w, "route", "median ms", "p95 ms", "bytes")]
    lines += ["%-*s  %10.1f  %8.1f  %8d" % (w, r, med, hi, n) for r, med, hi, n in rows]
    return "\n".join(lines)


def slow(rows, limit_ms=LIMIT_MS):
    return [r for r in rows if r[1] >= limit_ms]


@contextlib.contextmanager
def serving(db_path, projects=None):
    """web.server in-process on 127.0.0.1:0; yields the port. Fixed usage and memory callbacks, no network."""
    index = TokenIndex(projects or Path(db_path).parent / "no-projects")
    index.refresh()
    httpd = server.make_server(db_path, "127.0.0.1", 0, (), tokens=index.snapshot,
                               usage_fn=lambda: {"five_hour": 10.0, "seven_day": 20.0, "fresh": True, "age_min": 1},
                               mem_fn=lambda: 2048, defaults={"max_agents": "6"})
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    try:
        yield httpd.server_address[1]
    finally:
        httpd.shutdown()
        httpd.server_close()


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--runs", type=int, default=RUNS)
    ap.add_argument("--limit-ms", type=float, default=LIMIT_MS)
    a = ap.parse_args(argv)
    if a.runs < 1:
        ap.error("--runs must be >= 1")
    tmp = Path(tempfile.mkdtemp(prefix="web-render-bench-"))
    try:
        import screenshots  # noqa: E402  (the demo DB path the README screenshots use)
        demo = tmp / "demo"
        try:
            screenshots.seed(demo, screenshots.current_hour())
            db = demo / "db" / "factory.db"
            routes = page_routes(db)
            with serving(db, demo / "home" / ".claude" / "projects") as port:
                with contextlib.redirect_stderr(io.StringIO()):  # the server logs every request to stderr
                    rows = bench(port, routes, a.runs)
        except (screenshots.Failed, SetupError, OSError) as e:
            print("web_render_bench: %s" % e, file=sys.stderr)
            return 2
        print(table(rows))
        bad = slow(rows, a.limit_ms)
        for r, med, _, _ in bad:
            print("SLOW %s: median %.1f ms >= %g ms" % (r, med, a.limit_ms), file=sys.stderr)
        return 1 if bad else 0
    finally:
        shutil.rmtree(tmp, True)


if __name__ == "__main__":
    sys.exit(main())
