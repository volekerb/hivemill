import unittest

from harness import FactoryTestCase


class TaskNoteIdTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_task(status="todo", pr_url=None)

    def notes(self):
        return self.env.sql(
            "SELECT detail FROM events WHERE entity='task' AND entity_id='T-0001' AND action='note'"
        )

    def refuse(self, *args):
        before = len(self.notes())
        p = self.env.run("task", "note", *args)
        self.assertNotEqual(p.returncode, 0, p.stdout)
        self.assertNotIn("noted", p.stdout)
        self.assertEqual(len(self.notes()), before, p.stdout + p.stderr)

    def test_two_task_ids_refused(self):
        self.refuse("T-0001", "T-0002")

    def test_unknown_task_refused(self):
        self.refuse("T-9999", "hi")

    def test_spaced_id_refused(self):
        self.refuse("T-0931 T-0932", "hi")

    def test_extra_argument_refused(self):
        self.refuse("T-0001", "ok", "extra")

    def test_garbage_id_refused(self):
        self.refuse("not-a-task", "hi")

    def test_real_note_still_works(self):
        self.ok("task", "note", "T-0001", "inline note")
        self.assertEqual(self.notes()[-1][0], "inline note")


if __name__ == "__main__":
    unittest.main()
