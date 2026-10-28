"""Tests for web/views/throughput.py, quality.py and changelog.py."""
import json
import re
import sqlite3
import sys
import tempfile
import time
import unittest
from html.parser import HTMLParser
from pathlib import Path
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from harness import FactoryEnv  # noqa: E402
from test_web_queries import CANARY, NOW, make_db, ts  # noqa: E402
from web import db as wdb  # noqa: E402
from web import html as wh  # noqa: E402
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


class TestThroughput(MetricsCase):
    def test_rejection_rate_absent_from_json_payload(self):
        d = self.page(quality)[0]
        self.assertNotIn("rejection_rate", json.dumps(d))

    def test_kpis_median_p90_and_in_progress(self):
        d, html = self.page(throughput)
        self.assertEqual(d["days"], 14)
        self.assertEqual(d["merges"], 6)        # T-1, T-5 and the four timed tasks
        self.assertEqual(d["timed"], 5)         # T-5 has no agent spawn: unknown, not guessed
        # totals 600, 1200, 1800, 2400 and T-1 (10:00 -> 11:20 = 4800): median 1800, nearest-rank p90 4800
        self.assertEqual((d["median_s"], d["p90_s"]), (1800, 4800))
        self.assertEqual(d["open_tasks"], 2)    # one in_progress + one in_review in the fixture
        for needle in ("30m 00s", "1h 20m", "Median spawn to merge", "Merges (14 days)"):
            self.assertIn(needle, html)

    def test_open_tasks_counts_in_review_and_live_workers(self):
        x = self.w.execute
        base = self.page(throughput)[0]
        x(T_SQL, ("T-90", "G-1", "t-a", "review one", "s", "a", "coder", "in_review", 1, ts(8, 0), ts(9, 0)))
        x("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,?,?)",
          ("coder-t-90", "coder", "G-1", "t-a", "T-90", "live", ts(9, 0)))
        x("INSERT INTO agents(name,role,goal_id,team_id,status,started) VALUES(?,?,?,?,?,?)",
          ("lead-x", "lead", "G-1", "t-a", "live", ts(9, 0)))   # live but no task: not a worker
        d, html = self.page(throughput)
        want = self.w.execute("SELECT COUNT(*) FROM tasks WHERE status IN ('in_progress','in_review')").fetchone()[0]
        self.assertEqual(d["open_tasks"], want)
        self.assertEqual(d["open_tasks"], base["open_tasks"] + 1)    # T-90 is in_review
        self.assertEqual(d["open_tasks"], 3)    # fixture's in_progress + in_review + T-90
        self.assertEqual(d["live_workers"], base["live_workers"] + 1)   # the lead has no task: not counted
        self.assertIn("Open tasks now", html)
        self.assertIn("Live workers now", html)
        self.assertIn("in progress or in review", html)
        self.assertNotIn("In progress now", html)

    def test_scaled_bars_show_count_not_percent(self):
        _, html = self.page(throughput)
        merges = re.findall(r'<div class="bar bar--\d+" aria-label="(\d+) of max (\d+)"><span class="mono">(\d+)</span>', html)
        self.assertTrue(merges)
        for n, mx, shown in merges:
            self.assertEqual(n, shown)
            self.assertLessEqual(int(n), int(mx))
        self.assertEqual(max(int(m[1]) for m in merges), max(int(m[0]) for m in merges))
        # the share bars say what they are a share of
        self.assertRegex(html, r'<span class="mono">\d+% of total</span>')
        for label in re.findall(r'aria-label="[^"]*of max[^"]*"><span class="mono">([^<]*)</span>', html):
            self.assertFalse(label.endswith("%"), label)

    def test_stage_sums_and_unknown(self):
        d, html = self.page(throughput)
        st = {s["key"]: s for s in d["stages"]}
        # T-1: coder spawn 10:00 -> first pr update 10:30; review 11:00 -> 11:15; judge 11:15 -> 11:20; rework 10:45 -> 11:00
        self.assertEqual(st["coding_s"]["sum_s"], 1800)
        self.assertEqual(st["review_s"]["sum_s"], 900)
        self.assertEqual(st["judge_s"]["sum_s"], 300)
        self.assertEqual(st["rework_s"]["sum_s"], 900)
        # only T-1 has a coding / review / judge stage; the other five merged tasks are unknown
        self.assertEqual((st["coding_s"]["known"], st["coding_s"]["unknown"]), (1, 5))
        self.assertEqual(st["coding_s"]["median_s"], 1800)
        self.assertIn("5 unknown", html)
        self.assertIn("bar bar--", html)

    def test_merges_per_day_match_events(self):
        d = self.page(throughput, days="30")[0]
        self.assertEqual(d["days"], 30)
        self.assertEqual(len(d["merges_per_day"]), 30)
        want = dict(self.w.execute("SELECT substr(ts,1,10), count(*) FROM events WHERE entity='task' AND action='merged' "
                                   "GROUP BY 1").fetchall())
        got = {r["day"]: r["merges"] for r in d["merges_per_day"] if r["merges"]}
        self.assertEqual(got, want)
        self.assertEqual(sum(got.values()), 6)

    def test_per_goal_and_slowest(self):
        d, html = self.page(throughput)
        g = {x["goal_id"]: x for x in d["goals"]}
        self.assertEqual((g["G-1"]["done"], g["G-1"]["median_s"]), (5, 1800))
        self.assertEqual((g["G-2"]["done"], g["G-2"]["median_s"]), (1, None))
        self.assertEqual([s["task_id"] for s in d["slowest"]], ["T-1", "T-74", "T-73", "T-72", "T-71"])
        self.assertRegex(html, r'href="/task/T-1"')
        self.assertNotIn("[SC", html)

    def test_invalid_days_fall_back(self):
        for bad in ("abc", "0", "-3", "9999", "", "7.5", "14; DROP"):
            self.assertEqual(self.page(throughput, days=bad)[0]["days"], 14, bad)
        self.assertEqual(self.page(throughput, days="7")[0]["days"], 7)

    def test_empty_db(self):
        self.w.executescript("DELETE FROM events; DELETE FROM tasks; DELETE FROM agents; DELETE FROM verdicts;")
        d, html = self.page(throughput)
        self.assertEqual((d["merges"], d["median_s"], d["p90_s"]), (0, None, None))
        self.assertIn("No merges in this period.", html)
        self.assertIn("No merged tasks in this period.", html)
        self.assertIn("No timed tasks in this period.", html)


