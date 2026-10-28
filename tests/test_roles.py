import unittest

from harness import VALID_PR_BODY, FactoryTestCase


class RoleTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_task("T-0001", role="coder", status="in_progress", team="g-001-a")
        e.seed_task("T-0002", role="pm", status="in_progress", pr_url=None, team="g-001-a")
        for name, role in (("zz-test-coder", "coder"), ("zz-test-lead", "lead"), ("zz-test-boss", "boss"),
                           ("zz-test-director", "director"), ("zz-test-qa", "qa"), ("zz-test-reviewer", "reviewer"),
                           ("zz-test-judge", "judge"), ("zz-test-pm", "pm"), ("zz-test-cleaner", "cleaner")):
            e.seed_agent(name, role, goal="G-001", team="g-001-a", task="T-0001")

    def test_roles_coder_cannot_goal_new(self):
        p = self.refused(3, "goal", "new", "--title", "zz-test", agent="zz-test-coder")
        self.assertIn("only the boss creates goals", p.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM goals")[0]["c"], 1)

    def test_roles_coder_cannot_set_config(self):
        self.refused(3, "config", "max_live_agents", "99", agent="zz-test-coder")
        self.assertEqual(self.env.sql("SELECT value FROM config WHERE key='max_live_agents'")[0]["value"], "6")

    def test_roles_coder_can_read_config(self):
        self.ok("config", agent="zz-test-coder")

    def test_roles_director_can_set_config(self):
        self.ok("config", "max_live_agents", "7", agent="zz-test-director")
        self.assertEqual(self.env.sql("SELECT value FROM config WHERE key='max_live_agents'")[0]["value"], "7")

    def test_roles_lead_cannot_spawn_lead_or_director(self):
        for role in ("lead", "director", "boss"):
            self.refused(3, "spawn", role, "--name", "zz-test-x", agent="zz-test-lead")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM agents WHERE name='zz-test-x'")[0]["c"], 0)

    def test_roles_lead_can_spawn_worker(self):
        p = self.ok("spawn", "pm", "--name", "zz-test-pm2", agent="zz-test-lead")
        self.assertIn("SPAWNED zz-test-pm2", p.stdout)

    def test_roles_boss_can_only_spawn_director(self):
        for role in ("coder", "lead", "pm", "judge"):
            self.refused(3, "spawn", role, "--name", "zz-test-x", agent="zz-test-boss")
        p = self.ok("spawn", "director", "--name", "zz-test-director2", agent="zz-test-boss")
        self.assertIn("SPAWNED zz-test-director2", p.stdout)

    def test_roles_workers_cannot_spawn(self):
        for who in ("zz-test-coder", "zz-test-qa", "zz-test-reviewer", "zz-test-judge", "zz-test-pm"):
            self.refused(3, "spawn", "coder", "--name", "zz-test-x", agent=who)

    def test_roles_qa_reviewer_cannot_mark_task_done(self):
        for who in ("zz-test-qa", "zz-test-reviewer"):
            self.refused(3, "task", "update", "T-0002", "--status", "done", agent=who)
            self.refused(3, "task", "update", "T-0001", "--status", "done", agent=who)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "in_progress")

    def test_roles_qa_can_mark_blocked(self):
        self.ok("task", "update", "T-0001", "--status", "blocked", agent="zz-test-qa")

    def test_roles_coder_cannot_mark_coder_task_done(self):
        p = self.refused(3, "task", "update", "T-0001", "--status", "done", agent="zz-test-coder")
        self.assertIn("judge merges", p.stderr)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "in_progress")

    def test_roles_coder_can_mark_in_review(self):
        self.env.seed_pr(url="https://x/pull/9", body=VALID_PR_BODY)
        self.ok("task", "update", "T-0001", "--status", "in_review", "--pr", "https://x/pull/9", agent="zz-test-coder")
        self.assertEqual(self.env.sql("SELECT status,pr_url FROM tasks WHERE id='T-0001'")[0]["status"], "in_review")

    def test_roles_owner_can_mark_coder_task_done(self):
        self.ok("task", "update", "T-0001", "--status", "done")

    def test_roles_worker_cannot_send_escalation(self):
        for who in ("zz-test-coder", "zz-test-qa", "zz-test-reviewer", "zz-test-pm", "zz-test-judge"):
            self.refused(3, "msg", "director", "help", "x", "--kind", "escalation", agent=who)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages")[0]["c"], 0)

    def test_roles_lead_can_send_escalation(self):
        self.env.seed_agent("zz-test-director-live", "director")
        self.ok("msg", "director", "help", "x", "--kind", "escalation", agent="zz-test-lead")

    def test_roles_unknown_agent_refused(self):
        for args in (("goal", "new", "--title", "zz-test"), ("config", "git_push", "1"),
                     ("spawn", "coder", "--name", "zz-test-x"), ("plan", "G-001", "body"),
                     ("task", "add", "--goal", "G-001", "--title", "t"), ("verdict", "T-0001", "--pass", "--summary", "s"),
                     ("judge", "T-0001", "--merge", "--reason", "r"), ("eval", "g-001-a", "--score", "3")):
            self.refused(3, *args, agent="zz-test-ghost")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM goals")[0]["c"], 1)

    def test_roles_unknown_agent_cannot_escalate(self):
        self.refused(3, "msg", "director", "x", "y", "--kind", "escalation", agent="zz-test-ghost")

    def test_roles_coder_cannot_verdict(self):
        self.env.seed_pr()
        self.refused(3, "verdict", "T-0001", "--pass", "--summary", "lgtm", agent="zz-test-coder")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM verdicts")[0]["c"], 0)

    def test_roles_reviewer_cannot_judge(self):
        self.env.seed_pr()
        for who in ("zz-test-reviewer", "zz-test-qa", "zz-test-coder", "zz-test-lead"):
            self.refused(3, "judge", "T-0001", "--merge", "--reason", "r", agent=who)
        self.assertEqual([c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "merge"]], [])

    def test_roles_pm_can_plan_coder_and_qa_cannot(self):
        self.ok("plan", "G-001", "a plan", agent="zz-test-pm")
        self.refused(3, "plan", "G-001", "a plan", agent="zz-test-coder")
        self.refused(3, "plan", "G-001", "a plan", agent="zz-test-qa")


if __name__ == "__main__":
    unittest.main()
