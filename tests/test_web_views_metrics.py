"""Tests for web/views/throughput.py (the Flow page), quality.py and changelog.py, and the queries behind them."""
import json
import re
import sqlite3
import sys
import tempfile
import time
import unittest
from pathlib import Path
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from harness import FactoryEnv  # noqa: E402
from test_web_queries import CANARY, NOW, make_db, ts  # noqa: E402
from test_web_views_tokens import flow_obs, obs_ctx  # noqa: E402
from web import db as wdb  # noqa: E402
from web import html as wh  # noqa: E402
from web import queries  # noqa: E402
from web.views import ROUTES, changelog, quality, throughput  # noqa: E402

XSS = "<script>alert(1)</script>"
E_SQL = "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)"
T_SQL = ("INSERT INTO tasks(id,goal_id,team_id,title,spec,acceptance,role,status,attempts,created,updated) "
         "VALUES(?,?,?,?,?,?,?,?,?,?,?)")


class MetricsCase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / "factory.db"
        self.w = make_db(self.path)
        self.addCleanup(self.w.close)
        x = self.w.execute
        # four more merged tasks of G-1 with a known spawn -> merge of 10, 20, 30 and 40 minutes
        for n, mins in enumerate((10, 20, 30, 40), start=1):
            tid = f"T-7{n}"
            x(T_SQL, (tid, "G-1", "t-a", f"[SC1] timed {n}", "s", "a", "coder", "done", 1, ts(8, 0), ts(9, 50)))
            x("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,?,?)",
              (f"coder-{tid}", "coder", "G-1", "t-a", tid, "done", ts(9, 0)))
            x(E_SQL, (ts(9, mins), "judge", "task", tid, "merged", f"@{n}abcdef0"))

    def ctx(self, **query):
        con = wdb.connect(self.path)
        self.addCleanup(con.close)
        return SimpleNamespace(db=con, now=NOW, usage_fn=None, mem_fn=None, tokens=None, defaults={},
                               factory_home=None, query=query)

    def page(self, view, **query):
        c = self.ctx(**query)
        d = view.data(c)
        json.dumps(d)
        return d, str(view.render(c, d))


