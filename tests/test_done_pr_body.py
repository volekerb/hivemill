import unittest

from harness import PR_URL, FactoryTestCase, pr_body


class DonePrBodyTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_task(status="in_review", team="g-001-a")
        e.seed_agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        e.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", task="T-0001")

    def agent_status(self, name="zz-test-coder"):
        return self.env.sql("SELECT status FROM agents WHERE name=?", (name,))[0]["status"]

    def untouched(self):
        self.assertEqual(self.agent_status(), "live")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE sender='zz-test-coder'")[0]["c"], 0)

    def test_overlong_body_blocks_done(self):
        self.env.seed_pr(url=PR_URL, body=pr_body(words=400))
        p = self.refused(3, "done", "x", agent="zz-test-coder")
        self.assertIn("words outside <details>", p.stderr)
        self.assertIn("pre-pr-check", p.stderr)
        self.untouched()

    def test_force_does_not_bypass(self):
        self.env.seed_pr(url=PR_URL, body=pr_body(words=400))
        p = self.refused(3, "done", "--force", "x", agent="zz-test-coder")
        self.assertIn("words outside <details>", p.stderr)
        self.untouched()

    def test_gh_failure_fails_closed(self):
        p = self.env.run("done", "x", agent="zz-test-coder")  # the stub has no such PR: gh exits 1
        self.assertNotEqual(p.returncode, 0, p.stdout)
        self.assertIn("cannot read the body", p.stderr)
        self.untouched()

    def test_conforming_body_allows_done(self):
        self.env.seed_pr(url=PR_URL, body=pr_body())
        self.ok("done", "x", agent="zz-test-coder")
        self.assertEqual(self.agent_status(), "done")

    def test_in_progress_task_not_checked(self):
        self.env.sql("UPDATE tasks SET status='in_progress' WHERE id='T-0001'")
        self.env.seed_pr(url=PR_URL, body=pr_body(words=400))
        self.ok("done", "x", agent="zz-test-coder")
        self.assertEqual(self.agent_status(), "done")

    def test_non_coder_not_checked(self):
        self.env.seed_agent("zz-test-qa", "qa", goal="G-001", team="g-001-a", task="T-0001")
        self.env.seed_pr(url=PR_URL, body=pr_body(words=400))
        self.ok("done", "x", agent="zz-test-qa")
        self.assertEqual(self.agent_status("zz-test-qa"), "done")


if __name__ == "__main__":
    unittest.main()
