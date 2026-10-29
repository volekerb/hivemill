"""T-0647: an in-process spawn in a test must not print SPAWNED/QUEUED lines to the runner's output, and the harness
refuses to load the CLI when FACTORY_HOME is outside the temp dir or the stub is not first on PATH."""
import contextlib
import io
import os
import unittest
from pathlib import Path

from harness import FactoryTestCase, assert_hermetic


class HermeticSpawnCase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team()

    def test_in_process_spawn_prints_nothing_to_the_runner(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            with self.env.load_factory() as f:
                f.spawn(f.db(), "lead", "G-001", "g-001-a", name="zz-hermetic")
        self.assertEqual(out.getvalue(), "")
        starts = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]
        self.assertEqual(len(starts), 1)  # the stub saw it: the spawn really ran

    def test_guard_accepts_the_harness_env(self):
        with self.env.load_factory():
            assert_hermetic(self.env.home_dir, self.env.stub_bin)

    def test_guard_rejects_a_live_home(self):
        with self.env.load_factory():
            os.environ["FACTORY_HOME"] = str(Path("~/factory").expanduser())
            with self.assertRaisesRegex(RuntimeError, "not the temp test home"):
                assert_hermetic(self.env.home_dir, self.env.stub_bin)

    def test_guard_rejects_a_real_binary_on_path(self):
        with self.env.load_factory():
            os.environ["PATH"] = "/usr/bin:/bin"
            with self.assertRaisesRegex(RuntimeError, "not the stub"):
                assert_hermetic(self.env.home_dir, self.env.stub_bin)

    def test_load_factory_refuses_a_non_temp_home(self):
        self.env.home_dir = Path("~/factory").expanduser()
        with self.assertRaisesRegex(RuntimeError, "not the temp test home"):
            with self.env.load_factory():
                pass


if __name__ == "__main__":
    unittest.main()