class TestMergeQueries(MetricsCase):
    """queries.throughput and queries.merges_per_day: the factory-db side the Home and Goals pages still use."""

    def tp(self, days=14, **kw):
        con = wdb.connect(self.path)
        self.addCleanup(con.close)
        return queries.throughput(con, days, now=NOW, **kw)

    def test_rejection_rate_absent_from_json_payload(self):
        d = self.page(quality)[0]
        self.assertNotIn("rejection_rate", json.dumps(d))

    def test_total_seconds_per_merged_task_and_unknown_not_guessed(self):
        d = self.tp()
        self.assertEqual(d["days"], 14)
        tot = {t["task_id"]: t["total_s"] for t in d["tasks"]}
        # the four timed tasks spawn at 09:00 and merge 10/20/30/40 minutes later; T-1 is 10:00 -> 11:20
        self.assertEqual(tot["T-71"], 600)
        self.assertEqual((tot["T-72"], tot["T-73"], tot["T-74"]), (1200, 1800, 2400))
        self.assertEqual(tot["T-1"], 4800)
        self.assertIsNone(tot["T-5"])           # no agent spawn: unknown, not guessed
        self.assertEqual(len(tot), 6)
        self.assertEqual(sorted(v for v in tot.values() if v is not None), [600, 1200, 1800, 2400, 4800])

    def test_goal_filter_keeps_only_that_goals_tasks_and_merges(self):
        d = self.tp(goals={"G-2"})
        self.assertEqual([t["task_id"] for t in d["tasks"]], ["T-5"])
        self.assertEqual({g["goal_id"] for g in d["tasks_per_goal"]}, {"G-2"})
        self.assertEqual(sum(r["merges"] for r in d["merges_per_day"]), 1)
        con = wdb.connect(self.path)
        self.addCleanup(con.close)
        self.assertEqual(sum(r["merges"] for r in queries.merges_per_day(con, 14, now=NOW, goals={"G-1"})), 5)
        self.assertEqual(sum(r["merges"] for r in queries.merges_per_day(con, 14, now=NOW, goals=set())), 0)

    def test_merges_per_day_is_dense_and_oldest_first(self):
        con = wdb.connect(self.path)
        self.addCleanup(con.close)
        rows = queries.merges_per_day(con, 7, now=NOW)
        self.assertEqual(len(rows), 7)
        self.assertEqual([r["day"] for r in rows], sorted(r["day"] for r in rows))
        self.assertEqual(rows[-1]["day"], NOW.astimezone().date().isoformat())   # today is the last day
        self.assertEqual(sum(r["merges"] for r in rows), 6)
        self.assertEqual(rows, self.tp(7)["merges_per_day"])        # one source of truth for both callers

    def test_stage_seconds_for_one_task_and_unknown_for_the_rest(self):
        st = {t["task_id"]: t for t in self.tp()["tasks"]}
        # T-1: coder spawn 10:00 -> first pr update 10:30; review 11:00 -> 11:15; judge 11:15 -> 11:20; rework 10:45 -> 11:00
        t1 = st["T-1"]
        self.assertEqual((t1["coding_s"], t1["review_s"], t1["judge_s"], t1["rework_s"]), (1800, 900, 300, 900))
        for tid in ("T-5", "T-71", "T-72", "T-73", "T-74"):     # no pr update or verdict events: unknown, never 0
            self.assertEqual((st[tid]["coding_s"], st[tid]["review_s"], st[tid]["judge_s"]), (None, None, None), tid)

    def test_merges_per_day_match_events(self):
        d = self.tp(30)
        self.assertEqual(d["days"], 30)
        self.assertEqual(len(d["merges_per_day"]), 30)
        want = dict(self.w.execute("SELECT substr(ts,1,10), count(*) FROM events WHERE entity='task' AND action='merged' "
                                   "GROUP BY 1").fetchall())
        got = {r["day"]: r["merges"] for r in d["merges_per_day"] if r["merges"]}
        self.assertEqual(got, want)
        self.assertEqual(sum(got.values()), 6)

    def test_done_tasks_per_goal(self):
        g = {x["goal_id"]: x for x in self.tp()["tasks_per_goal"]}
        self.assertEqual((g["G-1"]["done"], g["G-2"]["done"]), (5, 1))
        self.assertEqual(g["G-1"]["title"], "Goal one")
        self.assertEqual([t["task_id"] for t in self.tp()["tasks"] if t["goal_id"] == "G-1"],
                         ["T-1", "T-71", "T-72", "T-73", "T-74"])

    def test_a_task_merged_before_the_window_is_not_counted(self):
        self.w.execute("UPDATE events SET ts=? WHERE entity_id='T-71' AND action='merged'", (ts(9, 10, day=1),))
        d = self.tp(7)
        self.assertNotIn("T-71", [t["task_id"] for t in d["tasks"]])
        self.assertEqual(sum(r["merges"] for r in d["merges_per_day"]), 5)
        self.assertIn("T-71", [t["task_id"] for t in self.tp(30)["tasks"]])

    def test_range_param_falls_back_to_default(self):
        self.assertEqual(queries.range_param({"range": "24h"})[1], 24)
        for bad in ("abc", "0", "-3", "9999", "", "7.5", "14; DROP", None):
            self.assertEqual(queries.range_param({"range": bad})[0], "7d", bad)
        self.assertEqual(queries.range_param(None)[0], "7d")

    def test_empty_db(self):
        self.w.executescript("DELETE FROM events; DELETE FROM tasks; DELETE FROM agents; DELETE FROM verdicts;")
        d = self.tp()
        self.assertEqual((d["tasks"], d["tasks_per_goal"]), ([], []))
        self.assertEqual(len(d["merges_per_day"]), 14)
        self.assertEqual(sum(r["merges"] for r in d["merges_per_day"]), 0)


def flow_page(**over):
    """(ctx, html) of the Flow page over a flow_obs fixture."""
    c = obs_ctx(flow=flow_obs(**over))
    d = throughput.data(c)
    json.dumps(d)
    return c, str(throughput.render(c, d))


