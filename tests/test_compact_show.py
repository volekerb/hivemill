import unittest

from harness import FactoryTestCase

PLAN = "\n".join(f"plan-body-line-{i}" for i in range(7))


class CompactShowTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.sql("UPDATE goals SET intent='zz-intent full', success_criteria='zz-sc full' WHERE id='G-001'")
        e.seed_team(lead="zz-test-lead")
        for n, r in (("zz-test-coder", "coder"), ("zz-test-qa", "qa"), ("zz-test-lead", "lead"),
                     ("zz-test-director", "director")):
            e.seed_agent(n, r, goal="G-001", team="g-001-a")
        e.seed_task("T-0001", status="in_progress", spec="zz-spec full", acceptance="1. zz-acc full")
        e.seed_task("T-0002", status="todo")
        e.sql("UPDATE tasks SET title='zz-other-task-title' WHERE id='T-0002'")
        for i in range(2):
            e.sql("INSERT INTO verdicts(task_id,role,verdict,summary,author,created) VALUES('T-0001','qa','pass',?,'q',?)",
                  (f"verdict-{i}", f"2026-01-01T00:0{i}:00"))
        ev = [("note", f"note-{i}") for i in range(2)] + [("update", f"other-{i}") for i in range(12)]
        for i, (act, det) in enumerate(ev):
            e.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                  (f"2026-01-02T00:{i:02d}:00", "x", "task", "T-0001", act, det))
        e.sql("INSERT INTO plans(goal_id,version,body,author,created) VALUES('G-001',3,?,'pm-x','2026-01-03')", (PLAN,))
        e.sql("INSERT INTO audits(goal_id,version,verdict,body,author,created) VALUES('G-001',2,'ready','audit-body','pm-x','2026-01-04')")

    def test_task_show_compact_for_worker(self):
        out = self.ok("task", "show", "T-0001", agent="zz-test-coder").stdout
        self.assertIn("zz-spec full", out)
        self.assertIn("1. zz-acc full", out)
        self.assertIn("verdict-0", out)
        self.assertIn("verdict-1", out)
        self.assertIn("note-0", out)
        self.assertIn("note-1", out)
        self.assertEqual([i for i in range(12) if f"other-{i}\n" in out + "\n"], [7, 8, 9, 10, 11])
        self.assertIn("(7 earlier events: factory task show T-0001 --full)", out)

    def test_task_show_full_and_hq_identical(self):
        full = self.ok("task", "show", "T-0001", "--full", agent="zz-test-coder").stdout
        self.assertEqual(self.ok("task", "show", "T-0001", agent="zz-test-lead").stdout, full)
        self.assertEqual(self.ok("task", "show", "T-0001", "--full", agent="zz-test-lead").stdout, full)
        self.assertEqual([i for i in range(12) if f"other-{i}\n" in full + "\n"], list(range(12)))
        self.assertNotIn("earlier events", full)

    def test_task_show_few_events_no_pointer(self):
        self.env.sql("DELETE FROM events WHERE action='update' AND detail IN ('other-0','other-1')")
        out = self.ok("task", "show", "T-0001", agent="zz-test-coder").stdout
        self.assertIn("(5 earlier events", out)
        self.env.sql("DELETE FROM events WHERE action='update' AND CAST(substr(detail,7) AS INT) < 7")
        self.assertNotIn("earlier events", self.ok("task", "show", "T-0001", agent="zz-test-coder").stdout)

    def test_goal_show_compact_for_worker(self):
        out = self.ok("goal", "show", "G-001", agent="zz-test-qa").stdout
        self.assertIn("zz-intent full", out)
        self.assertIn("zz-sc full", out)
        self.assertIn("plan v3 by pm-x 2026-01-03, 7 lines: factory goal show G-001 --full", out)
        self.assertIn("audit v2 ready", out)
        self.assertIn("tasks: 2 (todo 1, in_progress 1, in_review 0, done 0, cancelled 0)", out)
        for absent in ("plan-body-line-0", "audit-body", "## teams", "lead_agent", "zz-other-task-title"):
            self.assertNotIn(absent, out)

    def test_goal_show_compact_counts_other_statuses(self):
        self.env.sql("UPDATE tasks SET status='blocked' WHERE id='T-0002'")
        out = self.ok("goal", "show", "G-001", agent="zz-test-qa").stdout
        self.assertIn("tasks: 2 (todo 0, in_progress 1, in_review 0, done 0, cancelled 0, blocked 1)", out)

    def test_goal_show_full_and_hq_identical(self):
        full = self.ok("goal", "show", "G-001", "--full", agent="zz-test-qa").stdout
        self.assertEqual(self.ok("goal", "show", "G-001", agent="zz-test-director").stdout, full)
        for present in ("plan-body-line-6", "audit-body", "## teams", "zz-other-task-title"):
            self.assertIn(present, full)


if __name__ == "__main__":
    unittest.main()