class _KpiTree(HTMLParser):
    """Walks the first .grid: its direct children, and every <p> in it with whether a .kpi card encloses it."""
    def __init__(self):
        super().__init__()
        self.stack, self.grid_at, self.done = [], None, False
        self.children, self.paras = [], []

    def handle_starttag(self, tag, attrs):
        cls = (dict(attrs).get("class") or "").split()
        if self.grid_at is not None and not self.done:
            if len(self.stack) == self.grid_at + 1:
                self.children.append(cls)
            if tag == "p":
                self.paras.append(any("kpi" in c for _, c in self.stack[self.grid_at + 1:]))
        elif "grid" in cls and not self.done:
            self.grid_at = len(self.stack)
        self.stack.append((tag, cls))

    def handle_endtag(self, tag):
        self.stack.pop()
        if self.grid_at is not None and len(self.stack) == self.grid_at:
            self.done = True    # first grid closed; ignore the rest of the page


class TestQuality(MetricsCase):
    def test_kpi_definitions_are_inside_their_cards(self):
        _, html = self.page(quality)
        t = _KpiTree()
        t.feed(html)
        self.assertEqual(len(t.children), 10)
        self.assertTrue(all("kpi" in c for c in t.children), t.children)    # grid items are the cards themselves
        self.assertEqual(t.paras, [True] * 10)      # one definition per card, each a descendant of .kpi

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
        for label, value in (("Merged PRs", "6"), ("Merged first try", "5"), ("Merged after &gt;= 1 rejection", "1"),
                             ("Rejections per merged PR", "0.50")):
            self.assertRegex(html, rf'<div class="kpi-label">{label}</div><div class="kpi-value">{value}</div>'
                                   r'<p class="kpi-note">[^<]+</p></div>')

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
        self.assertIn("n/a, see G-013", html)
        self.assertIn("Rejections by cause", html)

    def test_cause_is_na_with_definition(self):
        _, html = self.page(quality)
        self.assertRegex(html, r'Rejections by cause</div><div class="kpi-value">n/a, see G-013</div>'
                               r'<p class="kpi-note">[^<]+</p></div>')

    def test_rejections_week_bars_show_counts(self):
        _, html = self.page(quality)
        bars = re.findall(r'aria-label="(\d+) of max (\d+)"><span class="mono">([^<]*)</span>', html)
        self.assertTrue(bars)
        for n, mx, shown in bars:
            self.assertEqual(shown, n)
            self.assertFalse(shown.endswith("%"))

    def test_team_table_has_score_and_date(self):
        d, html = self.page(quality)
        t = {x["team_id"]: x for x in d["teams"]}
        self.assertEqual((t["t-a"]["score"], t["t-a"]["eval_date"]), (5, "2026-10-24"))
        self.assertIsNone(t["t-b"]["score"])
        self.assertIn("2026-10-24", html)
        head = html.split("Per team")[1].split("</tr>")[0]
        self.assertNotIn("Rej. rate", html)
        self.assertEqual(len(re.findall(r"<th>", head)), 6)

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
        self.assertRegex(html, r'Gate refusals</div><div class="kpi-value">\d+</div><p class="kpi-note">[^<]+</p></div>')
        self.assertRegex(html, r'Rejections</div><div class="kpi-value">\d+</div><p class="kpi-note">[^<]+</p></div>')
        self.assertIn("counts incidents, not events", html)

    def test_rejections_per_week(self):
        d, html = self.page(quality)
        self.assertEqual(d["weeks"], [{"week": "2026-10-19", "rejections": 3}])   # 2026-10-24 is a Saturday
        self.assertIn("2026-10-19", html)
        self.w.execute("INSERT INTO verdicts(task_id,role,verdict,created) VALUES('T-2','qa','fail',?)", (ts(9, 0, day=14),))
        d = self.page(quality)[0]
        self.assertEqual(d["weeks"], [{"week": "2026-10-12", "rejections": 1}, {"week": "2026-10-19", "rejections": 3}])

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
        self.assertIn("Goal one", html)

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
        self.assertIn("&lt;script&gt;", self.page(throughput)[1])

    def test_only_known_css_classes_and_fast(self):
        for path, view in self.VIEWS:
            t0 = time.perf_counter()
            html = self.page(view)[1]
            self.assertLess(time.perf_counter() - t0, 1.0, path)
            used = {c for m in re.findall(r'class="([^"]*)"', html) for c in m.split()}
            self.assertEqual(used - wh.CLASSES, set(), path)
            self.assertNotIn("style=", html)
            self.assertNotIn("<script", html)


if __name__ == "__main__":
    unittest.main()
