"""Tests for web/views/agents.py and web/views/resources.py (teams/agents, derived gate, timeline, /proc cache)."""
import http.client
import json
import os
import re
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_queries import CANARY, NOW, make_db, ts  # noqa: E402
from web import db as wdb, server  # noqa: E402
from web import html as wh  # noqa: E402
from web.views import agents, resources  # noqa: E402

XSS = "<script>alert(1)</script>"
DEFAULTS = {"max_live_agents": "6", "max_total_claude": "10", "min_mem_available_mb": "1024",
            "max_five_hour_pct": "92", "max_seven_day_pct": "92"}
FRESH = {"five_hour": 10.0, "seven_day": 20.0, "fresh": True, "age_min": 1}


def fake_proc(root, procs):
    """procs: {pid: (argv, ppid, rss_kb)} written like /proc/<pid>/{cmdline,stat,status}."""
    for pid, (argv, ppid, rss) in procs.items():
        d = Path(root) / str(pid)
        d.mkdir()
        (d / "cmdline").write_bytes(b"\0".join(a.encode() for a in argv) + b"\0")
        (d / "stat").write_text(f"{pid} (claude) S {ppid} 0 0\n")
        (d / "status").write_text(f"Name:\tclaude\nVmRSS:\t{rss} kB\n")


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / "factory.db"
        self.w = make_db(self.path)
        self.addCleanup(self.w.close)
        self.proc = Path(self.tmp.name) / "proc"
        self.proc.mkdir()
        p = mock.patch.dict(os.environ, {"FACTORY_PROC_DIR": str(self.proc)})
        p.start()
        self.addCleanup(p.stop)
        agents._cache.update(at=None, value=None)
        self.addCleanup(agents._cache.update, at=None, value=None)

    def ctx(self, query=None, usage=FRESH, mem=4000, now=NOW):
        con = wdb.connect(self.path)
        self.addCleanup(con.close)
        return SimpleNamespace(db=con, now=now, usage_fn=lambda: usage, mem_fn=lambda: mem, tokens=None,
                               defaults=DEFAULTS, factory_home=None, query=query or {})

    def page(self, view, **kw):
        c = self.ctx(**kw)
        d = view.data(c)
        json.dumps(d)
        return d, str(view.render(c, d))


