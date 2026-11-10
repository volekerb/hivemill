import datetime as dt
import sqlite3
import unittest
from pathlib import Path

from web.prices import cost
from web.tokens import attribute, goal_usage, rollup, task_usage

SCHEMA = (Path(__file__).parent / "fixtures" / "web" / "schema.sql").read_text()
NOW = dt.datetime(2026, 11, 10, 12, 0, tzinfo=dt.timezone.utc)
SONNET, OPUS = "claude-sonnet-5-5", "claude-opus-5-5"


def row(sid, model=SONNET, inp=1000, out=500, cr=2000, w5=300, w1=100):
    return {"session": sid, "model": model, "day": "2026-11-09", "fast": False, "input": inp, "output": out,
            "cache_read": cr, "cache_write_5m": w5, "cache_write_1h": w1, "requests": 1}


def snap(rows, ready=True):
    return {"ready": ready, "rows": rows,
            "sessions": {r["session"]: {"agent_name": None, "first_ts": None, "project": "", "cwd": ""} for r in rows}}


class Base(unittest.TestCase):
    def setUp(self):
        self.con = sqlite3.connect(":memory:")
        self.con.row_factory = sqlite3.Row
        self.con.executescript(SCHEMA)

    def agent(self, name, role, task, sid, started, ended=None, goal="G-1", spawn_args=None):
        self.con.execute("INSERT INTO agents(name,role,goal_id,task_id,status,session_id,started,ended,spawn_args) "
                         "VALUES(?,?,?,?,?,?,?,?,?)", (name, role, goal, task, "done", sid, started, ended, spawn_args))

    def verdict(self, task, v, created):
        self.con.execute("INSERT INTO verdicts(task_id,role,verdict,created) VALUES(?,?,?,?)", (task, "qa", v, created))

    def usage(self, rows, task="T-1"):
        s = snap(rows)
        att = attribute(s, self.con)
        return s, att, task_usage(s, att, self.con, task, NOW)


