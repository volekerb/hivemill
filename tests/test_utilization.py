"""G-018 B: utilization(), cap_breaches(), util_samples + util_sample (tick), the res lines and the web home KPI."""
import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
import re
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from harness import FACTORY_SRC, FactoryTestCase  # noqa: E402
from web import db as wdb  # noqa: E402
from web.views import home  # noqa: E402

LINE = "live 17: workers 7 / leads 4 / judges 3 / HQ 2 / queued 3; P1 goals without workers: G-C"
RES2 = "utilization: workers 7/10 target · leads+judges 7/17 (max 1/3) · UNDER TARGET · HQ-HEAVY"
NOW = "2026-10-27T10:00:00"


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.mod = None

    def load(self):
        """bin/factory imported in-process against this test's temp FACTORY_HOME."""
        old = os.environ.get("FACTORY_HOME")
        os.environ["FACTORY_HOME"] = str(self.env.home_dir)
        self.addCleanup(lambda: os.environ.pop("FACTORY_HOME") if old is None else os.environ.update(FACTORY_HOME=old))
        loader = importlib.machinery.SourceFileLoader("factory_util_under_test", str(FACTORY_SRC))
        mod = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
        loader.exec_module(mod)
        self.mod = mod
        con = mod.db()
        self.addCleanup(con.close)
        return con

    def seed_item2(self):
        e = self.env
        e.run("config", "max_live_agents", "22", agent=None)
        e.sql("INSERT OR REPLACE INTO config(key,value) VALUES('max_live_agents','22')")
        for gid, prio in (("G-A", 1), ("G-B", 1), ("G-C", 1), ("G-D", 2)):
            e.seed_goal(gid)
            e.sql("UPDATE goals SET priority=? WHERE id=?", (prio, gid))
        n = 0

        def add(role, count, status="live", goal=None):
            nonlocal n
            for _ in range(count):
                n += 1
                e.seed_agent(f"{role}-{n}", role, goal=goal, status=status)
        add("coder", 1, goal="G-A")
        add("coder", 2)
        add("qa", 1)
        add("reviewer", 1)
        add("pm", 1)
        add("cleaner", 1)
        add("lead", 4)
        add("judge", 3)
        add("boss", 1)
        add("director", 1)
        add("coder", 1, status="starting")
        add("coder", 2, status="queued")
        add("judge", 1, status="queued", goal="G-B")


