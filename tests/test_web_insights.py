"""G-019 B6: the insights pages /quality, /throughput, /resources and /tokens (web/views/{quality,throughput,resources,tokens}.py)
and the queries appended to web/queries.py for them. Seeded temp DBs only; time is the fixed NOW of test_web_queries."""
import ast
import json
import re
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

import _gitenv  # noqa: F401,E402
from test_web_queries import NOW, make_db, ts  # noqa: E402
from test_web_server import ServerCase  # noqa: E402
from web import db as wdb  # noqa: E402
from web import html as wh  # noqa: E402
from web import queries, server  # noqa: E402
from web.views import quality, resources, throughput, tokens  # noqa: E402

E_SQL = "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)"
SNAP = {"ready": True, "rows": [], "sessions": {}, "files": 0, "bytes_total": 0}

# every definition string quality.py passed to kpi() on main, word for word (acceptance 3)
DEFINITIONS = (
    "Distinct tasks with a merge event.",
    "Merged tasks that were never rejected.",
    "Merged tasks rejected at least once before they merged.",
    "Reviewer, QA and judge fail or reject verdicts, plus judge rejections without a verdict row.",
    "Rejections divided by merged PRs.",
    "Judge-blocked and merge-failed events: stale verdict, red or pending CI, conflicts.",
    "No cause is stored yet (counts incidents, not events).",
    "Attempts beyond the first, per merged task.",
    "Merged / (merged + tasks the judge blocked for red checks).",
    "Newest evaluation per team.",
)
# data() keys on main (acceptance 6): the JSON contract only grows
KEYS = {
    "quality": {"overall", "teams", "weeks", "formula"},
    "throughput": {"days", "merges", "median_s", "p90_s", "timed", "open_tasks", "live_workers", "merges_per_day", "stages",
                   "goals", "slowest"},
    "resources": {"gate", "machine", "config", "timeline", "per_day", "before"},
    "tokens": {"period", "prices_as_of", "usage", "ready", "files", "mb_read", "goal", "model", "models", "totals", "by_goal",
               "by_task", "by_agent", "by_model", "by_day", "via", "unpriced"},
}


def tile_labels(html):
    return re.findall(r'<div class="tile-label"><span>([^<]+)</span>', html)


class InsightServer(ServerCase):
    """The real server with a ready (empty) token index, so /tokens renders its controls and breakdown."""

    def setUp(self):
        super().setUp()
        self.httpd.server_close()
        self.httpd = server.make_server(self.db_path, "127.0.0.1", 0, (), tokens=lambda: SNAP,
                                        usage_fn=lambda: {"five_hour": 10.0, "seven_day": 20.0, "fresh": True, "age_min": 1},
                                        mem_fn=lambda: 2048, defaults={"max_agents": "6"})
        import threading
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        self.addCleanup(lambda: (self.httpd.shutdown(), self.httpd.server_close()))

    def text(self, path):
        code, _, body = self.get(path)
        self.assertEqual(code, 200, path)
        return body.decode()