class TestAgents(Base):
    def test_team_scope_cut_to_120_and_escaped(self):
        self.w.execute("UPDATE teams SET scope=? WHERE id='t-a'", (XSS + "x" * 500,))
        d, html = self.page(agents)
        scope = next(t for t in d["teams"] if t["id"] == "t-a")["scope"]
        self.assertLessEqual(len(scope), 120)
        self.assertNotIn("<script>", html)
        self.assertNotIn("x" * 130, json.dumps(d))

    def test_counts_match_group_by_and_teams_listed(self):
        d, html = self.page(agents)
        want = dict(self.w.execute("SELECT status, count(*) FROM agents GROUP BY status"))
        self.assertEqual(d["counts"], want)
        self.assertEqual(len(d["live"]), want["live"])
        self.assertEqual(len(d["queued"]), want["queued"])
        self.assertEqual({t["id"] for t in d["teams"]}, {"t-a", "t-b"})
        self.assertEqual(next(t for t in d["teams"] if t["id"] == "t-a")["live_workers"], 2)
        for needle in ("t-a", "lead-b", "coder-2", "sonnet", "queued-x", "gate closed: low RAM", "coder-t-1"):
            self.assertIn(needle, html)
        self.assertRegex(html, r'href="/task/T-2"')

    def test_stop_requested_flag_and_age(self):
        self.w.execute("UPDATE agents SET stop_requested_at=? WHERE name='coder-2'", (ts(11, 30),))
        d, html = self.page(agents)
        self.assertTrue(next(a for a in d["live"] if a["name"] == "coder-2")["stop_requested"])
        self.assertIn("stop requested", html)
        self.assertEqual(next(a for a in d["live"] if a["name"] == "coder-2")["age_s"], 3600)

    def test_ended_only_last_24h_summary_truncated_and_escaped(self):
        self.w.execute("UPDATE agents SET summary=? WHERE name='coder-t-1'", (XSS + "x" * 500,))
        self.w.execute("INSERT INTO agents(name,role,status,started,ended) VALUES('old-one','coder','done',?,?)",
                       (ts(9, 0, day=20), ts(10, 0, day=20)))
        d, html = self.page(agents)
        names = {a["name"] for a in d["ended"]}
        self.assertIn("coder-t-1", names)
        self.assertNotIn("old-one", names)
        s = next(a for a in d["ended"] if a["name"] == "coder-t-1")["summary"]
        self.assertLessEqual(len(s), 200)
        self.assertNotIn("<script>", html)
        self.assertIn("&lt;script&gt;", html)
        self.assertIn("<details>", html)

    def test_no_kickoff_or_spawn_args_anywhere(self):
        d, html = self.page(agents)
        blob = json.dumps(d) + html
        self.assertNotIn(CANARY, blob)
        self.assertNotIn("kickoff", blob)
        self.assertNotIn("spawn_args", blob)

    def test_rss_found_by_session_flag_and_missing_shows_dash(self):
        fake_proc(self.proc, {101: (["/usr/bin/claude", "--session-id", "s2"], 1, 204800),
                              102: (["node", "x"], 1, 5)})
        d, html = self.page(agents)
        by = {a["name"]: a for a in d["live"]}
        self.assertEqual(by["lead-a"]["rss_mb"], 200)
        self.assertIsNone(by["coder-2"]["rss_mb"])   # no session id
        self.assertIn("200 MB", html)
        self.assertIn("—", html)

    def test_resume_flag_also_matches(self):
        fake_proc(self.proc, {7: (["claude", "--resume=s2"], 1, 1024)})
        d, _ = self.page(agents)
        self.assertEqual(next(a for a in d["live"] if a["name"] == "lead-a")["rss_mb"], 1)

    def test_proc_scan_cached_30s(self):
        calls = []
        real = agents._scan
        with mock.patch.object(agents, "_scan", side_effect=lambda: calls.append(1) or real()):
            clock = [100.0]
            with mock.patch.object(agents, "_clock", lambda: clock[0]):
                self.page(agents)
                self.page(agents)
                self.page(resources)
                self.assertEqual(len(calls), 1)
                clock[0] += 29
                self.page(agents)
                self.assertEqual(len(calls), 1)
                clock[0] += 2
                self.page(agents)
                self.assertEqual(len(calls), 2)

    def test_unreadable_proc_dir_renders(self):
        with mock.patch.dict(os.environ, {"FACTORY_PROC_DIR": str(self.proc / "nope")}):
            d, html = self.page(agents)
        self.assertFalse(d["proc_scan"])
        self.assertIn("coder-2", html)

    def test_empty_db_renders_empty_states(self):
        self.w.executescript("DELETE FROM agents; DELETE FROM teams;")
        d, html = self.page(agents)
        self.assertIn("No live agents.", html)
        self.assertIn("Nothing is queued.", html)
        self.assertIn("No data yet.", html)