class TestFlowPage(unittest.TestCase):
    def test_strip_has_five_figures_and_definitions(self):
        _, html = flow_page()
        strip = html.split('<dl class="strip">')[1].split("</dl>")[0]
        self.assertEqual(re.findall(r'<dt class="strip-label">([^<]*)</dt><dd class="strip-value">([^<]*)</dd>', strip),
                         [("Merged", "6"), ("Median open → merge", "4.5 h"), ("p90 open → merge", "12 h"),
                          ("2+ review rounds", "38%"), ("Open &gt; 6 h", "1")])
        self.assertIn('<dd class="strip-delta muted">7 days</dd>', strip)
        self.assertIn('<dd class="strip-delta fg-warning">! 3 of 8</dd>', strip)
        self.assertIn('<dd class="strip-delta fg-warning">! see Stuck now</dd>', strip)
        self.assertIn("How these are counted", html)

    def test_asked_hm_obs_for_the_default_window(self):
        c, _ = flow_page()
        self.assertEqual(c.calls, [("flow", ["--hours", "168"])])

    def test_stage_map_lanes_and_stage_bar_aria(self):
        _, html = flow_page()
        main = html.split('<p class="stage-lane">Main line</p>')[1].split('<p class="stage-lane">Rework loop</p>')[0]
        self.assertEqual(re.findall(r'<span class="stage-name">([^<]*)</span>', main), ["Coder", "CI wait", "Review + QA", "Judge", "Merge"])
        self.assertEqual(main.count("lane-main"), 5)
        rework = html.split("Rework loop</p>")[1].split('<p class="stage-lane">Side queues</p>')[0]
        self.assertIn('<span class="stage-name">Rework</span>', rework)
        self.assertIn("lane-rework", rework)
        side = html.split("Side queues</p>")[1]
        self.assertIn('<span class="stage-name">Hold</span>', side)
        self.assertIn("lane-side", side)
        self.assertRegex(html, r'<span class="stage-name">Review \+ QA</span><svg [^>]*role="img"[^>]*class="stage-bar" '
                               r'aria-label="40% of stage hours">')
        self.assertIn('<rect class="stage-fill" x="0" y="0" width="40.0" height="8"></rect>', html)

    def test_stage_hours_and_share_are_printed_per_stage(self):
        _, html = flow_page()
        rows = re.findall(r'<span class="stage-name">([^<]*)</span><svg [^>]*>.*?</svg><span class="stage-h">([^<]*)</span>'
                          r'<span class="stage-share">([^<]*)</span>', html)
        self.assertEqual(rows, [("CI wait", "10 h", "20%"), ("Review + QA", "20 h", "40%"), ("Judge", "5.0 h", "10%"),
                                ("Rework", "10 h", "20%"), ("Hold", "5.0 h", "10%")])
        self.assertIn("50 h of stage time in the window.", html)
        # Coder and Merge are the line's ends: a dash, no bar, no share
        self.assertIn('<span class="stage-name">Coder</span><span class="stage-h">–</span></li>', html)

    def test_stuck_now_table_links_the_pr_and_the_task(self):
        _, html = flow_page()
        row = html.split("<h2>Stuck now")[1].split("<tbody>")[1].split("</tbody>")[0]
        self.assertIn('<a href="https://github.com/acme/factory/pull/812" rel="noopener" class="mono">acme/factory#812</a>', row)
        self.assertIn('<a href="/task/T-7">T-7</a>', row)
        self.assertIn("<td>Review + QA</td>", row)      # the stage's label, not its key
        self.assertIn('<span class="cl">Age</span>9.5 h', row)

    def test_nothing_stuck_says_so(self):
        _, html = flow_page(stuck=[], open_over_6h=0)
        self.assertIn("No open PR is older than 6 hours", html)
        self.assertNotIn("<tbody>", html)
        self.assertNotIn("see Stuck now", html)

    def test_banners_come_from_obs_and_start_with_a_bang(self):
        _, html = flow_page(banners=["! 38% of PRs needed 2+ review rounds (3 of 8): too many.", "no bang in the input"])
        texts = re.findall(r'<p class="banner" role="note">([^<]*)</p>', html)
        self.assertEqual(texts, ["! 38% of PRs needed 2+ review rounds (3 of 8): too many.", "! no bang in the input"])
        self.assertNotIn('class="banners"', flow_page(banners=[])[1])

    def test_error_state_is_one_banner_and_no_figures(self):
        c = obs_ctx(flow=None, error="hm-obs exited 2")
        html = str(throughput.render(c, throughput.data(c)))
        self.assertIn('<p class="banner" role="note">! Flow data unavailable: hm-obs exited 2</p>', html)
        self.assertNotIn('class="strip"', html)
        self.assertNotIn("Where PRs wait", html)
        self.assertIn("How this is counted", html)

    def test_no_obs_in_the_server_is_an_error_state_not_a_crash(self):
        c = obs_ctx(flow=flow_obs())
        del c.obs
        html = str(throughput.render(c, throughput.data(c)))
        self.assertIn("! Flow data unavailable: hm-obs is not available in this server", html)

    def test_empty_stage_hours_state(self):
        _, html = flow_page(stages=[], stage_hours=0, stuck=[], merged=0, prs=0, prs_2plus_rounds=0, prs_2plus_rounds_pct=None,
                            open_to_merge_median_h=None, open_to_merge_p90_h=None, open_over_6h=0)
        self.assertIn("No stage hours in this range", html)
        self.assertNotIn('class="stage lane-main"><span class="stage-name">CI wait', html)
        self.assertIn('<span class="stage-name">Coder</span>', html)     # the line's ends stay drawn

    def test_unknown_values_show_a_dash_not_zero(self):
        _, html = flow_page(open_to_merge_median_h=None, open_to_merge_p90_h=None, prs=0, prs_2plus_rounds=0,
                            prs_2plus_rounds_pct=None)
        strip = html.split('<dl class="strip">')[1].split("</dl>")[0]
        self.assertIn('<dt class="strip-label">Median open → merge</dt><dd class="strip-value">–</dd>', strip)
        self.assertIn('<dt class="strip-label">p90 open → merge</dt><dd class="strip-value">–</dd>', strip)
        self.assertIn('<dt class="strip-label">2+ review rounds</dt><dd class="strip-value">–</dd>', strip)
        self.assertNotIn("0.0 h", strip)
        self.assertNotIn("0%", strip)

    def test_low_two_plus_rounds_is_not_a_warning(self):
        _, html = flow_page(prs=10, prs_2plus_rounds=1, prs_2plus_rounds_pct=10.0)
        self.assertIn('<dd class="strip-value">10%</dd><dd class="strip-delta muted">1 of 10</dd>', html)
        self.assertNotIn("! 1 of 10", html)

    def test_range_query_reaches_hm_obs(self):
        c = obs_ctx(flow=flow_obs(), range="24h", project="proj-a")
        throughput.data(c)
        self.assertEqual(c.calls, [("flow", ["--hours", "24", "--project", "proj-a"])])
        c = obs_ctx(flow=flow_obs(), range="bogus", project="x; rm -rf")
        throughput.data(c)
        self.assertEqual(c.calls, [("flow", ["--hours", "168"])])


