"""Tests for web/views/tokens.py, the Spend page (G-069): every number comes from ctx.obs("spend")."""
import json
import re
import sys
import time
import unittest
from pathlib import Path
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_queries import CANARY, NOW  # noqa: E402
from web import html as wh  # noqa: E402
from web import queries  # noqa: E402
from web.views import tokens  # noqa: E402

XSS = "<script>alert(1)</script>"
UUID_RE = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-")


def spend_obs(**over):
    """A `hm-obs api spend` answer (24 h, group by role) shaped like the real output: three roles plus the Not
    attributed row (key ""), hourly bins, a previous window for deltas and the polling banner."""
    rows = [
        {"key": "coder", "label": "coder", "sessions": 27, "requests": 903, "cost_usd": 23.0, "share_pct": 46.0,
         "spawn_usd": 1.36, "polling_usd": 10.29, "work_usd": 11.35, "per_merged_pr_usd": 4.6, "polling_pct": 44.7,
         "spawn_pct": 5.9, "polling_warn": True, "spawn_warn": False, "spark_usd": [1.0, 2.5, 3.0]},
        {"key": "lead", "label": "lead", "sessions": 4, "requests": 300, "cost_usd": 15.0, "share_pct": 30.0,
         "spawn_usd": 3.0, "polling_usd": 2.0, "work_usd": 10.0, "per_merged_pr_usd": 3.0, "polling_pct": 13.3,
         "spawn_pct": 20.0, "polling_warn": False, "spawn_warn": True, "spark_usd": [2.0, 1.0]},
        {"key": "", "label": "", "sessions": 3, "requests": 40, "cost_usd": 4.0, "share_pct": 8.0,
         "spawn_usd": 0.0, "polling_usd": 0.5, "work_usd": 3.5, "per_merged_pr_usd": None, "polling_pct": 12.5,
         "spawn_pct": 0.0, "polling_warn": False, "spawn_warn": False, "spark_usd": [0.0, 4.0]},
    ]
    d = {"window_h": 24, "since": "2026-10-23T12:00:00Z", "until": "2026-10-24T12:00:00Z", "group": "role",
         "drill": "model", "filters": {},
         "current": {"cost_usd": 50.0, "requests": 1243, "sessions": 34, "spawn_usd": 4.36, "polling_usd": 12.79,
                     "work_usd": 32.85, "cache_write_usd": 10.0, "unpriced_requests": 0, "polling_pct": 25.6,
                     "cache_write_pct": 20.0, "merged_prs": 5, "per_merged_pr_usd": 10.0},
         "previous": {"cost_usd": 40.0, "requests": 1000, "sessions": 30, "spawn_usd": 4.0, "polling_usd": 8.0,
                      "work_usd": 28.0, "cache_write_usd": 9.0, "unpriced_requests": 0, "polling_pct": 20.0,
                      "cache_write_pct": 22.5, "merged_prs": 4, "per_merged_pr_usd": 10.0},
         "unattributed_usd": 4.0, "unattributed_pct": 8.0, "bin_h": 1,
         "bins": [{"start": "2026-10-24T09:00:00Z", "work_usd": 20.0, "polling_usd": 10.0, "spawn_usd": 1.25},
                  {"start": "2026-10-24T10:00:00Z", "work_usd": 12.85, "polling_usd": 2.79, "spawn_usd": 3.11}],
         "rows": rows,
         "banners": ["! Polling is 26% of spend ($12.79 of $50.00) in the last 24 h: over 20% means agents pay to "
                     "re-read context while they wait."],
         "ingested_at": "2026-10-24T11:58:00Z", "flow_at": "2026-10-24T11:55:00Z"}
    d.update(over)
    return d