class TestGate(Base):
    def gate(self, **kw):
        return resources.derive_gate(self.ctx(**kw))

    def test_closed_when_live_at_cap(self):
        for i in range(4):
            self.w.execute("INSERT INTO agents(name,role,status,started) VALUES(?,?,'live',?)", (f"x{i}", "coder", ts(11, 0)))
        g = self.gate()
        self.assertFalse(g["open"])
        self.assertTrue(g["text"].startswith("likely CLOSED — 6/6"), g["text"])

    def test_open_otherwise(self):
        g = self.gate()
        self.assertTrue(g["open"])
        self.assertEqual(g["text"], "likely OPEN")

    def test_cap_from_config_over_default(self):
        self.w.execute("INSERT INTO config VALUES('max_live_agents','2')")
        self.assertIn("2/2", self.gate()["text"])

    def test_low_memory_and_usage_reasons_in_order(self):
        self.assertIn("MemAvailable 500MB < floor 1024MB", self.gate(mem=500)["text"])
        self.assertIn("5h usage 95% ≥ 92%", self.gate(usage={**FRESH, "five_hour": 95.0})["text"])
        self.assertIn("weekly usage 93% ≥ 92%", self.gate(usage={**FRESH, "seven_day": 93.0})["text"])
        both = self.gate(mem=500, usage={**FRESH, "five_hour": 95.0})
        self.assertIn("MemAvailable", both["text"])

    def test_total_claude_cap(self):
        fake_proc(self.proc, {i: (["claude"], 1, 10) for i in range(10, 20)})
        self.assertIn("10/10 claude processes", self.gate()["text"])

    def test_child_claude_not_counted(self):
        fake_proc(self.proc, {10: (["claude"], 1, 10), 11: (["claude", "-p"], 10, 10)})
        self.assertIn("claude processes 1/10", " ".join(self.gate()["checks"]))

    def test_stale_and_unreadable_usage_like_factory_res(self):
        stale = {**FRESH, "five_hour": 99.0, "fresh": False, "age_min": 400}
        g = self.gate(usage=stale)
        self.assertTrue(g["open"])    # stale usage never closes the gate
        self.assertIn("(stale: 400 min old)", " ".join(g["checks"]))
        unread = {"five_hour": None, "seven_day": None, "fresh": False, "age_min": -1, "unreadable": True}
        g = self.gate(usage=unread)
        self.assertTrue(g["open"])
        self.assertIn("claude usage unreadable", g["checks"])

    def test_rendered_label(self):
        _, html = self.page(resources)
        self.assertIn("derived (no herdr check)", html)
        self.assertIn("likely OPEN", html)


