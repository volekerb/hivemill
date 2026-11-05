import unittest

from harness import FactoryTestCase


class TaskNoteEmptyTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_task(status="todo", pr_url=None)

    def notes(self):
        return self.env.sql("SELECT detail FROM events WHERE entity='task' AND entity_id='T-0001' AND action='note'")

    def refuse(self, text, stdin=None):
        before = len(self.notes())
        p = self.env.run("task", "note", "T-0001", text, stdin=stdin)
        self.assertNotEqual(p.returncode, 0, p.stdout)
        self.assertIn("empty", p.stderr)
        self.assertEqual(len(self.notes()), before)

    def test_empty_argument_refused(self):
        self.refuse("")

    def test_whitespace_argument_refused(self):
        self.refuse("  \n ")

    def test_empty_stdin_refused(self):
        self.refuse("-", stdin="")

    def test_whitespace_stdin_refused(self):
        self.refuse("-", stdin="\n  \n")

    def test_real_note_still_works(self):
        self.ok("task", "note", "T-0001", "inline note")
        self.ok("task", "note", "T-0001", "-", stdin="stdin note\n")
        self.assertEqual([r[0] for r in self.notes()][-2:], ["inline note", "stdin note"])


if __name__ == "__main__":
    unittest.main()
