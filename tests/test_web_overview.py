"""G-019 B4: the overview cockpit (web/views/home.py) and the queries it appends to web/queries.py."""
import contextlib
import io
import json
import re
import statistics
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

from test_web_queries import NOW, ts, make_db  # noqa: E402
from test_web_server import ServerCase, get  # noqa: E402
from web import db as wdb  # noqa: E402
from web import queries, server  # noqa: E402
from web.views import home  # noqa: E402

SECTIONS = ["needs-you", "system", "goals", "agents", "shipped", "quality"]
FRESH = {"five_hour": 10.0, "seven_day": 20.0, "fresh": True, "age_min": 1}
MACHINE = {"load": [1.5, 1.0, 1.0], "cpus": 8, "mem_available_mb": 2048}


def text(markup):
    return re.sub(r"<[^>]+>", " ", str(markup))


def section(page, sid):
    m = re.search(r'<section id="%s".*?</section>' % sid, page, re.S)
    assert m, f"no section {sid}"
    return m.group(0)


class Base(unittest.TestCase):
    seed = True

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / "factory.db"
        self.w = make_db(self.path, seed=self.seed)
        self.addCleanup(self.w.close)
        p = mock.patch.object(home, "machine", lambda: MACHINE)
        p.start()
        self.addCleanup(p.stop)

    def ctx(self, usage=FRESH, mem=2048, now=NOW):
        con = wdb.connect(self.path)
        self.addCleanup(con.close)
        return SimpleNamespace(db=con, now=now, usage_fn=lambda: usage, mem_fn=lambda: mem, tokens=None,
                               defaults={"max_agents": "6"}, factory_home=None, query={})

    def page(self, **kw):
        c = self.ctx(**kw)
        d = home.data(c)
        json.dumps(d)
        return d, str(home.render(c, d))


class TestSections(Base):
    def test_six_sections_in_order(self):
        _, page = self.page()
        ids = re.findall(r'<section id="([\w-]+)" class="sect ov-sect"', page)
        self.assertEqual(ids, SECTIONS)

    def test_json_keeps_the_original_keys_and_adds_new_ones(self):
        d, _ = self.page()
        for k in ("live_agents", "queued_agents", "open_prs", "goals_by_status", "owner_pending", "usage", "utilization"):
            self.assertIn(k, d)
        self.assertEqual(d["live_agents"], 2)
        self.assertEqual(d["usage"]["five_hour"], 10.0)
        for k in ("needs_you", "system", "goals", "agents", "shipped", "quality"):
            self.assertIn(k, d)

    def test_shipped_has_the_per_day_caption(self):
        _, page = self.page()
        self.assertIn("per day", section(page, "shipped"))

    def test_shipped_lists_recent_merges_newest_first(self):
        d, _ = self.page()
        self.assertEqual([m["task_id"] for m in d["shipped"]["recent"]], ["T-5", "T-1"])
        self.assertEqual(d["shipped"]["total"], 2)

    def test_all_svgs_are_accessible(self):
        _, page = self.page()
        for svg in re.findall(r"<svg\b[^>]*>", page):
            if 'aria-hidden="true"' in svg:
                continue
            with self.subTest(svg[:80]):
                self.assertIn('role="img"', svg)
                self.assertRegex(svg, r'aria-label="[^"]+"')
        self.assertRegex(page, r'class="spark"[^>]*>.*?</svg><table class="sr-only">')

    def test_system_gauges_name_the_zone_in_text(self):
        _, page = self.page()
        s = section(page, "system")
        for label in ("Agents", "Memory", "Load", "Claude usage"):
            self.assertIn(f'<span class="gl">{label}</span>', s)
        self.assertEqual(len(re.findall(r'class="gz fg-\w+"', s)), 4)
        self.assertEqual(s.count('class="gauge"'), 5)   # 5-hour and weekly bars stacked

    def test_over_cap_agents_say_so(self):
        for i in range(6):
            self.w.execute("INSERT INTO agents(name,role,status,started) VALUES(?,?,?,?)",
                           (f"x{i}", "coder", "live", ts(11, 0)))
        _, page = self.page()
        self.assertIn(">Over cap</b>", section(page, "system"))

    def test_unknown_memory_and_usage_do_not_crash(self):
        _, page = self.page(usage=None, mem=None)
        s = section(page, "system")
        self.assertEqual(s.count(">Unknown</b>"), 2)
        self.assertIn("fill-neutral", s)

    def test_quality_tiles_and_bouncing_list(self):
        d, page = self.page()
        q = section(page, "quality")
        for label in ("Rejections per merged PR", "First-try merge rate", "Median spawn to merge", "Bouncing now"):
            self.assertIn(label, q)
        self.assertEqual(q.count('class="tile"'), 3)
        self.assertEqual([b["task_id"] for b in d["quality"]["bouncing"]], ["T-2", "T-3"])
        self.assertEqual([b["rejections"] for b in d["quality"]["bouncing"]], [1, 1])   # T-3: reject verdict + judge-reject event count once
        self.assertIn("/task/T-3", q)

    def test_agents_section_summarises_and_links_all(self):
        _, page = self.page()
        a = section(page, "agents")
        self.assertIn("2 running · 1 worker · 1 lead · 0 HQ · 1 queued", a)
        self.assertIn('href="/agents"', a)
        self.assertNotIn("more · All", a)   # 3 agents fit under the top-4 cut


