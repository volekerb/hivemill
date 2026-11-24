"""G-019 B6 insights pages: /quality and /resources (seeded temp DBs, fixed NOW of test_web_queries) and, since G-069,
/spend (was /tokens) and /flow (was /throughput), which take every number from ctx.obs(kind) = `hm-obs api spend|flow`.
The obs fixtures come from test_web_views_tokens; HTTP tests mock only the process boundary with a stub hm-obs script."""
import ast
import datetime as dt
import json
import re
import shlex
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

import _gitenv  # noqa: F401,E402
from test_web_queries import NOW, make_db, ts  # noqa: E402
from test_web_server import ServerCase  # noqa: E402
from test_web_views_tokens import flow_obs, obs_ctx, spend_obs  # noqa: E402
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
# data() keys per page (acceptance 6); throughput and tokens are the Flow and Spend dicts since G-069
KEYS = {
    "quality": {"overall", "teams", "weeks", "formula"},
    "throughput": {"range", "range_label", "obs", "error"},
    "resources": {"gate", "machine", "config", "timeline", "per_day", "before"},
    "tokens": {"range", "range_label", "obs", "error", "usage", "query"},
}


def strip_labels(html):
    return [m.replace("&gt;", ">") for m in re.findall(r'<dt class="strip-label">(?:<a [^>]*>)?([^<]+)', html)]


def strip_values(html):
    """{label: (value, delta or None)} of the strip items on the page."""
    out = {}
    for item in re.findall(r'<div class="strip-item">(.*?)</div>', html):
        label = re.search(r'<dt class="strip-label">(?:<a [^>]*>)?([^<]+)', item).group(1).replace("&gt;", ">")
        val = re.search(r'<dd class="strip-value">([^<]*)', item).group(1)
        delta = re.search(r'<dd class="strip-delta[^"]*">([^<]*)', item)
        out[label] = (val, delta.group(1) if delta else None)
    return out


def howto_count(html):
    return html.count("How this is counted</a>")


def banner_texts(html):
    return [re.sub(r"<[^>]+>", "", m) for m in re.findall(r'<p class="banner" role="note">(.*?)</p>', html)]


def section_after(html, title):
    """The HTML of one <section> by its h2 title."""
    return html.split(f"<h2>{title} </h2>", 1)[1].split("</section>", 1)[0]


def obs_render(view, spend=None, flow=None, error=None, **query):
    """(data, html, ctx) of an obs-backed page for a fixture answer; the data dict must stay JSON-able."""
    c = obs_ctx(spend=spend, flow=flow, error=error, **query)
    d = view.data(c)
    json.dumps(d)
    return d, str(view.render(c, d)), c


class InsightServer(ServerCase):
    """The real server with a ready (empty) token index. With OBS = True it also gets a stub `hm-obs` (a shell script that
    logs its argv and prints the fixture JSON for `api spend|flow`): the only mock, at the process boundary."""
    OBS = False
    SPEND = None
    FLOW = None

    def setUp(self):
        super().setUp()
        # pin the clock the request handler stamps pages with (HH:MM in the header): two renders must not straddle a minute
        frozen = NOW  # pin to fixture NOW (not wall clock) so 7-day windows match seeded data
        clock = type("FrozenDatetime", (dt.datetime,), {"now": classmethod(lambda cls, tz=None: frozen.astimezone(tz) if tz else frozen.replace(tzinfo=None))})
        patcher = mock.patch.object(server, "dt", SimpleNamespace(datetime=clock))
        patcher.start()
        self.addCleanup(patcher.stop)
        # stop the base class's serve_forever thread before closing: closing under a running loop leaves it spinning on a dead socket
        self.httpd.shutdown()
        self.httpd.server_close()
        extra = {}
        self.args_log = Path(self.tmp.name) / "obs-args.log"
        if self.OBS:
            home = Path(self.tmp.name) / "home"
            home.mkdir()
            (home / "spend.json").write_text(json.dumps(self.SPEND or spend_obs()))
            (home / "flow.json").write_text(json.dumps(self.FLOW or flow_obs()))
            stub = Path(self.tmp.name) / "hm-obs"
            stub.write_text('#!/bin/sh\necho "$@" >> %s\ncase "$2" in\n  spend) cat %s;;\n  flow) cat %s;;\n  *) exit 3;;\nesac\n'
                            % (shlex.quote(str(self.args_log)), shlex.quote(str(home / "spend.json")), shlex.quote(str(home / "flow.json"))))
            stub.chmod(0o755)
            extra = {"factory_home": home, "obs_bin": str(stub)}
        self.httpd = server.make_server(self.db_path, "127.0.0.1", 0, (), tokens=lambda: SNAP,
                                        usage_fn=lambda: {"five_hour": 10.0, "seven_day": 20.0, "fresh": True, "age_min": 1},
                                        mem_fn=lambda: 2048, defaults={"max_agents": "6"}, **extra)
        import threading
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        self.addCleanup(lambda: (self.httpd.shutdown(), self.httpd.server_close()))

    def text(self, path):
        code, _, body = self.get(path)
        self.assertEqual(code, 200, path)
        return body.decode()

    def obs_calls(self):
        """The argv tails the stub hm-obs was run with, one line per call."""
        return self.args_log.read_text().splitlines() if self.args_log.exists() else []


class ObsServer(InsightServer):
    OBS = True