def flow_obs(**over):
    """A `hm-obs api flow` answer (7 d) shaped like the real output: main line, rework loop and side queues with
    hours and shares, one stuck PR and the 2+ rounds banner."""
    stages = [
        {"stage": "ci_wait", "label": "CI wait", "lane": "main", "hours": 10.0, "share_pct": 20.0},
        {"stage": "review", "label": "Review + QA", "lane": "main", "hours": 20.0, "share_pct": 40.0},
        {"stage": "judge", "label": "Judge", "lane": "main", "hours": 5.0, "share_pct": 10.0},
        {"stage": "rework", "label": "Rework", "lane": "rework", "hours": 10.0, "share_pct": 20.0},
        {"stage": "hold", "label": "Hold", "lane": "side", "hours": 5.0, "share_pct": 10.0},
    ]
    d = {"window_h": 168, "project": "", "repos": ["acme/factory"], "at": "2026-10-24T11:55:00Z", "merged": 6,
         "open_to_merge_median_h": 4.5, "open_to_merge_p90_h": 12.25, "prs": 8, "prs_2plus_rounds": 3,
         "prs_2plus_rounds_pct": 37.5, "open_over_6h": 1, "stage_hours": 50.0, "stages": stages,
         "stuck": [{"repo": "acme/factory", "number": 812, "task": "T-7", "stage": "review", "age_h": 9.5}],
         "merged_per_day": [{"day": "2026-10-23", "merged": 2}, {"day": "2026-10-24", "merged": 4}],
         "banners": ["! 38% of PRs needed 2+ review rounds (3 of 8): over 30% means specs or self-review miss things.",
                     "! Review + QA holds 40% of stage hours: over 30% makes it the bottleneck."],
         "warnings": []}
    d.update(over)
    return d


def obs_ctx(spend=None, flow=None, error=None, **query):
    """A view ctx whose ctx.obs(kind) answers with the given fixture (or the error), and records each call's
    argv tail in ctx.calls so a test can check what the page asked hm-obs for."""
    from web import queries
    calls = []

    def obs(kind):
        calls.append((kind, queries.obs_args(kind, query)))
        ans = {"spend": spend, "flow": flow}.get(kind)
        return (None, error or "no fixture") if ans is None else (ans, None)
    return SimpleNamespace(db=None, now=NOW, usage_fn=fresh_usage, mem_fn=None, tokens=None, defaults={},
                           factory_home=None, query=query, obs=obs, calls=calls)


def fresh_usage():
    return {"five_hour": 42.0, "seven_day": 17.0, "fresh": True, "age_min": 5,
            "five_hour_resets_at": 1792000000, "seven_day_resets_at": 1792500000}


class SpendCase(unittest.TestCase):
    """Renders the Spend page from the obs fixtures; `calls` of the last page() holds what it asked hm-obs for."""

    def page(self, usage=fresh_usage, spend=None, error=None, **query):
        c = obs_ctx(spend=spend_obs() if spend is None and not error else spend, error=error, **query)
        c.usage_fn = usage
        d = tokens.data(c)
        json.dumps(d)
        self.calls = c.calls
        return d, str(tokens.render(c, d))

    @staticmethod
    def strip_items(html):
        """{label: (value, delta text or None)} of the headline strip."""
        out = {}
        for item in re.findall(r'<div class="strip-item">(.*?)</div>', html):
            lab = re.search(r'strip-label">([^<]*)<', item).group(1)
            val = re.search(r'strip-value">([^<]*)<', item).group(1)
            dl = re.search(r'strip-delta[^"]*">([^<]*)<', item)
            out[lab] = (val, dl.group(1) if dl else None)
        return out


class TestStrip(SpendCase):
    def test_five_figures_with_deltas_and_not_attributed(self):
        _, html = self.page()
        items = self.strip_items(html)
        self.assertEqual(list(items), ["Spend", "Per merged PR", "Polling", "Cache writes", "Weekly limit"])
        self.assertEqual(items["Spend"], ("$50.00", "! +$10.00 vs previous · 8.0% not attributed"))
        self.assertEqual(items["Per merged PR"], ("$10.00", "no change vs previous"))
        self.assertEqual(items["Polling"], ("26%", "! +5.6% vs previous"))
        self.assertEqual(items["Cache writes"], ("20%", "−2.5% vs previous"))   # cheaper than before: no "!"
        self.assertEqual(items["Weekly limit"], ("17%", None))
        self.assertIn("How these are counted", html)

    def test_unknown_values_show_a_dash(self):
        cur = dict(spend_obs()["current"], per_merged_pr_usd=None, cache_write_pct=None)
        _, html = self.page(spend=spend_obs(current=cur, previous=None, unattributed_pct=None))
        items = self.strip_items(html)
        self.assertEqual(items["Per merged PR"], ("–", None))
        self.assertEqual(items["Cache writes"], ("–", None))
        self.assertEqual(items["Spend"], ("$50.00", None))        # no previous window: no delta, no "not attributed"
        self.assertNotIn("not attributed", items["Spend"][1] or "")

    def test_weekly_limit_warn_tone_adds_a_bang_to_its_delta(self):
        # the warn tone (80% and up) only shows through the delta line, so a stale label is where "!" appears
        u = dict(fresh_usage(), seven_day=85.0, fresh=False, age_min=200)
        _, html = self.page(usage=lambda: u)
        self.assertEqual(self.strip_items(html)["Weekly limit"], ("85%", "! stale: 200 min old"))
        u["seven_day"] = 79.0
        _, calm = self.page(usage=lambda: u)
        self.assertEqual(self.strip_items(calm)["Weekly limit"], ("79%", "stale: 200 min old"))

    def test_weekly_limit_near_the_limit_says_so_when_fresh(self):
        u = dict(fresh_usage(), seven_day=85.0)
        _, html = self.page(usage=lambda: u)
        self.assertEqual(self.strip_items(html)["Weekly limit"], ("85%", "! near the limit"))   # amber never alone


