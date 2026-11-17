import unittest

from taskdeps import resolve_order, ready_tasks


class ResolveOrder(unittest.TestCase):
    def test_simple_chain(self):
        tasks = {"T-3": ["T-2"], "T-2": ["T-1"], "T-1": []}
        self.assertEqual(resolve_order(tasks), ["T-1", "T-2", "T-3"])

    def test_duplicate_deps_are_harmless(self):
        tasks = {"T-2": ["T-1", "T-1"], "T-1": []}
        self.assertEqual(resolve_order(tasks), ["T-1", "T-2"])

    def test_unknown_dependency_is_an_error(self):
        with self.assertRaises(ValueError) as cm:
            resolve_order({"T-1": ["T-9"]})
        self.assertIn("unknown dependency T-9 for T-1", str(cm.exception))


class Ready(unittest.TestCase):
    def test_ready(self):
        tasks = {"T-1": [], "T-2": ["T-1"], "T-3": ["T-2"]}
        self.assertEqual(ready_tasks(tasks, ["T-1"]), ["T-2"])


if __name__ == "__main__":
    unittest.main()
