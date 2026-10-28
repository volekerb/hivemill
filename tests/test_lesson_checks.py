import unittest

from harness import FactoryTestCase

WORKERS = ("pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer")


class LessonCheckTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_task("T-0001", role="coder", status="in_progress", pr_url=None, team="g-001-a")
        e.sql("UPDATE tasks SET spec='old spec', acceptance='old acc' WHERE id='T-0001'")
        e.seed_agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        e.seed_agent("zz-test-lead2", "lead", goal="G-001", team="g-001-b")
        e.seed_agent("zz-test-boss", "boss")
        e.seed_agent("zz-test-director", "director")

    def task(self, col="spec"):
        return self.env.sql(f"SELECT {col} c FROM tasks WHERE id='T-0001'")[0]["c"]

    # -- 1. boss-only goal new ------------------------------------------
    def test_lesson_goal_new_boss_and_owner_ok(self):
        self.ok("goal", "new", "--title", "zz-test b", agent="zz-test-boss")
        self.ok("goal", "new", "--title", "zz-test o")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM goals")[0]["c"], 3)

    def test_lesson_goal_new_refused_for_everyone_else(self):
        for i, role in enumerate(("director", "lead") + WORKERS):
            name = f"zz-test-x{i}"
            self.env.seed_agent(name, role)
            p = self.refused(3, "goal", "new", "--title", "zz-test", agent=name)
            self.assertIn("only the boss creates goals", p.stderr, role)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM goals")[0]["c"], 1)

    # -- 2. no spec edit under a live worker -----------------------------
    def test_lesson_spec_edit_blocked_under_live_worker(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", task="T-0001")
        for flag, col in (("--spec", "spec"), ("--acceptance", "acceptance")):
            p = self.refused(2, "task", "update", "T-0001", flag, "new", agent="zz-test-lead")
            self.assertIn("live worker zz-test-coder", p.stderr)
            self.assertIn("factory stop zz-test-coder", p.stderr)
            self.refused(2, "task", "update", "T-0001", flag, "new")  # the owner too
            self.assertEqual(self.task(col), "old spec" if col == "spec" else "old acc")

    def test_lesson_other_task_fields_ok_under_live_worker(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", task="T-0001")
        self.ok("task", "update", "T-0001", "--title", "zz-test renamed", "--note", "n", agent="zz-test-lead")

    def test_lesson_spec_edit_ok_after_stop_requested(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", task="T-0001")
        self.env.sql("UPDATE agents SET stop_requested_at='t' WHERE name='zz-test-coder'")
        self.ok("task", "update", "T-0001", "--spec", "new", agent="zz-test-lead")
        self.assertEqual(self.task(), "new")

    def test_lesson_spec_edit_ok_for_queued_done_exited_agent(self):
        for st in ("queued", "done", "exited"):
            self.env.seed_agent(f"zz-test-{st}", "coder", goal="G-001", task="T-0001", status=st)
        self.ok("task", "update", "T-0001", "--spec", "new", "--acceptance", "new acc", agent="zz-test-lead")
        self.assertEqual(self.task("acceptance"), "new acc")

    # -- 3. spec-by-msg detection ----------------------------------------
    def events(self):
        return self.env.sql("SELECT * FROM events WHERE action='spec-by-msg'")

    def test_lesson_msg_spec_warns_and_delivers(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", task="T-0001")
        p = self.ok("msg", "zz-test-coder", "update", "new acceptance item: add X", agent="zz-test-lead")
        self.assertIn("zz-test-coder is running T-0001", p.stderr)
        self.assertIn("M", p.stdout)
        ev = self.events()
        self.assertEqual([(e["entity"], e["entity_id"]) for e in ev], [("task", "T-0001")])
        self.assertIn("zz-test-lead->zz-test-coder", ev[0]["detail"])
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE recipient='zz-test-coder'")[0]["c"], 1)

    def test_lesson_msg_without_spec_words_no_warning(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", task="T-0001")
        p = self.ok("msg", "zz-test-coder", "status?", "how is it going", agent="zz-test-lead")
        self.assertNotIn("warning", p.stderr)
        self.assertEqual(self.events(), [])

    def test_lesson_msg_decision_and_lead_to_lead_no_warning(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", task="T-0001")
        p = self.ok("msg", "zz-test-coder", "scope", "acceptance changed", "--kind", "decision", agent="zz-test-lead")
        self.assertNotIn("warning", p.stderr)
        p = self.ok("msg", "zz-test-lead2", "scope", "acceptance changed", agent="zz-test-lead")
        self.assertNotIn("warning", p.stderr)
        self.assertEqual(self.events(), [])

    def test_lesson_msg_to_idle_worker_without_task_no_warning(self):
        """Misleading name, kept: the recipient is a *queued* coder holding T-0001 (not live), so no warning."""
        self.env.seed_agent("zz-test-queued", "coder", goal="G-001", task="T-0001", status="queued")
        p = self.ok("msg", "zz-test-queued", "spec", "new spec", agent="zz-test-lead")
        self.assertNotIn("warning", p.stderr)

    # -- 4. spawn needs the spec in the DB -------------------------------
    def starts(self):
        return [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]

    def spawn(self, role, task="T-0001", name="zz-test-s"):
        return self.env.run("spawn", role, "--goal", "G-001", "--task", task, "--name", name, agent="zz-test-director")

    def test_lesson_spawn_empty_spec_refused(self):
        self.env.sql("UPDATE tasks SET spec='' WHERE id='T-0001'")
        p = self.spawn("coder")
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("has no spec in the DB", p.stderr)
        self.assertEqual(self.starts(), [])
        self.assertEqual(self.env.sql("SELECT count(*) c FROM agents WHERE name='zz-test-s'")[0]["c"], 0)

    def test_lesson_spawn_empty_acceptance_refused_for_coder_and_qa(self):
        self.env.sql("UPDATE tasks SET acceptance=NULL WHERE id='T-0001'")
        for role in ("coder", "qa"):
            p = self.spawn(role)
            self.assertEqual(p.returncode, 2, p.stderr)
            self.assertIn("no acceptance", p.stderr)
        self.assertEqual(self.starts(), [])

    def test_lesson_spawn_pm_without_acceptance_ok(self):
        self.env.seed_task("T-0002", role="pm", status="todo", pr_url=None, acceptance=None)
        p = self.spawn("pm", task="T-0002")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(len(self.starts()), 1)

    def test_lesson_spawn_unknown_task_refused(self):
        p = self.spawn("coder", task="T-9999")
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertEqual(self.starts(), [])


if __name__ == "__main__":
    unittest.main()
