import unittest

from harness import FactoryTestCase


class TaskUpdateTeamTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_goal("G-002")
        e.seed_team("g-001-a")
        e.seed_team("g-001-b")
        e.seed_team("g-002-a", goal="G-002")
        e.seed_task("T-0001", status="todo", pr_url=None, team="g-001-a")
        e.seed_agent("lead-g-001-a", "lead", goal="G-001", team="g-001-a")
        e.seed_agent("director", "director")
        e.seed_agent("coder-t-0001", "coder", goal="G-001", team="g-001-a", task="T-0001")

    def team(self):
        return self.env.sql("SELECT team_id FROM tasks WHERE id='T-0001'")[0]["team_id"]

    def history(self):
        return [r["detail"] for r in self.env.sql(
            "SELECT detail FROM events WHERE entity='task' AND entity_id='T-0001' AND action='update'")]

    def test_valid_team_changes_team_id_and_logs_history(self):
        for agent in ("lead-g-001-a", "director"):
            self.env.sql("UPDATE tasks SET team_id='g-001-a' WHERE id='T-0001'")
            p = self.ok("task", "update", "T-0001", "--team", "g-001-b", agent=agent)
            self.assertIn("T-0001 updated", p.stdout)
            self.assertEqual(self.team(), "g-001-b")
            self.assertTrue(any("team=g-001-b" in d for d in self.history()), self.history())

    def test_unknown_team_refused(self):
        p = self.refused(2, "task", "update", "T-0001", "--team", "g-001-zz", agent="lead-g-001-a")
        self.assertIn("g-001-zz", p.stderr)
        self.assertNotIn("updated", p.stdout)
        self.assertEqual(self.team(), "g-001-a")

    def test_team_of_another_goal_refused(self):
        p = self.refused(2, "task", "update", "T-0001", "--team", "g-002-a", agent="director")
        self.assertIn("g-002-a", p.stderr)
        self.assertNotIn("updated", p.stdout)
        self.assertEqual(self.team(), "g-001-a")

    def test_worker_refused(self):
        p = self.refused(3, "task", "update", "T-0001", "--team", "g-001-b", agent="coder-t-0001")
        self.assertIn("--team", p.stderr)
        self.assertNotIn("updated", p.stdout)
        self.assertEqual(self.team(), "g-001-a")

    def test_update_without_team_keeps_team(self):
        self.ok("task", "update", "T-0001", "--title", "new", agent="lead-g-001-a")
        self.assertEqual(self.team(), "g-001-a")

    def seed_goal_b_task(self):
        self.env.seed_task("T-0002", goal="G-002", status="todo", pr_url=None, team="g-002-a")
        self.env.seed_team("g-002-b", goal="G-002")

    def team_of(self, tid):
        return self.env.sql("SELECT team_id FROM tasks WHERE id=?", (tid,))[0]["team_id"]

    def test_lead_of_another_goal_refused(self):
        self.seed_goal_b_task()
        p = self.refused(3, "task", "update", "T-0002", "--team", "g-002-b", agent="lead-g-001-a")
        self.assertIn("--team on T-0002", p.stderr)
        self.assertNotIn("updated", p.stdout)
        self.assertEqual(self.team_of("T-0002"), "g-002-a")

    def test_lead_without_goal_refused(self):
        self.env.seed_agent("lead-nogoal", "lead")
        p = self.refused(3, "task", "update", "T-0001", "--team", "g-001-b", agent="lead-nogoal")
        self.assertIn("--team on T-0001", p.stderr)
        self.assertEqual(self.team(), "g-001-a")

    def test_lead_own_goal_allowed(self):
        self.ok("task", "update", "T-0001", "--team", "g-001-b", agent="lead-g-001-a")
        self.assertEqual(self.team(), "g-001-b")

    def test_director_and_boss_any_goal_allowed(self):
        self.seed_goal_b_task()
        self.env.seed_agent("boss", "boss")
        for agent, tid, tm in (("director", "T-0002", "g-002-b"), ("boss", "T-0001", "g-001-b")):
            self.ok("task", "update", tid, "--team", tm, agent=agent)
            self.assertEqual(self.team_of(tid), tm)


if __name__ == "__main__":
    unittest.main()
