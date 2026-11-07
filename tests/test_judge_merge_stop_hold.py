"""T-1085: `factory judge --merge` re-reads the judge agent stop flag and task judge hold immediately before `gh pr merge`."""
import unittest

from harness import FactoryTestCase, gates_summary

GREEN = [{"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}]


class JudgeMergeStopHoldTests(FactoryTestCase):
    INPROC = True

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        for name, role in (("zz-test-reviewer", "reviewer"), ("zz-test-qa", "qa"), ("zz-test-judge", "judge")):
            e.seed_agent(name, role, goal="G-001", task="T-0001")
        e.use_git_head()
        e.seed_pr(checks=GREEN)
        self.ok("config", "judge_ci_wait_s", "0")
        self.verdicts()

    def verdicts(self):
        self.ok("verdict", "T-0001", "--pass", "--summary", "ok", agent="zz-test-reviewer")
        self.ok("verdict", "T-0001", "--pass", "--summary", gates_summary(), agent="zz-test-qa")

    def judge(self):
        return self.env.run("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge")

    def merges(self):
        return [c["argv"] for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "merge"]]

    def assert_refused(self, p, text):
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("merge refused", p.stderr)
        self.assertIn(text, p.stderr)
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "in_review")
        ev = self.env.sql("SELECT detail FROM events WHERE action='judge-blocked'")
        self.assertTrue(ev and text in ev[-1]["detail"])

    def test_judge_hold_blocks_merge(self):
        self.ok("task", "update", "T-0001", "--judge-hold", "scope check")
        p = self.judge()
        self.assert_refused(p, "judge hold: scope check")
        self.assertEqual(self.env.sql("SELECT detail FROM events WHERE action='judge-hold'")[-1]["detail"], "scope check")

    def test_judge_agent_stop_requested_blocks_merge(self):
        self.env.sql("UPDATE agents SET stop_requested_at='2026-01-01T00:00:00+00:00' WHERE name='zz-test-judge'")
        p = self.judge()
        self.assert_refused(p, "judge agent zz-test-judge has stop requested")

    def test_judge_agent_stopped_blocks_merge(self):
        self.env.sql("UPDATE agents SET status='stopped', ended='2026-01-01T00:00:00+00:00' WHERE name='zz-test-judge'")
        p = self.judge()
        self.assert_refused(p, "judge agent zz-test-judge is stopped")


if __name__ == "__main__":
    unittest.main()
