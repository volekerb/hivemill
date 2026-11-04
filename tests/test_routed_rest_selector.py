"""G-016 T-0355: ci/guard_impl_unittest.py --routed-rest lists the hook_run-routed modules the guard-diff set does not run."""
import io
import os
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TOOL = os.path.join(ROOT, "ci", "guard_impl_unittest.py")
WF = os.path.join(ROOT, ".github", "workflows", "tests.yml")
# stems, .py appended at runtime (the coverage test greps test sources for the hook path spelling)
DIFF_SET = [s + ".py" for s in ("test_worker_guard", "test_priv_guard", "test_guard_diff", "test_worker_guard_public_text")]


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
        self.assertIn("needs: [unittest-shard, go, guard-unittest, guard-diff, guard-equiv]", text)
        self.assertIn("needs.guard-equiv.result", text)
        self.assertIn("if: ${{ !cancelled() && steps.paths.outputs.run == 'true' }}", job)  # REAL_GO logs after a red step

    def test_guard_equiv_runs_on_every_pr(self):
        with open(WF, encoding="utf-8") as f:
            text = f.read()
        on = text.split("\non:\n", 1)[1].split("\n\n", 1)[0]
        self.assertNotIn("paths", on)
        job = text.split("  guard-equiv:\n", 1)[1].split("\n  unittest:\n", 1)[0]
        head = job.split("    steps:\n", 1)[0]
        self.assertNotRegex(head, r"(?m)^    (if|needs):")
        self.assertIn('echo "run=false"', job)  # non-guard paths skip the steps, the job still succeeds

    def test_unknown_selector_flag_is_refused(self):
        self.assertNotEqual(run("list", "--bogus").returncode, 0)


def _mod(d, name, body):
    with open(os.path.join(d, name), "w", encoding="utf-8") as f:
        f.write("import unittest\nclass T(unittest.TestCase):\n" + body + "\n")


PASS = "    def test_ok(self):\n        pass\n"
FAIL = "    def test_bad(self):\n        self.fail('x')\n    def test_ok(self):\n        pass\n"


class RoutedRestRunnerTest(unittest.TestCase):
    def go(self, mods):
        sys.path.insert(0, os.path.join(ROOT, "ci"))
        try:
            import guard_impl_unittest as g
        finally:
            sys.path.pop(0)
        with tempfile.TemporaryDirectory() as d:
            for n, b in mods:
                _mod(d, n, b)
            out = io.StringIO()
            rc = g.run_routed_rest(out=out, names=[n for n, _ in mods], tests_dir=d)
        return rc, out.getvalue()

    def test_failing_module_does_not_stop_later_ones(self):
        rc, out = self.go([("test_a.py", FAIL), ("test_b.py", PASS)])
        self.assertEqual(rc, 1)
        self.assertIn("module test_a.py: ran=2 failures=1 errors=0 rc=1", out)
        self.assertIn("module test_b.py: ran=1 failures=0 errors=0 rc=0", out)
        self.assertIn("summary: modules_run=2 modules_failed=1 tests_run=3 tests_failed=1 failed=test_a.py", out)

    def test_first_fails_last_passes_is_still_red(self):
        rc, _ = self.go([("test_a.py", FAIL), ("test_b.py", PASS), ("test_c.py", PASS)])
        self.assertNotEqual(rc, 0)

    def test_all_pass_is_green(self):
        rc, out = self.go([("test_a.py", PASS), ("test_b.py", PASS)])
        self.assertEqual(rc, 0)
        self.assertIn("modules_failed=0", out)
        self.assertIn("failed=-", out)


if __name__ == "__main__":
    unittest.main()
