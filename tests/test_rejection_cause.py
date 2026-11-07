"""T-1175: rejection and gate-refusal incidents require a --cause; /quality reads the counts."""
import sqlite3
import unittest

from harness import PR_URL, FactoryTestCase, gates_summary


class RejectionCauseTests(FactoryTestCase):
    INPROC = True

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal()
        e.seed_task()
        e.seed_pr()
        e.seed_agent("zz-test-qa", "qa", goal="G-001", task="T-0001")
        e.seed_agent("zz-test-reviewer", "reviewer", goal="G-001", task="T-0001")
        e.seed_agent("zz-test-judge", "judge", goal="G-001", task="T-0001", status="exited")

    def test_verdict_fail_without_cause_refused(self):
        before = self.env.sql("SELECT count(*) c FROM verdicts")[0]["c"]
        p = self.refused(2, "verdict", "T-0001", "--fail", "--summary", "bad", agent="zz-test-qa")
        self.assertIn("pass --cause", p.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM verdicts")[0]["c"], before)

    def test_verdict_fail_unknown_cause_refused(self):
        before = self.env.sql("SELECT count(*) c FROM rejection_incidents")[0]["c"]
        p = self.refused(2, "verdict", "T-0001", "--fail", "--cause", "not_a_real_cause", "--summary", "bad",
                        agent="zz-test-reviewer")
        self.assertIn("unknown rejection cause", p.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM rejection_incidents")[0]["c"], before)

    def test_verdict_fail_with_cause_records_incident(self):
        p = self.ok("verdict", "T-0001", "--fail", "--cause", "bug", "--summary", "bad", agent="zz-test-qa")
        self.assertIn("FAIL", p.stdout)
        rows = [{k: r[k] for k in ("cause", "kind", "task_id")} for r in self.env.sql("SELECT cause, kind, task_id FROM rejection_incidents")]
        self.assertEqual(rows, [{"cause": "bug", "kind": "qa", "task_id": "T-0001"}])

    def test_judge_reject_without_cause_refused(self):
        for role, agent in (("reviewer", "zz-test-reviewer"), ("qa", "zz-test-qa")):
            self.ok("verdict", "T-0001", "--pass", "--summary", gates_summary(), agent=agent)
        before = self.env.sql("SELECT count(*) c FROM verdicts WHERE role='judge'")[0]["c"]
        p = self.refused(2, "judge", "T-0001", "--reject", "--reason", "nope", agent="zz-test-judge")
        self.assertIn("pass --cause", p.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM verdicts WHERE role='judge'")[0]["c"], before)

    def test_gate_refusal_records_incident(self):
        for role, agent in (("reviewer", "zz-test-reviewer"), ("qa", "zz-test-qa")):
            self.ok("verdict", "T-0001", "--pass", "--summary", gates_summary(), agent=agent)
        self.env.seed_pr(checks=[{"name": "build", "status": "COMPLETED", "conclusion": "FAILURE"}])
        self.refused(4, "judge", "T-0001", "--merge", "--reason", "ok", agent="zz-test-judge")
        rows = self.env.sql("SELECT cause, kind FROM rejection_incidents WHERE kind='gate'")
        self.assertEqual(len(rows), 1)
        self.assertIn(rows[0]["cause"], ("missing_test", "gate_refusal", "stale_or_conflicting_branch"))

    def test_rejection_cause_counts(self):
        with self.env.load_factory() as mod:
            con = sqlite3.connect(self.env.db_path)
            con.row_factory = sqlite3.Row
            self.addCleanup(con.close)
            mod.record_rejection_incident(con, "bug", "qa", "T-0001")
            mod.record_rejection_incident(con, "bug", "reviewer", "T-0002")
            mod.record_rejection_incident(con, "other", "gate", "T-0003")
            con.commit()
            self.assertEqual(mod.rejection_cause_counts(con), {"bug": 2, "other": 1})

if __name__ == "__main__":
    unittest.main()