class UtilCase(Base):
    def test_counts_item2(self):
        self.seed_item2()
        con = self.load()
        u = self.mod.utilization(con)
        self.assertEqual(u["line"], LINE)
        self.assertEqual((u["target"], u["under"], u["demand"], u["hq_heavy"]), (10, True, True, True))
        self.assertEqual(u["p1_starved"], ["G-C"])
        self.assertEqual(u["p2_starved"], ["G-D"])
        self.assertEqual((u["workers"], u["leads"], u["judges"], u["hq"], u["other"], u["live"]), (7, 4, 3, 2, 1, 17))

    def test_cap_variants(self):
        con = self.load()
        con.execute("INSERT OR REPLACE INTO config(key,value) VALUES('max_live_agents','6')")
        self.assertEqual(self.mod.utilization(con)["target"], 4)
        con.execute("UPDATE config SET value='abc' WHERE key='max_live_agents'")
        self.assertEqual(self.mod.utilization(con)["cap"], int(self.mod.DEFAULT_CONFIG["max_live_agents"]))

    def test_demand_from_ready_task_only(self):
        e = self.env
        e.seed_goal("G-A")
        e.seed_task("T-0001", goal="G-A", role="coder", status="done")
        e.seed_task("T-0002", goal="G-A", role="coder", status="todo")
        e.sql("UPDATE tasks SET depends_on='[\"T-0001\"]' WHERE id='T-0002'")
        e.seed_task("T-0003", goal="G-A", role="coder", status="todo")
        e.sql("UPDATE tasks SET depends_on='[\"T-0009\"]' WHERE id='T-0003'")
        con = self.load()
        self.assertEqual((self.mod.utilization(con)["ready"], self.mod.utilization(con)["demand"]), (1, True))
        e.seed_agent("coder-x", "coder", goal="G-A", task="T-0002", status="queued")
        u = self.mod.utilization(self.load())
        self.assertEqual((u["ready"], u["queued_workers"]), (0, 1))
        e.sql("DELETE FROM agents")
        e.sql("UPDATE tasks SET status='cancelled' WHERE status='todo'")
        self.assertFalse(self.mod.utilization(self.load())["demand"])

    def test_res_keeps_old_lines_and_adds_two(self):
        self.seed_item2()
        out = self.env.run("res").stdout.splitlines()
        i = next(k for k, l in enumerate(out) if l.startswith("agents live "))
        self.assertTrue(out[i + 1].startswith("workers live "), out[i + 1])
        self.assertEqual(out[i + 2], LINE)
        self.assertEqual(out[i + 3], RES2)
        old = out[:i + 1] + out[i + 4:]
        self.assertTrue(old.pop(5).startswith("blocked tasks:"))
        for want, got in zip(["spawn gate:", "load gate:", "test runs:", "gate runs:", "agents live ", "mem available", "tmp used"], old):
            self.assertTrue(got.startswith(want), (want, got))

    def test_res_no_flags_when_healthy(self):
        out = self.env.run("res").stdout
        self.assertIn("P1 goals without workers: none", out)
        line = next(l for l in out.splitlines() if l.startswith("utilization: workers"))
        self.assertNotIn("UNDER TARGET", line)
        self.assertNotIn("HQ-HEAVY", line)

    def test_migration_adds_table(self):
        self.env.sql("DROP TABLE util_samples")
        self.assertEqual(self.env.run("res").returncode, 0)
        names = {r[0] for r in self.env.sql("SELECT name FROM sqlite_master")}
        self.assertIn("util_samples", names)
        self.assertIn("idx_util_samples_ts", names)

    def test_sampling_throttle_and_prune(self):
        self.seed_item2()
        con = self.load()
        old = self.mod.digest_clock
        def sample(now):
            with mock.patch.dict(os.environ, {"FACTORY_NOW": now}):
                self.mod.util_sample(con)
        rows = lambda: [dict(r) for r in con.execute("SELECT * FROM util_samples ORDER BY id")]
        sample(NOW)
        sample("2026-10-27T10:00:30")
        self.assertEqual(len(rows()), 1)
        sample("2026-10-27T10:01:00")
        r = rows()
        self.assertEqual(len(r), 2)
        self.assertEqual(r[1]["ts"], self.mod.digest_clock.__globals__["dt"].datetime.fromisoformat("2026-10-27T10:01:00").astimezone().isoformat(timespec="seconds"))
        self.assertEqual((r[1]["live"], r[1]["workers"], r[1]["leads"], r[1]["judges"], r[1]["hq"], r[1]["other"],
                          r[1]["queued"], r[1]["queued_workers"], r[1]["cap"], r[1]["target"], r[1]["demand"]),
                         (17, 7, 4, 3, 2, 1, 3, 2, 22, 10, 1))
        self.assertEqual((json.loads(r[1]["p1_starved"]), json.loads(r[1]["p2_starved"])), (["G-C"], ["G-D"]))
        self.assertIs(self.mod.digest_clock, old)

    def test_daily_prune_35_days(self):
        con = self.load()
        ins = ("INSERT INTO util_samples(ts,live,workers,leads,judges,hq,other,queued,queued_workers,cap,target,demand,p1_starved,p2_starved) "
               "VALUES(?,0,0,0,0,0,0,0,0,6,4,0,'[]','[]')")
        con.execute(ins, ("2026-09-21T09:00:00+00:00",))   # 36 days before NOW
        con.execute(ins, ("2026-09-23T09:00:00+00:00",))   # 34 days before NOW
        with mock.patch.dict(os.environ, {"FACTORY_NOW": "2026-10-27T10:00:00+00:00"}):
            self.mod.util_sample(con)
        left = [r["ts"] for r in con.execute("SELECT ts FROM util_samples ORDER BY id")]
        self.assertNotIn("2026-09-21T09:00:00+00:00", left)
        self.assertIn("2026-09-23T09:00:00+00:00", left)
        self.assertEqual(len(left), 2)

    def test_prune_compares_instants_across_dst(self):
        con = self.load()
        ins = ("INSERT INTO util_samples(ts,live,workers,leads,judges,hq,other,queued,queued_workers,cap,target,demand,p1_starved,p2_starved) "
               "VALUES(?,0,0,0,0,0,0,0,0,6,4,0,'[]','[]')")
        # cutoff = 2026-10-27T10:00+01:00 minus 35 days = 2026-09-22T09:00:00Z (CEST ended 2026-10-25)
        gone = "2026-09-22T10:30:00+02:00"  # 08:30Z, before the cutoff, but string-after "2026-09-22T09:00:00+00:00"
        kept = "2026-09-22T10:30:00+01:00"  # 09:30Z, after the cutoff, but string-equal in prefix
        for ts in (gone, kept):
            con.execute(ins, (ts,))
        with mock.patch.dict(os.environ, {"FACTORY_NOW": "2026-10-27T10:00:00+01:00"}):
            self.mod.util_sample(con)
        left = [r["ts"] for r in con.execute("SELECT ts FROM util_samples ORDER BY id")]
        self.assertNotIn(gone, left)
        self.assertIn(kept, left)
        self.assertEqual(len(left), 2)

    def test_tick_wiring(self):
        src = FACTORY_SRC.read_text().splitlines()
        hits = [i for i, l in enumerate(src) if "util_sample(con)" in l and not l.lstrip().startswith(("def ", "#"))]
        self.assertEqual(len(hits), 1)
        self.assertEqual(src[hits[0] - 1].strip(), "tick_relaunches(con)")

    def test_tick_runs_sampler(self):
        p = self.env.run("tick", extra_env={"FACTORY_NOW": NOW})
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM util_samples")[0]["c"], 1)

    def test_export_skips_samples(self):
        con = self.load()
        with mock.patch.dict(os.environ, {"FACTORY_NOW": NOW}):
            self.mod.util_sample(con)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM util_samples")[0]["c"], 1)
        self.assertEqual(self.env.run("export").returncode, 0)
        dump = (self.env.home_dir / "state" / "factory.sql").read_text()
        self.assertNotIn('INSERT INTO "util_samples"', dump)
        self.assertIn("util_samples", dump)  # the table itself is still dumped

    def test_failure_never_breaks_and_logs_once(self):
        con = self.load()
        with mock.patch.object(self.mod, "utilization", side_effect=RuntimeError("boom")):
            with mock.patch.dict(os.environ, {"FACTORY_NOW": NOW}):
                self.mod.util_sample(con)
            with mock.patch.dict(os.environ, {"FACTORY_NOW": "2026-10-27T10:30:00"}):
                self.mod.util_sample(con)
            with mock.patch.dict(os.environ, {"FACTORY_NOW": "2026-10-27T11:30:00"}):
                self.mod.util_sample(con)
        ev = con.execute("SELECT detail FROM events WHERE action='util-sample-failed' ORDER BY id").fetchall()
        self.assertEqual([e["detail"] for e in ev], ["boom", "boom"])  # 10:00 and 11:30; 10:30 is inside the hour
        self.assertEqual(con.execute("SELECT count(*) FROM util_samples").fetchone()[0], 0)

    def test_failure_inside_the_hour_logs_one_event(self):
        con = self.load()
        with mock.patch.object(self.mod, "utilization", side_effect=RuntimeError("boom")):
            for now in (NOW, "2026-10-27T10:20:00", "2026-10-27T10:40:00"):
                with mock.patch.dict(os.environ, {"FACTORY_NOW": now}):
                    self.mod.util_sample(con)
        self.assertEqual(con.execute("SELECT count(*) FROM events WHERE action='util-sample-failed'").fetchone()[0], 1)

    def test_tick_exits_zero_when_sampling_cannot_write(self):
        self.env.sql("DROP TABLE util_samples")
        self.env.sql("CREATE TABLE util_samples (id INTEGER PRIMARY KEY, ts TEXT NOT NULL)")  # old shape: the INSERT fails
        p = self.env.run("tick", extra_env={"FACTORY_NOW": NOW})
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action='util-sample-failed'")[0]["c"], 1)

    def test_cap_breaches(self):
        con = self.load()
        self.env.seed_agent("director", "director")
        ins = "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)"
        con.execute(ins, ("2026-10-27T09:00:00+00:00", "director", "config", "max_live_agents", "set", "25"))
        con.execute(ins, ("2026-10-27T09:01:00+00:00", "owner", "config", "max_live_agents", "set", "22"))
        con.execute(ins, ("2026-10-27T09:02:00+00:00", "director", "config", "digest_hour", "set", "9"))
        con.execute(ins, ("2026-10-27T12:00:00+00:00", "director", "config", "max_live_agents", "set", "26"))
        rows = self.mod.cap_breaches(con, "2026-10-27T08:00:00+00:00", "2026-10-27T12:00:00+00:00")
        self.assertEqual([(r["actor"], r["entity_id"], r["detail"]) for r in rows], [("director", "max_live_agents", "25")])


