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
import unittest.mock
import uuid
from collections import Counter
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "ci"))

import shard  # noqa: E402

NAMES = ["test_%s.py" % c for c in "qwertyuiopasdfghjklzxcvbnm"] + ["test_a1.py", "test_a2.py", "test_b2.py"]


def assert_module_counts(self, mods, total, timings=None):
    """Each module appears once per shard, except split-by-id modules (T-1168) which appear on every shard."""
    timings = shard.load_timings() if timings is None else timings
    split = shard.split_modules(mods, timings, total)
    got = [m for s in range(1, total + 1) for m in shard.select(mods, s, total)]
    c = Counter(got)
    for m in mods:
        self.assertEqual(c[m], total if m in split else 1, "%s N=%d" % (m, total))


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
            assert_module_counts(self, mods, total)

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
            assert_module_counts(self, self.MODS, total, self.T)

    def test_checked_in_timings_pack_within_15_percent(self):
        mods = shard.all_modules()
        timings = shard.load_timings()
        self.assertTrue(timings, "ci/shard_timings.json is empty; regenerate it (docs/tests-and-ci.md)")
        w = shard.weights(mods, timings)
        self.assert_packed(w, self.loads(shard.pack(mods, 10, timings), w))

    def assert_packed(self, w, loads):
        """Pack rule for 10 shards. A module bigger than total/10*1.15 must sit alone on a shard, and the
        other 9 shards must still pack within 15% of their own mean (no flat max(limit, largest) bound).
        Split-by-id modules (T-1168) are excluded from the greedy loads but listed on every shard; only the
        remaining modules are checked for the 15% bound."""
        keys = list(w.keys())
        sm = shard.split_modules(keys, w, 10)
        total_w, biggest = sum(w.values()), max(w.values())
        limit = total_w / 10 * 1.15
        if biggest <= limit:
            self.assertLessEqual(max(loads), limit, loads)
            return
        if sm:
            rest_total = sum(w[m] for m in keys if m not in sm)
            rest_limit = rest_total / 10 * 1.15
            packs = shard.pack(keys, 10, w)
            for b in packs:
                self.assertTrue(sm.issubset(set(b)), b)
            loads_rest = [sum(w[m] for m in b if m not in sm) for b in packs]
            self.assertLessEqual(max(loads_rest), rest_limit, (loads_rest, sm))
            return
        self.assertAlmostEqual(max(loads), biggest, msg="big module shares its shard: %s" % loads)
        rest = sorted(loads)[:-1]
        self.assertLessEqual(max(rest), (total_w - biggest) / 9 * 1.15, loads)

    def test_oversize_rule_goes_red_on_unbalanced_rest_or_shared_big_shard(self):
        w = {"test_big.py": 100.0}
        w.update({"test_s%d.py" % i: 10.0 for i in range(9)})  # total 190, limit 21.85, biggest 100 > limit
        self.assert_packed(w, [100.0] + [10.0] * 9)  # alone + balanced rest: green
        with self.assertRaises(AssertionError):  # other work shares the big module's shard
            self.assert_packed(w, [110.0] + [10.0] * 8 + [0.0])
        with self.assertRaises(AssertionError):  # rest unbalanced: 40 > 90/9*1.15 = 11.5
            self.assert_packed(w, [100.0, 40.0] + [5.0] * 8)
        small = {"test_s%d.py" % i: 10.0 for i in range(10)}  # biggest <= limit: plain bound still applies
        self.assert_packed(small, [10.0] * 10)
        with self.assertRaises(AssertionError):
            self.assert_packed(small, [30.0] + [7.0] * 9)

    def test_real_modules_assigned_exactly_once_for_10_shards(self):
        mods = shard.all_modules()
        assert_module_counts(self, mods, 10)

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


