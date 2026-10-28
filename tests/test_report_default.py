import unittest

from harness import FactoryTestCase

WORKERS = ("pm", "coder", "qa", "reviewer", "judge", "cleaner")


class ReportDefaultTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_agent("zz-test-director", "director")
        e.seed_agent("zz-test-boss", "boss")
        e.seed_agent("lead-g-001-a", "lead", goal="G-001", team="g-001-a")
        e.seed_team("g-001-a", "G-001", lead="lead-g-001-a")

    def report(self, agent, *extra):
        p = self.env.run("report", "h", "b", "--goal", "G-001", *extra, agent=agent)
        rows = self.env.sql("SELECT recipient FROM messages WHERE kind='report' ORDER BY id DESC LIMIT 1")
        return p, (rows[0]["recipient"] if rows else None)

    def test_worker_with_team_defaults_to_lead(self):
        for role in WORKERS:
            with self.subTest(role=role):
                name = f"zz-test-{role}"
                self.env.seed_agent(name, role, goal="G-001", team="g-001-a")
                p, rcpt = self.report(name)
                self.assertEqual(p.returncode, 0, p.stderr)
                self.assertIn("report → lead-g-001-a", p.stdout)
                self.assertEqual(rcpt, "lead-g-001-a")

    def test_hq_and_lead_default_unchanged(self):
        for name in ("zz-test-boss", "zz-test-director", "lead-g-001-a"):
            with self.subTest(agent=name):
                p, rcpt = self.report(name)
                self.assertEqual(p.returncode, 0, p.stderr)
                self.assertEqual(rcpt, "zz-test-director")

    def test_owner_default_unchanged(self):
        p, rcpt = self.report("owner")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(rcpt, "zz-test-director")

    def test_explicit_to_honoured(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a")
        self.assertEqual(self.report("zz-test-coder", "--to", "director")[1], "zz-test-director")
        self.assertEqual(self.report("zz-test-coder", "--to", "boss")[1], "zz-test-boss")
        self.assertEqual(self.report("zz-test-director", "--to", "lead:g-001-a")[1], "lead-g-001-a")

    def test_worker_without_team_defaults_to_director(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001")
        p, rcpt = self.report("zz-test-coder")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(rcpt, "zz-test-director")

    def test_worker_team_without_lead_falls_back(self):
        self.env.seed_team("g-001-b", "G-001", lead=None)
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-b")
        p, rcpt = self.report("zz-test-coder")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(rcpt, "zz-test-director")
        self.assertIn("has no lead", p.stderr)

    def test_staff_engineer_defaults_to_director(self):
        self.env.seed_agent("staff-engineer", "staff-engineer")
        p, rcpt = self.report("staff-engineer")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(rcpt, "zz-test-director")

    def test_worker_report_to_staff_engineer_refused_like_msg(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a")
        r = self.env.run("report", "x", "y", "--to", "staff-engineer", agent="zz-test-coder")
        m = self.env.run("msg", "staff-engineer", "x", "y", agent="zz-test-coder")
        self.assertEqual(r.returncode, 3, r.stderr)
        self.assertEqual((r.returncode, r.stderr), (m.returncode, m.stderr))
        self.assertIn("may not message staff-engineer", r.stderr)
        self.assertEqual(self.env.sql("SELECT 1 FROM messages WHERE kind='report'"), [])

    def test_lead_and_director_report_to_staff_engineer_allowed(self):
        for name in ("lead-g-001-a", "zz-test-director"):
            with self.subTest(agent=name):
                p, rcpt = self.report(name, "--to", "staff-engineer")
                self.assertEqual(p.returncode, 0, p.stderr)
                self.assertEqual(rcpt, "staff-engineer")


if __name__ == "__main__":
    unittest.main()
