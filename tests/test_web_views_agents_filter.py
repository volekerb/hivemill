"""Tests for the /agents filters (?status=, ?role=, ?goal=), status counts, blocked marker and the JSON API."""
import http.client
import json
import re
import sys
import threading
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_queries import CANARY  # noqa: E402
from test_web_views_agents import DEFAULTS, FRESH, Base  # noqa: E402
from web import html as wh, server  # noqa: E402
from web.views import agents  # noqa: E402


class FilterBase(Base):
    def setUp(self):
        super().setUp()
        # coder-2 (live, coder) is blocked since a fixed epoch; add a G-2 lead so goal filters differ
        self.w.execute("UPDATE agents SET blocked_since=1792000000 WHERE name='coder-2'")
        self.w.execute("INSERT INTO agents(name,role,goal_id,team_id,status,started) VALUES('lead-g2','lead','G-2','t-b','live',?)",
                       ("2026-10-24T09:00:00+00:00",))

    def sql(self, where, args=()):
        return {r[0] for r in self.w.execute(f"SELECT name FROM agents WHERE {where}", args)}

    def names(self, **q):
        d, _ = self.page(agents, query=q)
        return {a["name"] for a in d["matches"]}


class TestFilters(FilterBase):
    def test_status_live_matches_sql(self):
        self.assertEqual(self.names(status="live"), self.sql("status='live'"))

    def test_status_comma_separated(self):
        self.assertEqual(self.names(status="live,queued"), self.sql("status IN ('live','queued')"))

    def test_role_alone(self):
        self.assertEqual(self.names(role="coder"), self.sql("role='coder'"))

    def test_goal_alone(self):
        self.assertEqual(self.names(goal="G-2"), self.sql("goal_id='G-2'"))
        self.assertNotEqual(self.names(goal="G-2"), set())

    def test_combined_is_and_across_filters(self):
        self.assertEqual(self.names(status="live", role="coder", goal="G-1"),
                         self.sql("status='live' AND role='coder' AND goal_id='G-1'"))
        self.assertEqual(self.names(status="done,live", role="lead"), self.sql("status IN ('done','live') AND role='lead'"))

    def test_no_filter_is_default_view(self):
        d, html = self.page(agents)
        self.assertEqual(d["filters"], {})
        self.assertEqual(d["matches"], [])
        self.assertIn("Live agents", html)
        self.assertNotIn("Matching agents", html)

    def test_bad_values_ignored_with_200_and_escaped(self):
        for q in ({"status": "bogus"}, {"goal": "../x"}, {"role": "<script>alert(1)</script>"},
                  {"status": "live,bogus", "goal": "G-1;DROP"}):
            d, html = self.page(agents, query=q)
            self.assertNotIn("<script>alert", html)
            self.assertNotIn("../x", html)
        d, html = self.page(agents, query={"status": "bogus", "role": "<script>alert(1)</script>"})
        self.assertEqual(d["filters"], {})
        self.assertIn("Live agents", html)
        d, _ = self.page(agents, query={"status": "live,bogus"})
        self.assertEqual(d["filters"], {"status": ["live"]})

    def test_non_string_query_value_ignored(self):
        d, _ = self.page(agents, query={"status": None, "role": 5})
        self.assertEqual(d["filters"], {})


class TestCountsAndLinks(FilterBase):
    def test_counts_unfiltered_equal_group_by_and_link(self):
        want = dict(self.w.execute("SELECT status, count(*) FROM agents GROUP BY status"))
        d, html = self.page(agents, query={"status": "queued"})
        self.assertEqual(d["counts"], want)   # counts ignore the active filter
        for s, n in want.items():
            self.assertRegex(html, rf'href="/agents\?status={s}[^"]*"[^>]*>(<span[^>]*>)?{s} {n}<')

    def test_status_set_comes_from_the_table_incl_starting(self):
        self.w.execute("INSERT INTO agents(name,role,goal_id,team_id,status) VALUES('start-x','coder','G-1','t-a','starting')")
        want = dict(self.w.execute("SELECT status, count(*) FROM agents GROUP BY status"))
        self.assertEqual(want["starting"], 1)
        d, html = self.page(agents)
        self.assertEqual(d["counts"], want)
        self.assertRegex(html, r'href="/agents\?status=starting"[^>]*>(<span[^>]*>)?starting 1<')
        self.assertEqual(self.names(status="starting"), {"start-x"})
        d, _ = self.page(agents, query={"status": "starting,bogus"})
        self.assertEqual(d["filters"], {"status": ["starting"]})

    def test_filters_persist_in_links_and_clear_removes_all(self):
        _, html = self.page(agents, query={"role": "coder", "goal": "G-1"})
        self.assertIn('href="/agents?status=live&amp;role=coder&amp;goal=G-1"', html)
        self.assertIn('href="/agents?role=lead&amp;goal=G-1"', html)
        self.assertIn('href="/agents?role=coder&amp;goal=G-2"', html)
        self.assertIn("Showing: role=coder, goal=G-1", html)
        self.assertIn('<a href="/agents">clear</a>', html)

    def test_comma_filters_roundtrip_through_link(self):
        _, html = self.page(agents, query={"status": "live,queued", "role": "coder"})
        self.assertIn("Showing: status=live,queued, role=coder", html)
        self.assertIn('href="/agents?status=blocked&amp;role=coder"', html)

    def test_role_and_goal_link_lists(self):
        d, _ = self.page(agents)
        self.assertEqual(d["roles"], dict(self.w.execute("SELECT role, count(*) FROM agents GROUP BY role")) | {})
        self.assertEqual(d["goals"], dict(self.w.execute("SELECT goal_id, count(*) FROM agents WHERE goal_id IS NOT NULL GROUP BY goal_id")))