class TestQualityPage(InsightServer):
    def test_first_row_is_four_strip_figures_and_not_measured_is_spelled_out(self):
        html = self.text("/quality")
        self.assertEqual(strip_labels(html)[:4], ["Rejections per merged PR", "First-try merge rate", "CI-blocked merges", "Median attempts"])
        self.assertEqual(html.count('<dl class="strip">'), 2)       # the headline strip and the one in More metrics
        self.assertNotIn("n/a, see G-013", html)
        self.assertIn("Not measured yet", html)

    def test_every_definition_from_main_is_in_a_how_these_are_counted_list(self):
        html = self.text("/quality")
        self.assertIn("How these are counted", html)
        for d in DEFINITIONS:
            self.assertRegex(html, r"<dd>[^<]*" + re.escape(d) + "</dd>", d)

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


def no_org_nav(html):
    """The page without the header's org filter (G-028), which has its own aria-current item."""
    return re.sub(r'<nav class="seg seg-links orgseg".*?</nav>', "", html, flags=re.S)


class TestFlowServed(ObsServer):
    """/flow through the real server with a stub hm-obs."""

    def test_range_segment_marks_the_current_range_and_a_bad_range_falls_back_to_7_days(self):
        for key in ("24h", "3d", "7d", "30d"):
            html = self.text(f"/flow?range={key}")
            nav = re.search(r'<nav class="seg seg-links" aria-label="Time range">.*?</nav>', html, re.S).group(0)
            self.assertEqual(nav.count('aria-current="true"'), 1, key)
            self.assertRegex(nav, rf'<a href="[^"]*range={key}"[^>]*aria-current="true"')
        nav = re.search(r'<nav class="seg seg-links" aria-label="Time range">.*?</nav>', self.text("/flow?range=9d"), re.S).group(0)
        self.assertRegex(nav, r'range=7d"[^>]*aria-current="true"')

    def test_page_renders_the_stub_answer_and_passes_the_range_as_hours(self):
        html = self.text("/flow?range=3d&project=acme")
        self.assertEqual(strip_labels(html), ["Merged", "Median open → merge", "p90 open → merge", "2+ review rounds", "Open > 6 h"])
        self.assertIn("Where PRs wait", html)
        self.assertIn("Stuck now", html)
        self.assertEqual(self.obs_calls(), ["api flow --home " + str(Path(self.tmp.name) / "home") + " --hours 72 --project acme"])

    def test_unknown_params_are_ignored_and_the_page_is_the_same(self):
        self.assertEqual(self.text("/flow?sort=evil&dir=up&days=9"), self.text("/flow"))
        self.assertEqual({c.split(" --hours ")[1] for c in self.obs_calls()}, {"168"})

    def test_json_is_the_flow_data_dict_with_the_obs_answer(self):
        d = json.loads(self.text("/api/throughput.json"))
        self.assertEqual(d["obs"]["merged"], 6)
        self.assertEqual(d["range"], "7d")
        self.assertIsNone(d["error"])


class TestFlowWithoutHmObs(InsightServer):
    def test_flow_is_200_with_a_banner_and_the_docs_link_when_hm_obs_is_not_built(self):
        html = self.text("/flow")
        self.assertEqual(banner_texts(html), ["! Flow data unavailable: hm-obs is not built yet: the tick builds it from go/cmd/hm-obs within a few minutes"])
        self.assertEqual(howto_count(html), 1)
        self.assertEqual(json.loads(self.text("/api/throughput.json"))["obs"], None)

    def test_api_obs_is_503_with_the_error_when_hm_obs_is_missing(self):
        code, headers, body = self.get("/api/obs/flow")
        self.assertEqual(code, 503)
        self.assertIn("hm-obs is not built yet", json.loads(body)["error"])
        self.assertEqual(headers.get("Retry-After"), "60")


class TestOldPathsRedirect(ObsServer):
    def test_tokens_and_throughput_301_to_spend_and_flow_keeping_the_query(self):
        for old, new in (("/tokens", "/spend"), ("/throughput", "/flow")):
            for q in ("", "?range=30d", "?range=24h&goal=G-1&group=model", "?days=30&by=task"):
                code, headers, body = self.get(old + q)
                self.assertEqual(code, 301, old + q)
                self.assertEqual(headers["Location"], new + q, old + q)
                self.assertEqual(body, b"")

    def test_new_paths_are_200_and_the_redirect_target_renders(self):
        self.assertIn("Where it goes", self.text("/spend?range=30d"))
        self.assertIn("Where PRs wait", self.text("/flow?range=30d"))

    def test_nav_marks_spend_and_flow_active_on_their_own_paths(self):
        for path, name in (("/spend", "Spend"), ("/flow", "Flow")):
            html = self.text(path)
            self.assertIn(f'<a href="{path}" class="nav-item nav-item--active" aria-current="page">', html)