def figures(html):
    """{label: value} of every strip item on the page (a label shared by two strips has one value)."""
    return dict(re.findall(r'<dt class="strip-label">([^<]*)</dt><dd class="strip-value">([^<]*)</dd>', html))


def definitions(html):
    """{label: text} of every 'How these are counted' definition on the page."""
    return dict(re.findall(r"<dt>([^<]*)</dt><dd>([^<]*)</dd>", html))


class TestQuality(MetricsCase):
    def test_headline_strip_has_four_figures_each_with_a_definition(self):
        _, html = self.page(quality)
        head = html.split('<dl class="strip">')[1].split("</dl>")[0]
        self.assertEqual(re.findall(r'<dt class="strip-label">([^<]*)</dt><dd class="strip-value">([^<]*)</dd>', head),
                         [("Rejections per merged PR", "0.50"), ("First-try merge rate", "83%"), ("CI-blocked merges", "2"),
                          ("Median attempts", "1")])
        self.assertEqual(head.count('class="strip-item"'), 4)
        self.assertNotIn("tile", html)
        defs = html.split('data-key="q-head-defs"')[1].split("</details>")[0]
        self.assertEqual(re.findall(r"<dt>([^<]*)</dt>", defs),
                         ["Rejections per merged PR", "First-try merge rate", "CI-blocked merges", "Median attempts"])
        self.assertNotIn("n/a, see G-013", html)
        self.assertIn("Not measured yet", html)

    def test_overall_kpis_and_formula(self):
        d, html = self.page(quality)
        o = d["overall"]
        self.assertEqual(o["merged"], 6)
        self.assertEqual(o["rejections"], 3)    # 2 verdict rows + legacy judge-reject event on T-2
        self.assertNotIn("Rejection rate", html)
        self.assertIn("Retries per merged task", html)
        self.assertIn("CI pass rate", html)
        self.assertIn("merged / (merged + tasks the judge blocked", html)   # formula in small text
        self.assertIn("t-a 5/5", html)          # latest score, not the average

    def test_merged_split_and_rejections_per_merged(self):
        d, html = self.page(quality)
        o = d["overall"]
        # merged: T-1 (one fail verdict), T-5, T-71..T-74; T-2 and T-3 are rejected but not merged
        self.assertEqual((o["merged"], o["merged_first_try"], o["merged_after_rejection"]), (6, 5, 1))
        self.assertEqual(o["merged_first_try"] + o["merged_after_rejection"], o["merged"])
        self.assertEqual(o["rejections_per_merged"], 0.5)     # 3 rejection events / 6 merged
        f = figures(html)
        for label, value in (("Merged PRs", "6"), ("Merged first try", "5"), ("Merged after &gt;= 1 rejection", "1"),
                             ("Rejections per merged PR", "0.50")):
            self.assertEqual(f[label], value, label)
        self.assertEqual(definitions(html)["Merged first try"], "Merged tasks that were never rejected.")

    def test_second_rejection_moves_the_numbers(self):
        x = self.w.execute
        x("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
          "VALUES('T-71','u','dddddddd11','qa','fail','x','qa',?)", (ts(9, 5),))
        x(E_SQL, (ts(9, 6), "judge", "task", "T-71", "judge-reject", "again"))
        o = self.page(quality)[0]["overall"]
        self.assertEqual((o["merged_first_try"], o["merged_after_rejection"]), (4, 2))
        self.assertEqual(o["rejections_per_merged"], 0.83)    # 5 / 6

    def test_no_merges_gives_dash_and_cause_is_na(self):
        self.w.executescript("DELETE FROM events WHERE action='merged';")
        d, html = self.page(quality)
        self.assertIsNone(d["overall"]["rejections_per_merged"])
        self.assertEqual(d["overall"]["merged_first_try"], 0)
        self.assertNotIn("n/a, see G-013", html)
        self.assertIn("Rejections by cause", html)
        self.assertEqual(d["windows"]["current"]["rejections_per_merged"], None)      # nothing merged: no delta, no divide by 0
        self.assertIn('<dt class="strip-label">Rejections per merged PR</dt><dd class="strip-value">-</dd>', html)
        self.assertEqual((figures(html)["Rejections per merged PR"], figures(html)["First-try merge rate"]), ("-", "-"))
        self.assertNotIn("0.00", html)

    def test_cause_is_not_measured_yet_with_definition(self):
        _, html = self.page(quality)
        self.assertEqual(figures(html)["Rejections by cause"], "Not measured yet")
        self.assertEqual(definitions(html)["Rejections by cause"], "No cause is stored yet (counts incidents, not events).")

    def test_rejections_by_cause_shows_recorded_counts(self):
        x = self.w.execute
        x("INSERT INTO rejection_incidents(cause,kind,task_id,created) VALUES('bug','qa','T-71',?)", (ts(9, 9),))
        x("INSERT INTO rejection_incidents(cause,kind,task_id,created) VALUES('bug','reviewer','T-72',?)", (ts(9, 10),))
        x("INSERT INTO rejection_incidents(cause,kind,task_id,created) VALUES('gate_refusal','gate','T-73',?)", (ts(9, 11),))
        _, html = self.page(quality)
        self.assertEqual(figures(html)["Rejections by cause"], "3")
        self.assertNotIn("Not measured yet", html)
        self.assertIn("bug 2, gate refusal 1.", definitions(html)["Rejections by cause"])

    def test_rejections_per_week_chart_with_table_fallback(self):
        _, html = self.page(quality)
        box = html.split("<h2>Per week")[1].split("</section>")[0]
        self.assertRegex(box, r'<svg [^>]*role="img"[^>]*aria-label="Rejections per week: 3 over 12 weeks, peak 3"[^>]*class="axis">')
        self.assertEqual(re.findall(r'<text class="axis-x"[^>]*>([^<]*)</text>', box), ["08-03", "08-17", "08-31", "09-14", "09-28", "10-12"])
        self.assertIn("<title>10-19 Rejections: 3</title>", box)
        self.assertIn('<table class="sr-only"><caption>Rejections per week</caption>', box)
        rows = re.findall(r"<tr><td>([^<]*)</td><td>(\d+)</td></tr>", box)
        self.assertEqual(len(rows), 12)
        self.assertEqual(rows[-1], ("10-19", "3"))
        self.assertEqual(sum(int(n) for _, n in rows), 3)

    def test_team_table_is_sortable_and_score_is_in_more_metrics(self):
        d, html = self.page(quality)
        t = {x["team_id"]: x for x in d["teams"]}
        self.assertEqual((t["t-a"]["score"], t["t-a"]["eval_date"]), (5, "2026-10-24"))
        self.assertIsNone(t["t-b"]["score"])
        head = html.split("Per team")[1].split("</tr>")[0]
        self.assertEqual(re.findall(r">([^<]+)</a></th>", head), ["Team", "Merged", "Rejections", "Rejections per merged PR"])
        self.assertIn("t-a 5/5", html)
        self.assertNotIn("g-001-a 5/5, ", html)       # the old comma-joined tile is gone from the headline

    def test_team_sort_whitelist(self):
        d, html = self.page(quality, sort="merged", dir="asc")
        self.assertRegex(html, r'aria-sort="ascending"[^>]*><a href="/quality\?sort=merged&amp;dir=desc"')
        for bad in ("evil", "merged; DROP TABLE tasks", "", "team'--"):
            _, html = self.page(quality, sort=bad)
            self.assertRegex(html, r'aria-sort="ascending"[^>]*><a href="/quality\?sort=team&amp;dir=desc"', bad)
        _, html = self.page(quality, sort="merged", dir="evil")
        self.assertRegex(html, r'aria-sort="descending"[^>]*><a href="/quality\?sort=merged&amp;dir=asc"')

    def test_matches_scorecard_rows(self):
        # legacy judge-reject events without a verdict row are the one documented difference: scorecard ignores them
        d = self.page(quality)[0]
        env = FactoryEnv()
        self.addCleanup(env.cleanup)
        con = sqlite3.connect(self.path)
        con.row_factory = sqlite3.Row
        self.addCleanup(con.close)
        with env.load_factory() as mod:
            rows = {r["team"]: r for r in mod.scorecard_rows(con)}
        t = {x["team_id"]: x for x in d["teams"]}
        for team, r in rows.items():
            self.assertEqual((t[team]["tasks"], t[team]["retries"]), (r["tasks"], r["retries"]), team)
        self.assertEqual(t["t-a"]["rejections"], rows["t-a"]["rejections"] + 1)   # T-2's old-style judge-reject event
        self.w.execute("DELETE FROM events WHERE action='judge-reject' AND entity_id='T-2'")
        d = self.page(quality)[0]
        t = {x["team_id"]: x for x in d["teams"]}
        for team, r in rows.items():
            self.assertEqual(t[team]["rejections"], r["rejections"], team)
        # scorecard counts 'merge' verdict rows; only T-1 has one, the page counts merged events (T-1 and T-71..T-74)
        self.assertEqual(rows["t-a"]["merged"], 1)
        self.assertEqual(t["t-a"]["merged"], 5)

    def test_null_team_task_attributed_like_scorecard(self):
        x = self.w.execute
        x(T_SQL, ("T-90", "G-1", None, "no team", "s", "a", "coder", "done", 3, ts(8, 0), ts(9, 0)))
        x(E_SQL, (ts(9, 5), "judge", "task", "T-90", "merged", "@9abcdef0"))
        x("INSERT INTO verdicts(task_id,role,verdict,created) VALUES('T-90','qa','fail',?)", (ts(8, 30),))
        d, html = self.page(quality)
        env = FactoryEnv()
        self.addCleanup(env.cleanup)
        con = sqlite3.connect(self.path)
        con.row_factory = sqlite3.Row
        self.addCleanup(con.close)
        with env.load_factory() as mod:
            rows = {r["team"]: r for r in mod.scorecard_rows(con)}
        goal1 = [r for r in rows.values() if r["goal"] == "G-1"]
        self.assertTrue(goal1)
        t = {y["team_id"]: y for y in d["teams"]}
        for r in rows.values():
            self.assertEqual((t[r["team"]]["tasks"], t[r["team"]]["retries"]), (r["tasks"], r["retries"]), r["team"])
        self.assertIn("Tasks without a team are counted in every team of their goal.", html)

    def test_null_team_task_changes_team_ratios(self):
        x = self.w.execute
        gate0 = {t["team_id"]: t for t in self.page(quality)[0]["teams"]}["t-a"]["gate_refusals"]
        x(T_SQL, ("T-90", "G-1", None, "no team", "s", "a", "coder", "done", 1, ts(8, 0), ts(9, 0)))
        x(E_SQL, (ts(9, 5), "judge", "task", "T-90", "merged", "@9abcdef0"))
        x("INSERT INTO verdicts(task_id,role,verdict,created) VALUES('T-90','qa','fail',?)", (ts(8, 30),))
        x("INSERT INTO verdicts(task_id,role,verdict,created) VALUES('T-90','qa','fail',?)", (ts(8, 40),))
        x(E_SQL, (ts(9, 1), "judge", "task", "T-90", "judge-blocked", "stale"))
        # the NULL-team task is attributed to every team of G-1, so the per-team numbers move with it
        d = self.page(quality)[0]
        a = {t["team_id"]: t for t in d["teams"]}["t-a"]
        self.assertEqual(a["merged"], 6)       # T-1, T-71..T-74 and T-90
        self.assertEqual(a["rejections_per_merged"], round(a["rejections"] / a["merged"], 2))
        self.assertEqual(a["rejections"], 5)   # 3 from the fixture + 2 qa on T-90
        self.assertEqual(a["rejections_per_merged"], 0.83)
        self.assertEqual(a["gate_refusals"], gate0 + 1)
        self.assertEqual(a["merged_first_try"] + a["merged_after_rejection"], a["merged"])

    def test_rejections_and_gate_refusals_by_source(self):
        x = self.w.execute
        base = self.page(quality)[0]["overall"]
        x("INSERT INTO verdicts(task_id,role,verdict,created) VALUES('T-71','reviewer','fail',?)", (ts(9, 1),))
        x("INSERT INTO verdicts(task_id,role,verdict,created) VALUES('T-71','qa','fail',?)", (ts(9, 2),))
        x("INSERT INTO verdicts(task_id,role,verdict,created) VALUES('T-72','judge','reject',?)", (ts(9, 3),))
        x(E_SQL, (ts(9, 4), "judge", "task", "T-72", "judge-reject", "same as verdict row"))   # no extra count
        x(E_SQL, (ts(9, 5), "judge", "task", "T-73", "judge-reject", "no verdict row"))        # counted
        x(E_SQL, (ts(9, 6), "judge", "task", "T-73", "judge-blocked", "checks not green"))
        x(E_SQL, (ts(9, 7), "judge", "task", "T-73", "judge-blocked", "stale verdict"))
        x(E_SQL, (ts(9, 8), "judge", "task", "T-74", "merge-failed", "conflict"))
        d, html = self.page(quality)
        o = d["overall"]
        self.assertEqual(o["rej_reviewer"] - base["rej_reviewer"], 1)
        self.assertEqual(o["rej_qa"] - base["rej_qa"], 1)
        self.assertEqual(o["rej_judge"] - base["rej_judge"], 2)
        self.assertEqual(o["rej_reviewer"] + o["rej_qa"] + o["rej_judge"], o["rejections"])
        self.assertEqual((o["gate_judge_blocked"], o["gate_merge_failed"], o["gate_refusals"]),
                         (base["gate_judge_blocked"] + 2, base["gate_merge_failed"] + 1, base["gate_refusals"] + 3))
        self.assertEqual(o["rejections"] - base["rejections"], 4)    # gate refusals are not rejections
        for label in ("Reviewer", "QA", "Judge", "Gate: judge-blocked", "Gate: merge-failed", "Gate refusals"):
            self.assertIn(label, html)
        f, defs = figures(html), definitions(html)
        self.assertEqual((f["Gate refusals"], f["Rejections"]), (str(o["gate_refusals"]), str(o["rejections"])))
        for label in ("Gate refusals", "Rejections"):
            self.assertTrue(defs[label], label)
        # the source rows are meters with their counts
        total = o["rej_reviewer"] + o["rej_qa"] + o["rej_judge"]
        self.assertRegex(html, rf'<span class="ins-name">Judge</span><div class="meter"><svg [^>]*aria-label="{o["rej_judge"]} of {total} rejections done"')
        self.assertIn("counts incidents, not events", html)

    def test_rejections_per_week(self):
        d, html = self.page(quality)
        self.assertEqual(d["weeks"], [{"week": "2026-10-19", "rejections": 3}])   # 2026-10-24 is a Saturday
        self.assertIn("<td>10-19</td><td>3</td>", html)
        self.w.execute("INSERT INTO verdicts(task_id,role,verdict,created) VALUES('T-2','qa','fail',?)", (ts(9, 0, day=14),))
        d, html = self.page(quality)
        self.assertEqual(d["weeks"], [{"week": "2026-10-12", "rejections": 1}, {"week": "2026-10-19", "rejections": 3}])
        self.assertIn("<td>10-12</td><td>1</td>", html)
        self.assertRegex(html, r'aria-label="Rejections per week: 4 over 12 weeks, peak 3"')

    def test_empty_db(self):
        self.w.executescript("DELETE FROM events; DELETE FROM tasks; DELETE FROM verdicts; DELETE FROM teams; "
                             "DELETE FROM evaluations;")
        d, html = self.page(quality)
        self.assertIn("No teams yet.", html)
        self.assertIn("No rejections recorded.", html)


