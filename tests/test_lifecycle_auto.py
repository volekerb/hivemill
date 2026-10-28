"""T-0153: status transitions are mechanical: staffing a team or starting work activates the goal, and a worker that
really starts moves its todo task to in_progress (never while QUEUED)."""
import unittest

from harness import FactoryTestCase


class LifecycleBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_agent("zz-director", "director")
        e.seed_agent("zz-lead", "lead", goal="G-001", team="g-001-a")

    def goal_status(self, gid="G-001"):
        return self.env.sql("SELECT status FROM goals WHERE id=?", (gid,))[0]["status"]

    def task_status(self, tid="T-0001"):
        return self.env.sql("SELECT status FROM tasks WHERE id=?", (tid,))[0]["status"]

    def events(self, action):
        return self.env.sql("SELECT entity,entity_id,detail FROM events WHERE action=?", (action,))

    def set_goal(self, status):
        self.env.sql("UPDATE goals SET status=? WHERE id='G-001'", (status,))

    def team_new(self):
        self.ok("team", "new", "--goal", "G-001", "--scope", "zz scope", "--no-lead", agent="zz-director")


class GoalAutoActiveTests(LifecycleBase):
    def test_a_team_new_activates_proposed_goal(self):
        self.set_goal("proposed")
        self.team_new()
        self.assertEqual(self.goal_status(), "active")
        ev = self.events("goal-auto-active")
        self.assertEqual([(r["entity"], r["entity_id"]) for r in ev], [("goal", "G-001")])
        self.assertIn("staffed", ev[0]["detail"])

    def test_b_team_new_activates_planning_goal(self):
        self.set_goal("planning")
        self.team_new()
        self.assertEqual(self.goal_status(), "active")
        self.assertEqual(len(self.events("goal-auto-active")), 1)

    def test_c_terminal_and_blocked_goals_unchanged(self):
        for status in ("done", "closed", "cancelled", "blocked"):
            with self.subTest(status=status):
                self.set_goal(status)
                self.team_new()
                self.assertEqual(self.goal_status(), status)
                self.assertEqual(self.events("goal-auto-active"), [])

    def test_d_task_in_progress_activates_planning_goal(self):
        self.set_goal("planning")
        self.env.seed_task(status="todo", pr_url=None)
        self.ok("task", "update", "T-0001", "--status", "in_progress", agent="zz-lead")
        self.assertEqual(self.goal_status(), "active")
        ev = self.events("goal-auto-active")
        self.assertEqual(len(ev), 1)
        self.assertIn("T-0001", ev[0]["detail"])

    def test_d_task_update_does_not_reopen_done_goal(self):
        self.set_goal("done")
        self.env.seed_task(status="todo", pr_url=None)
        self.ok("task", "update", "T-0001", "--status", "in_progress", agent="zz-lead")
        self.assertEqual(self.goal_status(), "done")
        self.assertEqual(self.events("goal-auto-active"), [])

    def test_h_idempotent(self):
        self.set_goal("proposed")
        self.team_new()
        self.team_new()
        self.env.seed_task(status="todo", pr_url=None)
        self.ok("task", "update", "T-0001", "--status", "in_progress", agent="zz-lead")
        self.assertEqual(len(self.events("goal-auto-active")), 1)


class TaskAutoStartTests(LifecycleBase):
    def spawn_coder(self):
        return self.ok("spawn", "coder", "--task", "T-0001", "--name", "zz-coder", agent="zz-lead")

    def test_e_spawn_started_moves_todo_task(self):
        self.env.seed_task(status="todo", pr_url=None, team="g-001-a")
        self.spawn_coder()
        self.assertEqual(self.task_status(), "in_progress")
        ev = self.events("task-auto-start")
        self.assertEqual([(r["entity"], r["entity_id"]) for r in ev], [("task", "T-0001")])
        self.assertIn("zz-coder", ev[0]["detail"])

    def test_e_spawn_activates_planning_goal(self):
        self.set_goal("planning")
        self.env.seed_task(status="todo", pr_url=None, team="g-001-a")
        self.spawn_coder()
        self.assertEqual(self.goal_status(), "active")
        self.assertEqual(len(self.events("goal-auto-active")), 1)

    def test_f_queued_spawn_leaves_todo_until_tick_starts_it(self):
        self.env.seed_task(status="todo", pr_url=None, team="g-001-a")
        self.env.set_herdr(fail=True)
        self.assertIn("QUEUED", self.spawn_coder().stdout)
        self.assertEqual(self.task_status(), "todo")
        self.assertEqual(self.events("task-auto-start"), [])
        self.env.set_herdr(fail=False)
        self.ok("tick")
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-coder'")[0]["status"], "live")
        self.assertEqual(self.task_status(), "in_progress")
        self.assertEqual(len(self.events("task-auto-start")), 1)

    def test_g_reviewer_on_in_review_task_stays_in_review(self):
        self.env.seed_task(status="in_review", team="g-001-a")
        self.ok("spawn", "reviewer", "--task", "T-0001", "--name", "zz-rev", agent="zz-lead")
        self.assertEqual(self.task_status(), "in_review")
        self.assertEqual(self.events("task-auto-start"), [])

    def test_g_blocked_done_cancelled_tasks_untouched(self):
        for i, status in enumerate(("blocked", "done", "cancelled"), start=2):
            with self.subTest(status=status):
                tid = f"T-000{i}"
                self.env.seed_task(tid, status=status, pr_url=None, team="g-001-a")
                self.ok("spawn", "coder", "--task", tid, "--name", f"zz-c{i}", agent="zz-lead")
                self.assertEqual(self.task_status(tid), status)
        self.assertEqual(self.events("task-auto-start"), [])

    def test_i_non_worker_spawned_next_to_a_todo_task_does_not_start_it(self):
        self.env.seed_task(status="todo", pr_url=None, team="g-001-a")
        self.ok("spawn", "lead", "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001", "--name", "zz-lead2", agent="zz-director")
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-lead2'")[0]["status"], "live")
        self.assertEqual(self.task_status(), "todo")
        self.assertEqual(self.events("task-auto-start"), [])

    def test_h_second_spawn_records_no_second_event(self):
        self.env.seed_task(status="todo", pr_url=None, team="g-001-a")
        self.spawn_coder()
        self.ok("spawn", "qa", "--task", "T-0001", "--name", "zz-qa", agent="zz-lead")
        self.assertEqual(self.task_status(), "in_progress")
        self.assertEqual(len(self.events("task-auto-start")), 1)


if __name__ == "__main__":
    unittest.main()