class TestSpendServed(ObsServer):
    """/spend through the real server with a stub hm-obs."""

    def test_group_whitelist_falls_back_to_role_and_the_stub_is_asked_for_it(self):
        for bad in ("evil", "goal%27--", "", "../x", "task"):
            html = self.text(f"/spend?group={bad}")
            self.assertRegex(html, r'<a href="/spend\?group=[^"]*"[^>]*aria-current="true">Role</a>|<a href="/spend[^"]*" aria-current="true">Role</a>', bad)
        for call in self.obs_calls():
            self.assertTrue(call.endswith("--hours 168 --group role"), call)
        for g in ("model", "goal", "project", "agent", "session"):
            self.text(f"/spend?group={g}")
        self.assertTrue(self.obs_calls()[-1].endswith(f"--hours 168 --group session"))

    def test_old_urls_with_junk_query_still_answer_200_and_never_reach_the_binary_unvalidated(self):
        for p in ("/spend?range=bogus", "/spend?goal=%27", "/spend?role=a%3Bb&model=%24x", "/spend?group=evil&goal=G-1"):
            self.text(p)
        for call in self.obs_calls():
            self.assertNotIn(";", call)
            self.assertNotIn("$", call)
            self.assertNotIn("'", call)
        self.assertTrue(any(c.endswith("--hours 168 --group role --goal G-1") for c in self.obs_calls()))

    def test_range_and_group_links_keep_the_other_filters(self):
        html = self.text("/spend?range=30d&goal=G-1&group=agent")
        self.assertIn('href="/spend?range=30d&amp;group=model&amp;goal=G-1"', html)
        self.assertIn('href="/spend?range=30d&amp;group=session&amp;goal=G-1"', html)

    def test_usage_gauge_is_the_weekly_limit_strip_figure_from_the_server_usage(self):
        html = self.text("/spend")
        self.assertEqual(strip_values(html)["Weekly limit"], ("20%", None))

    def test_api_obs_spend_is_the_stub_answer_for_the_validated_query(self):
        code, headers, body = self.get("/api/obs/spend?range=24h&group=model")
        self.assertEqual((code, headers["Content-Type"]), (200, "application/json"))
        self.assertEqual(json.loads(body)["current"]["cost_usd"], 50.0)
        self.assertTrue(self.obs_calls()[-1].endswith("--hours 24 --group model"))