class TestQualityPage(InsightServer):
    def test_first_row_is_four_tiles_and_not_measured_is_spelled_out(self):
        html = self.text("/quality")
        row = html[html.index("tiles--4"):]
        row = row[:row.index("</div></div></div>") + 18]
        self.assertEqual(row.count('class="tile"'), 4)
        self.assertEqual(tile_labels(html)[:4], ["Rejections per merged PR", "First-try merge rate", "CI-blocked merges", "Median attempts"])
        self.assertNotIn("n/a, see G-013", html)
        self.assertIn("Not measured yet", html)

    def test_every_definition_from_main_is_inside_an_i_details(self):
        html = self.text("/quality")
        for d in DEFINITIONS:
            self.assertRegex(html, r'<details class="disc--i"><summary[^>]*>i</summary><p class="tile-def">' + re.escape(d) + "</p>", d)

    def test_definitions_match_the_kpi_calls_on_main_when_git_has_it(self):
        r = subprocess.run(["git", "show", "origin/main:web/views/quality.py"], cwd=ROOT, capture_output=True, text=True)
        if r.returncode != 0:
            self.skipTest("origin/main is not available in this checkout; DEFINITIONS above is the pinned copy")
        calls = [n for n in ast.walk(ast.parse(r.stdout)) if isinstance(n, ast.Call)]
        found = {n.args[2].value for n in calls if getattr(n.func, "id", "") in ("k", "kpi") and len(n.args) == 3
                 and isinstance(n.args[2], ast.Constant)}
        found |= {kw.value.value for n in calls if getattr(n.func, "id", "") == "tile" for kw in n.keywords
                  if kw.arg == "definition" and isinstance(kw.value, ast.Constant)}
        self.assertTrue(found)
        self.assertEqual(found - set(DEFINITIONS), set(), "a definition on main is not in the pinned list")
        html = self.text("/quality")
        for d in found:
            self.assertIn(d, html)

    def test_sort_whitelist(self):
        base = self.text("/quality")
        for bad in ("evil", "team%27--", "merged%3BDROP", ""):
            self.assertEqual(self.text(f"/quality?sort={bad}"), base, bad)
        self.assertIn('aria-sort="ascending"', self.text("/quality?sort=rejections&dir=asc"))

    def test_more_metrics_is_a_collapsed_disclosure(self):
        html = self.text("/quality")
        self.assertRegex(html, r'<details class="disc" data-key="more-metrics"><summary>')

    def test_week_chart_has_a_table_fallback(self):
        html = self.text("/quality")
        self.assertTrue("No rejections recorded." in html or '</svg><table class="sr-only">' in html)


class TestThroughputPage(InsightServer):
    def test_segmented_range_and_fallback(self):
        for days in (7, 14, 30):
            html = self.text(f"/throughput?days={days}")
            self.assertRegex(html, rf'<a href="/throughput\?days={days}" aria-current="true">{days} days</a>')
            self.assertEqual(html.count('aria-current="true"'), 1)
        self.assertRegex(self.text("/throughput?days=9"), r'<a href="/throughput\?days=14" aria-current="true">14 days</a>')

    def test_four_tiles_chart_and_no_bar_table(self):
        html = self.text("/throughput")
        self.assertEqual(tile_labels(html)[:4], ["Merges", "Median spawn to merge", "p90 spawn to merge", "Agents running now"])
        self.assertNotIn("bar--", html)
        self.assertRegex(html, r'<svg [^>]*role="img"[^>]*aria-label="[^"]*merges in 14 days[^"]*"[^>]*class="cols">')
        self.assertRegex(html, r'</svg><table class="sr-only">')
        self.assertIn("Where the time goes", html)
        self.assertIn("Slowest 10 tasks", html)

    def test_slowest_sort_whitelist_keeps_days(self):
        self.assertEqual(self.text("/throughput?sort=evil"), self.text("/throughput"))
        self.assertIn('/throughput?days=7&amp;sort=total&amp;dir=asc', self.text("/throughput?days=7"))


class TestTokensPage(InsightServer):
    def test_by_whitelist(self):
        for bad in ("evil", "goal%27--", "", "task%3B", "../x"):
            html = self.text(f"/tokens?by={bad}")
            self.assertRegex(html, r'<a href="/tokens\?days=7" aria-current="true">By goal</a>', bad)
            self.assertEqual(html.count("aria-current=\"true\""), 2, bad)    # the range and the breakdown
        for by, label in (("task", "By task"), ("agent", "By agent"), ("model", "By model"), ("goal", "By goal")):
            self.assertRegex(self.text(f"/tokens?by={by}"), rf'aria-current="true">{label}</a>')

    def test_old_urls_still_work(self):
        for p in ("/tokens?days=30", "/tokens?days=all&goal=G-1&model=claude-sonnet-5-5", "/tokens?days=bogus", "/tokens?goal=%27"):
            self.text(p)
        self.assertRegex(self.text("/tokens?days=30"), r'aria-current="true">30 days</a>')

    def test_range_links_keep_the_other_filters(self):
        html = self.text("/tokens?days=30&goal=G-1&by=task")
        self.assertIn('href="/tokens?days=7&amp;goal=G-1&amp;by=task"', html)
        self.assertIn("Filtered by goal G-1", html)

    def test_usage_gauges_are_the_overview_component(self):
        html = self.text("/tokens")
        self.assertIn('<span class="gl">Claude usage</span>', html)
        self.assertIn('aria-label="Claude usage 5-hour 10%, cap 92%"', html)
        self.assertIn('aria-label="Claude usage weekly 20%, cap 92%"', html)