class TaskUsageTests(Base):
    def test_four_roles_sum_equals_rollup(self):
        for i, (n, r) in enumerate([("coder-1", "coder"), ("rev-1", "reviewer"), ("qa-1", "qa"), ("judge-1", "judge")]):
            self.agent(n, r, "T-1", f"s{i}", f"2026-11-09T1{i}:00:00+00:00")
        rows = [row("s0"), row("s1", OPUS, 10, 20, 30, 4, 5), row("s2"), row("s3", OPUS)]
        s, att, u = self.usage(rows)
        self.assertEqual([r["agent"] for r in u["rows"]], ["coder-1", "rev-1", "qa-1", "judge-1"])
        self.assertEqual({r["role"] for r in u["rows"]}, {"coder", "reviewer", "qa", "judge"})
        by_task = rollup(s, att)["by_task"]["T-1"]
        t = u["totals"]
        self.assertEqual(t["tokens"], sum(by_task[k] for k in ("input", "output", "cache_read", "cache_write_5m", "cache_write_1h")))
        self.assertAlmostEqual(t["cost"], by_task["cost"])
        self.assertEqual(u["rows"][1]["cache_write"], 9)
        self.assertEqual(u["rows"][0]["cost"], cost(SONNET, rows[0]))
        self.assertEqual(t["rework_share"], 0.0)
        self.assertFalse(any(r["rework"] for r in u["rows"]))

    def test_rejected_task_flags_only_later_agents(self):
        self.agent("coder-1", "coder", "T-1", "s0", "2026-11-09T10:00:00+00:00")
        self.agent("qa-1", "qa", "T-1", "s1", "2026-11-09T11:00:00+00:00")
        self.agent("coder-2", "coder", "T-1", "s2", "2026-11-09T13:00:00+00:00")
        self.verdict("T-1", "pass", "2026-11-09T10:30:00+00:00")
        self.verdict("T-1", "fail", "2026-11-09T12:00:00+00:00")
        _, _, u = self.usage([row("s0"), row("s1"), row("s2", OPUS)])
        self.assertEqual([r["rework"] for r in u["rows"]], [False, False, True])
        t = u["totals"]
        self.assertAlmostEqual(t["rework_cost"], u["rows"][2]["cost"])
        self.assertAlmostEqual(t["rework_share"], t["rework_cost"] / t["cost"])

    def test_judge_reject_event_marks_rework(self):
        self.agent("coder-1", "coder", "T-1", "s0", "2026-11-09T10:00:00+00:00")
        self.agent("coder-2", "coder", "T-1", "s1", "2026-11-09T13:00:00+00:00")
        self.con.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) "
                         "VALUES('2026-11-09T12:00:00+00:00','judge','task','T-1','judge-reject','r')")
        _, _, u = self.usage([row("s0"), row("s1")])
        self.assertEqual([r["rework"] for r in u["rows"]], [False, True])

    def test_unpriced_model_gives_none_cost_and_share(self):
        self.agent("coder-1", "coder", "T-1", "s0", "2026-11-09T10:00:00+00:00")
        self.agent("coder-2", "coder", "T-1", "s1", "2026-11-09T13:00:00+00:00")
        self.verdict("T-1", "reject", "2026-11-09T12:00:00+00:00")
        _, _, u = self.usage([row("s0"), row("s1", "mystery-model")])
        self.assertIsNone(u["rows"][1]["cost"])
        self.assertIsNone(u["totals"]["cost"])
        self.assertIsNone(u["totals"]["rework_share"])
        self.assertGreater(u["totals"]["tokens"], 0)

    def test_partially_priced_agent_has_no_cost(self):
        self.agent("coder-1", "coder", "T-1", "s0", "2026-11-09T10:00:00+00:00")
        _, _, u = self.usage([row("s0"), row("s0", "mystery-model")])
        self.assertIsNone(u["rows"][0]["cost"])
        self.assertIsNone(u["totals"]["cost"])
        self.assertIsNone(u["totals"]["rework_share"])

    def test_zero_cost_gives_no_share(self):
        self.agent("coder-1", "coder", "T-1", "s0", "2026-11-09T10:00:00+00:00")
        _, _, u = self.usage([row("s0", inp=0, out=0, cr=0, w5=0, w1=0)])
        self.assertEqual(u["totals"]["cost"], 0)
        self.assertIsNone(u["totals"]["rework_share"])

    def test_cursor_task_is_not_available_never_zero(self):
        self.agent("cursor-coder-t-1", "coder", "T-1", None, "2026-11-09T10:00:00+00:00", "2026-11-09T10:30:00+00:00")
        self.agent("rt-coder", "coder", "T-1", None, "2026-11-09T11:00:00+00:00", "2026-11-09T11:10:00+00:00",
                   spawn_args='{"runtime": "cursor"}')
        _, _, u = self.usage([])
        self.assertEqual(len(u["rows"]), 2)
        for r in u["rows"]:
            self.assertEqual(r["state"], "not_available")
            for k in ("input", "output", "cache_read", "cache_write", "cost"):
                self.assertIsNone(r[k])
            self.assertGreaterEqual(r["sessions"], 1)
            self.assertGreater(r["wall_s"], 0)
        self.assertEqual(u["rows"][0]["wall_s"], 1800)
        t = u["totals"]
        self.assertEqual(t["state"], "not_available")
        for k in ("tokens", "cost", "rework_cost", "rework_share"):
            self.assertIsNone(t[k])
        self.assertGreater(t["wall_s"], 0)

    def test_not_ready(self):
        s = snap([row("s0")], ready=False)
        u = task_usage(s, {}, self.con, "T-1", NOW)
        self.assertEqual((u["state"], u["rows"], u["totals"]), ("not_ready", [], None))
        g = goal_usage(s, {}, self.con, "G-1", NOW)
        self.assertEqual(g["state"], "not_ready")


class GoalUsageTests(Base):
    def test_lead_is_overhead_not_in_a_task(self):
        self.agent("coder-1", "coder", "T-1", "s0", "2026-11-09T10:00:00+00:00")
        self.agent("lead-1", "lead", None, "s1", "2026-11-09T09:00:00+00:00")
        self.agent("cursor-coder-t-2", "coder", "T-2", None, "2026-11-09T10:00:00+00:00", "2026-11-09T10:20:00+00:00")
        s = snap([row("s0"), row("s1", OPUS, 5000, 900, 0, 0, 0)])
        att = attribute(s, self.con)
        g = goal_usage(s, att, self.con, "G-1", NOW)
        self.assertEqual(sorted(g["tasks"]), ["T-1", "T-2"])
        self.assertEqual([r["agent"] for r in g["tasks"]["T-1"]["rows"]], ["coder-1"])
        self.assertEqual([r["agent"] for r in g["overhead"]["rows"]], ["lead-1"])
        lead_cost = cost(OPUS, s["rows"][1])
        self.assertAlmostEqual(g["overhead"]["totals"]["cost"], lead_cost)
        self.assertAlmostEqual(g["totals"]["cost"], g["tasks"]["T-1"]["totals"]["cost"] + lead_cost)
        self.assertEqual(g["tasks"]["T-2"]["rows"][0]["state"], "not_available")
        self.assertEqual(g["totals"]["state"], "partial")
        by_goal = rollup(s, att)["by_goal"]["G-1"]
        self.assertAlmostEqual(g["totals"]["cost"], by_goal["cost"])


if __name__ == "__main__":
    unittest.main()