class TestChangelog(MetricsCase):
    def test_lists_every_merged_task_grouped_by_goal(self):
        d, html = self.page(changelog)
        self.assertEqual(d["total"], self.w.execute("SELECT count(DISTINCT entity_id) FROM events WHERE action='merged'").fetchone()[0])
        self.assertEqual([g["goal_id"] for g in d["groups"]], ["G-2", "G-1"])   # newest activity first (T-5 at 11:30)
        g1 = d["groups"][1]
        self.assertEqual([i["task_id"] for i in g1["items"]], ["T-1", "T-74", "T-73", "T-72", "T-71"])
        self.assertNotIn("[SC", html)
        self.assertIn("one", html)
        self.assertRegex(html, r'href="https://github\.com/o/r/pull/5"')
        self.assertIn("deadbeef"[:8], html)
        self.assertRegex(html, r"<h2>(Today|Yesterday|[A-Z][a-z]{2} \d\d) ")   # default view groups by day (B3)
        by_goal = self.page(changelog, group="goal")[1]
        self.assertIn("Goal one", by_goal)

    def test_goal_filter_and_invalid_values(self):
        d = self.page(changelog, goal="G-2")[0]
        self.assertEqual([g["goal_id"] for g in d["groups"]], ["G-2"])
        self.assertEqual(d["goal"], "G-2")
        for bad in ("G-999", "x", "", "G-1' OR 1=1", "../etc", "G-3"):   # G-3 exists but has nothing merged
            dd = self.page(changelog, goal=bad)[0]
            self.assertIsNone(dd["goal"], bad)
            self.assertEqual(len(dd["groups"]), 2, bad)

    def test_non_github_pr_url_is_plain_text(self):
        self.w.execute("UPDATE tasks SET pr_url='javascript:alert(1)' WHERE id='T-5'")
        html = self.page(changelog)[1]
        self.assertNotRegex(html, r'href="javascript')

    def test_empty_db(self):
        self.w.executescript("DELETE FROM events;")
        self.assertIn("Nothing has been merged yet.", self.page(changelog)[1])