class TestNeedsYou(Base):
    def test_no_message_body_and_id_once_per_row(self):
        self.w.execute("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created) VALUES(?,?,?,?,?,?,?,?)",
                       ("lead-a", "owner", "escalation", "SUBJ-X", "BODY-SECRET", "G-1", None, ts(11, 59)))
        d, page = self.page()
        self.assertIn("SUBJ-X", page)
        self.assertNotIn("BODY-SECRET", page)
        self.assertNotIn("BODY-SECRET", json.dumps(d))
        rows = re.findall(r'<li class="needs">.*?</li>', section(page, "needs-you"), re.S)
        self.assertTrue(rows)
        for row in rows:
            m = re.search(r'class="mono nid">([^<]+)<', row)
            self.assertTrue(m, row)
            self.assertEqual(text(row).count(m.group(1)), 1, row)

    def test_escalation_without_goal_or_task_shows_the_message_number(self):
        _, page = self.page()   # the seeded "boss one" escalation has neither
        self.assertRegex(section(page, "needs-you"), r"boss one.*?Message #5")

    def test_more_than_four_items_link_the_rest(self):
        d, page = self.page()
        self.assertEqual(len(d["needs_you"]), 5)
        self.assertIn("1 more", section(page, "needs-you"))
        self.assertEqual(section(page, "needs-you").count('class="needs"'), 4)

    def test_blocked_work_comes_first(self):
        d, _ = self.page()
        self.assertEqual(d["needs_you"][0]["kind"], "Escalation")   # seeded data has no owner-blocked task; first is the escalation
        self.w.execute("INSERT INTO tasks(id,goal_id,title,status,waits,created,updated) VALUES('T-9','G-1','[SC1] stuck','blocked','owner',?,?)",
                       (ts(8, 0), ts(9, 0)))
        d, _ = self.page()
        self.assertEqual((d["needs_you"][0]["kind"], d["needs_you"][0]["subject"]), ("Blocked task", "stuck"))


class TestEmpty(Base):
    seed = False

    def test_empty_db_renders_calm_lines(self):
        d, page = self.page()
        self.assertIn("Nothing needs you", page)
        self.assertIn("No merges in", page)
        self.assertIn("No open goals", page)
        self.assertNotIn("Traceback", page)
        self.assertEqual(re.findall(r'<section id="([\w-]+)"', page), SECTIONS)
        self.assertEqual(d["quality"]["median_spawn_to_merge_s"], None)

    def test_empty_db_over_http(self):
        httpd = server.make_server(self.path, "127.0.0.1", 0, (), tokens=lambda: {"rows": []}, usage_fn=lambda: FRESH,
                                   mem_fn=lambda: 2048, defaults={"max_agents": "6"})
        threading.Thread(target=httpd.serve_forever, daemon=True).start()
        self.addCleanup(lambda: (httpd.shutdown(), httpd.server_close()))
        with contextlib.redirect_stderr(io.StringIO()):
            code, _, body = get(httpd, "/")
        self.assertEqual(code, 200)
        self.assertIn("Nothing needs you", body.decode())
        self.assertNotIn("Traceback", body.decode())