class TestResources(Base):
    def test_machine_config_and_perday(self):
        d, html = self.page(resources)
        self.assertGreater(d["machine"]["mem_available_mb"], 0)
        self.assertIn("MemAvailable", html)
        keys = {r["key"] for r in d["config"]}
        self.assertTrue({"max_agents", "judge_mode", "max_live_agents"} <= keys)
        self.assertEqual(sum(x["merges"] for x in d["per_day"]), 2)
        self.assertEqual(sum(x["queued"] for x in d["per_day"]), 2)
        self.assertEqual(sum(x["escalations"] for x in d["per_day"]), 4)

    def test_per_day_bars_show_counts(self):
        d, html = self.page(resources)
        found = re.findall(r'aria-label="(\d+) of max (\d+)"><span class="mono">([^<]*)</span>', html)
        self.assertEqual(len(found), 4 * len(d["per_day"]))
        for n, mx, shown in found:
            self.assertEqual(shown, n)
            self.assertLessEqual(int(n), int(mx))
        self.assertIn("2 of max 2", html)   # the merges column peaks at 2 on one day

    def test_timeline_contents_and_no_bodies(self):
        d, html = self.page(resources)
        for needle in ("queued-x queued: gate closed: low RAM", "config max_agents = 6", "task T-1 merged",
                       "task T-3 judge-reject", "boss one", "lead-a → owner", "timeline-item", "badge"):
            self.assertIn(needle, html)
        self.assertNotIn(CANARY, json.dumps(d) + html)
        self.assertNotIn("old style", html)      # judge-reject reason is free text: not rendered
        self.assertNotIn("checks not green", html)
        subj = [i["text"] for i in d["timeline"]["items"] if "lead-a → owner: E" in i["text"]][0]
        self.assertLessEqual(len(subj), 150)

    def test_only_last_7_days(self):
        self.w.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                       (ts(9, 0, day=10), "d", "agent", "ancient", "spawn", None))
        d, html = self.page(resources)
        self.assertNotIn("ancient", html)

    def test_paging_before_and_invalid_ignored(self):
        for i in range(60):
            self.w.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                           (ts(5, 0, day=24), "d", "agent", f"bulk{i:02d}", "spawn", None))
        d, html = self.page(resources)
        nb = d["timeline"]["next_before"]
        self.assertIsNotNone(nb)
        self.assertIn(f"/resources?before={nb}", html)
        d2, html2 = self.page(resources, query={"before": str(nb)})
        txt = {i["text"] for i in d["timeline"]["items"]}
        txt2 = {i["text"] for i in d2["timeline"]["items"]}
        self.assertTrue(txt2)
        self.assertFalse(txt & txt2)
        self.assertIn("Newest", html2)
        for bad in ("abc", "-1", "1e3", "9" * 40, "", "1; DROP TABLE events", "²", "٣", "①", "99999999999999999999", "1e9", "5", "1-2-3", "1-1-x", "٣-1-1"):
            dd, _ = self.page(resources, query={"before": bad})
            self.assertEqual(dd["timeline"]["items"], d["timeline"]["items"], bad)

    def test_paging_does_not_drop_events_or_messages(self):
        n = resources.PAGE_SIZE * 2 + 5
        for i in range(n):
            self.w.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                           (ts(4, 0, day=24), "d", "agent", f"pg{i:03d}", "spawn", None))
            self.w.execute("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES(?,?,?,?,?,?)",
                           ("a", "b", "report", f"msg{i:03d}", CANARY, ts(4, 0, day=24)))
        seen, before = [], None
        for _ in range(20):
            d, _h = self.page(resources, query={"before": str(before)} if before else {})
            seen += [i["text"] for i in d["timeline"]["items"]]
            before = d["timeline"]["next_before"]
            if before is None:
                break
        for i in range(n):
            self.assertEqual(sum(1 for t in seen if t.startswith(f"pg{i:03d} ")), 1, i)
            self.assertEqual(sum(1 for t in seen if t.endswith(f"msg{i:03d}")), 1, i)

    def test_timeline_scan_is_bounded_by_the_window(self):
        for i in range(300):
            self.w.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                           (ts(9, 0, day=1), "d", "agent", f"old{i}", "spawn", None))
            self.w.execute("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES(?,?,?,?,?,?)",
                           ("a", "b", "report", f"old{i}", CANARY, ts(9, 0, day=1)))
        fetched = []

        class Counting:
            def __init__(self, con):
                self.con = con

            def execute(self, *a):
                for r in self.con.execute(*a):
                    fetched.append(r)
                    yield r
        c = self.ctx()
        out = resources.timeline(Counting(c.db), c.now)
        self.assertFalse([i for i in out["items"] if i["text"].startswith("old") or i["text"].endswith("old1")])
        self.assertLess(len(fetched), 100)   # the 600 ancient rows are filtered in SQL, not in Python

    def test_thousand_messages_in_one_window_all_reachable_via_more_links(self):
        for i in range(1000):
            self.w.execute("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES(?,?,?,?,?,?)",
                           ("a", "b", "report", f"bulk{i:04d}", CANARY, ts(4, 0, day=24)))
        seen, query, pages = [], {}, 0
        while True:
            d, html = self.page(resources, query=query)
            pages += 1
            seen += [i["text"] for i in d["timeline"]["items"]]
            nb = d["timeline"]["next_before"]
            if nb is None:
                self.assertNotIn(">More<", html)
                break
            self.assertIn(f"/resources?before={nb}", html)
            query = {"before": nb}
            self.assertLess(pages, 100)
        bulk = [t for t in seen if "bulk" in t]
        self.assertEqual(len(bulk), 1000)
        self.assertEqual(len(set(bulk)), 1000)

    def test_hostile_values_escaped(self):
        self.w.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                       (ts(11, 50), "d", "agent", XSS, "queued", XSS))
        self.w.execute("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES(?,?,?,?,?,?)",
                       (XSS, "boss", "decision", XSS, CANARY, ts(11, 51)))
        self.w.execute("INSERT INTO config VALUES(?,?)", (XSS, XSS))
        _, html = self.page(resources)
        self.assertNotIn("<script>", html)
        self.assertIn("&lt;script&gt;", html)
        self.assertNotIn(CANARY, html)

    def test_empty_db(self):
        self.w.executescript("DELETE FROM events; DELETE FROM messages; DELETE FROM config; DELETE FROM agents;")
        _, html = self.page(resources)
        self.assertIn("No data yet.", html)

    def test_no_usage_or_mem_fn(self):
        c = self.ctx()
        c.usage_fn = c.mem_fn = None
        d = resources.data(c)
        self.assertIn("unknown", " ".join(d["gate"]["checks"]))
        str(resources.render(c, d))


