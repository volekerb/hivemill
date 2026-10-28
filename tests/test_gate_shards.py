"""T-0515: tests/gate_shards.py, the parallel sharded suite a project's gate_cmd runs. Uses a tiny fake tree whose
ci/shard.py is the real one and whose tests/ holds three stub modules; the real suite is never run here."""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
HELPER = ROOT / "tests" / "gate_shards.py"


def make_tree(fail=False):
    d = Path(tempfile.mkdtemp(prefix="zz-test-gate-shards-"))
    (d / "ci").mkdir()
    (d / "tests").mkdir()
    shutil.copy2(ROOT / "ci" / "shard.py", d / "ci" / "shard.py")
    for n in "abc":
        body = "import unittest\nclass T(unittest.TestCase):\n    def test_one(self): pass\n    def test_two(self): pass\n"
        if fail and n == "b":
            body += "    def test_bad(self): self.fail('boom')\n"
        (d / "tests" / f"test_{n}.py").write_text(body)
    return d


def run(tree, shards, report=None):
    env = {**os.environ, "FACTORY_TEST_SLOT_HELD": "1", "GATE_SHARDS": str(shards), "GATE_REPORT_DIR": str(report)}
    return subprocess.run([sys.executable, str(HELPER)], cwd=tree, env=env, capture_output=True, text=True, timeout=120)


class GateShards(unittest.TestCase):
    def setUp(self):
        self.tree = make_tree()
        self.report = Path(tempfile.mkdtemp(prefix="zz-test-gate-report-"))
        for d in (self.tree, self.report):
            self.addCleanup(shutil.rmtree, d, True)

    def test_all_shards_pass_and_counts_aggregate(self):
        p = run(self.tree, 3, self.report)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        summary = json.loads((self.report / "summary.json").read_text())
        self.assertEqual((summary["shards"], summary["tests"]), (3, 6))
        self.assertIn("gate shards: 3 shards, 6 tests", p.stdout)
        self.assertIn("3 modules, 6 tests across 3 shards", p.stdout)

    def test_a_failing_test_in_one_shard_fails_the_run_and_shows_its_log(self):
        shutil.rmtree(self.tree)
        self.tree = make_tree(fail=True)
        self.addCleanup(shutil.rmtree, self.tree, True)
        p = run(self.tree, 3, self.report)
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("boom", p.stdout)
        self.assertIn("FAIL, shards with a non-zero exit", p.stderr)

    def test_zero_tests_is_a_failure(self):
        for f in (self.tree / "tests").glob("test_*.py"):
            f.write_text("import unittest\n")
        p = run(self.tree, 2, self.report)
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)

    def test_bad_shard_count_is_refused(self):
        self.assertEqual(run(self.tree, 0, self.report).returncode, 2)


if __name__ == "__main__":
    unittest.main()
