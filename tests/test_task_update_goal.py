"""T-0552: `factory task update --goal` really moves the task (director/owner) or refuses; never a silent no-op."""
import unittest

from harness import FactoryTestCase


class TaskUpdateGoalTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal("G-001")
        e.seed_goal("G-002")
        e.seed_team("g-001-a", goal="G-001")
        e.seed_task("T-0001", goal="G-001", status="todo", pr_url=None, team="g-001-a")
        e.seed_agent("zz-director", "director")
        e.seed_agent("zz-lead", "lead", goal="G-001", team="g-001-a")

    def goal_of(self, tid="T-0001"):
        r = self.env.sql("SELECT goal_id, team_id FROM tasks WHERE id=?", (tid,))[0]
        return r["goal_id"], r["team_id"]

    def test_a_director_moves_task_and_event_records_old_new(self):
        p = self.ok("task", "update", "T-0001", "--goal", "G-002", agent="zz-director")
        self.assertIn("T-0001 updated", p.stdout)
        self.assertEqual(self.goal_of(), ("G-002", None))
        ev = self.env.sql("SELECT detail FROM events WHERE entity='task' AND entity_id='T-0001' AND action='update'")
        self.assertIn("goal=G-001 -> G-002", ev[-1]["detail"])

    def test_b_lead_refused_and_unchanged(self):
        p = self.refused(3, "task", "update", "T-0001", "--goal", "G-002", agent="zz-lead")
        self.assertNotIn("updated", p.stdout)
        self.assertEqual(self.goal_of(), ("G-001", "g-001-a"))

    def test_c_unknown_goal_same_goal_and_active_task_refused(self):
        self.refused(2, "task", "update", "T-0001", "--goal", "G-999", agent="zz-director")
        self.refused(2, "task", "update", "T-0001", "--goal", "G-001", agent="zz-director")
        self.env.sql("UPDATE tasks SET status='in_progress' WHERE id='T-0001'")
        self.refused(2, "task", "update", "T-0001", "--goal", "G-002", agent="zz-director")
        self.assertEqual(self.goal_of()[0], "G-001")

    def test_d_live_agent_refused(self):
        self.env.seed_agent("zz-coder", "coder", goal="G-001", task="T-0001")
        p = self.refused(2, "task", "update", "T-0001", "--goal", "G-002", agent="zz-director")
        self.assertIn("zz-coder", p.stderr)
        self.assertEqual(self.goal_of()[0], "G-001")

    def test_e_dependency_links_refused(self):
        self.env.seed_task("T-0002", goal="G-001", status="todo", pr_url=None)
        self.env.sql("UPDATE tasks SET depends_on='[\"T-0001\"]' WHERE id='T-0002'")
        self.refused(2, "task", "update", "T-0001", "--goal", "G-002", agent="zz-director")
        self.refused(2, "task", "update", "T-0002", "--goal", "G-002", agent="zz-director")

    def test_f_empty_update_is_an_error_not_updated(self):
        p = self.refused(2, "task", "update", "T-0001", agent="zz-director")
        self.assertNotIn("updated", p.stdout)
        n = self.env.sql("SELECT COUNT(*) c FROM events WHERE entity_id='T-0001' AND action='update'")[0]["c"]
        self.assertEqual(n, 0)


if __name__ == "__main__":
    unittest.main()
