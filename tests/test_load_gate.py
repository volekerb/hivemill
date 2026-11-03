"""T-0905 (owner M16816): while load1 >= gate_max_load_ratio x CPUs, at most one local suite box-wide. The shared load
decision (tests/_slot.load_state), tests/_slot.acquire, ci/affected.py --run, ci/guard_diff.py and the prcheck slot loop."""
import fcntl
import os
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
from pathlib import Path

import _slot
from harness import FactoryTestCase

REPO = Path(__file__).resolve().parent.parent
MSG = "one local suite box-wide (M16816); rely on PR CI"
CPUS = os.cpu_count() or 1
CAP = 1.25 * CPUS


def hold(directory, n=1):
    """Hold the first n slot files exclusively (a suite running elsewhere); returns the open files."""
    directory.mkdir(parents=True, exist_ok=True)
    files = []
    for i in range(n):
        f = open(directory / f"slot-{i}.lock", "a+")
        fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        files.append(f)
    return files


class LoadStateTests(unittest.TestCase):
    def test_below_at_above_cap(self):
        self.assertFalse(_slot.load_state(1.25, CAP - 0.01)[2])
        self.assertTrue(_slot.load_state(1.25, CAP)[2])
        self.assertTrue(_slot.load_state(1.25, CAP + 3)[2])
        now, cap, over = _slot.load_state(1.25, CAP)
        self.assertEqual((now, cap), (CAP, CAP))

    def test_load1_is_read_once_per_decision(self):
        calls = []
        with mock.patch.object(os, "getloadavg", lambda: calls.append(1) or (1.0, 0, 0)):
            os.environ.pop("FACTORY_LOADAVG", None)
            _slot.load_state(1.25)
        self.assertEqual(len(calls), 1)

    def test_pinned_loadavg_env(self):
        old = os.environ.get("FACTORY_LOADAVG")
        os.environ["FACTORY_LOADAVG"] = "123.5"
        try:
            self.assertEqual(_slot.load_state(1.25)[0], 123.5)
        finally:
            if old is None:
                os.environ.pop("FACTORY_LOADAVG")
            else:
                os.environ["FACTORY_LOADAVG"] = old


class AcquireUnderLoadTests(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp(prefix="zz-load-slots-"))
        self.addCleanup(__import__("shutil").rmtree, self.dir, True)
        self.addCleanup(_slot.release)
        self.out = open(os.devnull, "w")
        self.addCleanup(self.out.close)

    def test_below_cap_second_slot_is_taken(self):
        self.addCleanup(lambda: [f.close() for f in files])
        files = hold(self.dir, 1)
        got = _slot.acquire(directory=self.dir, n=2, out=self.out, state=(CAP - 1, CAP, False))
        self.assertEqual(got, 1)

    def test_over_cap_with_a_slot_held_is_refused_at_once(self):
        files = hold(self.dir, 1)
        self.addCleanup(lambda: [f.close() for f in files])
        waits = []
        with open(self.dir / "out.txt", "w+") as out:
            got = _slot.acquire(directory=self.dir, n=2, out=out, require=True, sleep=waits.append,
                                state=(CAP + 1, CAP, True))
            out.seek(0)
            text = out.read()
        self.assertIsNone(got)
        self.assertEqual(waits, [], "must not queue")
        self.assertIn(MSG, text)

    def test_over_cap_non_required_exits_with_load_code(self):
        files = hold(self.dir, 1)
        self.addCleanup(lambda: [f.close() for f in files])
        with self.assertRaises(SystemExit) as cm:
            _slot.acquire(directory=self.dir, n=2, out=self.out, state=(CAP + 1, CAP, True))
        self.assertEqual(cm.exception.code, _slot.LOAD_EXIT)

    def test_over_cap_with_nothing_held_takes_the_one_slot(self):
        got = _slot.acquire(directory=self.dir, n=2, out=self.out, state=(CAP + 1, CAP, True))
        self.assertEqual(got, 0)