class SplitModuleTest(unittest.TestCase):
    """T-1168: one oversized module's test ids are split across shards (round-robin on sorted ids)."""

    def test_shard_of_test_ids_matches_t1039_rule(self):
        ids = ["a", "b", "c", "d", "e"]
        shards = [shard.shard_of_test_ids(ids, i, 3) for i in (1, 2, 3)]
        flat = [x for s in shards for x in s]
        self.assertEqual(sorted(flat), sorted(ids))
        self.assertEqual(len(flat), len(set(flat)))
        self.assertEqual(shards[1], [ids[1], ids[4]])  # positions 1,4 -> shard 2

    def test_verify_split_module_across_shards(self):
        mod = "test_split_demo.py"
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        tests = os.path.join(tmp.name, "tests")
        os.mkdir(tests)
        lines = (
            "import unittest\n"
            "class T(unittest.TestCase):\n"
            + "".join("    def test_%d(self): pass\n" % i for i in range(7))
        )
        Path(tests, mod).write_text(lines)
        full = shard.collect_test_ids(mod, tests)
        self.assertEqual(len(full), 7)
        timings = {mod: 9999.0, "test_z.py": 1.0}
        old_split = shard.SPLIT_TEST_IDS_MODULES
        try:
            shard.SPLIT_TEST_IDS_MODULES = frozenset({mod})
            self.assertIn(mod, shard.split_modules([mod, "test_z.py"], timings, 3))
            for i in (1, 2, 3):
                chunk = shard.shard_of_test_ids(full, i, 3)
                rpath = os.path.join(tmp.name, "shard-%d.json" % i)
                Path(rpath).write_text(json.dumps({
                    "shard": i, "total": 3, "modules": [mod],
                    "tests": len(chunk), "seconds": {mod: 1.0},
                    "test_ids": {mod: chunk},
                }))
            repdir = tmp.name
            summary, n = shard.verify(repdir, 3, tests, only={mod})
        finally:
            shard.SPLIT_TEST_IDS_MODULES = old_split
        self.assertEqual(n, 7)
        self.assertIn("7 tests across 3 shards", summary)

    def test_verify_split_module_rejects_duplicate_test_id_across_shards(self):
        uid = uuid.uuid4().hex[:8]
        mod = "test_split_dup_%s.py" % uid
        base = mod[:-3]
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        tests = os.path.join(tmp.name, "tests")
        os.mkdir(tests)
        Path(tests, mod).write_text(
            "import unittest\nclass T(unittest.TestCase):\n"
            + "".join("    def test_%d(self): pass\n" % i for i in range(4)))
        full = shard.collect_test_ids(mod, tests)
        sys.modules.pop(base, None)
        old_split = shard.SPLIT_TEST_IDS_MODULES
        try:
            shard.SPLIT_TEST_IDS_MODULES = frozenset({mod})
            good1 = shard.shard_of_test_ids(full, 1, 2)
            good2 = shard.shard_of_test_ids(full, 2, 2)
            bad2 = good2 + good1[:1]
            for i, chunk in ((1, good1), (2, bad2)):
                Path(os.path.join(tmp.name, "shard-%d.json" % i)).write_text(json.dumps({
                    "shard": i, "total": 2, "modules": [mod], "tests": len(chunk),
                    "seconds": {mod: 1.0}, "test_ids": {mod: chunk},
                }))
            with self.assertRaisesRegex(shard.ShardError, "id coverage wrong"):
                shard.verify(tmp.name, 2, tests, only={mod})
        finally:
            shard.SPLIT_TEST_IDS_MODULES = old_split
            sys.modules.pop(base, None)


FIXTURE = ROOT / "ci" / "testdata" / "quarantine_fixture.json"
MOD3 = "import unittest\nclass T(unittest.TestCase):\n    def test_1(self): pass\n    def test_2(self): pass\n    def test_3(self): pass\n"


def entry(**kw):
    e = {"id": "test_q.T.test_2", "owner": "T-0001", "added": "2026-11-10", "expires": "2026-11-20",
         "reason": "flaky", "evidence": "https://example.invalid/r/1"}
    e.update(kw)
    return {k: v for k, v in e.items() if v is not None}


