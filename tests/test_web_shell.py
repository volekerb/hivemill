"""App shell (G-019 B2): every route renders the shared shell, the nav works without JS, nav badges mean only
"needs attention", enum labels are human, and shell_status picks the health state."""
import contextlib
import datetime as dt
import io
import re
import sqlite3
import sys
import tempfile
import threading
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_queries import make_db  # noqa: E402
from test_web_server import PAGES, ServerCase, get  # noqa: E402
from web import html as wh  # noqa: E402
from web import queries, server  # noqa: E402

NOW = dt.datetime(2026, 10, 27, 14, 52).astimezone()
SHELL = {"now": NOW, "needs": 0, "agents_attention": 0, "health": {"tone": "success", "text": "All normal"}}


def main_nav(text):
    m = re.search(r'<nav aria-label="Main".*?</nav>', text, re.S)
    assert m, "no Main nav"
    return m.group(0)


def nav_item(text, href):
    m = re.search(r'<a href="%s" class="nav-item[^"]*"[^>]*>.*?</a>' % re.escape(href), main_nav(text), re.S)
    assert m, f"no nav item {href}"
    return m.group(0)


class TestEveryRouteRenders(ServerCase):
    def test_eleven_routes_have_the_shell(self):
        self.assertEqual(len(PAGES), 11)
        for path in PAGES:
            with self.subTest(path):
                code, _, body = self.get(path)
                text = body.decode()
                self.assertEqual(code, 200)
                self.assertIn('href="#content"', text)
                self.assertIn('<nav aria-label="Main"', text)
                self.assertIn("Updated ", text)
                self.assertEqual(text.count('aria-current="page"'), 1)
                self.assertIn('<main id="content"', text)

    def test_nav_has_all_nine_links_without_js(self):
        text = self.get("/")[2].decode()
        nav = main_nav(text)
        hrefs = re.findall(r'<a href="([^"]+)"', nav)
        self.assertEqual(hrefs[0], "/")   # the wordmark
        self.assertEqual(hrefs[1:], [p for p, _ in wh.NAV])
        self.assertEqual(len(hrefs[1:]), 9)
        self.assertEqual(re.findall(r"<script\b[^>]*>", text), ['<script src="/static/theme.js" defer>'])

    def test_phone_more_nav_lists_every_page_and_tabbar_has_five_items(self):
        text = self.get("/tasks")[2].decode()
        more = re.search(r'<nav id="more".*?</nav>', text, re.S).group(0)
        self.assertEqual(len(re.findall(r'<a href="/[a-z]*"', more)), 9)
        tabs = re.search(r'<nav aria-label="Primary tabs".*?</nav>', text, re.S).group(0)
        self.assertEqual(len(re.findall(r"<a ", tabs)), 5)
        self.assertIn('href="#more"', tabs)

    def test_active_item_for_detail_pages_is_the_list(self):
        self.assertIn("nav-item--active", nav_item(self.get("/task/T-1")[2].decode(), "/tasks"))

    def test_tasks_use_human_labels_not_enums(self):
        text = self.get("/tasks")[2].decode()
        self.assertIn("In progress", text)
        self.assertNotIn(">in_progress<", text)
        self.assertNotIn(">in_review<", text)

    def test_header_cluster_shows_updated_time_health_and_needs_you(self):
        text = self.get("/goals")[2].decode()
        self.assertRegex(text, r'Updated <time datetime="[^"]+">\d\d:\d\d</time>')
        self.assertIn('class="pill hide-phone tone-', text)
        self.assertIn('href="/#needs-you"', text)   # the seed has blocked work and an unread escalation


