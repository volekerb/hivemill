"""Tests for web/views/tokens.py."""
import json
import re
import shutil
import sys
import tempfile
import time
import unittest
from pathlib import Path
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_queries import CANARY, NOW, make_db  # noqa: E402
from test_web_tokens import line, usage_row  # noqa: E402
from web import db as wdb  # noqa: E402
from web import html as wh  # noqa: E402
from web.prices import PRICES_AS_OF  # noqa: E402
from web.tokens import TokenIndex  # noqa: E402
from web.views import tokens  # noqa: E402

XSS = "<script>alert(1)</script>"
DAY = "2026-10-24T09:00:00Z"
OLD = "2026-09-01T09:00:00Z"
UUID_RE = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-")


def fresh_usage():
    return {"five_hour": 42.0, "seven_day": 17.0, "fresh": True, "age_min": 5,
            "five_hour_resets_at": 1792000000, "seven_day_resets_at": 1792500000}


class TokensCase(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.path = self.tmp / "factory.db"
        self.w = make_db(self.path)
        self.addCleanup(self.w.close)
        root = self.tmp / "projects"
        for rel, rows in {
            # s1 -> coder-t-1 (G-1 / T-1): sonnet 1M in + 100k out today, 10 in / 10 out long ago
            "p/s1.jsonl": [usage_row("s1", "m1", "r1", DAY, 1_000_000, 100_000, cr=2000, w5=300, w1=100),
                           usage_row("s1", "m2", "r2", OLD, 10, 10)],
            # s2 -> lead-a (G-1, no task): opus 500k in + 50k out
            "p/s2.jsonl": [usage_row("s2", "m3", "r3", DAY, 500_000, 50_000, model="claude-opus-5-5")],
            # s3 -> unattributed: an unpriced model with tokens, plus a zero-token <synthetic> row
            "p/s3.jsonl": [usage_row("s3", "m4", "r4", DAY, 1000, 0, model="claude-nope"),
                           usage_row("s3", "m5", "r5", DAY, 0, 0, model="<synthetic>")],
            # s4 -> unattributed sonnet 2000 in; its message text must never show up
            "p/s4.jsonl": [usage_row("s4", "m6", "r6", DAY, 2000, 0, message_text=f"prompt {CANARY}")],
        }.items():
            p = root / rel
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text("".join(line(r) for r in rows))
        self.index = TokenIndex(root)
        self.index.refresh()

    def ctx(self, snap=None, usage=fresh_usage, **query):
        con = wdb.connect(self.path)
        self.addCleanup(con.close)
        fn = (lambda: snap) if snap is not None else self.index.snapshot
        return SimpleNamespace(db=con, now=NOW, usage_fn=usage, mem_fn=None, tokens=fn, defaults={},
                               factory_home=None, query=query)

    def page(self, usage=fresh_usage, snap=None, **query):
        c = self.ctx(snap=snap, usage=usage, **query)
        d = tokens.data(c)
        json.dumps(d)
        return d, str(tokens.render(c, d))

    @staticmethod
    def by(d, section):
        return {e["key"]: e for e in d[section][0]}


class TestTotals(TokensCase):
    def test_by_section_values(self):
        d, _ = self.page(days="all")
        t = d["totals"]
        self.assertEqual((t["input"], t["output"], t["cache_read"], t["cache_write"], t["requests"]),
                         (1_503_010, 150_010, 2000, 400, 6))
        # sonnet: (1000010*2 + 100010*10 + 2000*.2 + 300*2.5 + 100*4)/1e6 + 2000*2/1e6 ; opus: 500k*4 + 50k*20
        sonnet = (1_000_010 * 2 + 100_010 * 10 + 2000 * 0.1 + 300 * 2.5 + 100 * 4 + 2000 * 2) / 1e6
        opus = (500_000 * 4 + 50_000 * 20) / 1e6
        self.assertAlmostEqual(t["cost"], sonnet + opus, places=9)
        self.assertTrue(t["cost_unknown"])                    # claude-nope has tokens and no price
        self.assertEqual(t["unpriced_requests"], 1)           # the zero-token <synthetic> row is not unpriced
        self.assertEqual(d["unpriced"], ["claude-nope"])
        g = self.by(d, "by_goal")
        self.assertEqual(g["G-1"]["input"], 1_500_010)
        self.assertEqual(g["G-1"]["title"], "Goal one")
        self.assertEqual(g["unattributed"]["input"], 3000)
        self.assertAlmostEqual(g["G-1"]["cost"], (1_000_010 * 2 + 100_010 * 10 + 2000 * 0.1 + 300 * 2.5 + 100 * 4) / 1e6 + opus, places=9)
        self.assertEqual(self.by(d, "by_task")["T-1"]["input"], 1_000_010)
        a = self.by(d, "by_agent")
        self.assertEqual((a["coder-t-1"]["role"], a["coder-t-1"]["sessions"]), ("coder", ["s1"]))
        self.assertEqual(a["lead-a"]["input"], 500_000)
        self.assertAlmostEqual(a["lead-a"]["cost"], opus, places=9)
        m = self.by(d, "by_model")
        self.assertIsNone(m["claude-nope"]["cost"])           # all unknown: n/a, not 0
        self.assertEqual(m["<synthetic>"]["cost"], 0.0)
        self.assertFalse(m["<synthetic>"]["cost_unknown"])
        self.assertEqual(d["via"]["session"], 2)
        self.assertEqual(d["via"]["unattributed"], 2)

    def test_render_values_and_na(self):
        d, html = self.page(days="all")
        for needle in ("1.5M", "Goal one", "$6.01", "Not attributed", f"list prices as of {PRICES_AS_OF}",
                       "Cost estimate", "By day", "Total tokens", "Cache read share", "Requests"):
            self.assertIn(needle, html)
        _, models = self.page(days="all", by="model")
        for needle in ("claude-nope", "n/a", "claude-opus-5-5"):
            self.assertIn(needle, models)

    def test_by_day_is_a_column_chart_with_a_table_fallback(self):
        _, html = self.page(days="all")
        section = html.split("By day")[1]
        self.assertRegex(section, r'<svg [^>]*role="img"[^>]*aria-label="[^"]*tokens in \d+ days[^"]*"[^>]*class="cols">')
        self.assertIn('</svg><table class="sr-only">', section)
        self.assertNotIn("bar--", html)

    def test_breakdown_is_one_table_with_a_by_switch(self):
        d, html = self.page(days="all")
        seg = html.split('aria-label="Breakdown"')[1].split("</nav>")[0]
        self.assertEqual(re.findall(r'<a href="([^"]+)"( aria-current="true")?>([^<]+)</a>', seg),
                         [("/tokens?days=all", ' aria-current="true"', "By goal"), ("/tokens?days=all&amp;by=task", "", "By task"),
                          ("/tokens?days=all&amp;by=agent", "", "By agent"), ("/tokens?days=all&amp;by=model", "", "By model")])
        self.assertEqual(html.count('<table class="dt">'), 1)
        rows = re.findall(r'<tr><td class="c-title">(.*?)</td>', html.split('aria-label="Breakdown"')[1])
        self.assertIn("Not attributed", rows[-1])        # pinned last, with its (i) definition
        self.assertIn("What is Not attributed?", rows[-1])
        self.assertIn("G-1", rows[0])

    def test_agent_rows_hide_session_ids_in_the_title_tooltip(self):
        d, html = self.page(days="all", by="agent")
        self.assertRegex(html, r'<span title="sessions: s1">coder-t-1 <span class="muted">coder</span></span>')
        self.assertNotIn(">s1<", html)

    def test_total_not_na_because_of_synthetic_rows(self):
        c = self.ctx(days="all", model="<synthetic>")
        d = tokens.data(c)
        self.assertEqual(d["model"], "<synthetic>")
        self.assertFalse(d["totals"]["cost_unknown"])
        self.assertIn("$0.00", str(tokens.render(c, d)))

    def test_period_filter(self):
        d, _ = self.page(days="7")
        self.assertEqual(d["totals"]["input"], 1_503_000)     # the two-month-old row is out
        d1, _ = self.page(days="1")
        self.assertEqual(d1["totals"]["input"], 1_503_000)
        dbad, _ = self.page(days="bogus")
        self.assertEqual(dbad["period"], "7")

    def test_goal_and_model_filters(self):
        d, _ = self.page(days="all", goal="G-1")
        self.assertEqual(d["totals"]["input"], 1_500_010)
        d, _ = self.page(days="all", goal="unattributed")
        self.assertEqual(d["totals"]["input"], 3000)
        d, _ = self.page(days="all", model="claude-opus-5-5")
        self.assertEqual(d["totals"]["input"], 500_000)
        d, _ = self.page(days="all", goal="x; DROP", model="nope")
        self.assertEqual((d["goal"], d["model"], d["totals"]["requests"]), (None, None, 6))


class TestStates(TokensCase):
    def test_not_ready_shows_indexing(self):
        t0 = time.time()
        d, html = self.page(snap={"ready": False, "files": 3, "bytes_total": 5 * 1048576, "rows": [], "sessions": {}})
        self.assertLess(time.time() - t0, 1.0)
        self.assertIn("indexing… 3 files / 5.0 MB read", html)
        self.assertNotIn("Total tokens", html)
        self.assertIn("Claude usage", html)

    def test_no_index_at_all(self):
        c = self.ctx()
        c.tokens = None
        self.assertIn("indexing…", str(tokens.render(c, tokens.data(c))))

    def test_empty_sections(self):
        snap = self.index.snapshot()
        snap["rows"] = []
        d, html = self.page(snap=snap)
        self.assertEqual(html.count("no data yet"), 1)
        self.assertIn("No tokens in this range", html)


class TestUsage(TokensCase):
    def test_fresh_values_and_reset(self):
        d, html = self.page()
        self.assertEqual((d["usage"]["five_hour"], d["usage"]["seven_day"]), (42.0, 17.0))
        self.assertIn('aria-label="Claude usage 5-hour 42%, cap 92%"', html)
        self.assertIn('aria-label="Claude usage weekly 17%, cap 92%"', html)
        self.assertIn("resets ", html)
        self.assertNotIn("stale:", html)

    def test_reset_from_real_shaped_strings(self):
        raw = {"five_hour": "9", "seven_day": "40", "fresh": True, "age_min": 1,
               "five_hour_resets_at": 1792888800, "seven_day_resets_at": 1793000000}
        d, html = self.page(usage=lambda: raw)
        self.assertEqual((d["usage"]["five_hour_resets_at"], d["usage"]["seven_day_resets_at"]),
                         (1792888800, 1793000000))
        for ts in (1792888800, 1793000000):
            want = "resets " + time.strftime("%m-%d %H:%M", time.localtime(ts))
            self.assertIn(want, html)
        self.assertIn("9%", html)
        self.assertIn("40%", html)

    def test_unparsed_strings_are_not_reparsed_here(self):
        # usage() owns the parsing; the view only keeps ints, so a raw string never becomes a reset time
        u = dict(fresh_usage(), five_hour_resets_at="1792888800", seven_day_resets_at=float("inf"))
        d, html = self.page(usage=lambda: u)
        self.assertEqual((d["usage"]["five_hour_resets_at"], d["usage"]["seven_day_resets_at"]), (None, None))
        self.assertNotIn("resets ", html)

    def test_reset_garbage_is_omitted(self):
        u = dict(fresh_usage(), five_hour_resets_at="soon", seven_day_resets_at=True)
        d, html = self.page(usage=lambda: u)
        self.assertEqual((d["usage"]["five_hour_resets_at"], d["usage"]["seven_day_resets_at"]), (None, None))
        self.assertNotIn("resets ", html)

    def test_stale_label(self):
        u = dict(fresh_usage(), fresh=False, age_min=200)
        d, html = self.page(usage=lambda: u)
        self.assertIn("stale: 200 min old", html)

    def test_unreadable_and_failing(self):
        _, html = self.page(usage=lambda: {"five_hour": None, "seven_day": None, "fresh": False, "age_min": -1, "unreadable": True})
        self.assertIn("unreadable", html)

        def boom():
            raise OSError("x")
        _, html = self.page(usage=boom)
        self.assertIn("unreadable", html)
        _, html = self.page(usage=None)
        self.assertIn("No usage data", html)


class TestSafety(TokensCase):
    def test_privacy_and_classes(self):
        d, html = self.page(days="all")
        blob = html + json.dumps(d)
        self.assertNotIn(CANARY, blob)
        self.assertNotIn("<script", html)
        self.assertIsNone(UUID_RE.search(blob))
        self.assertNotIn("/tmp/x", blob)                      # no cwd
        used = {c for m in re.findall(r'class="([^"]*)"', html) for c in m.split()}
        self.assertEqual(used - wh.CLASSES, set())
        self.assertNotIn("style=", html)

    def test_xss_titles_escaped(self):
        self.w.execute("UPDATE goals SET title=? WHERE id='G-1'", (XSS,))
        self.w.execute("UPDATE tasks SET title=? WHERE id='T-1'", (XSS,))
        d, html = self.page(days="all")
        self.assertIn("&lt;script&gt;", html)
        self.assertNotIn("<script>alert", html)

    def test_registered_and_fast(self):
        t0 = time.time()
        self.page(days="all")
        self.assertLess(time.time() - t0, 1.0)


if __name__ == "__main__":
    unittest.main()