class ToolGateTests(unittest.TestCase):
    """ci/affected.py --run and ci/guard_diff.py as subprocesses, HOME and slots in a temp dir."""

    def setUp(self):
        self.home = Path(tempfile.mkdtemp(prefix="zz-load-home-"))
        self.addCleanup(__import__("shutil").rmtree, self.home, True)
        self.slots = self.home / ".cache" / "factory-test-slots"
        self.files = hold(self.slots, 1)
        self.addCleanup(lambda: [f.close() for f in self.files])

    def run_tool(self, script, *args, load=CAP + 4, ci=""):
        env = {k: v for k, v in os.environ.items() if not k.startswith(("GIT_", "FACTORY_"))}
        env.update(HOME=str(self.home), FACTORY_LOADAVG=str(load), CI=ci, FACTORY_HOME=str(self.home / "nofactory"))
        return subprocess.run([sys.executable, str(REPO / "ci" / script), *args], capture_output=True, text=True,
                              cwd=REPO, env=env, timeout=60)

    def test_affected_run_refused_under_load_with_slot_held(self):
        p = self.run_tool("affected.py", "--base", "HEAD", "--run")
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertIn(MSG, p.stderr)

    def test_affected_selection_mode_is_never_gated(self):
        p = self.run_tool("affected.py", "--base", "HEAD", "--list")
        self.assertNotEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertNotIn(MSG, p.stderr)

    def test_gate_skipped_in_ci_with_a_held_slot_and_below_cap(self):
        over = (CAP + 1, CAP, True)
        self.assertIsNone(_slot.local_run_gate({"CI": "true"}, self.slots, over))
        self.assertIsNone(_slot.local_run_gate({"FACTORY_TEST_SLOT_HELD": "1"}, self.slots, over))
        self.assertIsNone(_slot.local_run_gate({}, self.slots, (CAP - 1, CAP, False)))
        self.assertIn(MSG, _slot.local_run_gate({}, self.slots, over))

    def test_guard_diff_refused_locally_under_load(self):
        p = self.run_tool("guard_diff.py", "--base", "HEAD")
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertIn(MSG, p.stderr)

    def test_guard_diff_never_gated_in_ci(self):
        p = self.run_tool("guard_diff.py", "--base", "HEAD", "--impl", "bogus", ci="true")
        self.assertNotEqual(p.returncode, 5)
        self.assertNotIn(MSG, p.stderr)

    def test_guard_diff_below_cap_not_gated(self):
        p = self.run_tool("guard_diff.py", "--base", "HEAD", "--impl", "bogus", load=CAP - 1)
        self.assertNotIn(MSG, p.stderr)

    def test_first_suite_under_load_runs_and_a_second_is_refused(self):
        for f in self.files:
            f.close()
        self.files = []
        self.assertIsNone(_slot.local_run_gate({}, self.slots, (CAP + 1, CAP, True)))
        self.addCleanup(_slot.release)
        self.assertIn(MSG, _slot.local_run_gate({}, self.slots, (CAP + 1, CAP, True)))


class PrcheckRaceTests(FactoryTestCase):
    def test_waiting_below_cap_then_load_climbs_with_a_slot_held_is_refused(self):
        self.ok("config", "test_slots", "2")
        d = self.env.user_home / ".cache" / "factory-test-slots"
        files = hold(d, 2)  # both slots busy, load below the cap: the prcheck waits
        self.addCleanup(lambda: [f.close() for f in files])
        loads = iter([CAP - 2, CAP - 2, CAP + 3])  # third poll sees the load over the cap
        sleeps = []
        with self.env.load_factory() as mod:
            mod.load1 = lambda: next(loads)
            ticks = iter(range(0, 1000))
            mod.time.monotonic = lambda: float(next(ticks))
            mod.time.sleep = sleeps.append
            os.environ["FACTORY_PRCHECK_SLOT_WAIT_S"] = "500"
            self.addCleanup(os.environ.pop, "FACTORY_PRCHECK_SLOT_WAIT_S", None)
            got = mod.prcheck_take_slot(mod.db())
        self.assertIsInstance(got, str)
        self.assertIn(MSG, got)
        self.assertEqual(len(sleeps), 2, "waited twice below the cap, then refused instead of a third wait")

    def test_effective_test_slots_reuses_one_reading(self):
        self.ok("config", "test_slots", "3")
        with self.env.load_factory() as mod:
            reads = []
            mod.load1 = lambda: reads.append(1) or CAP + 1
            con = mod.db()
            state = mod.load_slot_cap(con)
            self.assertEqual(mod.effective_test_slots(con, 2, state), 1)
            self.assertEqual(len(reads), 1)
            mod.load1 = lambda: CAP - 1
            self.assertEqual(mod.effective_test_slots(con, 2), 3)


if __name__ == "__main__":
    unittest.main()