class TestChart(SpendCase):
    def test_hourly_stacked_chart_with_legend(self):
        _, html = self.page()
        self.assertIn("Spend by hour", html)
        self.assertRegex(html, r'<svg viewBox="0 0 960 220" height="220" width="100%" role="img"')
        self.assertEqual(re.findall(r'<li><span class="lg-sw (b-\w+)" aria-hidden="true"></span>(\w+)</li>', html),
                         [("b-spawn", "Spawn"), ("b-polling", "Polling"), ("b-work", "Work")])
        self.assertEqual(len(re.findall(r'<rect class="b-work"', html)), 2)       # one work rect per hourly bin
        self.assertIn("Work: $20</title>", html)
        self.assertIn('<table class="sr-only">', html)

    def test_daily_bins_and_empty_chart(self):
        days = [{"start": "2026-10-22T00:00:00Z", "work_usd": 5.0, "polling_usd": 1.0, "spawn_usd": 0.0},
                {"start": "2026-10-23T00:00:00Z", "work_usd": 7.0, "polling_usd": 2.0, "spawn_usd": 1.0}]
        _, html = self.page(spend=spend_obs(bin_h=24, bins=days), range="7d")
        self.assertIn("Spend by day", html)
        self.assertRegex(html, r'<text class="axis-x"[^>]*>\d\d-\d\d</text>')
        _, none = self.page(spend=spend_obs(bins=[]))
        self.assertIn("No spend in this range", none)
        self.assertNotIn('<svg viewBox="0 0 960 220"', none)


class TestTable(SpendCase):
    def test_group_by_links_keep_range_project_and_org(self):
        _, html = self.page(range="24h", project="p1", org="cursor", group="model", role="coder")
        seg = html.split('aria-label="Group by"')[1].split("</nav>")[0]
        links = re.findall(r'<a href="([^"]+)"( aria-current="true")?>([^<]+)</a>', seg)
        self.assertEqual([t for _, _, t in links], ["Role", "Model", "Goal", "Project", "Agent", "Session"])
        for href, _, _ in links:
            for part in ("range=24h", "project=p1", "org=cursor", "role=coder"):
                self.assertIn(part, href)
            self.assertTrue(href.startswith("/spend?"))
        self.assertIn("group=session", links[-1][0])
        self.assertEqual([t for _, cur, t in links if cur], ["Role"])           # the answer's group is the current one

    def test_not_attributed_row_and_its_drill_link(self):
        _, html = self.page()
        self.assertIn('<span class="muted">Not attributed</span>', html)
        self.assertIn(f"role={queries.NOT_ATTRIBUTED}&amp;group=model", html)
        self.assertEqual(queries.NOT_ATTRIBUTED, "-")
        coder = re.search(r'href="(/spend\?[^"]*role=coder[^"]*)"', html).group(1)
        self.assertIn("group=model", coder)
        # the link goes back through obs_args: "-" reaches hm-obs as the empty (Not attributed) value
        self.assertEqual(queries.obs_args("spend", {"role": "-", "group": "model"}), ["--hours", "168", "--group", "model", "--role", ""])

    def test_polling_and_spawn_warn_cells(self):
        _, html = self.page()
        self.assertEqual(re.findall(r'<span class="warn-cell">(! [^<]+)</span>', html), ["! $10.29", "! $3.00"])
        self.assertIn("! marks polling over 20%", html)
        self.assertNotIn("! $0.50", html)

    def test_unknown_cells_and_no_rows(self):
        _, html = self.page()
        self.assertRegex(html, r'<span class="cl">\$/PR</span>–</td>')            # Not attributed row has no $/PR
        _, none = self.page(spend=spend_obs(rows=[]))
        self.assertIn("No spend in this range", none)
        self.assertNotIn('<table class="dt">', none)

    def test_filters_crumb_and_clear_link(self):
        flt = {"role": "coder", "model": ""}
        _, html = self.page(spend=spend_obs(filters=flt, group="goal"), range="24h", role="coder", model="-", org="cursor")
        crumb = re.search(r'<p class="muted drill">(.*?)</p>', html).group(1)
        self.assertTrue(crumb.startswith("Filtered by role coder, model Not attributed"))
        clear = re.search(r'<a href="([^"]+)">clear</a>', crumb).group(1)
        self.assertEqual(clear, "/spend?range=24h&amp;org=cursor")               # drill filters dropped, header state kept
        _, plain = self.page()
        self.assertNotIn("Filtered by", plain)

    def test_clear_keeps_the_header_project(self):
        _, html = self.page(spend=spend_obs(filters={"role": "coder"}), project="p1", role="coder")
        clear = re.search(r'<a href="([^"]+)">clear</a>', html).group(1)
        self.assertEqual(clear, "/spend?project=p1")

    def test_links_carry_only_known_query_keys(self):
        _, html = self.page(org="evil", sort="<b>x", range="9y", group="nope", foo="bar")
        links = re.findall(r'href="(/spend[^"]*)"', html)
        self.assertTrue(links)
        for h in links:
            for bad in ("evil", "sort", "foo", "nope", "9y", "%3Cb"):
                self.assertNotIn(bad, h)

    def test_money_and_percent_formats(self):
        self.assertEqual((tokens._money(None), tokens._money(0.5), tokens._money(1234.5), tokens._pct(None), tokens._pct(8.0),
                          tokens._pct(25.6)), ("–", "$0.50", "$1,234", "–", "8.0%", "26%"))


