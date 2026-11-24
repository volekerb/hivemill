import unittest

from taskdeps import resolve_order, format_order


class Hidden(unittest.TestCase):
    def test_unknown_dep_message_and_no_leak(self):
        with self.assertRaises(ValueError) as cm:
            resolve_order({"T-1": [], "T-2": ["T-1", "T-7"]})
        self.assertIn("unknown dependency T-7 for T-2", str(cm.exception))

    def test_cycle_is_an_error(self):
        with self.assertRaises(ValueError) as cm:
            resolve_order({"T-1": ["T-2"], "T-2": ["T-1"]})
        self.assertIn("dependency cycle", str(cm.exception))
        self.assertIn("T-1", str(cm.exception))
        self.assertIn("T-2", str(cm.exception))

    def test_self_dependency_is_a_cycle(self):
        with self.assertRaises(ValueError):
            resolve_order({"T-1": ["T-1"]})

    def test_long_cycle_behind_valid_tasks(self):
        tasks = {"T-0": [], "T-1": ["T-0", "T-3"], "T-2": ["T-1"], "T-3": ["T-2"]}
        with self.assertRaises(ValueError):
            resolve_order(tasks)

    def test_diamond_each_task_once(self):
        tasks = {"T-4": ["T-2", "T-3"], "T-3": ["T-1"], "T-2": ["T-1"], "T-1": []}
        self.assertEqual(resolve_order(tasks), ["T-1", "T-2", "T-3", "T-4"])

    def test_format_order(self):
        self.assertEqual(format_order({"T-2": ["T-1"], "T-1": []}),
                         "1. T-1\n2. T-2 (after T-1)")


if __name__ == "__main__":
    unittest.main()