class WebHomeCase(Base):
    def page(self, now):
        con = wdb.connect(self.env.db_path)
        self.addCleanup(con.close)
        import datetime as dt
        ctx = SimpleNamespace(db=con, now=dt.datetime.fromisoformat(now).astimezone(), usage_fn=None, defaults={})
        return str(home.render(ctx, home.data(ctx)))

    def test_matches_res_line_and_flags(self):
        self.seed_item2()
        con = self.load()
        with mock.patch.dict(os.environ, {"FACTORY_NOW": NOW}):
            self.mod.util_sample(con)
        html = self.page("2026-10-27T10:03:00")
        self.assertIn(LINE + " (UNDER TARGET)", html)
        self.assertIn(LINE, self.env.run("res").stdout)

    def test_no_sample_and_stale_sample(self):
        self.assertIn("utilization: no fresh tick sample", self.page(NOW))
        con = self.load()
        with mock.patch.dict(os.environ, {"FACTORY_NOW": NOW}):
            self.mod.util_sample(con)
        self.assertIn("P1 goals without workers", self.page("2026-10-27T10:04:00"))
        html = self.page("2026-10-27T10:06:00")
        self.assertIn("utilization: no fresh tick sample", html)
        self.assertNotIn("P1 goals without workers", html)

    def test_not_under_target_without_demand(self):
        con = self.load()
        with mock.patch.dict(os.environ, {"FACTORY_NOW": NOW}):
            self.mod.util_sample(con)
        html = self.page("2026-10-27T10:01:00")
        self.assertIn("live 0:", html)
        self.assertNotIn("UNDER TARGET", html)


if __name__ == "__main__":
    unittest.main()