class TestSpendPage(unittest.TestCase):
    """The Spend render from the obs fixture: every figure is the fixture's, nothing is recomputed from the DB."""

    def test_strip_has_the_five_headline_figures_with_deltas_against_the_previous_window(self):
        _, html, _ = obs_render(tokens, spend=spend_obs())
        v = strip_values(html)
        self.assertEqual(list(v), ["Spend", "Per merged PR", "Polling", "Cache writes", "Weekly limit"])
        self.assertEqual(v["Spend"], ("$50.00", "! +$10.00 vs previous · 8.0% not attributed"))
        self.assertEqual(v["Per merged PR"], ("$10.00", "no change vs previous"))
        self.assertEqual(v["Polling"], ("26%", "! +5.6% vs previous"))
        self.assertEqual(v["Cache writes"], ("20%", "−2.5% vs previous"))
        self.assertEqual(v["Weekly limit"], ("17%", None))

    def test_per_merged_pr_figure_rising_against_previous_is_a_warned_delta(self):
        s = spend_obs()
        s["current"]["per_merged_pr_usd"], s["previous"]["per_merged_pr_usd"] = 12.5, 10.0
        v = strip_values(obs_render(tokens, spend=s)[1])
        self.assertEqual(v["Per merged PR"], ("$12.50", "! +$2.50 vs previous"))

    def test_per_merged_pr_falling_is_a_good_delta_without_the_warning_mark(self):
        s = spend_obs()
        s["current"]["per_merged_pr_usd"], s["previous"]["per_merged_pr_usd"] = 7.0, 10.0
        html = obs_render(tokens, spend=s)[1]
        self.assertEqual(strip_values(html)["Per merged PR"], ("$7.00", "−$3.00 vs previous"))
        self.assertIn('<dd class="strip-delta fg-success">−$3.00 vs previous</dd>', html)

    def test_zero_merges_shows_the_empty_dash_and_no_delta(self):
        s = spend_obs()
        s["current"].update(merged_prs=0, per_merged_pr_usd=None)
        v = strip_values(obs_render(tokens, spend=s)[1])
        self.assertEqual(v["Per merged PR"], ("–", None))

    def test_no_previous_window_has_no_deltas_and_no_division(self):
        v = strip_values(obs_render(tokens, spend=spend_obs(previous=None))[1])
        self.assertEqual(v["Per merged PR"], ("$10.00", None))
        self.assertEqual(v["Spend"][1], " · 8.0% not attributed")

    def test_per_row_dollars_per_pr_come_from_the_rows_and_null_is_a_dash(self):
        _, html, _ = obs_render(tokens, spend=spend_obs())
        cells = {m[0]: m[1] for m in re.findall(r'<span class="cl">\$/PR</span>([^<]*)</td>', html) and
                 zip(["coder", "lead", "Not attributed"], re.findall(r'<span class="cl">\$/PR</span>([^<]*)</td>', html))}
        self.assertEqual(cells, {"coder": "$4.60", "lead": "$3.00", "Not attributed": "–"})

    def test_table_has_the_pr_column_and_phone_labels_on_every_numeric_cell(self):
        _, html, _ = obs_render(tokens, spend=spend_obs())
        html = html.split("Where it goes", 1)[1]         # the chart's fallback table comes first
        head = re.search(r"<thead>.*?</thead>", html, re.S).group(0)
        self.assertEqual(re.findall(r"<th[^>]*>([^<]*)</th>", head),
                         ["Role", "Sessions", "Requests", "$", "Share", "Spawn", "Polling", "Work", "$/PR", "Trend"])
        body = re.search(r"<tbody>.*?</tbody>", html, re.S).group(0)
        self.assertEqual(body.count('<td class="num">'), body.count('<td class="num"><span class="cl">'))

    def test_warned_cells_start_with_a_bang_and_only_the_flagged_ones(self):
        _, html, _ = obs_render(tokens, spend=spend_obs())
        self.assertEqual(re.findall(r'<span class="warn-cell">([^<]*)</span>', html), ["! $10.29", "! $3.00"])

    def test_not_attributed_row_is_pinned_text_and_drills_with_the_dash_key(self):
        _, html, _ = obs_render(tokens, spend=spend_obs())
        self.assertIn('<span class="muted">Not attributed</span>', html)
        self.assertIn('href="/spend?role=-&amp;group=model"', html)
        self.assertIn('href="/spend?role=coder&amp;group=model"', html)

    def test_drill_filters_show_a_crumb_with_a_clear_link(self):
        s = spend_obs(group="model", drill="goal", filters={"role": "coder"})
        _, html, _ = obs_render(tokens, spend=s, role="coder", group="model")
        self.assertIn("Filtered by role coder", html)
        self.assertIn('<a href="/spend?group=model">clear</a>', html)
        self.assertIn('href="/spend?group=goal&amp;role=coder&amp;model=coder"', html)     # the next drill keeps the role filter

    def test_last_group_has_no_drill_links(self):
        s = spend_obs(group="session", drill=None)
        html = obs_render(tokens, spend=s, group="session")[1]
        self.assertNotRegex(html, r'<td class="c-title"><a ')

    def test_banner_from_hm_obs_starts_with_a_bang_and_a_plain_one_gets_it(self):
        html = obs_render(tokens, spend=spend_obs(banners=["! Polling is high.", "Cache writes doubled."]))[1]
        self.assertEqual(banner_texts(html), ["! Polling is high.", "! Cache writes doubled."])

    def test_definitions_are_in_one_how_these_are_counted_list_word_for_word(self):
        html = obs_render(tokens, spend=spend_obs())[1]
        self.assertEqual(html.count("How these are counted"), 1)
        self.assertIn("<dt>Per merged PR</dt><dd>Spend divided by the PRs merged in the window. – when none merged.</dd>", html)

    def test_exactly_one_how_this_is_counted_link_on_data_and_on_error(self):
        self.assertEqual(howto_count(obs_render(tokens, spend=spend_obs())[1]), 1)
        self.assertEqual(howto_count(obs_render(tokens, error="boom")[1]), 1)
        self.assertIn('href="/docs/obs.md"', obs_render(tokens, spend=spend_obs())[1])

    def test_hm_obs_error_is_a_banner_and_an_empty_state_not_a_crash(self):
        d, html, _ = obs_render(tokens, error="cannot run hm-obs (No such file)")
        self.assertIsNone(d["obs"])
        self.assertEqual(banner_texts(html), ["! Spend data unavailable: cannot run hm-obs (No such file)"])
        self.assertIn("Spend appears once the tick has run hm-obs ingest.", html)
        self.assertNotIn("strip-item", html)

    def test_ctx_without_obs_says_hm_obs_is_not_available(self):
        c = obs_ctx(spend=spend_obs())
        del c.obs
        d = tokens.data(c)
        self.assertEqual(d["error"], "hm-obs is not available in this server")
        self.assertIn("! Spend data unavailable", str(tokens.render(c, d)))

    def test_empty_rows_and_bins_are_calm_empty_states(self):
        html = obs_render(tokens, spend=spend_obs(rows=[], bins=[]))[1]
        self.assertIn("No spend in this range", html)
        self.assertIn("No spend in this range</", html)
        self.assertNotIn("<table", html)

    def test_chart_is_stacked_work_polling_spawn_with_a_table_fallback(self):
        html = obs_render(tokens, spend=spend_obs())[1]
        sec = section_after(html, "Spend by hour")
        for cls in ("b-work", "b-polling", "b-spawn"):
            self.assertIn(f'<rect class="{cls}"', sec)
        self.assertIn('</svg><table class="sr-only">', sec)
        self.assertIn("<title>11:00 Polling: $10</title>", sec) if False else self.assertRegex(sec, r"<title>\d\d:00 Polling: \$10</title>")

    def test_daily_bins_title_the_chart_by_day(self):
        s = spend_obs(bin_h=24, bins=[{"start": "2026-10-23T00:00:00Z", "work_usd": 5.0, "polling_usd": 1.0, "spawn_usd": 0.0}])
        self.assertIn("<h2>Spend by day </h2>", obs_render(tokens, spend=s)[1])

    def test_asks_hm_obs_for_the_query_range_group_and_drill_filters_only(self):
        _, _, c = obs_render(tokens, spend=spend_obs(), range="24h", group="model", goal="G-1", role="x;y", evil="1")
        self.assertEqual(c.calls, [("spend", ["--hours", "24", "--group", "model", "--goal", "G-1"])])

    def test_no_message_text_style_attributes_or_unknown_classes(self):
        s = spend_obs()
        s["rows"][0]["label"] = "<script>alert(1)</script>"
        _, html, _ = obs_render(tokens, spend=s)
        self.assertNotIn("<script", html)
        self.assertIn("&lt;script&gt;", html)
        self.assertNotIn("style=", html)
        used = {c for m in re.findall(r'class="([^"]*)"', html) for c in m.split()}
        self.assertEqual(used - wh.CLASSES, set())

    def test_usage_failures_show_on_the_weekly_limit_figure_only(self):
        c = obs_ctx(spend=spend_obs())
        c.usage_fn = lambda: (_ for _ in ()).throw(OSError("x"))
        v = strip_values(str(tokens.render(c, tokens.data(c))))
        self.assertEqual(v["Weekly limit"], ("–", None))
        self.assertEqual(v["Spend"][0], "$50.00")

    def test_stale_usage_labels_the_weekly_limit_and_a_high_value_warns(self):
        c = obs_ctx(spend=spend_obs())
        c.usage_fn = lambda: {"five_hour": 5.0, "seven_day": 85.0, "fresh": False, "age_min": 200}
        v = strip_values(str(tokens.render(c, tokens.data(c))))
        self.assertEqual(v["Weekly limit"], ("85%", "! stale: 200 min old"))

    def test_obs_dict_is_passed_through_untouched_in_the_json(self):
        s = spend_obs()
        d, _, _ = obs_render(tokens, spend=s)
        self.assertEqual(d["obs"], s)
        self.assertEqual(d["range"], "7d")


