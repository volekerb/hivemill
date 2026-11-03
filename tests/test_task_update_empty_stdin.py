import unittest

from harness import FactoryTestCase


class TaskUpdateEmptyStdinTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_task(status="todo", pr_url=None)

    def row(self):
        r = self.env.sql("SELECT status,title,spec,acceptance,updated FROM tasks WHERE id='T-0001'")[0]
        return tuple(r)

    def refuse(self, flag, stdin, *extra):
        before = self.row()
        p = self.env.run("task", "update", "T-0001", *extra, flag, "-", stdin=stdin)
        self.assertNotEqual(p.returncode, 0, p.stdout)
        self.assertIn(flag, p.stderr)
        self.assertEqual(self.row(), before)

    def test_empty_stdin_spec_refused_and_unchanged(self):
        self.refuse("--spec", "")

    def test_empty_stdin_acceptance_refused_and_unchanged(self):
        self.refuse("--acceptance", "")

    def test_whitespace_stdin_refused_for_both_fields(self):
        self.refuse("--spec", "\n  \n")
        self.refuse("--acceptance", "\n  \n")

    def test_refusal_is_whole_call(self):
        self.refuse("--spec", "", "--status", "blocked", "--title", "new")

    def test_nonempty_stdin_and_inline_are_stored(self):
        self.ok("task", "update", "T-0001", "--spec", "-", stdin="from stdin\n")
        self.ok("task", "update", "T-0001", "--acceptance", "-", stdin="1. acc\n")
        r = self.row()
        self.assertEqual((r[2], r[3]), ("from stdin", "1. acc"))
        self.ok("task", "update", "T-0001", "--spec", "inline")
        self.assertEqual(self.row()[2], "inline")


if __name__ == "__main__":
    unittest.main()
