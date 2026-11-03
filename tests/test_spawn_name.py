"""T-0872: `factory spawn --name X` never renames silently. With the org prefix on, a bare X is refused with the rule;
an already prefixed X is honoured exactly; without --name the default org-prefixed name is unchanged. Subprocess, temp home."""
import unittest

from harness import FactoryTestCase


class SpawnName(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team("g-001-a", lead="zz-lead")
        self.env.seed_agent("zz-director", "director")
        self.ok("config", "org_prefix_default", "1")

    def spawn(self, *extra):
        return self.env.run("spawn", "pm", "--goal", "G-001", "--team", "g-001-a", *extra, agent="zz-director")

    def names(self):
        return [r["name"] for r in self.env.sql("SELECT name FROM agents WHERE role='pm'")]

    def test_bare_name_refused_with_rule(self):
        p = self.spawn("--name", "pm-x")
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("claude-pm-x", p.stdout + p.stderr)
        self.assertIn("org naming rule", p.stdout + p.stderr)
        self.assertEqual(self.names(), [])

    def test_prefixed_name_honoured_exactly(self):
        p = self.spawn("--name", "claude-pm-x")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.names(), ["claude-pm-x"])

    def test_no_name_keeps_default_prefixed_name(self):
        p = self.spawn()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.names(), ["claude-pm-g-001-a"])

    def test_help_documents_the_rule(self):
        p = self.env.run("spawn", "--help", agent="zz-director")
        self.assertIn("else refused", " ".join((p.stdout + p.stderr).split()))


if __name__ == "__main__":
    unittest.main()