class TestFlowPage(unittest.TestCase):
    """The Flow render from the obs fixture."""

    def test_strip_figures_are_merged_median_p90_two_plus_rounds_and_open_over_six_hours(self):
        _, html, _ = obs_render(throughput, flow=flow_obs())
        v = strip_values(html)
        self.assertEqual(list(v), ["Merged", "Median open → merge", "p90 open → merge", "2+ review rounds", "Open > 6 h"])
        self.assertEqual(v["Merged"], ("6", "7 days"))
        self.assertEqual(v["Median open → merge"], ("4.5 h", None))
        self.assertEqual(v["p90 open → merge"], ("12 h", None))
        self.assertEqual(v["2+ review rounds"], ("38%", "! 3 of 8"))
        self.assertEqual(v["Open > 6 h"], ("1", "! see Stuck now"))

    def test_range_label_follows_the_range_param(self):
        v = strip_values(obs_render(throughput, flow=flow_obs(), range="24h")[1])
        self.assertEqual(v["Merged"], ("6", "24 h"))

    def test_two_plus_rounds_under_30_percent_is_neutral_with_n_of_m(self):
        v = strip_values(obs_render(throughput, flow=flow_obs(prs=5, prs_2plus_rounds=1, prs_2plus_rounds_pct=20.0, open_over_6h=0))[1])
        self.assertEqual(v["2+ review rounds"], ("20%", "1 of 5"))
        self.assertEqual(v["Open > 6 h"], ("0", None))

    def test_empty_window_shows_dashes_and_calm_empty_states_not_zero_of_zero(self):
        f = flow_obs(merged=0, open_to_merge_median_h=None, open_to_merge_p90_h=None, prs=0, prs_2plus_rounds=0,
                     prs_2plus_rounds_pct=None, open_over_6h=0, stage_hours=0, stages=[], stuck=[], banners=[])
        html = obs_render(throughput, flow=f)[1]
        v = strip_values(html)
        self.assertEqual((v["Median open → merge"][0], v["p90 open → merge"][0], v["2+ review rounds"]), ("–", "–", ("–", None)))
        self.assertNotIn("0 of 0", html)
        self.assertIn("No stage hours in this range", html)
        self.assertIn("No open PR is older than 6 hours", html)
        self.assertNotIn('class="banners"', html)

    def test_hours_format_one_decimal_under_ten_and_whole_hours_above(self):
        f = flow_obs(open_to_merge_median_h=9.94, open_to_merge_p90_h=1234.4)
        v = strip_values(obs_render(throughput, flow=f)[1])
        self.assertEqual((v["Median open → merge"][0], v["p90 open → merge"][0]), ("9.9 h", "1,234 h"))

    def test_stage_map_main_line_between_coder_and_merge_with_hours_and_share(self):
        html = obs_render(throughput, flow=flow_obs())[1]
        sec = section_after(html, "Where PRs wait")
        main = sec.split('<ol class="stages">')[1].split("</ol>")[0]
        names = re.findall(r'<span class="stage-name">([^<]+)</span>', main)
        self.assertEqual(names, ["Coder", "CI wait", "Review + QA", "Judge", "Merge"])
        self.assertEqual(re.findall(r'<span class="stage-h">([^<]+)</span><span class="stage-share">([^<]+)</span>', main),
                         [("10 h", "20%"), ("20 h", "40%"), ("5.0 h", "10%")])
        self.assertEqual(main.count('<span class="stage-h">–</span>'), 2)           # the two ends hold no queue time
        self.assertIn('aria-label="40% of stage hours"', main)
        self.assertIn('<rect class="stage-fill" x="0" y="0" width="40.0" height="8">', main)

    def test_rework_and_side_queues_are_their_own_lanes_after_the_main_line(self):
        sec = section_after(obs_render(throughput, flow=flow_obs())[1], "Where PRs wait")
        self.assertEqual(re.findall(r'<p class="stage-lane">([^<]+)</p>', sec), ["Main line", "Rework loop", "Side queues"])
        rework = sec.split("Rework loop")[1].split("Side queues")[0]
        self.assertIn("Rework", rework)
        self.assertIn('<span class="stage-h">10 h</span><span class="stage-share">20%</span>', rework)
        side = sec.split("Side queues")[1]
        self.assertIn('class="stage lane-side"', side)
        self.assertIn("Hold", side)
        self.assertIn("50 h of stage time in the window.", sec)

    def test_a_lane_without_stages_has_no_heading(self):
        f = flow_obs(stages=[s for s in flow_obs()["stages"] if s["lane"] == "main"], stage_hours=35.0)
        sec = section_after(obs_render(throughput, flow=f)[1], "Where PRs wait")
        self.assertEqual(re.findall(r'<p class="stage-lane">([^<]+)</p>', sec), ["Main line"])

    def test_share_bar_is_clamped_to_the_viewbox_and_has_no_style_attribute(self):
        stages = [{"stage": "hold", "label": "Hold", "lane": "side", "hours": 900.0, "share_pct": 250.0}]
        html = obs_render(throughput, flow=flow_obs(stages=stages, stage_hours=900.0))[1]
        self.assertIn('width="100.0"', html)
        self.assertNotIn("style=", html)

    def test_stuck_now_table_links_the_pr_and_task_with_stage_label_and_age(self):
        html = obs_render(throughput, flow=flow_obs())[1]
        sec = section_after(html, "Stuck now")
        self.assertEqual(re.findall(r"<th[^>]*>([^<]*)</th>", sec), ["PR", "Task", "Stage", "Age"])
        self.assertIn('<a href="https://github.com/acme/factory/pull/812" rel="noopener" class="mono">acme/factory#812</a>', sec)
        self.assertIn('<a href="/task/T-7">T-7</a>', sec)
        self.assertIn("<td>Review + QA</td>", sec)
        self.assertIn('<span class="cl">Age</span>9.5 h', sec)

    def test_stuck_row_without_task_or_repo_degrades_to_plain_text(self):
        stuck = [{"repo": "", "number": 5, "task": None, "stage": "mystery", "age_h": 7.0}]
        sec = section_after(obs_render(throughput, flow=flow_obs(stuck=stuck))[1], "Stuck now")
        self.assertIn("#5", sec)
        self.assertNotIn("<a ", sec)
        self.assertIn("<td>–</td><td>mystery</td>", sec)

    def test_banners_are_the_hm_obs_ones_each_starting_with_a_bang(self):
        html = obs_render(throughput, flow=flow_obs())[1]
        texts = banner_texts(html)
        self.assertEqual(len(texts), 2)
        self.assertTrue(all(t.startswith("! ") for t in texts))
        self.assertIn("38% of PRs needed 2+ review rounds (3 of 8)", texts[0])
        self.assertTrue(banner_texts(obs_render(throughput, flow=flow_obs(banners=["Slow CI"]))[1]) == ["! Slow CI"])

    def test_banner_and_warning_text_is_escaped(self):
        html = obs_render(throughput, flow=flow_obs(banners=["<script>alert(1)</script>"], warnings=["<b>x</b>"]))[1]
        self.assertNotIn("<script>", html)
        self.assertNotIn("<b>x", html)
        self.assertIn("&lt;b&gt;x&lt;/b&gt;", html)

    def test_definitions_list_and_exactly_one_how_this_is_counted_link(self):
        html = obs_render(throughput, flow=flow_obs())[1]
        self.assertEqual(html.count("How these are counted"), 1)
        self.assertEqual(howto_count(html), 1)
        self.assertIn("<dt>Open &gt; 6 h</dt><dd>Open PRs whose age is over 6 hours right now.</dd>", html)

    def test_hm_obs_error_is_a_banner_and_an_empty_state(self):
        d, html, _ = obs_render(throughput, error="hm-obs api flow took longer than 8 s")
        self.assertIsNone(d["obs"])
        self.assertEqual(banner_texts(html), ["! Flow data unavailable: hm-obs api flow took longer than 8 s"])
        self.assertIn("Flow appears once the tick has run hm-obs ingest.", html)
        self.assertEqual(howto_count(html), 1)

    def test_asks_hm_obs_for_the_range_and_the_validated_project(self):
        self.assertEqual(obs_render(throughput, flow=flow_obs(), range="30d", project="acme")[2].calls,
                         [("flow", ["--hours", "720", "--project", "acme"])])
        self.assertEqual(obs_render(throughput, flow=flow_obs(), project="a b;c")[2].calls, [("flow", ["--hours", "168"])])

    def test_data_timestamp_is_the_obs_at_field(self):
        self.assertIn("Data as of 2026-10-24T11:55:00Z.", obs_render(throughput, flow=flow_obs())[1])


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

    def test_flow_and_spend_json_hold_no_old_throughput_or_tokens_fields(self):
        flow = json.loads(self.text("/api/throughput.json"))
        spend = json.loads(self.text("/api/tokens.json"))
        for gone in ("days", "merges", "unattended", "cost", "stages", "slowest"):
            self.assertNotIn(gone, flow)
        for gone in ("totals", "by_goal", "by_task", "by_agent", "by_model", "by_day", "unpriced"):
            self.assertNotIn(gone, spend)


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


