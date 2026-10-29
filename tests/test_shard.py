"""Tests for ci/shard.py: every test module lands in exactly one shard; verify catches gaps."""
import contextlib
import io
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "ci"))

import shard  # noqa: E402

NAMES = ["test_%s.py" % c for c in "qwertyuiopasdfghjklzxcvbnm"] + ["test_a1.py", "test_a2.py", "test_b2.py"]


class SelectTest(unittest.TestCase):
    def test_every_module_in_exactly_one_shard(self):
        for total in range(1, 11):
            got = [m for s in range(1, total + 1) for m in shard.select(NAMES, s, total)]
            self.assertEqual(sorted(got), sorted(NAMES), "N=%d" % total)
            self.assertEqual(len(got), len(set(got)), "N=%d" % total)

    def test_real_tests_dir_partitions_for_n_1_to_10(self):
        mods = shard.all_modules()
        self.assertGreater(len(mods), 10)
        for total in range(1, 11):
            got = [m for s in range(1, total + 1) for m in shard.select(mods, s, total)]
            self.assertEqual(sorted(got), mods)

    def test_selection_is_deterministic_and_input_order_free(self):
        self.assertEqual(shard.select(NAMES, 2, 4), shard.select(list(reversed(NAMES)), 2, 4))

    def test_out_of_range_shard_rejected(self):
        for s, t in [(0, 4), (5, 4), (1, 0)]:
            with self.assertRaises(shard.ShardError):
                shard.select(NAMES, s, t)

    def test_cli_list_matches_select(self):
        out = subprocess.run([sys.executable, str(ROOT / "ci" / "shard.py"), "list", "--shard", "3", "--total", "4"],
                             capture_output=True, text=True, check=True).stdout.split()
        self.assertEqual(out, shard.select(shard.all_modules(), 3, 4))


class PackTest(unittest.TestCase):
    T = {"test_%s.py" % c: float(i * i % 37 + 1) for i, c in enumerate("qwertyuiopasdfghjklzxcvbnm")}
    MODS = sorted(T)

    def loads(self, bins, w):
        return [sum(w[m] for m in b) for b in bins]

    def test_packing_is_balanced_deterministic_and_input_order_free(self):
        a = shard.pack(self.MODS, 4, self.T)
        self.assertEqual(a, shard.pack(list(reversed(self.MODS)), 4, self.T))
        self.assertEqual(sorted(m for b in a for m in b), self.MODS)
        loads = self.loads(a, self.T)
        self.assertLessEqual(max(loads) - min(loads), max(self.T.values()))

    def test_ties_break_by_name_then_lowest_shard(self):
        self.assertEqual(shard.pack(["test_b.py", "test_a.py", "test_c.py"], 2, {}),
                         [["test_a.py", "test_c.py"], ["test_b.py"]])

    def test_missing_module_gets_the_median(self):
        w = shard.weights(["test_a.py", "test_b.py", "test_c.py", "test_new.py"],
                          {"test_a.py": 1.0, "test_b.py": 5.0, "test_c.py": 9.0})
        self.assertEqual(w["test_new.py"], 5.0)
        self.assertEqual(shard.weights(["test_x.py"], {}), {"test_x.py": 1.0})

    def test_exactly_once_for_n_1_to_10_with_timings(self):
        for total in range(1, 11):
            got = [m for s in range(1, total + 1) for m in shard.select(self.MODS, s, total, self.T)]
            self.assertEqual(sorted(got), self.MODS, "N=%d" % total)

    def test_checked_in_timings_pack_within_15_percent(self):
        mods = shard.all_modules()
        timings = shard.load_timings()
        self.assertTrue(timings, "ci/shard_timings.json is empty; regenerate it (docs/tests-and-ci.md)")
        w = shard.weights(mods, timings)
        loads = self.loads(shard.pack(mods, 10, timings), w)
        self.assertLessEqual(max(loads), sum(w.values()) / 10 * 1.15, loads)

    def test_real_modules_assigned_exactly_once_for_10_shards(self):
        mods = shard.all_modules()
        got = [m for s in range(1, 11) for m in shard.select(mods, s, 10)]
        self.assertEqual(sorted(got), mods)
        self.assertEqual(len(got), len(set(got)))

    def test_checked_in_timings_have_no_stale_modules(self):
        self.assertEqual(sorted(set(shard.load_timings()) - set(shard.all_modules())), [])

    def test_timings_cli_merges_reports(self):
        with tempfile.TemporaryDirectory() as tmp:
            for i, sec in ((1, {"test_a.py": 2.5}), (2, {"test_b.py": 1.0})):
                Path(tmp, "s%d.json" % i).write_text(json.dumps({"shard": i, "seconds": sec}))
            out = os.path.join(tmp, "t.json")
            subprocess.run([sys.executable, str(ROOT / "ci" / "shard.py"), "timings", "--dir", tmp, "--out", out],
                           capture_output=True, text=True, check=True)
            self.assertEqual(json.loads(Path(out).read_text()), {"test_a.py": 2.5, "test_b.py": 1.0})


class VerifyTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.tests = os.path.join(self.tmp.name, "tests")
        os.mkdir(self.tests)
        for n in ["test_a.py", "test_b.py", "test_c.py"]:
            Path(self.tests, n).write_text("")
        self.reports = os.path.join(self.tmp.name, "r")
        os.mkdir(self.reports)

    def write(self, i, total, mods, tests=5):
        Path(self.reports, "shard-%d.json" % i).write_text(
            json.dumps({"shard": i, "total": total, "modules": mods, "tests": tests}))

    def test_complete_reports_pass_and_sum_tests(self):
        self.write(1, 2, ["test_a.py", "test_c.py"], 7)
        self.write(2, 2, ["test_b.py"], 3)
        summary, tests = shard.verify(self.reports, 2, self.tests)
        self.assertEqual(tests, 10)
        self.assertIn("3 modules, 10 tests across 2 shards", summary)

    def test_missing_shard_rejected(self):
        self.write(1, 2, ["test_a.py", "test_c.py"])
        with self.assertRaisesRegex(shard.ShardError, "missing shard reports: 2"):
            shard.verify(self.reports, 2, self.tests)

    def test_module_run_twice_rejected(self):
        self.write(1, 2, ["test_a.py", "test_b.py", "test_c.py"])
        self.write(2, 2, ["test_b.py"])
        with self.assertRaisesRegex(shard.ShardError, "ran twice=.*test_b"):
            shard.verify(self.reports, 2, self.tests)

    def test_module_never_run_rejected(self):
        self.write(1, 2, ["test_a.py"])
        self.write(2, 2, ["test_b.py"])
        with self.assertRaisesRegex(shard.ShardError, "never ran=.*test_c"):
            shard.verify(self.reports, 2, self.tests)

    def test_wrong_total_rejected(self):
        self.write(1, 3, ["test_a.py"])
        with self.assertRaises(shard.ShardError):
            shard.verify(self.reports, 2, self.tests)

    def test_only_expects_just_the_listed_modules(self):
        self.write(1, 2, ["test_a.py"])
        self.write(2, 2, [])
        summary, tests = shard.verify(self.reports, 2, self.tests, only={"test_a.py"})
        self.assertIn("1 modules, 10 tests across 2 shards", summary)

    def test_only_still_rejects_a_listed_module_that_never_ran(self):
        self.write(1, 2, ["test_a.py"])
        self.write(2, 2, [])
        with self.assertRaisesRegex(shard.ShardError, "never ran=.*test_b"):
            shard.verify(self.reports, 2, self.tests, only={"test_a.py", "test_b.py"})

    def test_cli_verify_only_file(self):
        self.write(1, 2, ["test_b.py"])
        self.write(2, 2, [])
        only = os.path.join(self.tmp.name, "only.txt")
        Path(only).write_text("test_b.py\n")
        r = subprocess.run([sys.executable, str(ROOT / "ci" / "shard.py"), "verify", "--total", "2", "--dir", self.reports,
                            "--only", only], capture_output=True, text=True)
        # the real tests/ dir is the reference, so only a listed module that exists there verifies
        self.assertEqual(r.returncode, 1, r.stdout)
        self.assertIn("unknown=['test_b.py']", r.stderr)


