"""T-0920: `factory spawn ROLE --task T` without --goal/--team takes goal_id/team_id from the task row, so utilization()
(factory res, the starved-goal alerts) counts the worker toward its goal. Explicit values win; no --task is unchanged.
A queued spawn keeps the same values once the tick dequeues it. Real CLI, temp FACTORY_HOME, herdr stub."""
import importlib.machinery
import importlib.util
import os
import unittest

from harness import FACTORY_SRC, FactoryTestCase

NAME = "zz-coder-a"


class SpawnTaskAttribution(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal("G-001")
        e.seed_goal("G-002")
        e.sql("UPDATE goals SET priority=1 WHERE id='G-001'")
        e.seed_team("g-001-a", lead="zz-lead")
        e.seed_team("g-002-a", lead="zz-lead2")
        e.seed_task("T-0001", goal="G-001", team="g-001-a", status="todo", pr_url=None)
        e.seed_task("T-0002", goal="G-001", team=None, status="todo", pr_url=None)
        e.seed_agent("zz-director", "director")

    def spawn(self, *extra, task="T-0001"):
        return self.env.run("spawn", "coder", "--name", NAME, "--task", task, *extra, agent="zz-director")

    def row(self):
        return self.env.sql("SELECT goal_id, team_id, status FROM agents WHERE name=?", (NAME,))[0]

    def utilization(self):
        old = os.environ.get("FACTORY_HOME")
        os.environ["FACTORY_HOME"] = str(self.env.home_dir)
        self.addCleanup(lambda: os.environ.pop("FACTORY_HOME") if old is None else os.environ.update(FACTORY_HOME=old))
        loader = importlib.machinery.SourceFileLoader("factory_attr_under_test", str(FACTORY_SRC))
        mod = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
        loader.exec_module(mod)
        con = mod.db()
        self.addCleanup(con.close)
        return mod.utilization(con)

    def test_teamless_spawn_takes_goal_and_team_from_task(self):
        p = self.spawn()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        r = self.row()
        self.assertEqual((r["goal_id"], r["team_id"], r["status"]), ("G-001", "g-001-a", "live"))

    def test_task_without_team_leaves_team_empty(self):
        self.assertEqual(self.spawn(task="T-0002").returncode, 0)
        r = self.row()
        self.assertEqual((r["goal_id"], r["team_id"]), ("G-001", None))

    def test_utilization_counts_the_worker_for_its_goal(self):
        self.assertEqual(self.spawn().returncode, 0)
        u = self.utilization()
        self.assertEqual(u["workers"], 1)
        self.assertNotIn("G-001", u["p1_starved"])

    def test_explicit_goal_and_team_keep_precedence(self):
        p = self.spawn("--goal", "G-002", "--team", "g-002-a")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        r = self.row()
        self.assertEqual((r["goal_id"], r["team_id"]), ("G-002", "g-002-a"))

    def test_queued_spawn_keeps_values_after_dequeue(self):
        self.env.set_herdr(fail=True)
        p = self.spawn()
        self.assertIn("QUEUED", p.stdout, p.stdout + p.stderr)
        r = self.row()
        self.assertEqual((r["goal_id"], r["team_id"], r["status"]), ("G-001", "g-001-a", "queued"))
        self.env.set_herdr(fail=False)
        self.ok("config", "max_live_agents", "5")
        self.ok("tick")
        r = self.row()
        self.assertEqual((r["goal_id"], r["team_id"], r["status"]), ("G-001", "g-001-a", "live"))

    def test_no_task_spawn_is_unchanged(self):
        p = self.env.run("spawn", "pm", "--name", "zz-pm-a", agent="zz-director")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        r = self.env.sql("SELECT goal_id, team_id FROM agents WHERE name='zz-pm-a'")[0]
        self.assertEqual((r["goal_id"], r["team_id"]), (None, None))


if __name__ == "__main__":
    unittest.main()
