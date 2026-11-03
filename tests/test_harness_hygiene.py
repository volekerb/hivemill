"""T-0232: a FactoryEnv used outside FactoryTestCase leaves no home behind (passing, failing or failing to build), and the
live-home guard fails a test that adds a top-level entry to the live home (proved against an injected fake live home)."""
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import harness
from harness import FactoryEnv, LiveHomeGuard

PREFIX = "zz-test-factory-"


def homes(root):
    return sorted(p.name for p in Path(root).iterdir() if p.name.startswith(PREFIX))


def run_inner(case_cls, root):
    """Run case_cls with tempfile creating its dirs under root; returns the TestResult."""
    with mock.patch.object(tempfile, "tempdir", root), open(os.devnull, "w") as null:
        return unittest.TextTestRunner(stream=null).run(unittest.defaultTestLoader.loadTestsFromTestCase(case_cls))


class EnvOutsideTestCase(unittest.TestCase):
    def setUp(self):
        harness._template_home()  # build the cached template in the real tmp dir, not under the patched root
        self._root = tempfile.TemporaryDirectory()
        self.addCleanup(self._root.cleanup)
        self.root = self._root.name

    def test_context_manager_removes_home_after_passing_body(self):
        with mock.patch.object(tempfile, "tempdir", self.root):
            with FactoryEnv() as env:
                self.assertEqual(len(homes(self.root)), 1)
                self.assertTrue(env.home.exists())
        self.assertEqual(homes(self.root), [])

    def test_context_manager_removes_home_after_failing_body(self):
        with mock.patch.object(tempfile, "tempdir", self.root):
            with self.assertRaises(ZeroDivisionError):
                with FactoryEnv():
                    1 / 0
        self.assertEqual(homes(self.root), [])

    def test_addcleanup_user_leaves_no_home_after_failing_test(self):
        class Inner(unittest.TestCase):
            def test_it(self):
                env = FactoryEnv()
                self.addCleanup(env.cleanup)
                self.assertTrue(env.home.exists())
                self.fail("boom")
        res = run_inner(Inner, self.root)
        self.assertEqual(len(res.failures), 1)
        self.assertEqual(homes(self.root), [])

    def test_constructor_failure_leaves_no_home(self):
        with mock.patch.object(tempfile, "tempdir", self.root), \
                mock.patch.object(harness.shutil, "copy2", side_effect=OSError("disk full")):
            with self.assertRaises(OSError):
                FactoryEnv()
        self.assertEqual(homes(self.root), [])


class LiveHomeGuardTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.live = Path(tmp.name) / "fakelive"
        (self.live / "state").mkdir(parents=True)
        (self.live / "db").mkdir()

    def inner(self, body):
        live = self.live

        class Inner(unittest.TestCase):
            def setUp(self):
                g = LiveHomeGuard([live])
                self.addCleanup(g.check, self)

            def test_it(self):
                body(live)
        return run_inner(Inner, tempfile.gettempdir())

    def test_new_top_level_dir_fails_the_test_with_its_path(self):
        res = self.inner(lambda live: (live / "sub").mkdir())
        self.assertEqual(len(res.failures), 1)
        self.assertIn(str(self.live / "sub"), res.failures[0][1])

    def test_new_top_level_file_fails_the_test(self):
        res = self.inner(lambda live: (live / "stray.txt").write_text("x"))
        self.assertEqual(len(res.failures), 1)

    def test_rewriting_existing_entries_and_nested_files_passes(self):
        def body(live):
            (live / "state" / "new.json").write_text("{}")  # nested: not a top-level entry
            os.utime(live / "state", (1, 1))  # mtime churn is the tick's normal behaviour
        res = self.inner(body)
        self.assertEqual((res.failures, res.errors), ([], []))

    def test_missing_live_home_is_nothing_to_guard(self):
        self.assertEqual(LiveHomeGuard([self.live / "nope"]).new_entries(), [])

    def test_guard_never_touches_the_live_home(self):
        before = {p: p.stat().st_mtime_ns for p in self.live.iterdir()}
        LiveHomeGuard([self.live]).new_entries()
        self.assertEqual({p: p.stat().st_mtime_ns for p in self.live.iterdir()}, before)


if __name__ == "__main__":
    unittest.main()
