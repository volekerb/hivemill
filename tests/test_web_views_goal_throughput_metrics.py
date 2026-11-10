"""/goal/<id> "Cost and time" card and /throughput median + p90 cost and tokens per merged PR (fixed fixture, fixed clock)."""
import datetime as dt
import re
import sqlite3
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from web import prices  # noqa: E402
from web.views import goals, throughput  # noqa: E402

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

    def throughput(self, **query):
        c = self.ctx(**query)
        d = throughput.data(c)
        return d, str(throughput.render(c, d))

    def goal_page(self, gid):
        c = self.ctx()
        d = goals.detail.data(c, id=gid)
        return d, str(goals.detail.render(c, d))


def cost_of(inp):
    return prices.cost(SONNET, usage_row("x", inp))


class ThroughputSpread(Base):
    def seed(self):
        self.goal("G-1")
        for n, inp in enumerate((100_000, 200_000, 300_000, 400_000, 500_000, 600_000, 700_000, 800_000, 900_000, 1_000_000), 1):
            self.merged(f"T-{n}", "G-1", 0, 10, inp=inp)

    def test_median_and_p90_cost_and_tokens_are_exact(self):
        self.seed()
        d, html = self.throughput()
        c = d["cost"]
        costs = sorted(cost_of(i) for i in range(100_000, 1_000_001, 100_000))
        self.assertEqual(c["priced"], 10)
        self.assertEqual(c["tokens_median"], 550_000)       # (500k + 600k) / 2
        self.assertEqual(c["tokens_p90"], 900_000)          # nearest rank: ceil(0.9 * 10) = 9th value
        self.assertAlmostEqual(c["cost_median"], (costs[4] + costs[5]) / 2, places=4)
        self.assertAlmostEqual(c["cost_p90"], costs[8], places=4)
        self.assertEqual(c["left_out_ids"], [])
        for text in ("Median cost per merged PR", "p90 cost per merged PR", "Median tokens per merged PR", "550,000", "900,000"):
            self.assertIn(text, html)

    def test_tasks_left_out_are_counted_and_named_not_zero(self):
        self.seed()
        self.merged("T-20", "G-1", 0, 12)                    # no tokens attributed
        d, html = self.throughput()
        self.assertEqual(d["cost"]["left_out_ids"], ["T-20"])
        self.assertEqual(d["cost"]["tokens_p90"], 900_000)   # the priced set is unchanged
        self.assertIn("Left out (no tokens or no price): T-20.", html)
        self.assertIn("10 of 11 Claude tasks priced", html)

    def test_cursor_tasks_are_named_and_add_nothing(self):
        self.seed()
        self.goal("G-9", org="cursor")
        self.merged("T-30", "G-9", 0, 15, agent_prefix="cursor-coder")
        d, html = self.throughput()
        self.assertEqual(d["cost"]["cursor_ids"], ["T-30"])
        self.assertEqual(d["cost"]["priced"], 10)
        self.assertIn("Cursor tasks, no cost or tokens reported: T-30.", html)

    def test_cursor_org_view_says_not_available(self):
        self.seed()
        self.goal("G-9", org="cursor")
        self.merged("T-30", "G-9", 0, 15, agent_prefix="cursor-coder")
        _, html = self.throughput(org="cursor")
        self.assertIn("Not available for Cursor", html)
        self.assertNotIn("Median cost per merged PR", html)

    def test_empty_period_shows_dash_not_zero_dollars(self):
        self.goal("G-1")
        _, html = self.throughput()
        box = html.split("Cost and tokens per merged PR")[1].split("</section>")[0]
        self.assertNotIn("$0.00", box)
        self.assertEqual(len(re.findall(r">-<", box)), 4)

    def test_not_ready_index_is_not_zero(self):
        self.seed()
        c = self.ctx(ready=False)
        html = str(throughput.render(c, throughput.data(c)))
        self.assertIn("Not ready: the token index is still warming up.", html)


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
