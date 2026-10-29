import unittest

from harness import FactoryTestCase


class TaskAddScTagTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()

    def add(self, title):
        return self.ok("task", "add", "--goal", "G-001", "--title", title, "--spec", "s")

    def criteria(self, text):
        self.env.sql("UPDATE goals SET success_criteria=? WHERE id='G-001'", (text,))

    def test_untagged_title_warns_on_stderr_and_still_files(self):
        self.criteria("1. a\n2. b")
        p = self.add("Build it")
        self.assertIn("title has no [SCn] tag", p.stderr)
        self.assertEqual(p.stdout.strip(), "T-0001")
        self.assertEqual(len(self.env.sql("SELECT id FROM tasks")), 1)

    def test_tagged_title_is_quiet(self):
        self.criteria("1. a\n2. b")
        for title in ("Build it [SC1]", "Build it [SC1,SC2]"):
            self.assertEqual(self.add(title).stderr, "")

    def test_goal_without_success_criteria_is_quiet(self):
        self.assertEqual(self.add("Build it").stderr, "")
        self.criteria("  ")
        self.assertEqual(self.add("Build it too").stderr, "")


if __name__ == "__main__":
    unittest.main()
