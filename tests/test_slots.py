"""T-0284: host-wide full-suite test slots, `factory res` lines, and the load-aware spawn gate."""
import fcntl
import os
import tempfile
import unittest
from pathlib import Path

import _slot
from harness import FactoryTestCase


class SlotTests(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp(prefix="zz-test-slots-"))
        self.addCleanup(__import__("shutil").rmtree, self.dir, True)
        # fresh slot state, so a slot the real full run took at load time is neither closed nor counted here
        for name, fresh in (("_held", []), ("_modules", set()), ("_taken", False)):
            self.addCleanup(setattr, _slot, name, getattr(_slot, name))
            setattr(_slot, name, fresh)
        self.addCleanup(_slot.release)
        self.out = open(os.devnull, "w")
        self.addCleanup(self.out.close)
        self.waits = []

    def modules(self, n):
        return {f"zz_mod_{i}" for i in range(n)}

    def feed(self, n, env, **kw):
        for m in sorted(self.modules(n)):
            _slot.note_module(m, env=env, directory=self.dir, n=2, out=self.out, **kw)

    def held(self):
        return sum(1 for f in _slot._held)

    def test_targeted_run_takes_no_slot(self):
        self.feed(_slot.FULL_RUN_MODULES, {})
        self.assertEqual(self.held(), 0)
        self.assertEqual(list(self.dir.glob("*")), [])

    def test_full_run_takes_a_slot(self):
        self.feed(_slot.FULL_RUN_MODULES + 1, {})
        self.assertEqual(self.held(), 1)
        self.assertEqual((self.dir / "slot-0.lock").read_text().strip(), str(os.getpid()))

    def test_ci_takes_no_slot(self):
        self.feed(_slot.FULL_RUN_MODULES + 20, {"CI": "true"})
        self.assertEqual(self.held(), 0)

    def test_third_full_run_waits_until_a_slot_frees(self):
        # two other processes hold both slots (flock is per open file description, so two opens in this process do too)
        others = [open(self.dir / f"slot-{i}.lock", "a+") for i in range(2)]
        for f in others:
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        ticks = []

        def sleep(_s):
            ticks.append(1)
            if len(ticks) == 3:  # a slot frees while the third run waits
                others[1].close()

        clock = iter(range(0, 1000)).__next__
        got = _slot.acquire(self.dir, 2, wait_max=600, sleep=sleep, clock=clock, out=self.out)
        self.assertEqual(got, 1)
        self.assertEqual(len(ticks), 3)
        others[0].close()

    def test_require_never_runs_slotless(self):
        """T-0515: the gate's mode: past wait_max it returns None with an ERROR and does not say 'running without one'."""
        others = [open(self.dir / f"slot-{i}.lock", "a+") for i in range(2)]
        for f in others:
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        out = Path(self.dir / "out.txt").open("w+")
        clock = iter(range(0, 100000, 10)).__next__
        got = _slot.acquire(self.dir, 2, wait_max=100, sleep=lambda s: None, clock=clock, out=out, require=True)
        self.assertIsNone(got)
        out.seek(0)
        text = out.read()
        self.assertIn("ERROR: no test slot free after 100 s; not running without one", text)
        self.assertNotIn("running without one\n", text.replace("not running without one", ""))
        out.close()
        for f in others:
            f.close()
        self.assertEqual(_slot.acquire(self.dir, 2, wait_max=0, out=self.out, require=True), 0)  # free again: taken

    def test_slot_held_by_a_parent_is_not_taken_again(self):
        self.feed(_slot.FULL_RUN_MODULES + 20, {"FACTORY_TEST_SLOT_HELD": "1"})
        self.assertEqual(self.held(), 0)

    def test_waits_then_runs_anyway_with_a_warning(self):
        others = [open(self.dir / f"slot-{i}.lock", "a+") for i in range(2)]
        for f in others:
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        out = Path(self.dir / "out.txt").open("w+")
        clock = iter(range(0, 100000, 10)).__next__
        got = _slot.acquire(self.dir, 2, wait_max=100, notice=30, sleep=lambda s: None, clock=clock, out=out)
        self.assertIsNone(got)
        out.seek(0)
        text = out.read()
        self.assertIn("waiting for a test slot", text)
        self.assertIn("WARNING: no test slot free after 100 s", text)
        self.assertGreaterEqual(text.count("waiting for a test slot"), 2)  # repeated every 30 s
        out.close()
        for f in others:
            f.close()

    def test_pre_held_slot_survives_the_slot_tests(self):
        # the slot a real full run took at load time must not be closed by this class's setUp/cleanup
        real = open(self.dir / "pre.lock", "a+")
        self.addCleanup(real.close)
        outer_held, outer_modules, outer_taken = _slot._held, _slot._modules, _slot._taken
        # emulate the real run: undo this test's isolation, take a slot, then run a nested SlotTests setUp/cleanup
        suite = unittest.TestSuite([SlotTests("test_full_run_takes_a_slot")])
        _slot._held = [real]
        result = unittest.TestResult()
        suite.run(result)
        self.assertEqual(result.errors + result.failures, [])
        self.assertEqual(_slot._held, [real])
        self.assertFalse(real.closed)
        _slot._held, _slot._modules, _slot._taken = outer_held, outer_modules, outer_taken

    def test_slot_count_from_env(self):
        old = os.environ.get("FACTORY_TEST_SLOTS")
        self.addCleanup(lambda: os.environ.pop("FACTORY_TEST_SLOTS", None) if old is None else os.environ.update(FACTORY_TEST_SLOTS=old))
        os.environ["FACTORY_TEST_SLOTS"] = "5"
        self.assertEqual(_slot.slot_count(), 5)


class ResAndLoadGateTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_task(status="todo", pr_url=None)
        self.cache = self.env.user_home / ".cache" / "factory-test-slots"
        self.held = []
        self.addCleanup(lambda: [f.close() for f in self.held])

    def hold(self, i):
        self.cache.mkdir(parents=True, exist_ok=True)
        f = open(self.cache / f"slot-{i}.lock", "a+")
        fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        self.held.append(f)

    def res(self, load="0"):
        return self.ok("res", extra_env={"FACTORY_LOADAVG": load}).stdout

    def spawn(self, role="coder", load="0"):
        return self.ok("spawn", role, "--goal", "G-001", "--task", "T-0001", "--name", "zz-test-c1",
                       extra_env={"FACTORY_LOADAVG": load})

    def test_res_shows_test_runs_none_held(self):
        self.assertIn("test runs: 0 full suites running (slots 0/2)", self.res())

    def test_res_shows_test_runs_held(self):
        self.hold(0)
        self.assertIn("test runs: 1 full suites running (slots 1/2)", self.res())
        self.hold(1)
        self.assertIn("test runs: 2 full suites running (slots 2/2)", self.res())

    def test_res_slot_count_follows_config(self):
        self.ok("config", "test_slots", "3", agent=None)
        self.assertIn("(slots 0/3)", self.res())

    def test_load_gate_holds_above_ratio(self):
        cap = 1.25 * (os.cpu_count() or 1)
        out = self.res(load=str(cap + 1))
        self.assertIn("spawn gate: CLOSED", out)
        self.assertIn("load1", out)
        self.assertIn("HOLDING spawns", out)
        p = self.spawn(load=str(cap + 1))
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("load1", p.stdout)
        self.assertEqual([c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]], [])

    def test_load_gate_opens_below_ratio(self):
        cap = 1.25 * (os.cpu_count() or 1)
        out = self.res(load=str(cap - 0.5))
        self.assertIn("spawn gate: OPEN", out)
        self.assertIn("load gate: load1", out)
        self.assertNotIn("HOLDING", out)
        self.assertIn("SPAWNED", self.spawn(load=str(cap - 0.5)).stdout)

    def test_load_gate_ratio_is_configurable(self):
        self.ok("config", "gate_max_load_ratio", "100")
        self.assertIn("spawn gate: OPEN", self.res(load="50"))

    def test_hq_relaunch_is_exempt_from_the_load_gate(self):
        high = str(10 * (os.cpu_count() or 1))
        self.env.seed_agent("zz-director", "director", status="live")
        self.env.seed_agent("zz-boss", "boss", status="live")
        self.env.sql("UPDATE agents SET session_id='s-1' WHERE name='zz-director'")
        self.env.set_herdr(agents=[])
        p = self.ok("relaunch", "zz-director", "--force", agent="zz-boss", extra_env={"FACTORY_LOADAVG": high})
        self.assertIn("SPAWNED", p.stdout)
        self.assertNotIn("QUEUED", p.stdout)

    def test_fresh_hq_spawn_is_not_exempt(self):
        high = str(10 * (os.cpu_count() or 1))
        p = self.ok("spawn", "director", "--name", "zz-director", extra_env={"FACTORY_LOADAVG": high})
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("load1", p.stdout)


if __name__ == "__main__":
    unittest.main()


class FullRunThresholdTests(unittest.TestCase):
    def test_real_module_count_exceeds_the_full_run_threshold(self):
        # if the suite shrinks below FULL_RUN_MODULES, a full run would silently stop taking a slot
        mods = {p.stem for p in Path(__file__).parent.glob("test_*.py") if "FactoryTestCase" in p.read_text()}
        self.assertGreater(len(mods), _slot.FULL_RUN_MODULES)

    def test_slot_usage_probe_sees_a_held_slot_and_leaves_it_alone(self):
        # bin/factory test_slot_usage probes with LOCK_SH; an exclusive holder must still count as held
        d = Path(tempfile.mkdtemp(prefix="zz-test-slots-"))
        self.addCleanup(__import__("shutil").rmtree, d, True)
        holder = open(d / "slot-0.lock", "a+")
        self.addCleanup(holder.close)
        fcntl.flock(holder, fcntl.LOCK_EX | fcntl.LOCK_NB)
        probe = open(d / "slot-0.lock", "a+")
        self.addCleanup(probe.close)
        with self.assertRaises(BlockingIOError):
            fcntl.flock(probe, fcntl.LOCK_SH | fcntl.LOCK_NB)
        holder.close()
        fcntl.flock(probe, fcntl.LOCK_SH | fcntl.LOCK_NB)