class TestAttendedTasks(Case):
    """F-06: queries.attended_tasks(con, ids) = the merged tasks with owner involvement, from factory records only. The old
    Unattended share tile is gone from the views (G-069); this is the logic that stays in web/queries.py."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / "factory.db"
        self.w = make_db(self.path, seed=False)
        self.addCleanup(self.w.close)
        self.w.execute("ALTER TABLE goals ADD COLUMN org TEXT")
        self.w.execute("INSERT INTO projects(id,name,created) VALUES('p1','P','x')")
        for g, org in (("G-1", "claude"), ("G-2", "cursor")):
            self.w.execute("INSERT INTO goals(id,project_id,title,status,created,updated,org) VALUES(?,?,?,?,?,?,?)",
                           (g, "p1", g, "active", ts(8, 0), ts(8, 0), org))

    def merged(self, tid, goal="G-1"):
        self.w.execute("INSERT INTO tasks(id,goal_id,title,role,status,created,updated) VALUES(?,?,?,?,?,?,?)",
                       (tid, goal, tid, "coder", "done", ts(8, 0), ts(8, 0)))
        self.w.execute(E_SQL, (ts(11, 0), "judge", "task", tid, "merged", "@abc"))

    def event(self, tid, action, detail):
        self.w.execute(E_SQL, (ts(10, 0), "x", "task", tid, action, detail))

    def msg(self, sender, kind, subject, body=None, task_id=None):
        self.w.execute("INSERT INTO messages(sender,recipient,kind,subject,body,task_id,created) VALUES(?,?,?,?,?,?,?)",
                       (sender, "lead", kind, subject, body, task_id, ts(10, 0)))

    def attended(self, *ids):
        return queries.attended_tasks(self.ctx().db, set(ids))

    def test_waits_owner_update_makes_one_of_three_attended(self):
        for t in ("T-1", "T-2", "T-3"):
            self.merged(t)
        self.event("T-2", "update", "status=blocked waits=owner")
        self.event("T-3", "update", "status=in_progress")
        self.assertEqual(self.attended("T-1", "T-2", "T-3"), {"T-2"})

    def test_judge_blocked_auto_merge_zero_is_attended_but_red_checks_are_not(self):
        self.merged("T-1")
        self.merged("T-2")
        self.event("T-1", "judge-blocked", "project has auto_merge=0 - boss must get the owner's OK")
        self.event("T-2", "judge-blocked", "checks not green: unittest")
        self.assertEqual(self.attended("T-1", "T-2"), {"T-1"})

    def test_boss_decision_by_task_id_or_whole_word_is_attended(self):
        for t in ("T-1", "T-2", "T-3", "T-4"):
            self.merged(t)
        self.msg("claude-boss", "decision", "go", task_id="T-1")
        self.msg("cursor-boss", "decision", "about T-2", None)
        self.msg("boss", "decision", "x", "see T-3.")
        self.assertEqual(self.attended("T-1", "T-2", "T-3", "T-4"), {"T-1", "T-2", "T-3"})    # T-4 stays unattended

    def test_non_boss_decision_and_other_kinds_do_not_count(self):
        self.merged("T-1")
        self.merged("T-2")
        self.msg("claude-director", "decision", "about T-1", task_id="T-1")
        self.msg("claude-boss", "report", "about T-2", task_id="T-2")
        self.assertEqual(self.attended("T-1", "T-2"), set())

    def test_t11_in_a_message_does_not_count_for_t111(self):
        self.merged("T-111")
        self.merged("T-11")
        self.msg("claude-boss", "decision", "ok", "T-11 is fine, not T-1112x")
        self.assertEqual(self.attended("T-111", "T-11"), {"T-11"})    # T-111 stays unattended

    def test_only_the_asked_ids_come_back_whatever_the_org_of_the_goal(self):
        self.merged("T-1", "G-1")
        self.merged("T-2", "G-2")
        self.merged("T-3", "G-2")
        self.event("T-3", "update", "waits=owner")
        self.event("T-1", "update", "waits=owner")
        self.assertEqual(self.attended("T-2", "T-3"), {"T-3"})
        self.assertEqual(self.attended("T-1"), {"T-1"})

    def test_no_ids_is_the_empty_set(self):
        self.event("T-1", "update", "waits=owner")
        self.assertEqual(self.attended(), set())

    def test_blind_spots_empty_waits_detail_and_hand_pushes_are_not_seen(self):
        self.merged("T-1")
        self.merged("T-2")
        self.event("T-1", "update", "")                      # `--waits owner` on an already blocked task logs an empty detail
        self.event("T-2", "pushed", "by the owner")          # an owner push by hand leaves no attended event
        self.assertEqual(self.attended("T-1", "T-2"), set())

    def test_read_only_and_no_view_calls_it_any_more(self):
        self.merged("T-1")
        c = self.ctx()
        before = [c.db.execute(f"SELECT COUNT(*) FROM {t}").fetchone()[0] for t in ("events", "messages", "tasks")]
        with mock.patch.object(queries, "attended_tasks", wraps=queries.attended_tasks) as m:
            queries.attended_tasks(c.db, {"T-1"})
            self.assertEqual(m.call_count, 1)
            for view, kw in ((throughput, {"flow": flow_obs()}), (tokens, {"spend": spend_obs()})):
                oc = obs_ctx(**kw)
                view.render(oc, view.data(oc))
            self.assertEqual(m.call_count, 1)                 # Spend and Flow never ask for it
        self.assertEqual(c.db.execute("PRAGMA query_only").fetchone()[0], 1)
        self.assertEqual(before, [c.db.execute(f"SELECT COUNT(*) FROM {t}").fetchone()[0] for t in ("events", "messages", "tasks")])


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

    def test_over_cap_row_gets_the_danger_tone_other_words_do_not(self):
        def row(word, state):
            return {"label": "Agents", "value": "9", "limit": "cap 8", "state": state, "word": word}
        html = str(resources._checks_section({"rows": [row("Over cap", "over"), row("At cap", "over"), row("OK", "ok"), row("Stale", "unknown")]}))
        self.assertEqual(html.count("ins-check--over"), 1)
        over = re.search(r'<li class="ins-check ins-check--over">.*?</li>', html, re.S).group(0)
        self.assertIn("Over cap", over)
        self.assertIn("<svg", over)         # glyph stays
        for word in ("At cap", "OK", "Stale"):
            self.assertRegex(html, rf'<li class="ins-check">(?:(?!</li>).)*{word}', word)

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
        self.assertGreaterEqual(len(re.findall(r'class="ins-check[ "]', html)), 6)   # R1: an over-cap row adds ins-check--over
        self.assertNotIn(" · ".join(g["checks"]), html)     # the dot string is replaced by rows

    def test_four_gauges_use_the_overview_labels(self):
        _, html = self.page(resources, usage=self.FRESH)
        for label in ("Agents", "Memory", "Load", "Claude usage", "Cursor usage"):
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


class TestResourcesOrgsCards(Case):
    GATE = {"orgs": [{"org": "claude", "live": 3, "workers": 1, "leads": 1, "hq": 1, "queued": 2, "runtimes": {"claude": 3}},
                     {"org": "cursor", "live": 4, "workers": 2, "leads": 1, "hq": 1, "queued": 0, "runtimes": {"cursor": 3, "claude": 1}}]}

    def test_all_seven_values_are_labelled_text_and_the_table_is_a_card_table(self):
        html = str(resources._orgs_section(self.GATE, None))
        self.assertIn('class="dt-wrap dt-wrap--cards"', html)
        rows = re.findall(r"<tbody>(.*?)</tbody>", html, re.S)[0].split("</tr>")[:-1]
        self.assertEqual(len(rows), 2)
        first = rows[0]
        self.assertEqual(re.findall(r'<span class="cl">([^<]*)</span>', first), ["Live", "Workers", "Leads", "HQ", "Queued", "Runtime"])
        self.assertEqual(re.sub(r"<[^>]+>", " ", re.sub(r'<span class="cl">[^<]*</span>', "", first)).split(),
                         ["Claude", "3", "1", "1", "1", "2", "claude", "3"])

    def test_desktop_headers_order_and_values_are_those_of_the_old_table(self):
        html = str(resources._orgs_section(self.GATE, None))
        head = re.search(r"<thead>.*?</thead>", html, re.S).group(0)
        self.assertEqual(re.findall(r"<th[^>]*>([^<]*)</th>", head), ["Org", "Live", "Workers", "Leads", "HQ", "Queued", "Runtime"])
        self.assertIn('<td class="num"><span class="cl">Live</span>4</td>', html)
        self.assertIn("claude 1 · cursor 3", html)

    def test_org_filter_keeps_one_row(self):
        html = str(resources._orgs_section(self.GATE, "cursor"))
        self.assertEqual(html.count("<tr><td"), 1)
        self.assertIn('<td class="c-title">Cursor</td>', html)        # G-069: rows carry the org name, no org chip class
        self.assertNotIn("Claude</td>", html)


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
        self.assertNotRegex(html, r"[↑↓] ")                   # nothing to compare: no arrow on any figure
        self.assertEqual(html.count('class="strip-delta'), 1)       # only CI-blocked can say "no change" (0 and 0)
        self.assertIn("no change vs prev 14 d", html)


class TestSourceAndAssets(unittest.TestCase):
    MARK = "/* == B6 insights == */"

    def test_no_interpolated_order_by(self):
        for f in ("quality", "throughput", "resources", "tokens"):
            src = (ROOT / "web" / "views" / f"{f}.py").read_text()
            self.assertIsNone(re.search(r"(?i)order by[^\"\n]*\{", src), f)

    def sections(self):
        css = (ROOT / "web" / "static" / "app.css").read_text()
        self.assertEqual(css.count(self.MARK), 1)
        return css, css.split(self.MARK)[1]

    def test_b6_css_section_is_single_and_uses_tokens_only(self):
        css, tail = self.sections()
        sec = tail.split("/* ==", 1)[0]               # G-069 appended its own section after B6: B6 ends at the next marker
        self.assertTrue(sec.strip())
        self.assertIsNone(re.search(r"#[0-9a-fA-F]{3,8}\b|rgba?\(|hsla?\(", sec))
        self.assertIsNone(re.search(r"(?<![\w-])(min-|max-)?height:\s*[0-9]+px", sec))
        self.assertEqual(tail.rstrip().count("/* =="), tail.rstrip().count("== */"))      # every later section marker is whole

    def test_g069_css_section_after_b6_also_uses_tokens_only(self):
        _, tail = self.sections()
        self.assertIn("/* == G-069", tail)
        sec = tail.split("/* == G-069", 1)[1]
        self.assertIsNone(re.search(r"#[0-9a-fA-F]{3,8}\b|rgba?\(|hsla?\(", sec))

    def test_every_class_the_pages_use_is_known(self):
        c = Case()
        c.setUp()
        try:
            used = set()
            for view, kw in ((quality, {}), (resources, {"usage": TestResourcesRender.FRESH})):
                _, html = c.page(view, **kw)
                for m in re.findall(r'class="([^"]+)"', html):
                    used |= set(m.split())
            for view, kw in ((throughput, {"flow": flow_obs()}), (tokens, {"spend": spend_obs()}), (tokens, {"error": "x"}),
                             (throughput, {"error": "x"})):
                for m in re.findall(r'class="([^"]+)"', obs_render(view, **kw)[1]):
                    used |= set(m.split())
            self.assertEqual(used - wh.CLASSES, set())
        finally:
            c.tmp.cleanup()

    def test_render_is_fast_on_the_seeded_db(self):
        c = Case()
        c.setUp()
        try:
            for view in (quality, resources):
                cx = c.ctx(usage=TestResourcesRender.FRESH)
                times = []
                for _ in range(5):
                    t0 = time.perf_counter()
                    view.render(cx, view.data(cx))
                    times.append(time.perf_counter() - t0)
                self.assertLess(sorted(times)[2], 1.0, view.__name__)       # the 200 ms target is measured in the PR
            for view, kw in ((throughput, {"flow": flow_obs()}), (tokens, {"spend": spend_obs()})):
                oc = obs_ctx(**kw)
                t0 = time.perf_counter()
                view.render(oc, view.data(oc))
                self.assertLess(time.perf_counter() - t0, 1.0, view.__name__)
        finally:
            c.tmp.cleanup()


class TestBreakdownColumns(unittest.TestCase):
    def test_every_spend_column_stays_on_a_phone_as_a_labelled_card_value(self):
        src = (ROOT / "web" / "views" / "tokens.py").read_text()
        self.assertNotIn("ins-wide", src)                      # no column is hidden on a phone any more
        html = obs_render(tokens, spend=spend_obs())[1]
        self.assertNotIn("ins-wide", html)
        self.assertIn('class="dt-wrap dt-wrap--cards"', html)
        row = re.search(r"<tbody><tr>(.*?)</tr>", html.split("Where it goes", 1)[1], re.S).group(1)
        self.assertEqual(re.findall(r'<span class="cl">([^<]*)</span>', row),
                         ["Sessions", "Requests", "$", "Share", "Spawn", "Polling", "Work", "$/PR"])
        css = (ROOT / "web" / "static" / "app.css").read_text()
        self.assertNotIn(".dt .ins-wide { display: none; }", css)


if __name__ == "__main__":
    unittest.main()
