"""G-018 C: util_alerts() tick hook: workers / P1 / P2 starvation alerts, boss escalation, dedupe, cap-breach alert."""
import datetime as dt
import importlib.machinery
import importlib.util
import json
import os
import sys
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from harness import FACTORY, FactoryTestCase  # noqa: E402

BASE = dt.datetime.fromisoformat("2026-10-27T10:00:00").astimezone()
INS = ("INSERT INTO util_samples(ts,live,workers,leads,judges,hq,other,queued,queued_workers,cap,target,demand,p1_starved,p2_starved) "
       "VALUES(?,?,?,?,?,?,0,?,?,22,?,?,?,?)")


def at(minute):
    return (BASE + dt.timedelta(minutes=minute)).isoformat(timespec="seconds")


class AlertCase(FactoryTestCase):
    def load(self):
        old = os.environ.get("FACTORY_HOME")
        os.environ["FACTORY_HOME"] = str(self.env.home_dir)
        self.addCleanup(lambda: os.environ.pop("FACTORY_HOME") if old is None else os.environ.update(FACTORY_HOME=old))
        loader = importlib.machinery.SourceFileLoader("factory_alerts_under_test", str(FACTORY))
        mod = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
        loader.exec_module(mod)
        self.mod = mod
        self.con = mod.db()
        self.addCleanup(self.con.close)

    def setUp(self):
        super().setUp()
        self.load()

    def samples(self, first, last, workers=5, target=10, demand=1, p1=(), p2=()):
        for m in range(first, last + 1):
            self.con.execute(INS, (at(m), 20, workers, 4, 3, 2, 3, 2, target, demand, json.dumps(list(p1)), json.dumps(list(p2))))

    def run_at(self, minute):
        with mock.patch.dict(os.environ, {"FACTORY_NOW": at(minute)}):
            self.mod.util_alerts(self.con)

    def msgs(self, to, like="%"):
        return [dict(r) for r in self.con.execute(
            "SELECT * FROM messages WHERE recipient=? AND subject LIKE ? ORDER BY id", (to, like))]


class WorkersAlerts(AlertCase):
    def test_threshold_15_min(self):
        self.samples(0, 14)
        self.run_at(14)
        self.assertEqual(self.msgs("director"), [])
        self.samples(15, 16)
        self.run_at(16)
        m = self.msgs("director")
        self.assertEqual(len(m), 1)
        self.assertEqual((m[0]["kind"], m[0]["sender"]), ("alert", "tick"))
        self.assertTrue(m[0]["subject"].startswith("utilization: workers 5/10 under target for"), m[0]["subject"])
        self.assertIn("live 20: workers 5 / leads 4 / judges 3 / HQ 2 / queued 3; P1 goals without workers: none", m[0]["body"])
        self.assertIn("Rule R1", m[0]["body"])
        self.assertEqual(self.msgs("boss"), [])

    def test_no_spam_and_escalation(self):
        self.samples(0, 16)
        self.run_at(16)
        for minute in (17, 20, 29):
            self.samples(minute, minute)
            self.run_at(minute)
        self.assertEqual(len(self.msgs("director")), 1)
        self.assertEqual(self.msgs("boss"), [])
        self.samples(18, 31)  # fills the gaps the loop skipped
        self.con.execute("DELETE FROM util_samples WHERE id IN (SELECT id FROM util_samples GROUP BY ts HAVING count(*)>1)")
        self.run_at(31)
        b = self.msgs("boss")
        self.assertEqual(len(b), 1)
        self.assertEqual(b[0]["kind"], "alert")
        self.assertTrue(b[0]["subject"].startswith("escalation: workers under target for"), b[0]["subject"])
        for minute in (32, 45):
            self.samples(minute, minute)
            self.run_at(minute)
        self.assertEqual((len(self.msgs("director")), len(self.msgs("boss"))), (1, 1))

    def test_rearm_after_recovery(self):
        self.samples(0, 16)
        self.run_at(16)
        self.samples(17, 17, workers=10)
        self.run_at(17)
        self.samples(18, 34)
        self.run_at(34)
        self.assertEqual(len(self.msgs("director")), 2)

    def test_gap_restarts_episode(self):
        self.samples(0, 14)
        self.samples(21, 23)  # 7 min after minute 14
        self.run_at(23)
        self.assertEqual((self.msgs("director"), self.msgs("boss")), ([], []))

    def test_stale_newest_sample(self):
        self.samples(0, 20)
        self.run_at(26)
        self.assertEqual((self.msgs("director"), self.msgs("boss")), ([], []))

    def test_no_demand_no_alert(self):
        self.samples(0, 40, demand=0)
        self.run_at(40)
        self.assertEqual((self.msgs("director"), self.msgs("boss")), ([], []))