class TestQualityPerTeamPhone(MetricsCase):
    def test_merged_rejections_and_rate_carry_real_text_labels_and_the_card_class(self):
        _, html = self.page(quality)
        box = html.split("<h2>Per team")[1].split("</section>")[0]
        self.assertIn('class="dt-wrap dt-wrap--cards"', box)
        row = re.search(r"<tbody><tr>(.*?)</tr>", box, re.S).group(1)
        self.assertEqual(re.findall(r'<span class="cl">([^<]*)</span>', row), ["Merged", "Rejections", "Per merged PR"])
        self.assertNotIn('class="cl"', row.split("</td>")[0])             # the Team cell has no label
        head = re.search(r"<thead>.*?</thead>", box, re.S).group(0)    # desktop: same four headers and sort links
        self.assertEqual(re.findall(r"<a href=\"([^\"]*)\" class=\"sortlink\">([^<]*)</a>", head),
                         [("/quality?sort=team&amp;dir=desc", "Team"), ("/quality?sort=merged&amp;dir=desc", "Merged"),
                          ("/quality?sort=rejections&amp;dir=desc", "Rejections"), ("/quality?sort=rate&amp;dir=desc", "Rejections per merged PR")])

    def test_the_footnote_sits_between_the_bar_lists_not_inside_one(self):
        _, html = self.page(quality)
        box = html.split("<h2>Rejections by source")[1].split("</section>")[0]
        self.assertEqual(box.count('<div class="ins-list">'), 2)
        note = box.index("CI counts merges")
        self.assertEqual(box[:note].count("<div class=\"ins-list\">"), 1)
        self.assertRegex(box[:note], r'</div></div></div><p class="muted">$')


