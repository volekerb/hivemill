"""T-1039: ci/guard_impl_unittest.py run --shard I --shards N splits the guard-diff test ids over CI shards."""
import os
import re
import subprocess
import sys
import tempfile
import textwrap
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "ci"))
import guard_impl_unittest as g  # noqa: E402

TOOL = os.path.join(ROOT, "ci", "guard_impl_unittest.py")
WF = os.path.join(ROOT, ".github", "workflows", "tests.yml")
GOOD = """
import unittest
class A(unittest.TestCase):
    def test_a(self): pass
    def test_b(self): pass
    def test_c(self): pass
class B(unittest.TestCase):
    def test_a(self): pass
    def test_b(self): pass
"""


def write(d, name, body):
    with open(os.path.join(d, name), "w", encoding="utf-8") as f:
        f.write(textwrap.dedent(body))


def shard_run(tests_dir, modules, i, n):
    return subprocess.run([sys.executable, TOOL, "run", "--shard", str(i), "--shards", str(n),
                           "--tests-dir", tests_dir, "--modules", modules],
                          capture_output=True, text=True, cwd=ROOT)


class AssignmentTest(unittest.TestCase):
    def test_real_id_set_is_partitioned_exactly_once_and_deterministically(self):
        ids, _, problems = g.collect_ids(g.diff_module_names())
        self.assertEqual(problems, [])
        self.assertGreater(len(ids), 100)
        for n in (1, 3, 7):
            shards = [g.shard_of(ids, i, n) for i in range(1, n + 1)]
            flat = [x for s in shards for x in s]
            self.assertEqual(sorted(flat), ids)  # union is the full set, and no id appears twice
            self.assertEqual(len(flat), len(set(flat)))
            self.assertEqual(shards, [g.shard_of(list(reversed(ids)), i, n) for i in range(1, n + 1)])
        self.assertEqual(g.shard_of(ids, 2, 3), ids[1::3])  # round-robin on the sorted index

    def test_every_diff_module_contributes_ids(self):
        ids, _, _ = g.collect_ids(g.diff_module_names())
        for mod in ("test_worker_guard", "test_priv_guard", "test_guard_diff"):
            self.assertTrue(any(i.startswith(mod + ".") for i in ids), mod)

    def test_digest_ignores_order_and_changes_with_content(self):
        self.assertEqual(g.ids_digest(["b", "a"]), g.ids_digest(["a", "b"]))
        self.assertNotEqual(g.ids_digest(["a"]), g.ids_digest(["a", "b"]))


class ShardRunTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.d = self.tmp.name
        self.addCleanup(self.tmp.cleanup)

    def test_shards_pass_report_counts_and_one_shared_digest(self):
        write(self.d, "test_good.py", GOOD)
        outs = [shard_run(self.d, "test_good.py", i, 3) for i in (1, 2, 3)]
        for r in outs:
            self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        lines = [re.search(r"assigned=(\d+) total=(\d+) ids_sha256=([0-9a-f]{64})", r.stdout) for r in outs]
        self.assertTrue(all(lines))
        self.assertEqual(sum(int(m.group(1)) for m in lines), 5)
        self.assertEqual({m.group(2) for m in lines}, {"5"})
        self.assertEqual(len({m.group(3) for m in lines}), 1)

    def test_failing_test_fails_its_shard_only(self):
        write(self.d, "test_bad.py", GOOD.replace("def test_c(self): pass", "def test_c(self): self.fail('x')"))
        rcs = [shard_run(self.d, "test_bad.py", i, 3).returncode for i in (1, 2, 3)]
        # sorted ids: A.test_a A.test_b A.test_c B.test_a B.test_b; A.test_c is index 2 -> shard 3
        self.assertEqual(rcs, [0, 0, 1])

    def test_loader_error_fails_every_shard(self):
        write(self.d, "test_good.py", GOOD)
        write(self.d, "test_broken.py", "import no_such_module_t1039\n")
        for i in (1, 2, 3):
            r = shard_run(self.d, "test_good.py,test_broken.py", i, 3)
            self.assertEqual(r.returncode, 1, r.stdout)
            self.assertIn("test_broken.py", r.stdout)

    def test_syntax_error_module_fails(self):
        write(self.d, "test_syn.py", "def (:\n")
        r = shard_run(self.d, "test_syn.py", 1, 1)
        self.assertEqual(r.returncode, 1, r.stdout)

    def test_module_with_no_tests_fails(self):
        write(self.d, "test_empty.py", "import unittest\n")
        r = shard_run(self.d, "test_empty.py", 1, 1)
        self.assertEqual(r.returncode, 1, r.stdout)
        self.assertIn("no tests found", r.stdout)

    def test_empty_id_list_fails(self):
        write(self.d, "test_one.py", "import unittest\nclass T(unittest.TestCase):\n    def test_x(self): pass\n")
        self.assertEqual(shard_run(self.d, "test_one.py", 1, 2).returncode, 0)
        r = shard_run(self.d, "test_one.py", 2, 2)
        self.assertEqual(r.returncode, 1, r.stdout)
        self.assertIn("empty id list", r.stdout)

    def test_all_skipped_shard_is_not_green(self):
        write(self.d, "test_skip.py", """
            import unittest
            @unittest.skip("nope")
            class T(unittest.TestCase):
                def test_x(self): pass
                def test_y(self): pass
            """)
        r = shard_run(self.d, "test_skip.py", 1, 1)
        self.assertEqual(r.returncode, 1, r.stdout)
        self.assertIn("zero tests executed", r.stdout)

    def test_bad_shard_arguments_are_refused(self):
        write(self.d, "test_good.py", GOOD)
        self.assertEqual(shard_run(self.d, "test_good.py", 4, 3).returncode, 2)
        r = subprocess.run([sys.executable, TOOL, "run", "--shard", "1"], capture_output=True, text=True, cwd=ROOT)
        self.assertEqual(r.returncode, 2)


class WorkflowTest(unittest.TestCase):
    wf = open(WF, encoding="utf-8").read()

    def test_matrix_matches_shards_argument_and_fan_in_keeps_the_name(self):
        shard = self.wf.split("  guard-unittest-shard:\n", 1)[1].split("\n  guard-unittest:\n", 1)[0]
        n = len(re.search(r"shard: \[([\d, ]+)\]", shard).group(1).split(","))
        self.assertEqual(n, 3)
        self.assertIn("--shards %d" % n, shard)
        self.assertIn("timeout-minutes: 30", shard)
        fan = self.wf.split("\n  guard-unittest:\n", 1)[1].split("\n  guard-diff:\n", 1)[0]
        self.assertIn("name: guard-unittest\n", fan)
        self.assertIn("if: always()", fan)
        self.assertIn('"$SHARD_RESULT" != "success"', fan)


if __name__ == "__main__":
    unittest.main()