class ShellDb(unittest.TestCase):
    """Server on an empty DB that each test fills."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.con = make_db(Path(self.tmp.name) / "factory.db", seed=False)
        self.con.row_factory = sqlite3.Row
        self.addCleanup(self.con.close)
        self.usage = {"five_hour": 10.0, "seven_day": 20.0, "fresh": True, "age_min": 1}
        self.httpd = server.make_server(Path(self.tmp.name) / "factory.db", "127.0.0.1", 0, (), tokens=lambda: {"rows": []},
                                        usage_fn=lambda: self.usage, mem_fn=lambda: 2048,
                                        defaults={"max_live_agents": "6", "max_five_hour_pct": "92", "max_seven_day_pct": "92"})
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        self.addCleanup(lambda: (self.httpd.shutdown(), self.httpd.server_close()))

    def get(self, path):
        with contextlib.redirect_stderr(io.StringIO()):
            return get(self.httpd, path)[2].decode()

    def agents(self, n, status, prefix="a"):
        for i in range(n):
            self.con.execute("INSERT INTO agents(name,role,status,started) VALUES(?,?,?,?)",
                             (f"{prefix}{i}", "coder", status, "2026-10-27T10:00:00+01:00"))

    def test_eight_running_agents_give_the_agents_nav_item_no_badge(self):
        self.agents(8, "live")
        text = self.get("/agents")
        self.assertNotIn('class="count', nav_item(text, "/agents"))
        self.assertNotIn('class="count', nav_item(text, "/"))
        self.assertNotIn("Needs you", text)

    def test_queued_agents_do_get_a_badge_and_blocked_work_badges_overview(self):
        self.agents(2, "queued", "q")
        self.con.execute("INSERT INTO projects(id,name,created) VALUES('p','P','x')")
        self.con.execute("INSERT INTO goals(id,project_id,title,status,priority,created,updated) "
                         "VALUES('G-1','p','g','blocked',1,'x','x')")
        for n in (1, 2):
            self.con.execute("INSERT INTO tasks(id,goal_id,title,status,waits,created,updated) VALUES(?,?,?,?,?,?,?)",
                             (f"T-{n}", "G-1", "t", "blocked", "owner", "x", "x"))
        text = self.get("/")
        agents_item = nav_item(text, "/agents")
        self.assertRegex(agents_item, r'<span class="count" aria-label="2 agents queued or blocked">2</span>')
        needs = queries.shell_status(self.con, None, None)["needs"]
        self.assertEqual(needs, 3)   # blocked goal + 2 owner-blocked tasks
        self.assertRegex(nav_item(text, "/"), r'class="count count--bad" aria-label="3 items need you">3<')
        self.assertEqual(re.findall(r'aria-label="3 items need you"[^>]*>Needs you 3<', text).__len__(), 2)  # top bar + header


class TestShellStatus(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.con = make_db(Path(self.tmp.name) / "factory.db", seed=False)
        self.con.row_factory = sqlite3.Row
        self.addCleanup(self.con.close)
        self.fresh = {"five_hour": 10.0, "seven_day": 20.0, "fresh": True, "age_min": 1}
        self.dflt = {"max_live_agents": "2", "max_five_hour_pct": "92"}

    def health(self, usage, defaults=None):
        return queries.shell_status(self.con, usage, self.dflt if defaults is None else defaults)["health"]

    def agents(self, n, status):
        for i in range(n):
            self.con.execute("INSERT INTO agents(name,role,status,started) VALUES(?,?,?,?)", (f"{status}{i}", "coder", status, "x"))

    def test_all_normal(self):
        self.assertEqual(self.health(self.fresh), {"tone": "success", "text": "All normal"})

    def test_gate_closed_counts_queued(self):
        self.agents(2, "live")
        self.agents(3, "queued")
        self.assertEqual(self.health(self.fresh), {"tone": "warning", "text": "Gate closed · 3 queued"})

    def test_usage_high_warns_and_cap_is_danger(self):
        self.assertEqual(self.health({**self.fresh, "five_hour": 85.0}), {"tone": "warning", "text": "Claude usage 85%"})
        self.assertEqual(self.health({**self.fresh, "five_hour": 95.0}), {"tone": "danger", "text": "Usage cap reached"})

    def test_weekly_cap_alone_is_danger(self):
        self.assertEqual(self.health({**self.fresh, "five_hour": 10.0, "seven_day": 95.0}),
                         {"tone": "danger", "text": "Usage cap reached"})

    def test_weekly_cap_uses_configured_threshold(self):
        u = {**self.fresh, "seven_day": 60.0}
        self.assertEqual(self.health(u)["tone"], "success")
        self.assertEqual(self.health(u, {**self.dflt, "max_seven_day_pct": "60"})["text"], "Usage cap reached")

    def test_five_hour_cap_alone_is_danger(self):
        self.assertEqual(self.health({**self.fresh, "five_hour": 92.0, "seven_day": 5.0})["text"], "Usage cap reached")

    def test_both_windows_under_is_normal(self):
        self.assertEqual(self.health({**self.fresh, "five_hour": 50.0, "seven_day": 50.0}),
                         {"tone": "success", "text": "All normal"})

    def test_weekly_warning_band(self):
        self.assertEqual(self.health({**self.fresh, "seven_day": 80.0}), {"tone": "warning", "text": "Weekly usage 80%"})
        self.assertEqual(self.health({**self.fresh, "seven_day": 73.0})["tone"], "success")

    def test_unknown_or_stale_usage_is_neutral_not_green(self):
        self.assertEqual(self.health(None)["tone"], "neutral")
        self.assertEqual(self.health({**self.fresh, "age_min": 45})["tone"], "neutral")
        self.assertEqual(self.health({"unreadable": True})["tone"], "neutral")

    def test_cap_over_config_wins_over_default(self):
        self.agents(2, "live")
        self.con.execute("INSERT INTO config VALUES('max_live_agents','9')")
        self.assertEqual(self.health(self.fresh)["tone"], "success")

    def test_running_agents_never_count_as_attention(self):
        self.agents(8, "live")
        self.assertEqual(queries.shell_status(self.con, self.fresh, self.dflt)["agents_attention"], 0)
        self.agents(1, "queued")
        self.assertEqual(queries.shell_status(self.con, self.fresh, self.dflt)["agents_attention"], 1)


class TestHelpers(unittest.TestCase):
    def test_status_is_glyph_plus_human_label(self):
        out = str(wh.status("in_progress"))
        self.assertIn("In progress", out)
        self.assertNotIn(">in_progress<", out)
        self.assertIn('aria-hidden="true"', out)

    def test_shapes_separate_todo_from_cancelled_and_progress_from_review(self):
        g = {k: str(wh.glyph(k)) for k in ("todo", "cancelled", "in_progress", "in_review")}
        self.assertEqual(len(set(g.values())), 4)
        self.assertIn("fg-review", str(wh.status("in_review")))

    def test_every_label_maps_every_status_enum(self):
        for s in wh._STATUSES:
            with self.subTest(s):
                self.assertIn(s, wh.LABELS)
                self.assertIn(s, wh._SVG12)

    def test_breadcrumb_marks_last_item_and_keeps_one_aria_current_page(self):
        out = str(wh.crumbs(("/goals", "Goals"), ("/goal/G-1", "G-1")))
        self.assertIn('<nav aria-label="Breadcrumb"', out)
        self.assertIn('<a href="/goals">Goals</a>', out)
        self.assertIn('<span aria-current="location">G-1</span>', out)
        doc = wh.page("G-1", "<p>x</p>", "/goals", shell=SHELL, crumb_trail=wh.crumbs(("/goals", "Goals"), ("/goal/G-1", "G-1")))
        self.assertEqual(doc.count('aria-current="page"'), 1)

    def test_page_without_shell_data_still_has_skip_link_and_nav(self):
        doc = wh.page("Error", "<p>x</p>", "")
        self.assertIn('<a href="#content" class="skip">', doc)
        self.assertIn('<nav aria-label="Main"', doc)
        self.assertNotIn('aria-current="page"', doc)

    def test_updated_time_is_the_injected_now(self):
        self.assertIn(f'<time datetime="{NOW.isoformat(timespec="minutes")}">14:52</time>', wh.page("T", "", "/", shell=SHELL))

    def test_zero_needs_you_hides_the_badge(self):
        self.assertNotIn("Needs you", wh.page("T", "", "/", shell=SHELL))
        self.assertIn("Needs you 4", wh.page("T", "", "/", shell={**SHELL, "needs": 4}))


if __name__ == "__main__":
    unittest.main()