class RunTest(unittest.TestCase):
    def test_only_filters_the_shard_and_empty_selection_passes(self):
        with tempfile.TemporaryDirectory() as tmp:
            tests = os.path.join(tmp, "tests")
            os.mkdir(tests)
            Path(tests, "test_x.py").write_text("import unittest\nclass T(unittest.TestCase):\n    def test_1(self): pass\n")
            Path(tests, "test_y.py").write_text("import unittest\nclass T(unittest.TestCase):\n    def test_2(self): self.fail('x')\n")
            out = os.path.join(tmp, "o.json")
            cwd = os.getcwd()
            try:
                with contextlib.redirect_stderr(io.StringIO()):
                    rc = shard.run_shard(1, 1, out, tests, only={"test_x.py"})
                    rc_empty = shard.run_shard(1, 1, out + "2", tests, only=set())
            finally:
                os.chdir(cwd)
                sys.path[:] = [p for p in sys.path if p not in (tests, tmp)]
                for m in ("test_x", "test_y"):
                    sys.modules.pop(m, None)
            self.assertEqual((rc, json.loads(Path(out).read_text())["modules"]), (0, ["test_x.py"]))
            self.assertEqual((rc_empty, json.loads(Path(out + "2").read_text())["modules"]), (0, []))

    def test_run_writes_report_with_modules_and_count(self):
        with tempfile.TemporaryDirectory() as tmp:
            tests = os.path.join(tmp, "tests")
            os.mkdir(tests)
            Path(tests, "test_x.py").write_text(
                "import unittest\nfrom pkg import val\nclass T(unittest.TestCase):\n    def test_1(self): self.assertEqual(val, 1)\n    def test_2(self): pass\n")
            Path(tests, "test_y.py").write_text(
                "import unittest\nclass T(unittest.TestCase):\n    def test_3(self): self.fail('x')\n")
            os.mkdir(os.path.join(tmp, "pkg"))
            Path(tmp, "pkg", "__init__.py").write_text("val = 1\n")
            out = os.path.join(tmp, "o.json")
            cwd = os.getcwd()
            try:
                with contextlib.redirect_stderr(io.StringIO()):
                    rc1 = shard.run_shard(1, 2, out, tests)
                    rep1 = json.loads(Path(out).read_text())
                    rc2 = shard.run_shard(2, 2, out, tests)
                    rep2 = json.loads(Path(out).read_text())
            finally:
                os.chdir(cwd)
                sys.path[:] = [p for p in sys.path if p not in (tests, tmp)]
                sys.modules.pop("pkg", None)
                for m in ("test_x", "test_y"):
                    sys.modules.pop(m, None)
        self.assertEqual((rc1, rep1["modules"], rep1["tests"]), (0, ["test_x.py"], 2))
        self.assertEqual((rc2, rep2["modules"], rep2["tests"]), (1, ["test_y.py"], 1))


class WorkflowTest(unittest.TestCase):
    wf = (ROOT / ".github" / "workflows" / "tests.yml").read_text()

    def test_matrix_length_matches_shards_env(self):
        total = int(re.search(r"(?m)^  SHARDS: (\d+)$", self.wf).group(1))
        matrix = re.search(r"shard: \[([\d, ]+)\]", self.wf).group(1)
        self.assertEqual([int(x) for x in matrix.split(",")], list(range(1, total + 1)))

    def test_required_check_name_and_timeouts(self):
        self.assertRegex(self.wf, r"(?m)^  unittest:\n    name: unittest\n    needs: \[unittest-shard, go\]\n    if: always\(\)")
        self.assertEqual(re.findall(r"timeout-minutes: (\d+)", self.wf), ["20", "10", "5"])

    def test_edited_trigger_is_not_used(self):
        # An `edited` run would put a second `unittest` check (or a skipped one, which counts as
        # passing) on a SHA whose full run is pending or red, and would cancel that run via the
        # concurrency group. Dropped on purpose; see docs/tests-and-ci.md.
        self.assertRegex(self.wf, r"types: \[opened, synchronize, reopened\]\n")
        self.assertNotRegex(self.wf, r"(?m)^\s+types:.*edited")

    def test_unittest_aggregator_checks_go_result(self):
        self.assertIn("needs.go.result", self.wf)
        self.assertIn("needs.unittest-shard.result", self.wf)
        self.assertIn("GOTOOLCHAIN: local", self.wf)


if __name__ == "__main__":
    unittest.main()