class TestResourcesPage(InsightServer):
    def test_old_paging_url_and_json_still_work(self):
        d = json.loads(self.text("/api/resources.json"))
        self.assertTrue(KEYS["resources"] <= set(d))
        self.text("/resources?before=" + (d["timeline"]["next_before"] or "1-0-1"))
        self.text("/resources?before=garbage")

    def test_json_contract_of_all_four_pages(self):
        for page, keys in KEYS.items():
            d = json.loads(self.text(f"/api/{page}.json"))
            self.assertEqual(keys - set(d), set(), page)
        self.assertEqual(set(json.loads(self.text("/api/resources.json"))["gate"]) >= {"open", "reason", "checks", "text"}, True)


class Case(unittest.TestCase):
    """In-process render on a seeded temp DB at the fixed NOW."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / "factory.db"
        self.w = make_db(self.path)
        self.addCleanup(self.w.close)

    def ctx(self, usage=None, mem=2048, **query):
        con = wdb.connect(self.path)
        self.addCleanup(con.close)
        return SimpleNamespace(db=con, now=NOW, usage_fn=usage, mem_fn=lambda: mem, tokens=None,
                               defaults={"max_live_agents": "6", "judge_mode": "manual"}, factory_home=None, query=query)

    def page(self, view, **kw):
        q = kw.pop("query", {})
        c = self.ctx(**kw, **q)
        d = view.data(c)
        json.dumps(d)
        return d, str(view.render(c, d))


class TestResourcesRender(Case):
    FRESH = staticmethod(lambda: {"five_hour": 30.0, "seven_day": 10.0, "fresh": True, "age_min": 3,
                                  "five_hour_resets_at": 1792000000, "seven_day_resets_at": 1792500000})

    def test_events_and_config_are_collapsed_and_free_text_is_hidden(self):
        self.w.execute(E_SQL, (ts(11, 0), "coder-x", "agent", "coder-x", "spawn", "DETAIL-SECRET"))
        self.w.execute(E_SQL, (ts(11, 1), "judge", "task", "T-1", "judge-reject", "DETAIL-SECRET"))
        self.w.execute(E_SQL, (ts(11, 2), "judge", "task", "T-1", "merged", "DETAIL-SECRET"))
        d, html = self.page(resources, usage=self.FRESH)
        self.assertNotIn("DETAIL-SECRET", html + json.dumps(d))
        for label in ("7-day events", "Caps and config"):
            self.assertRegex(html, rf"<details class=\"disc\" data-key=\"[a-z]+\"><summary><span>{label}</span>", label)
        self.assertNotRegex(html, r"<details[^>]* open[ >]")
        self.assertIn("coder-x spawn", html)         # the structured part is still there, inside the disclosure
        self.assertEqual((ROOT / "web/views/resources.py").read_text().count("only structured details are shown"), 1)

    def test_more_page_opens_the_events_disclosure(self):
        first, _ = self.page(resources)
        # enough events for a second page
        for i in range(resources.PAGE_SIZE + 5):
            self.w.execute(E_SQL, (ts(8, 0, i % 60), "a", "agent", f"x{i}", "spawn", None))
        d, html = self.page(resources)
        self.assertTrue(d["timeline"]["next_before"])
        self.assertNotRegex(html, r"<details[^>]* open[ >]")
        _, more = self.page(resources, query={"before": d["timeline"]["next_before"]})
        self.assertRegex(more, r'<details class="disc" data-key="events" open>')

    def test_gate_sentence_pill_and_checks(self):
        for i in range(7):
            self.w.execute("INSERT INTO agents(name,role,status,started) VALUES(?,?,?,?)", (f"w{i}", "coder", "live", ts(9, 0)))
        self.w.execute("INSERT INTO agents(name,role,status,started) VALUES('q1','coder','queued',?)", (ts(9, 0),))
        d, html = self.page(resources, usage=self.FRESH)
        g = d["gate"]
        self.assertFalse(g["open"])
        self.assertIn("Gate closed", html)
        self.assertRegex(html, rf"{g['live']} agents running, cap 6; {g['queued']} queued")
        rows = {r["label"]: r for r in g["rows"]}
        self.assertEqual(rows["Agents"]["state"], "over")
        self.assertEqual(rows["Memory available"]["state"], "ok")
        self.assertEqual(rows["Claude usage"]["word"], "OK")
        self.assertGreaterEqual(html.count('class="ins-check"'), 6)
        self.assertNotIn(" · ".join(g["checks"]), html)     # the dot string is replaced by rows

    def test_four_gauges_use_the_overview_labels(self):
        _, html = self.page(resources, usage=self.FRESH)
        for label in ("Agents", "Memory", "Load", "Claude usage"):
            self.assertIn(f'<span class="gl">{label}</span>', html)
        self.assertIn("5-hour resets ", html)
        self.assertIn("data 3 min old", html)

    def test_config_has_human_labels_with_the_raw_key(self):
        _, html = self.page(resources)
        self.assertRegex(html, r'Max live agents <span class="mono ins-key">max_live_agents</span>')

    def test_missing_inputs_are_unknown_not_ok(self):
        d, html = self.page(resources, mem=None)
        rows = {r["label"]: r for r in d["gate"]["rows"]}
        self.assertEqual((rows["Memory available"]["state"], rows["Memory available"]["word"]), ("unknown", "Unknown"))
        self.assertEqual(rows["Claude usage"]["state"], "unknown")


class TestQueries(Case):
    def test_table_sort_ignores_anything_outside_the_whitelist(self):
        cols = {"a": "asc", "b": "desc"}
        self.assertEqual(queries.table_sort({"sort": "b"}, cols, ("a", "asc")), ("b", "desc"))
        self.assertEqual(queries.table_sort({"sort": "b", "dir": "asc"}, cols, ("a", "asc")), ("b", "asc"))
        self.assertEqual(queries.table_sort({"sort": "b", "dir": "up"}, cols, ("a", "asc")), ("b", "desc"))
        for bad in ("c", "b; DROP", "", None, 5, ["b"]):
            self.assertEqual(queries.table_sort({"sort": bad}, cols, ("a", "asc")), ("a", "asc"), bad)
        self.assertEqual(queries.table_sort(None, cols, ("a", "asc")), ("a", "asc"))

    def test_sort_rows_puts_none_last_either_way(self):
        rows = [{"v": 2}, {"v": None}, {"v": 5}, {"v": 1}]
        g = {"v": lambda r: r["v"]}
        self.assertEqual([r["v"] for r in queries.sort_rows(rows, "v", "asc", g)], [1, 2, 5, None])
        self.assertEqual([r["v"] for r in queries.sort_rows(rows, "v", "desc", g)], [5, 2, 1, None])

    def test_quality_windows_split_the_two_periods(self):
        x = self.w.execute
        x("DELETE FROM events WHERE action IN ('merged','judge-reject','judge-blocked')")
        x("DELETE FROM verdicts")
        x("UPDATE tasks SET attempts=1")
        # T-1 and T-3 merged in the last 14 days, T-2 20 days ago
        merges = [("T-1", ts(9, 0, day=21)), ("T-2", ts(9, 0, day=4)), ("T-3", ts(9, 0, day=24))]
        for tid, when in merges:
            x(E_SQL, (when, "judge", "task", tid, "merged", "@abc"))
        x("UPDATE tasks SET attempts=3 WHERE id='T-2'")
        x("INSERT INTO verdicts(task_id,role,verdict,created) VALUES('T-2','qa','fail',?)", (ts(8, 0, day=4),))
        x(E_SQL, (ts(9, 30, day=22), "judge", "task", "T-3", "judge-blocked", "checks not green"))
        w = queries.quality_windows(self.ctx().db, 14, now=NOW)
        cur, prev = w["current"], w["previous"]
        self.assertEqual((cur["merged"], cur["rejections"], cur["rejections_per_merged"], cur["first_try_pct"]), (2, 0, 0.0, 100))
        self.assertEqual((prev["merged"], prev["rejections"], prev["rejections_per_merged"], prev["first_try_pct"]), (1, 1, 1.0, 0))
        self.assertEqual((cur["ci_blocked"], prev["ci_blocked"]), (1, 0))
        self.assertEqual((cur["median_attempts"], prev["median_attempts"], w["all"]["median_attempts"]), (1, 3, 1))
        _, html = self.page(quality)
        self.assertIn("↓ 1.00 vs prev 14 d", html)       # rejections per merged PR went from 1.00 to 0.00
        self.assertIn("↑ 100 pts vs prev 14 d", html)

    def test_no_data_has_no_delta_and_no_division_by_zero(self):
        self.w.executescript("DELETE FROM events; DELETE FROM verdicts;")
        w = queries.quality_windows(self.ctx().db, 14, now=NOW)
        self.assertEqual(w["current"]["rejections_per_merged"], None)
        self.assertEqual(w["current"]["median_attempts"], None)
        _, html = self.page(quality)
        self.assertNotRegex(html, r"[↑↓] ")                   # nothing to compare: no arrow on any tile
        self.assertEqual(html.count('class="delta'), 1)       # only CI-blocked can say "no change" (0 and 0)


class TestSourceAndAssets(unittest.TestCase):
    def test_no_interpolated_order_by(self):
        for f in ("quality", "throughput", "resources", "tokens"):
            src = (ROOT / "web" / "views" / f"{f}.py").read_text()
            self.assertIsNone(re.search(r"(?i)order by[^\"\n]*\{", src), f)

    def test_b6_css_section_is_single_and_uses_tokens_only(self):
        css = (ROOT / "web" / "static" / "app.css").read_text()
        self.assertEqual(css.count("/* == B6 insights == */"), 1)
        sec = css.split("/* == B6 insights == */")[1]
        self.assertIsNone(re.search(r"#[0-9a-fA-F]{3,8}\b|rgba?\(|hsla?\(", sec))
        self.assertIsNone(re.search(r"(?<![\w-])(min-|max-)?height:\s*[0-9]+px", sec))
        self.assertEqual(css.rstrip().rsplit("/* == B6 insights == */", 1)[1].count("/* =="), 0)    # the last section

    def test_every_class_the_pages_use_is_known(self):
        c = Case()
        c.setUp()
        try:
            used = set()
            for view, kw in ((quality, {}), (throughput, {}), (resources, {"usage": TestResourcesRender.FRESH}), (tokens, {})):
                _, html = c.page(view, **kw)
                for m in re.findall(r'class="([^"]+)"', html):
                    used |= set(m.split())
            self.assertEqual(used - wh.CLASSES, set())
        finally:
            c.tearDown() if hasattr(c, "tearDown") else None
            c.tmp.cleanup()

    def test_render_is_fast_on_the_seeded_db(self):
        c = Case()
        c.setUp()
        try:
            for view in (quality, throughput, resources, tokens):
                cx = c.ctx(usage=TestResourcesRender.FRESH)
                times = []
                for _ in range(5):
                    t0 = time.perf_counter()
                    view.render(cx, view.data(cx))
                    times.append(time.perf_counter() - t0)
                self.assertLess(sorted(times)[2], 1.0, view.__name__)       # the 200 ms target is measured in the PR
        finally:
            c.tmp.cleanup()


class TestBreakdownColumns(unittest.TestCase):
    def test_only_name_tokens_and_share_stay_on_a_phone(self):
        src = (ROOT / "web" / "views" / "tokens.py").read_text()
        wide = re.findall(r'\{"key": "(\w+)", "label": "[^"]+", "cls": "num ins-wide"\}', src)
        self.assertEqual(wide, ["io", "req", "cost"])
        self.assertIn(".dt .ins-wide { display: none; }", (ROOT / "web" / "static" / "app.css").read_text())


if __name__ == "__main__":
    unittest.main()
