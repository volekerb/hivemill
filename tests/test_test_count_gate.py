import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "ci"))
import test_count_gate as gate  # noqa: E402

SCRIPT = ROOT / "ci" / "test_count_gate.py"


def make_tree(root, body):
    d = Path(root) / "tests"
    d.mkdir()
    (d / "test_x.py").write_text(body)
    return root


THREE = (
    "import unittest\n"
    "class T(unittest.TestCase):\n"
    "    def test_a(self): pass\n"
    "    def test_b(self): pass\n"
    "    def test_c(self): pass\n"
)


class DecideTests(unittest.TestCase):
    def test_equal_passes(self):
        ok, msg = gate.decide(5, 5, "")
        self.assertTrue(ok)
        self.assertIn("base=5 pr=5", msg)

    def test_higher_passes(self):
        self.assertTrue(gate.decide(5, 9, "")[0])

    def test_lower_without_marker_fails(self):
        ok, msg = gate.decide(5, 4, "just a body")
        self.assertFalse(ok)
        self.assertIn("base=5 pr=4", msg)
        self.assertIn("test count dropped from 5 to 4; add 'tests-removed: <reason>' to the PR body if intended", msg)

    def test_lower_with_marker_passes(self):
        ok, msg = gate.decide(5, 4, "text\ntests-removed: refactor\nmore")
        self.assertTrue(ok)
        self.assertIn("test count dropped from 5 to 4; allowed by tests-removed: refactor", msg)

    def test_empty_marker_fails(self):
        self.assertFalse(gate.decide(5, 4, "tests-removed:")[0])
        self.assertFalse(gate.decide(5, 4, "tests-removed:   \nnext line")[0])

    def test_case_insensitive_marker(self):
        self.assertTrue(gate.decide(5, 4, "Tests-Removed: x")[0])

    def test_mid_line_marker_fails(self):
        self.assertFalse(gate.decide(5, 4, "see tests-removed: x in prose")[0])

    def test_none_body(self):
        self.assertFalse(gate.decide(5, 4, None)[0])


class CountTests(unittest.TestCase):
    def test_three_tests(self):
        with tempfile.TemporaryDirectory() as d:
            make_tree(d, THREE)
            self.assertEqual(gate.count_tests(d), 3)

    def test_import_error_is_load_error(self):
        with tempfile.TemporaryDirectory() as d:
            make_tree(d, "import no_such_module_zz\n")
            with self.assertRaises(gate.LoadError):
                gate.count_tests(d)

    def test_no_tests_dir_is_zero(self):
        with tempfile.TemporaryDirectory() as d:
            self.assertEqual(gate.count_tests(d), 0)

    def test_state_restored(self):
        before = (list(sys.path), set(sys.modules))
        with tempfile.TemporaryDirectory() as d:
            make_tree(d, THREE)
            gate.count_tests(d)
        self.assertEqual(list(sys.path), before[0])
        self.assertEqual(set(sys.modules), before[1])


class CliTests(unittest.TestCase):
    def run_cli(self, *args):
        return subprocess.run([sys.executable, str(SCRIPT), *args], capture_output=True, text=True)

    def test_count_prints_int(self):
        with tempfile.TemporaryDirectory() as d:
            make_tree(d, THREE)
            r = self.run_cli("count", d)
        self.assertEqual((r.returncode, r.stdout.strip()), (0, "3"))

    def test_count_load_error_exits_2(self):
        with tempfile.TemporaryDirectory() as d:
            make_tree(d, "import no_such_module_zz\n")
            r = self.run_cli("count", d)
        self.assertEqual(r.returncode, 2)
        self.assertIn("discovery failed", r.stderr)

    def test_check_exit_codes(self):
        with tempfile.TemporaryDirectory() as d:
            f = Path(d) / "body.txt"
            f.write_text("nothing")
            r = self.run_cli("check", "--base", "5", "--pr", "4", "--body-file", str(f))
            self.assertEqual(r.returncode, 1)
            self.assertIn("test count dropped from 5 to 4", r.stdout)
            f.write_text("tests-removed: because")
            r = self.run_cli("check", "--base", "5", "--pr", "4", "--body-file", str(f))
            self.assertEqual(r.returncode, 0)
            self.assertIn("allowed by tests-removed: because", r.stdout)
            r = self.run_cli("check", "--base", "5", "--pr", "5", "--body-file", str(f))
            self.assertEqual(r.returncode, 0)

    def test_check_missing_body_file_is_empty_body(self):
        r = self.run_cli("check", "--base", "5", "--pr", "4", "--body-file", "/nonexistent/zz")
        self.assertEqual(r.returncode, 1)


if __name__ == "__main__":
    unittest.main()