class QuarantineTest(unittest.TestCase):
    """T-1742: ci/quarantine.json keeps known-flaky tests out of the run."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.tests = os.path.join(self.tmp.name, "tests")
        os.mkdir(self.tests)
        Path(self.tests, "test_q.py").write_text(MOD3)
        self.out = os.path.join(self.tmp.name, "o.json")
        self.qfile = os.path.join(self.tmp.name, "q.json")

    def qwrite(self, entries):
        Path(self.qfile).write_text(json.dumps({"entries": entries}))

    def run_q(self, today="2026-11-17", total=1, shard_i=1, split=False):
        cwd, path = os.getcwd(), list(sys.path)
        buf = io.StringIO()
        old_split, old_timings = shard.SPLIT_TEST_IDS_MODULES, shard.load_timings
        if split:
            shard.SPLIT_TEST_IDS_MODULES = frozenset({"test_q.py"})
            shard.load_timings = lambda *a, **k: {"test_q.py": 9999.0}
        try:
            with contextlib.redirect_stdout(buf), contextlib.redirect_stderr(io.StringIO()):
                rc = shard.run_shard(shard_i, total, self.out, self.tests, quarantine=self.qfile, today=today)
        finally:
            shard.SPLIT_TEST_IDS_MODULES, shard.load_timings = old_split, old_timings
            os.chdir(cwd)
            sys.path[:] = path
            sys.modules.pop("test_q", None)
        return rc, buf.getvalue(), json.loads(Path(self.out).read_text())

    def test_valid_entry_excludes_exactly_that_test(self):
        self.qwrite([entry()])
        rc, out, rep = self.run_q()
        self.assertEqual((rc, rep["tests"], rep["quarantined"]), (0, 2, ["test_q.T.test_2"]))
        self.assertIn("quarantine: excluded test_q.T.test_2 (owner T-0001, expires 2026-11-20)", out)

    def test_class_and_module_ids_drop_all_their_tests(self):
        for eid in ("test_q.T", "test_q"):
            self.qwrite([entry(id=eid)])
            rc, _, rep = self.run_q()
            self.assertEqual((rc, rep["tests"], rep["quarantined"]), (0, 0, [eid]))

    def test_empty_list_changes_nothing(self):
        self.qwrite([])
        rc, out, rep = self.run_q()
        self.assertEqual((rc, rep["tests"], "quarantined" in rep), (0, 3, False))
        self.assertNotIn("quarantine:", out)

    def test_invalid_entries_are_ignored_with_a_warning_each(self):
        bad = [(entry(owner=None), "missing owner"),
               (entry(expires="2026-11-25"), "expires is more than 14 days after added"),
               (entry(added="2026-02-30"), "bad date added"),
               (entry(evidence=""), "empty evidence")]
        self.qwrite([b for b, _ in bad])
        rc, out, rep = self.run_q()
        self.assertEqual((rc, rep["tests"], "quarantined" in rep), (0, 3, False))
        lines = [l for l in out.splitlines() if l.startswith("quarantine:")]
        self.assertEqual(len(lines), 4, out)
        for line, (_, why) in zip(lines, bad):
            self.assertIn("quarantine: ignored invalid entry test_q.T.test_2: " + why, line)

    def test_expired_entry_stays_excluded_and_warns(self):
        self.qwrite([entry()])
        rc, out, rep = self.run_q(today="2026-11-21")
        self.assertEqual((rc, rep["tests"]), (0, 2))
        self.assertIn("quarantine: EXPIRED test_q.T.test_2 owner T-0001 expired 2026-11-20", out)

    def test_corrupt_file_exits_2_and_writes_no_report(self):
        Path(self.qfile).write_text("not json")
        r = subprocess.run([sys.executable, str(ROOT / "ci" / "shard.py"), "run", "--shard", "1", "--total", "1",
                            "--out", self.out, "--quarantine", self.qfile, "--today", "2026-11-17"],
                           capture_output=True, text=True)
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertIn(self.qfile, r.stderr)
        self.assertFalse(os.path.exists(self.out))

    def test_wrong_shape_exits_2(self):
        Path(self.qfile).write_text("[]")
        with self.assertRaises(shard.QuarantineError):
            shard.load_quarantine(self.qfile, "2026-11-17")

    def test_missing_file_is_no_quarantine(self):
        self.assertEqual(shard.load_quarantine(os.path.join(self.tmp.name, "none.json"), "2026-11-17"), ([], []))

    def test_shared_fixture_active_set_and_warnings(self):
        fx = json.loads(FIXTURE.read_text())
        active, warnings = shard.load_quarantine(str(FIXTURE), fx["today"])
        self.assertEqual([e["id"] for e in active], fx["expected_active"])
        self.assertEqual(warnings, fx["expected_warnings"])

    def test_split_module_path_excludes_the_id_on_its_shard(self):
        self.qwrite([entry()])
        full = shard.collect_test_ids("test_q.py", self.tests)
        sys.modules.pop("test_q", None)
        self.assertEqual(len(full), 3)
        total = 2
        reps = []
        for i in (1, 2):
            rc, _, rep = self.run_q(total=total, shard_i=i, split=True)
            self.assertEqual(rc, 0)
            reps.append(rep)
        ran = [t for r in reps for t in r["test_ids"]["test_q.py"]]
        self.assertEqual(sorted(ran), ["test_q.T.test_1", "test_q.T.test_3"])
        self.assertEqual(sum(r["tests"] for r in reps), 2)
        # verify treats the dropped id as covered
        d = os.path.join(self.tmp.name, "reps")
        os.mkdir(d)
        for i, r in enumerate(reps, 1):
            Path(d, "s%d.json" % i).write_text(json.dumps(r))
        old_split, old_timings = shard.SPLIT_TEST_IDS_MODULES, shard.load_timings
        shard.SPLIT_TEST_IDS_MODULES = frozenset({"test_q.py"})
        shard.load_timings = lambda *a, **k: {"test_q.py": 9999.0}
        try:
            summary, n = shard.verify(d, total, self.tests)
        finally:
            shard.SPLIT_TEST_IDS_MODULES, shard.load_timings = old_split, old_timings
            sys.modules.pop("test_q", None)
        self.assertEqual(n, 2)

    def test_verify_ignores_the_quarantined_key(self):
        d = os.path.join(self.tmp.name, "reps")
        os.mkdir(d)
        Path(d, "s1.json").write_text(json.dumps({"shard": 1, "total": 1, "modules": ["test_q.py"], "tests": 2,
                                                  "seconds": {"test_q.py": 1.0}, "quarantined": ["test_q.T.test_2"]}))
        r = subprocess.run([sys.executable, str(ROOT / "ci" / "shard.py"), "verify", "--total", "1", "--dir", d],
                           capture_output=True, text=True)
        # the real tests/ tree is the reference, so only the key handling is under test: no "unreadable report"
        self.assertNotIn("unreadable", r.stderr)
        summary, n = shard.verify(d, 1, self.tests)
        self.assertEqual(n, 2)


class ScopeAndJobsTest(unittest.TestCase):
    """Owner 2026-11-17: a PR run stays near 10 minutes; the deep run (workflow_dispatch) runs everything."""

    MODS = ["test_a.py", "test_guard_diff.py", "test_priv_guard.py", "test_worker_guard_x.py", "test_guard_build.py"]

    def test_deep_run_drops_nothing(self):
        self.assertEqual(shard.scoped_out(self.MODS, env={"SHARD_DEEP": "true", "SHARD_BASE": "abc"}, changed=[]), frozenset())

    def test_corpus_module_is_deep_only_everywhere_else(self):
        self.assertEqual(shard.scoped_out(self.MODS, env={}), frozenset({"test_guard_diff.py"}))
        self.assertEqual(shard.scoped_out(self.MODS, only={"test_a.py"}, env={"SHARD_BASE": "abc"}, changed=[]),
                         frozenset({"test_guard_diff.py"}))

    def test_full_mode_runs_guard_modules_only_for_a_guard_path(self):
        env = {"SHARD_BASE": "abc"}
        dropped = shard.scoped_out(self.MODS, env=env, changed=["bin/factory_cli.py", "docs/x.md"])
        self.assertEqual(dropped, frozenset(self.MODS) - {"test_a.py"})
        for path in ("roles/hooks/worker" + "-guard.py", "roles/settings/coder.json", "ci/guard_diff.py", "ci/shard.py",
                     ".github/workflows/tests.yml", "go/internal/guard/x.go", "go/cmd/worker" + "-guard/main.go",
                     "go/internal/shlex/lex.go", "tests/test_worker_guard_x.py", "tests/harness.py", "tests/guard_impl.py"):
            self.assertEqual(shard.scoped_out(self.MODS, env=env, changed=[path]), frozenset({"test_guard_diff.py"}), path)

    def test_unknown_diff_keeps_the_guard_modules(self):
        with tempfile.TemporaryDirectory() as d:  # not a git repo: the diff cannot be read
            self.assertIsNone(shard.changed_paths("abc", root=d))
        self.assertEqual(shard.scoped_out(self.MODS, env={"SHARD_BASE": "0" * 40}), frozenset({"test_guard_diff.py"}))

    def test_default_jobs(self):
        with unittest.mock.patch.dict(os.environ, {"SHARD_JOBS": "3"}):
            self.assertEqual(shard.default_jobs(), 3)
        with unittest.mock.patch.dict(os.environ, {"SHARD_JOBS": ""}):
            self.assertTrue(1 <= shard.default_jobs() <= 4)

    def _tree(self, d, red=False):
        tests = os.path.join(d, "tests")
        os.mkdir(tests)
        for name in ("test_p1.py", "test_p2.py", "test_p3.py"):
            body = "self.assertTrue(%s)" % ("False" if red and name == "test_p2.py" else "True")
            Path(tests, name).write_text("import unittest\nclass T(unittest.TestCase):\n    def test_x(self):\n        %s\n" % body)
        return tests

    def _run(self, tests, out, jobs):
        cmd = [sys.executable, shard.__file__, "run", "--shard", "1", "--total", "1", "--out", out, "--jobs", str(jobs)]
        env = dict(os.environ, SHARD_DEEP="true")
        code = ("import sys; sys.path.insert(0, %r); import shard; "
                "sys.exit(shard.run_shard(1, 1, %r, tests_dir=%r, jobs=%d))" % (os.path.dirname(shard.__file__), out, tests, jobs))
        return subprocess.run([sys.executable, "-c", code], env=env, capture_output=True, text=True)

    def test_parallel_run_matches_the_serial_report(self):
        with tempfile.TemporaryDirectory() as d:
            tests = self._tree(d)
            reports = []
            for jobs in (1, 3):
                out = os.path.join(d, "r%d.json" % jobs)
                r = self._run(tests, out, jobs)
                self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
                with open(out) as f:
                    reports.append(json.load(f))
            self.assertEqual(reports[0]["modules"], reports[1]["modules"])
            self.assertEqual(reports[0]["tests"], 3)
            self.assertEqual(reports[1]["tests"], 3)
            self.assertEqual(sorted(reports[1]["seconds"]), ["test_p1.py", "test_p2.py", "test_p3.py"])

    def test_parallel_run_is_red_when_one_module_fails(self):
        with tempfile.TemporaryDirectory() as d:
            tests = self._tree(d, red=True)
            r = self._run(tests, os.path.join(d, "r.json"), 3)
            self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
            self.assertIn("failed modules: test_p2.py", r.stderr)


class WorkflowTest(unittest.TestCase):
    wf = (ROOT / ".github" / "workflows" / "tests.yml").read_text()

    def test_matrix_length_matches_shards_env(self):
        total = int(re.search(r"(?m)^  SHARDS: (\d+)$", self.wf).group(1))
        matrix = re.search(r"shard: \[([\d, ]+)\]", self.wf).group(1)
        self.assertEqual([int(x) for x in matrix.split(",")], list(range(1, total + 1)))

    def test_required_check_name_and_timeouts(self):
        self.assertRegex(
            self.wf,
            r"(?m)^  unittest:\n    name: unittest\n"
            r"    needs: \[ci-tier, docs-check, unittest-shard, go, guard-unittest, guard-diff, "
            r"guard-self-compare, guard-evaluator-home-loosen, guard-equiv-fanin, guard-equiv-rest\]\n"
            r"    if: always\(\)",
        )
        self.assertEqual(
            re.findall(r"timeout-minutes: (\d+)", self.wf),
            ["5", "10", "30", "40", "30", "5", "45", "30", "30", "30", "5", "30", "5"],
        )

    def test_edited_trigger_is_not_used(self):
        # An `edited` run would put a second `unittest` check (or a skipped one, which counts as
        # passing) on a SHA whose full run is pending or red, and would cancel that run via the
        # concurrency group. Dropped on purpose; see docs/tests-and-ci.md.
        self.assertRegex(self.wf, r"types: \[opened, synchronize, reopened\]\n")
        self.assertNotRegex(self.wf, r"(?m)^\s+types:.*edited")

    def test_unittest_aggregator_checks_go_result(self):
        self.assertIn("needs.go.result", self.wf)
        self.assertIn("needs.unittest-shard.result", self.wf)
        self.assertIn("needs.guard-unittest.result", self.wf)
        self.assertIn("needs.guard-diff.result", self.wf)
        self.assertIn("needs.guard-self-compare.result", self.wf)
        self.assertIn("needs.guard-evaluator-home-loosen.result", self.wf)
        self.assertIn("GOTOOLCHAIN: local", self.wf)


if __name__ == "__main__":
    unittest.main()
