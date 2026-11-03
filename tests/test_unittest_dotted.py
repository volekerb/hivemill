"""`python3 -m unittest tests.<module>` from the repo root and `discover -s tests` (CI) both import test modules.

Both forms run as real subprocesses from the repo root. `-k` selects no test, so only the import is exercised
(a module that fails to import errors out; one that imports reports "NO TESTS RAN").
"""
import subprocess
import sys
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
NO_MATCH = "zz_no_such_test_zz"
# modules that do `import harness` (the top-level import that used to fail with "No module named harness")
HARNESS_MODULES = ("test_digest", "test_web_server", "test_stop")


def unittest_run(*args):
    return subprocess.run([sys.executable, "-m", "unittest", *args, "-k", NO_MATCH], cwd=REPO,
                          capture_output=True, text=True, timeout=120)


class DottedUnittestFormTest(unittest.TestCase):
    def test_dotted_module_imports_harness_from_repo_root(self):
        for mod in HARNESS_MODULES:
            with self.subTest(mod):
                r = unittest_run(f"tests.{mod}")
                self.assertNotIn("No module named", r.stderr, r.stderr)
                self.assertIn("NO TESTS RAN", r.stderr, r.stderr)

    def test_discover_form_still_imports_harness(self):
        for mod in HARNESS_MODULES:
            with self.subTest(mod):
                r = unittest_run("discover", "-s", "tests", "-p", f"{mod}.py")
                self.assertNotIn("No module named", r.stderr, r.stderr)
                self.assertIn("NO TESTS RAN", r.stderr, r.stderr)

    def test_dotted_form_reports_a_missing_module(self):
        r = unittest_run("tests.test_no_such_module_zz")
        self.assertNotEqual(r.returncode, 0)
        self.assertNotIn("NO TESTS RAN", r.stderr)


if __name__ == "__main__":
    unittest.main()
