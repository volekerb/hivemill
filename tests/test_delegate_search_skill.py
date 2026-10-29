import os
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SKILL = os.path.join(ROOT, "roles", "plugins", "coder", "coder-skills", "skills", "delegate-search", "SKILL.md")


class DelegateSearchSkillTests(unittest.TestCase):
    def setUp(self):
        with open(SKILL) as f:
            self.text = f.read()

    def test_exact_call_and_rules(self):
        for needle in ('subagent_type: "Explore"', 'model: "sonnet"', "more than about 3", "At most 1 Explore",
                       "read the cited lines before relying on them", "file:line",
                       "Never delegate edits, tests, git or factory commands", "read it directly"):
            self.assertIn(needle, self.text)

    def test_worked_example_names_real_symbols(self):
        # line numbers drift as bin/factory changes; the symbols the example cites must still exist
        with open(os.path.join(ROOT, "bin", "factory_cli.py")) as f:
            src = f.read()
        self.assertIn('out += ["Drift: "', src)
        self.assertIn("def drift_items(", src)
        with open(os.path.join(ROOT, "tests", "test_drift.py")) as f:
            self.assertIn("Drift: none", f.read())


if __name__ == "__main__":
    unittest.main()