class TestStates(SpendCase):
    def test_banners_begin_with_a_bang(self):
        _, html = self.page(spend=spend_obs(banners=["! Polling is high", "Cache writes are high", ""]))
        texts = re.findall(r'<p class="banner" role="note">([^<]*)</p>', html)
        self.assertEqual(texts, ["! Polling is high", "! Cache writes are high"])
        _, calm = self.page(spend=spend_obs(banners=[]))
        self.assertNotIn('class="banners"', calm)

    def test_error_state_when_obs_fails(self):
        d, html = self.page(error="hm-obs is not built yet")
        self.assertIsNone(d["obs"])
        self.assertEqual(d["error"], "hm-obs is not built yet")
        self.assertIn("! Spend data unavailable: hm-obs is not built yet", html)
        self.assertIn("How this is counted", html)
        self.assertNotIn("strip-item", html)
        self.assertIn("Spend appears once the tick has run hm-obs ingest.", html)

    def test_no_obs_function_in_the_ctx(self):
        c = obs_ctx(spend=spend_obs())
        del c.obs
        d = tokens.data(c)
        self.assertIn("! Spend data unavailable: hm-obs is not available in this server", str(tokens.render(c, d)))
        self.assertIn("unknown error", str(tokens.render(c, dict(d, error=None))))

    def test_range_label_and_default(self):
        self.assertEqual(self.page(range="24h")[0]["range"], "24h")
        d, _ = self.page(range="bogus")
        self.assertEqual((d["range"], d["range_label"]), ("7d", "7 days"))


class TestObsArgs(SpendCase):
    def test_page_asks_hm_obs_for_range_group_drill_and_project(self):
        self.page(range="24h", group="model", role="coder", goal="-", project="p1")
        self.assertEqual(self.calls, [("spend", ["--hours", "24", "--group", "model", "--role", "coder", "--goal", "",
                                                 "--project", "p1"])])
        self.page()
        self.assertEqual(self.calls, [("spend", ["--hours", "168", "--group", "role"])])

    def test_hostile_values_never_reach_argv(self):
        self.page(group="evil", range="9999d", role="a b;rm -rf", model=XSS, goal="G-1")
        self.assertEqual(self.calls, [("spend", ["--hours", "168", "--group", "role", "--goal", "G-1"])])

    def test_flow_argv_has_only_hours_and_project(self):
        self.assertEqual(queries.obs_args("flow", {"range": "30d", "project": "p1", "group": "model", "role": "x"}),
                         ["--hours", "720", "--project", "p1"])