class TestHttp(ServerCase):
    def test_overview_page_and_json_contract(self):
        code, _, body = self.get("/")
        self.assertEqual(code, 200)
        self.assertEqual(re.findall(r'<section id="([\w-]+)" class="sect ov-sect"', body.decode()), SECTIONS)
        d = json.loads(self.get("/api/home.json")[2])
        for k in ("live_agents", "queued_agents", "open_prs", "goals_by_status", "owner_pending", "usage"):
            self.assertIn(k, d)


class TestSpeed(Base):
    def test_median_of_five_renders_under_200_ms(self):
        c = self.ctx()
        times = []
        for _ in range(5):
            t0 = time.perf_counter()
            home.render(c, home.data(c))
            times.append(time.perf_counter() - t0)
        med = statistics.median(times)
        print(f"overview render times ms: {[round(t * 1000, 1) for t in times]} median {med * 1000:.1f}")
        self.assertLess(med, 0.2)


class TestQueries(Base):
    def test_recent_merges_dedupes_and_orders(self):
        self.w.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                       (ts(11, 50), "judge", "task", "T-1", "merged", "@feedface"))
        m = queries.recent_merges(self.ctx().db, 5)
        self.assertEqual([x["task_id"] for x in m], ["T-1", "T-5"])
        self.assertEqual(m[0]["sha"], "feedface")
        self.assertEqual(m[1]["title"], "ship it")   # [SC1] tag stripped
        self.assertEqual(len(queries.recent_merges(self.ctx().db, 1)), 1)

    def test_merges_per_day_lists_every_day(self):
        days = queries.merges_per_day(self.ctx().db, 14, now=NOW)
        self.assertEqual(len(days), 14)
        self.assertEqual(days[-1], {"day": "2026-10-24", "merges": 2})
        self.assertEqual(sum(d["merges"] for d in days), 2)

    def test_last_activity_by_goal_and_agent(self):
        last = queries.last_activity_by_goal(self.ctx().db)
        self.assertEqual(last["G-1"], ts(11, 36))
        ev = queries.last_event_by_agent(self.ctx().db)
        self.assertEqual(ev["lead-a"], ts(10, 50))
        self.assertNotIn("coder-t-1", ev)   # ended agents are not listed

    def test_bouncing_excludes_done_and_cancelled(self):
        ids = [b["task_id"] for b in queries.bouncing_tasks(self.ctx().db)]
        self.assertNotIn("T-1", ids)    # merged, though it was rejected once
        self.assertEqual(ids, ["T-2", "T-3"])

    def test_message_refs_returns_ids_only(self):
        refs = queries.message_refs(self.ctx().db, [1, 5])
        self.assertEqual(refs, {1: ("G-1", None), 5: (None, None)})


class TestMachinePin(unittest.TestCase):
    """The demo/screenshot server pins the host's load so the System card shows the same state on any machine."""

    def test_pinned_load_is_used(self):
        from web.views import resources
        with mock.patch.dict("os.environ", {"FACTORY_LOADAVG": "1.4"}):
            self.assertEqual(resources.machine()["load"], [1.4, 1.4, 1.4])

    def test_unpinned_load_is_the_hosts(self):
        from web.views import resources
        env = {k: v for k, v in __import__("os").environ.items() if k != "FACTORY_LOADAVG"}
        with mock.patch.dict("os.environ", env, clear=True), mock.patch("os.getloadavg", return_value=(2.0, 3.0, 4.0)):
            self.assertEqual(resources.machine()["load"], [2.0, 3.0, 4.0])


if __name__ == "__main__":
    unittest.main()