class StarvedGoals(AlertCase):
    def test_p1_two_goals_levels_and_dedupe(self):
        self.samples(0, 16, workers=10, p1=["G-X", "G-Y"])
        self.run_at(16)
        for g in ("G-X", "G-Y"):
            m = [x for x in self.msgs("director") if x["goal_id"] == g]
            self.assertEqual(len(m), 1)
            self.assertTrue(m[0]["subject"].startswith(f"starved P1 goal {g}: no worker live or queued for"), m[0]["subject"])
        self.run_at(16)
        self.assertEqual(len(self.msgs("director")), 2)
        self.samples(17, 31, workers=10, p1=["G-X", "G-Y"])
        self.run_at(31)
        self.run_at(31)
        b = self.msgs("boss")
        self.assertEqual(sorted(x["subject"][:28] for x in b), ["escalation: P1 goal G-X star", "escalation: P1 goal G-Y star"])
        self.assertEqual(len(self.msgs("director")), 2)

    def test_p2_after_60_min_director_only(self):
        self.samples(0, 59, workers=10, p2=["G-Z"])
        self.run_at(59)
        self.assertEqual(self.msgs("director"), [])
        self.samples(60, 61, workers=10, p2=["G-Z"])
        self.run_at(61)
        d = self.msgs("director")
        self.assertEqual(len(d), 1)
        self.assertTrue(d[0]["subject"].startswith("starved P2 goal G-Z"), d[0]["subject"])
        self.samples(62, 120, workers=10, p2=["G-Z"])
        self.run_at(120)
        self.assertEqual((len(self.msgs("director")), self.msgs("boss")), (1, []))


class CapBreach(AlertCase):
    def breach(self, actor, key="max_live_agents", value="30"):
        self.con.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                         (at(0), actor, "config", key, "set", value))

    def test_agent_breach_alerts_once(self):
        self.env.seed_agent("director", "director")
        self.con.commit()
        self.load()
        self.breach("director", value="25")  # history: before the first run
        self.run_at(1)
        self.assertEqual(self.msgs("boss"), [])
        self.breach("director")
        self.run_at(2)
        b = self.msgs("boss")
        self.assertEqual(len(b), 1)
        self.assertTrue(b[0]["subject"].startswith("owner-cap breach: director set max_live_agents="), b[0]["subject"])
        self.assertIn("Rule R4", b[0]["body"])
        self.run_at(3)
        self.assertEqual(len(self.msgs("boss")), 1)

    def test_episode_failure_does_not_hold_back_breach(self):
        self.env.seed_agent("director", "director")
        self.run_at(1)
        self.samples(0, 2, p1=["G-X"])
        self.con.execute("UPDATE util_samples SET p1_starved='not json' WHERE ts=?", (at(2),))
        self.breach("director")
        self.run_at(2)
        self.assertEqual(len(self.msgs("boss", "owner-cap breach:%")), 1)
        self.assertEqual(self.con.execute("SELECT count(*) FROM events WHERE action='util-alert-failed'").fetchone()[0], 1)


    def test_out_of_order_ts_alerts_each_once_and_cursor_is_max_id(self):
        self.env.seed_agent("director", "director")
        self.con.commit()
        self.load()
        self.run_at(1)
        ins = "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)"
        self.con.execute(ins, (at(9), "director", "config", "max_live_agents", "set", "31"))
        self.con.execute(ins, (at(3), "director", "config", "max_total_claude", "set", "32"))  # higher id, earlier ts
        top = self.con.execute("SELECT max(id) FROM events").fetchone()[0]
        self.run_at(10)
        self.run_at(11)
        subjects = sorted(m["subject"] for m in self.msgs("boss", "owner-cap breach:%"))
        self.assertEqual(len(subjects), 2, subjects)
        self.assertIn("max_live_agents=31", subjects[0] + subjects[1])
        self.assertIn("max_total_claude=32", subjects[0] + subjects[1])
        last = self.con.execute("SELECT value FROM config WHERE key='util_cap_breach_last_id'").fetchone()["value"]
        self.assertEqual(int(last), top)

    def test_owner_set_is_not_a_breach(self):
        self.env.seed_agent("director", "director")
        self.run_at(1)
        self.breach("owner")
        self.run_at(2)
        self.assertEqual(self.msgs("boss"), [])


class TickLevel(AlertCase):
    def seed(self, p1_goal=False):
        e = self.env
        e.sql("INSERT OR REPLACE INTO config(key,value) VALUES('max_live_agents','22')")
        e.seed_goal("G-A")
        e.seed_task("T-0001", goal="G-A", role="coder", status="todo")  # ready task: demand
        if p1_goal:
            e.sql("UPDATE goals SET priority=1 WHERE id='G-A'")
        self.now = "2026-10-27T10:00:00"
        for m in range(-16, 0):
            e.sql(INS, (at(m), 0, 0, 0, 0, 0, 0, 0, 10, 1, json.dumps(["G-A"] if p1_goal else []), "[]"))

    def tick(self):
        p = self.env.run("tick", extra_env={"FACTORY_NOW": self.now})
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_tick_alerts_once(self):
        self.seed()
        self.tick()
        n = lambda: self.env.sql("SELECT count(*) c FROM messages WHERE recipient='director' AND subject LIKE 'utilization: workers%'")[0]["c"]
        self.assertEqual(n(), 1)
        self.tick()
        self.assertEqual(n(), 1)

    def test_wiring_one_call_after_sample(self):
        src = FACTORY.read_text().splitlines()
        hits = [i for i, l in enumerate(src) if "util_alerts(con)" in l and not l.lstrip().startswith(("def ", "#"))]
        self.assertEqual(len(hits), 1)
        self.assertEqual(src[hits[0] - 1].strip(), "util_sample(con)")

    def test_corrupt_sample_never_breaks_tick(self):
        self.seed(p1_goal=True)
        self.env.sql("UPDATE util_samples SET p1_starved='not json' WHERE ts=?", (at(-5),))
        self.tick()
        self.tick()
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action='util-alert-failed'")[0]["c"], 1)


if __name__ == "__main__":
    unittest.main()