class TestUsage(SpendCase):
    def test_fresh_values_and_reset(self):
        d, html = self.page()
        self.assertEqual((d["usage"]["five_hour"], d["usage"]["seven_day"]), (42.0, 17.0))
        self.assertEqual((d["usage"]["five_hour_resets_at"], d["usage"]["seven_day_resets_at"]), (1792000000, 1792500000))
        self.assertEqual(self.strip_items(html)["Weekly limit"], ("17%", None))
        self.assertNotIn("stale:", html)

    def test_reset_from_real_shaped_strings(self):
        raw = {"five_hour": "9", "seven_day": "40", "fresh": True, "age_min": 1,
               "five_hour_resets_at": 1792888800, "seven_day_resets_at": 1793000000}
        d, html = self.page(usage=lambda: raw)
        self.assertEqual((d["usage"]["five_hour_resets_at"], d["usage"]["seven_day_resets_at"]),
                         (1792888800, 1793000000))
        self.assertEqual(self.strip_items(html)["Weekly limit"][0], "40%")      # a numeric string still reads as a percent

    def test_unparsed_strings_are_not_reparsed_here(self):
        # usage() owns the parsing; the view only keeps ints, so a raw string never becomes a reset time
        u = dict(fresh_usage(), five_hour_resets_at="1792888800", seven_day_resets_at=float("inf"))
        d, html = self.page(usage=lambda: u)
        self.assertEqual((d["usage"]["five_hour_resets_at"], d["usage"]["seven_day_resets_at"]), (None, None))

    def test_reset_garbage_is_omitted(self):
        u = dict(fresh_usage(), five_hour_resets_at="soon", seven_day_resets_at=True)
        d, html = self.page(usage=lambda: u)
        self.assertEqual((d["usage"]["five_hour_resets_at"], d["usage"]["seven_day_resets_at"]), (None, None))

    def test_stale_label(self):
        u = dict(fresh_usage(), fresh=False, age_min=200)
        d, html = self.page(usage=lambda: u)
        self.assertEqual(d["usage"]["label"], "stale: 200 min old")
        self.assertEqual(self.strip_items(html)["Weekly limit"], ("17%", "stale: 200 min old"))

    def test_unreadable_and_failing(self):
        bad = {"five_hour": None, "seven_day": None, "fresh": False, "age_min": -1, "unreadable": True}
        d, html = self.page(usage=lambda: bad)
        self.assertEqual((d["usage"], self.strip_items(html)["Weekly limit"]), ({"unreadable": True, "label": "unreadable"}, ("–", None)))

        def boom():
            raise OSError("x")
        d, html = self.page(usage=boom)
        self.assertTrue(d["usage"]["unreadable"])
        self.assertEqual(self.strip_items(html)["Weekly limit"][0], "–")
        d, html = self.page(usage=None)
        self.assertIsNone(d["usage"])
        self.assertEqual(self.strip_items(html)["Weekly limit"][0], "–")


class TestSafety(SpendCase):
    def test_privacy_and_classes(self):
        d, html = self.page()
        blob = html + json.dumps(d)
        self.assertNotIn(CANARY, blob)
        self.assertNotIn("<script", html)
        self.assertIsNone(UUID_RE.search(blob))
        self.assertNotIn("/tmp/x", blob)                      # no cwd
        used = {c for m in re.findall(r'class="([^"]*)"', html) for c in m.split()}
        self.assertEqual(used - wh.CLASSES, set())
        self.assertNotIn("style=", html)

    def test_xss_in_labels_keys_and_filters_escaped(self):
        rows = spend_obs()["rows"] + [{"key": XSS, "label": XSS, "sessions": 1, "requests": 1, "cost_usd": 1.0, "share_pct": 2.0,
                                       "spawn_usd": 0.0, "polling_usd": 0.0, "work_usd": 1.0, "per_merged_pr_usd": None,
                                       "spark_usd": []}]
        _, html = self.page(spend=spend_obs(rows=rows, filters={"role": XSS}, banners=[f"! {XSS}"]), org=XSS)
        self.assertNotIn("<script", html)
        self.assertIn("&lt;script&gt;", html)
        self.assertNotIn("alert(1)</script>", html)

    def test_registered_and_fast(self):
        t0 = time.time()
        self.page()
        self.assertLess(time.time() - t0, 1.0)
        self.assertEqual((tokens.PATH, tokens.TITLE), ("/spend", "Spend"))


if __name__ == "__main__":
    unittest.main()
