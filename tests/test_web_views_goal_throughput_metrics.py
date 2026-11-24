"""/goal/<id> "Cost and time" card, and the Flow (hours per merged PR) and Spend (dollars per merged PR) pages from hm-obs fixtures."""
import datetime as dt
import json
import re
import sqlite3
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_views_tokens import flow_obs, obs_ctx, spend_obs  # noqa: E402
from web import prices  # noqa: E402
from web.views import goals, throughput, tokens  # noqa: E402

SCHEMA = (ROOT / "tests" / "fixtures" / "web" / "schema.sql").read_text()
NOW = dt.datetime(2026, 10, 24, 12, 0).astimezone()
SONNET = "claude-sonnet-5-5"


def ts(h, m, day=24):
    return dt.datetime(2026, 10, day, h, m).astimezone().isoformat(timespec="seconds")


def usage_row(sid, inp):
    return {"session": sid, "model": SONNET, "day": "2026-10-24", "fast": False, "input": inp, "output": 0,
            "cache_read": 0, "cache_write_5m": 0, "cache_write_1h": 0, "requests": 1}


class Base(unittest.TestCase):
    def setUp(self):
        self.con = sqlite3.connect(":memory:")
        self.con.row_factory = sqlite3.Row
        self.con.executescript(SCHEMA)
        self.con.execute("ALTER TABLE goals ADD COLUMN org TEXT")
        self.con.execute("INSERT INTO projects(id,name,created) VALUES('p1','P','x')")
        self.rows = []

    def goal(self, gid, org=None, status="active"):
        self.con.execute("INSERT INTO goals(id,project_id,title,status,created,updated,org) VALUES(?,?,?,?,?,?,?)",
                         (gid, "p1", f"Goal {gid}", status, ts(8, 0), ts(8, 0), org))

    def merged(self, tid, goal, spawn_min, merge_min, inp=None, agent_prefix="coder", started=None):
        """A merged task: one agent spawned at 09:<spawn_min>, merged at 09:<merge_min>; `inp` input tokens (None: none)."""
        self.con.execute("INSERT INTO tasks(id,goal_id,title,status,created,updated) VALUES(?,?,?,?,?,?)",
                         (tid, goal, f"title {tid}", "done", ts(8, 0), ts(9, 59)))
        name = f"{agent_prefix}-{tid.lower()}"
        self.con.execute("INSERT INTO agents(name,role,goal_id,task_id,status,session_id,started,ended) VALUES(?,?,?,?,?,?,?,?)",
                         (name, "coder", goal, tid, "done", f"s-{tid}", ts(9, spawn_min), ts(9, merge_min)))
        self.con.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                         (ts(9, merge_min), "judge", "task", tid, "merged", "@x"))
        if inp is not None:
            self.rows.append(usage_row(f"s-{tid}", inp))

    def ctx(self, ready=True, **query):
        snap = {"ready": ready, "rows": self.rows,
                "sessions": {r["session"]: {"agent_name": None, "first_ts": None, "project": "", "cwd": ""} for r in self.rows}}
        return SimpleNamespace(db=self.con, now=NOW, tokens=lambda: snap, defaults={}, factory_home=None, query=query)

    def goal_page(self, gid):
        c = self.ctx()
        d = goals.detail.data(c, id=gid)
        return d, str(goals.detail.render(c, d))


def cost_of(inp):
    return prices.cost(SONNET, usage_row("x", inp))


def render(view, **ctx_args):
    c = obs_ctx(**ctx_args)
    d = view.data(c)
    json.dumps(d)
    return c, str(view.render(c, d))


def items(html):
    return dict(re.findall(r'<dt class="strip-label">([^<]*)</dt><dd class="strip-value">([^<]*)</dd>', html))