class TestNoHerdrAndServer(Base):
    def test_no_herdr_or_subprocess_calls(self):
        stub = Path(self.tmp.name) / "bin"
        stub.mkdir()
        log = Path(self.tmp.name) / "herdr.log"
        (stub / "herdr").write_text(f"#!/bin/sh\necho call >> {log}\n")
        (stub / "herdr").chmod(0o755)
        boom = mock.Mock(side_effect=AssertionError("subprocess used"))
        with mock.patch.dict(os.environ, {"PATH": str(stub)}), mock.patch.object(subprocess, "run", boom), \
                mock.patch.object(subprocess, "Popen", boom), mock.patch.object(subprocess, "check_output", boom):
            self.page(agents)
            self.page(resources)
        self.assertFalse(log.exists())
        boom.assert_not_called()

    def test_source_has_no_herdr_or_gate_calls(self):
        import ast
        for f in ("agents", "resources"):
            tree = ast.parse((ROOT / "web" / "views" / f"{f}.py").read_text())
            names = {n.func.id if isinstance(n.func, ast.Name) else getattr(n.func, "attr", "")
                     for n in ast.walk(tree) if isinstance(n, ast.Call)}
            self.assertFalse(names & {"herdr", "gate", "run", "Popen", "system"}, f)
            mods = {a.name for n in ast.walk(tree) if isinstance(n, ast.Import) for a in n.names}
            self.assertNotIn("subprocess", mods, f)

    def test_served_pages_and_api(self):
        httpd = server.make_server(self.path, "127.0.0.1", 0, usage_fn=lambda: FRESH, mem_fn=lambda: 4000, defaults=DEFAULTS)
        t = threading.Thread(target=httpd.serve_forever, daemon=True)
        t.start()
        self.addCleanup(httpd.server_close)
        self.addCleanup(httpd.shutdown)
        port = httpd.server_address[1]
        for path in ("/agents", "/api/agents.json", "/resources", "/api/resources.json", "/resources?before=zz",
                     *(f"/resources?before={b}" for b in ("%C2%B2", "-1", "1e9", "99999999999999999999"))):
            c = http.client.HTTPConnection("127.0.0.1", port)
            t0 = time.time()
            c.request("GET", path, headers={"Host": f"127.0.0.1:{port}"})
            r = c.getresponse()
            body = r.read().decode()
            self.assertEqual(r.status, 200, path)
            self.assertLess(time.time() - t0, 1.0, path)
            self.assertNotIn(CANARY, body)
            if not path.startswith("/api"):
                self.assertIn('name="viewport"', body)
                used = {c for m in re.findall(r'class="([^"]*)"', body) for c in m.split()}
                self.assertEqual(used - wh.CLASSES, set(), path)
                self.assertNotIn("<script", body.replace('<script src="/static/theme.js" defer></script>', ""))  # only the deferred theme toggle
                self.assertNotIn("style=", body)


if __name__ == "__main__":
    unittest.main()