class TestBlocked(FilterBase):
    def test_blocked_filter_matches_blocked_since_rows(self):
        self.assertEqual(self.names(status="blocked"), self.sql("blocked_since IS NOT NULL"))
        self.assertEqual(self.names(status="blocked"), {"coder-2"})

    def test_blocked_with_other_status_is_or(self):
        self.assertEqual(self.names(status="blocked,queued"), {"coder-2", "queued-x"})

    def test_blocked_badge_and_count(self):
        d, html = self.page(agents, query={"status": "blocked"})
        self.assertEqual(d["blocked"], 1)
        self.assertRegex(html, r"blocked since \d\d:\d\d")
        self.assertIn("blocked 1", html)
        _, default = self.page(agents)
        self.assertRegex(default, r"blocked since \d\d:\d\d")   # live table shows it too

    def test_page_says_herdr_states_not_shown(self):
        for q in ({}, {"status": "live"}):
            _, html = self.page(agents, query=q)
            self.assertIn("herdr working/idle is not shown", html)

    def test_empty_and_garbage_blocked_since_agree_everywhere(self):
        self.w.execute("INSERT INTO agents(name,role,goal_id,team_id,status,started,blocked_since) VALUES('empty-blk','coder','G-1','t-a','live',?,'')",
                       ("2026-10-24T09:00:00+00:00",))
        self.w.execute("INSERT INTO agents(name,role,goal_id,team_id,status,started,blocked_since) VALUES('junk-blk','coder','G-1','t-a','live',?,'garbage')",
                       ("2026-10-24T09:00:00+00:00",))
        d, html = self.page(agents, query={"status": "blocked"})
        names = {a["name"] for a in d["matches"]}
        self.assertEqual(names, {"coder-2", "junk-blk"})   # '' is not blocked
        self.assertEqual(d["blocked"], len(names))   # count == list
        self.assertIn(f"blocked {len(names)}<", html)
        self.assertIn("blocked (time unknown)", html)
        self.assertNotIn("since ?", html)
        _, default = self.page(agents)
        self.assertEqual(default.count("blocked (time unknown)"), 1)   # '' gets no badge in the live table

    def test_no_blocked_since_column_still_works(self):
        self.w.execute("ALTER TABLE agents DROP COLUMN blocked_since")
        d, _ = self.page(agents, query={"status": "blocked"})
        self.assertEqual(d["matches"], [])
        self.assertEqual(d["blocked"], 0)


class TestSafety(FilterBase):
    def test_no_canary_and_classes_in_vocabulary(self):
        d, html = self.page(agents, query={"status": "live,blocked", "role": "coder"})
        self.assertNotIn(CANARY, json.dumps(d) + html)
        used = {c for m in re.findall(r'class="([^"]*)"', html) for c in m.split()}
        self.assertEqual(used - wh.CLASSES, set())

    def test_xss_in_agent_name_escaped(self):
        self.w.execute("UPDATE agents SET name=? WHERE name='queued-x'", ("<b>x</b>",))
        _, html = self.page(agents, query={"status": "queued"})
        self.assertNotIn("<b>x</b>", html)
        self.assertIn("&lt;b&gt;x&lt;/b&gt;", html)


class TestServed(FilterBase):
    def get(self, path):
        httpd = server.make_server(self.path, "127.0.0.1", 0, usage_fn=lambda: FRESH, mem_fn=lambda: 4000, defaults=DEFAULTS)
        threading.Thread(target=httpd.serve_forever, daemon=True).start()
        self.addCleanup(httpd.server_close)
        self.addCleanup(httpd.shutdown)
        port = httpd.server_address[1]
        c = http.client.HTTPConnection("127.0.0.1", port)
        c.request("GET", path, headers={"Host": f"127.0.0.1:{port}"})
        r = c.getresponse()
        return r.status, r.read().decode()

    def test_api_applies_filters(self):
        status, body = self.get("/api/agents.json?status=live&role=coder")
        self.assertEqual(status, 200)
        d = json.loads(body)
        self.assertEqual({a["name"] for a in d["matches"]}, self.sql("status='live' AND role='coder'"))
        self.assertEqual(d["filters"], {"status": ["live"], "role": ["coder"]})
        self.assertEqual({a["name"] for a in d["live"]}, self.sql("status='live' AND role='coder'"))

    def test_api_blocked_and_bad_values(self):
        status, body = self.get("/api/agents.json?status=blocked")
        self.assertEqual({a["name"] for a in json.loads(body)["matches"]}, self.sql("blocked_since IS NOT NULL"))
        for q in ("status=bogus", "goal=..%2Fx", "role=%3Cscript%3E"):
            status, body = self.get("/agents?" + q)
            self.assertEqual(status, 200, q)
            self.assertNotIn("<script>", body)
            status, body = self.get("/api/agents.json?" + q)
            self.assertEqual(json.loads(body)["filters"], {})

    def test_served_page_filtered(self):
        status, body = self.get("/agents?status=live,queued&role=coder")
        self.assertEqual(status, 200)
        self.assertIn("Showing: status=live,queued, role=coder", body)
        self.assertIn("coder-2", body)
        self.assertNotIn("lead-g2", body)


if __name__ == "__main__":
    unittest.main()