class TestChangelogFeedClass(MetricsCase):
    def test_both_groupings_use_the_log_modifier(self):
        for q in ({}, {"group": "goal"}):
            html = self.page(changelog, **q)[1]
            self.assertIn('<ul class="feed feed--log">', html, q)
            self.assertNotIn('<ul class="feed">', html, q)


class TestAllPages(MetricsCase):
    VIEWS = (("/throughput", throughput), ("/quality", quality), ("/changelog", changelog))

    def test_routes_are_these_modules(self):
        for path, view in self.VIEWS:
            self.assertIs(ROUTES[path], view)

    def test_hostile_titles_escaped_and_canary_absent(self):
        self.w.execute("UPDATE goals SET title=? WHERE id IN ('G-1','G-2')", (XSS,))
        self.w.execute("UPDATE tasks SET title=? WHERE id IN ('T-1','T-5','T-71')", (XSS,))
        self.w.execute("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('a','boss','report','s',?,?)",
                       (f"body {CANARY}", ts(9, 0)))
        for path, view in self.VIEWS:
            html = self.page(view)[1]
            self.assertNotIn("<script>alert", html, path)
            self.assertNotIn(CANARY, html, path)
        self.assertIn("&lt;script&gt;", self.page(changelog)[1])

    def test_hostile_flow_labels_repo_task_and_error_are_escaped(self):
        stages = flow_obs()["stages"]
        stages[0] = {**stages[0], "label": XSS}
        stuck = [{"repo": f"{XSS}/x", "number": 3, "task": XSS, "stage": "ci_wait", "age_h": 7.0},
                 {"repo": "acme/factory", "number": 4, "task": "T-9", "stage": XSS, "age_h": 8.0}]
        _, html = flow_page(stages=stages, stuck=stuck, banners=[XSS], warnings=[XSS])
        self.assertNotIn("<script>", html)
        self.assertGreaterEqual(html.count("&lt;script&gt;alert(1)&lt;/script&gt;"), 6)   # label, 2 stuck stage cells, repo, task, banner, warning
        c = obs_ctx(flow=None, error=XSS)
        err = str(throughput.render(c, throughput.data(c)))
        self.assertNotIn("<script>", err)
        self.assertIn("! Flow data unavailable: &lt;script&gt;", err)

    def test_only_known_css_classes_and_fast(self):
        for path, view in self.VIEWS:
            t0 = time.perf_counter()
            html = self.page(view)[1]
            self.assertLess(time.perf_counter() - t0, 1.0, path)
            used = {c for m in re.findall(r'class="([^"]*)"', html) for c in m.split()}
            self.assertEqual(used - wh.CLASSES, set(), path)
            self.assertNotIn("style=", html)
            self.assertNotIn("<script", html)

    def test_flow_page_from_obs_uses_only_known_css_classes_and_no_inline_style(self):
        _, html = flow_page()
        used = {c for m in re.findall(r'class="([^"]*)"', html) for c in m.split()}
        self.assertIn("stage-bar", used)
        self.assertEqual(used - wh.CLASSES, set())
        self.assertNotIn("style=", html)
        self.assertNotIn("<script", html)


if __name__ == "__main__":
    unittest.main()
