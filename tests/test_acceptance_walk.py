import time
import unittest

from harness import VALID_PR_BODY, FactoryTestCase

URL = "https://github.com/o/r/pull/7"


class AcceptanceWalkTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="lead-g-001-a")
        e.seed_task("T-0001", status="in_progress", pr_url=None, team="g-001-a")
        for name, role in (("coder-t-0001", "coder"), ("lead-g-001-a", "lead")):
            e.seed_agent(name, role, goal="G-001", team="g-001-a", task="T-0001")

    def status(self):
        return self.env.sql("SELECT status, pr_url FROM tasks WHERE id='T-0001'")[0]

    def in_review(self, agent="coder-t-0001", *extra):
        return self.env.run("task", "update", "T-0001", "--status", "in_review", "--pr", URL, *extra, agent=agent)

    def test_refused_without_heading(self):
        self.env.seed_pr(url=URL, body="summary\nAcceptance walk\n### Acceptance walk\ninline ## Acceptance walk")
        p = self.in_review()
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("pre-pr-check", p.stderr)
        self.assertIn("## Acceptance walk", p.stderr)
        self.assertEqual(self.status()["status"], "in_progress")
        self.assertIsNone(self.status()["pr_url"])

    def test_accepted_with_heading(self):
        self.env.seed_pr(url=URL, body=VALID_PR_BODY)
        p = self.in_review()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.status()["status"], "in_review")

    def test_gh_failure_refused(self):
        p = self.in_review()  # the stub has no such PR: gh exits 1
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("retry", p.stderr)
        self.assertEqual(self.status()["status"], "in_progress")

    def test_task_pr_field_used_without_flag(self):
        self.env.sql("UPDATE tasks SET pr_url=? WHERE id='T-0001'", (URL,))
        self.env.seed_pr(url=URL, body="no walk")
        p = self.env.run("task", "update", "T-0001", "--status", "in_review", agent="coder-t-0001")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.env.seed_pr(url=URL, body=VALID_PR_BODY)
        self.assertEqual(self.env.run("task", "update", "T-0001", "--status", "in_review", agent="coder-t-0001").returncode, 0)

    def test_no_pr_at_all_refused(self):
        started = time.monotonic()
        p = self.env.run("task", "update", "T-0001", "--status", "in_review", agent="coder-t-0001")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("--pr", p.stderr)
        self.assertEqual(self.env.calls("gh"), [], "no PR to look at: gh must not be called at all")
        self.assertLess(time.monotonic() - started, 5)  # T-0182: it was reported as hanging; it is one local refusal

    def test_non_coder_and_other_statuses_unchanged(self):
        p = self.in_review(agent="lead-g-001-a")  # no body, no heading: a lead is not checked
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.status()["status"], "in_review")
        p = self.env.run("task", "update", "T-0001", "--status", "blocked", agent="coder-t-0001")
        self.assertEqual(p.returncode, 0, p.stderr)


if __name__ == "__main__":
    unittest.main()
