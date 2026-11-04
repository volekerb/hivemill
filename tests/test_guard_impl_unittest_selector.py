"""G-016 T-0355: ci/guard_impl_unittest.py --routed-rest lists the hook_run-routed modules the guard-diff set does not run."""
import os
import subprocess
import sys
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TOOL = os.path.join(ROOT, "ci", "guard_impl_unittest.py")
WF = os.path.join(ROOT, ".github", "workflows", "tests.yml")
DIFF_SET = ["test_worker_guard.py", "test_priv_guard.py", "test_guard_diff.py"]


def run(*args):
    return subprocess.run([sys.executable, TOOL, *args], capture_output=True, text=True, cwd=ROOT)


class RoutedRestSelectorTest(unittest.TestCase):
    def test_list_routed_rest_is_disjoint_from_the_diff_set(self):
        r = run("list", "--routed-rest")
        self.assertEqual(r.returncode, 0, r.stderr)
        names = r.stdout.split()
        self.assertGreater(len(names), 10)
        self.assertTrue(set(names).isdisjoint(DIFF_SET))
        self.assertIn("test_worker_guard_git.py", names)
        self.assertNotIn("test_guard_build.py", names)  # allowlisted: never runs the hook
        self.assertNotIn("test_guard_impl_coverage.py", names)

    def test_every_listed_module_exists_and_uses_the_helper(self):
        for n in run("list", "--routed-rest").stdout.split():
            path = os.path.join(ROOT, "tests", n)
            self.assertTrue(os.path.isfile(path), n)
            with open(path, encoding="utf-8") as f:
                self.assertIn("guard_impl", f.read(), n)

    def test_modules_routed_rest_gives_dotted_paths(self):
        names = run("list", "--routed-rest").stdout.split()
        mods = run("modules", "--routed-rest").stdout.split()
        self.assertEqual(mods, ["tests." + n[:-3] for n in names])

    def test_default_list_is_unchanged(self):
        self.assertEqual(run("list").stdout.split(), DIFF_SET)
        self.assertEqual(run("modules").stdout.split(), ["tests." + n[:-3] for n in DIFF_SET])

    def test_workflow_names_no_module_and_keeps_the_strict_gate_in_guard_equiv(self):
        with open(WF, encoding="utf-8") as f:
            text = f.read()
        self.assertEqual(text.count("test_worker_guard"), 0)
        job = text.split("  guard-equiv:\n", 1)[1].split("\n  unittest:\n", 1)[0]
        self.assertIn("--impl go", job)
        self.assertIn("--strict", job)
        self.assertNotIn("continue-on-error", job)
        self.assertIn("--routed-rest", job)
        self.assertEqual(text.count("--impl go"), 1)
        self.assertEqual(text.count("--strict"), 1)
        self.assertIn("needs: [unittest-shard, go, guard-diff, guard-equiv]", text)
        self.assertIn("needs.guard-equiv.result", text)

    def test_unknown_selector_flag_is_refused(self):
        self.assertNotEqual(run("list", "--bogus").returncode, 0)


if __name__ == "__main__":
    unittest.main()