class FlowAndSpendFigures(unittest.TestCase):
    """The per-merged-PR spread now comes from hm-obs: hours on Flow, dollars on Spend. The page only formats them."""

    def test_median_and_p90_hours_are_exact_in_every_size_format(self):
        for med, p90, want in ((4.5, 12.25, ("4.5 h", "12 h")), (0.25, 8.26, ("0.2 h", "8.3 h")),
                               (9.96, 123.4, ("10 h", "123 h")), (1234.0, 2500.0, ("1,234 h", "2,500 h"))):
            _, html = render(throughput, flow=flow_obs(open_to_merge_median_h=med, open_to_merge_p90_h=p90))
            f = items(html)
            self.assertEqual((f["Median open → merge"], f["p90 open → merge"]), want, (med, p90))

    def test_unattributed_spend_is_a_named_row_and_a_share_not_dropped(self):
        _, html = render(tokens, spend=spend_obs())
        self.assertIn('<span class="muted">Not attributed</span>', html)
        self.assertIn("8.0% not attributed", html)
        row = html.split('<span class="muted">Not attributed</span>')[1].split("</tr>")[0]
        self.assertIn('<span class="cl">$/PR</span>–', row)       # no merged PR to divide by: a dash, not $0.00
        self.assertIn('<span class="cl">$</span>$4.00', row)
        self.assertNotIn("$0.00</td><td class=\"num\"><span class=\"cl\">$/PR", html)

    def test_rows_drill_to_the_next_group_and_keep_the_filter(self):
        _, html = render(tokens, spend=spend_obs(filters={"role": "coder"}, group="model", drill="session"),
                         group="model", role="coder")
        self.assertIn("Filtered by role coder", html)
        self.assertRegex(html, r'<a href="/spend\?[^"]*">clear</a>')
        self.assertRegex(html, r'<a href="/spend\?[^"]*group=session[^"]*"><span class="mono">coder</span></a>')

    def test_spend_asks_hm_obs_for_group_range_and_drill_filters_only(self):
        c, _ = render(tokens, spend=spend_obs(), group="model", role="coder", range="30d")
        self.assertEqual(c.calls, [("spend", ["--hours", "720", "--group", "model", "--role", "coder"])])
        c, _ = render(tokens, spend=spend_obs(), group="evil", model="x y; rm", range="nope")
        self.assertEqual(c.calls, [("spend", ["--hours", "168", "--group", "role"])])

    def test_empty_period_shows_dash_not_zero_dollars(self):
        empty = {"cost_usd": None, "requests": 0, "sessions": 0, "spawn_usd": None, "polling_usd": None, "work_usd": None,
                 "cache_write_usd": None, "unpriced_requests": 0, "polling_pct": None, "cache_write_pct": None,
                 "merged_prs": 0, "per_merged_pr_usd": None}
        _, html = render(tokens, spend=spend_obs(current=empty, previous={}, rows=[], bins=[], banners=[],
                                                 unattributed_usd=None, unattributed_pct=None))
        f = items(html)
        self.assertEqual((f["Spend"], f["Per merged PR"], f["Polling"], f["Cache writes"]), ("–", "–", "–", "–"))
        self.assertIn("No spend in this range", html)
        self.assertNotIn("$0.00", html)
        self.assertNotIn("not attributed", html)

    def test_unavailable_obs_is_a_banner_and_never_zero_dollars(self):
        _, html = render(tokens, error="hm-obs exited 3")
        self.assertIn('<p class="banner" role="note">! Spend data unavailable: hm-obs exited 3</p>', html)
        self.assertNotIn("$", html)
        self.assertNotIn('class="strip"', html)


class GoalCostTime(Base):
    def test_claude_goal_card_has_totals_overhead_merged_and_median_wall(self):
        self.goal("G-1")
        self.merged("T-1", "G-1", 0, 10, inp=1_000_000)
        self.merged("T-2", "G-1", 0, 30, inp=2_000_000)
        self.merged("T-3", "G-1", 0, 50, inp=3_000_000)
        self.con.execute("INSERT INTO agents(name,role,goal_id,task_id,status,session_id,started) VALUES(?,?,?,?,?,?,?)",
                         ("lead-g1", "lead", "G-1", None, "live", "s-lead", ts(8, 30)))
        self.rows.append(usage_row("s-lead", 500_000))
        d, html = self.goal_page("G-1")
        u = d["usage"]
        self.assertEqual(u["merged"], 3)
        self.assertEqual(u["median_s"], 30 * 60)
        self.assertEqual(u["tokens"], 6_500_000)
        self.assertAlmostEqual(u["tasks_cost"], cost_of(6_000_000), places=6)
        self.assertAlmostEqual(u["overhead_cost"], cost_of(500_000), places=6)
        self.assertAlmostEqual(u["cost"], cost_of(6_500_000), places=6)
        box = html.split("<h2>Cost and time")[1].split("</section>")[0]
        for text in ("6,500,000", "Estimated cost, tasks", "Estimated cost, goal overhead", "Estimated cost, total",
                     "Merged PRs", "Median wall time per merged PR", "Rework share", "list prices as of"):
            self.assertIn(text, box)
        self.assertIn(f"${cost_of(6_500_000):,.2f}", box)

    def test_cursor_goal_shows_agent_hours_and_not_available(self):
        self.goal("G-9", org="cursor")
        self.merged("T-30", "G-9", 0, 15, agent_prefix="cursor-coder")
        d, html = self.goal_page("G-9")
        self.assertEqual(d["usage"]["state"], "cursor")
        self.assertEqual(d["usage"]["hours"], 0.25)
        box = html.split("<h2>Cost and time")[1].split("</section>")[0]
        self.assertIn("Tokens and cost: not available.", box)
        self.assertIn("0.25 agent-hours", box)
        self.assertNotIn("$", box)

    def test_goal_without_merges_or_usage_shows_dashes(self):
        self.goal("G-2")
        d, html = self.goal_page("G-2")
        box = html.split("<h2>Cost and time")[1].split("</section>")[0]
        self.assertIsNone(d["usage"]["median_s"])
        self.assertNotIn("$0.00", box)
        self.assertIn("Median wall time per merged PR", box)

    def test_not_ready_index_says_so(self):
        self.goal("G-1")
        c = self.ctx(ready=False)
        d = goals.detail.data(c, id="G-1")
        self.assertEqual(d["usage"]["state"], "not_ready")
        self.assertIn("Cost: not ready", str(goals.detail.render(c, d)))


if __name__ == "__main__":
    unittest.main()
