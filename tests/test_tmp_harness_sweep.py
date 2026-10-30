"""T-0233: the tick sweeps stale <tmp>/zz-test-factory-<8 chars> harness homes (older than 1 h, no process inside, at most
500 per pass, hourly); everything else under <tmp> stays. Runs against FACTORY_TMP_DIR and a fake proc root only."""
import os
import time
import unittest

from harness import FactoryTestCase


class HarnessSweep(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.tmp = self.env.home / "faketmp"
        self.tmp.mkdir(exist_ok=True)
        self.proc = self.env.home / "fakeproc"
        self.proc.mkdir()

    def home_dir(self, name="zz-test-factory-abcd1234", age_h=2, parent=None):
        d = (parent or self.tmp) / name
        (d / "sub").mkdir(parents=True)
        (d / "sub" / "f").write_text("x" * 1000)
        t = time.time() - age_h * 3600
        for p in (d / "sub" / "f", d / "sub", d):
            os.utime(p, (t, t))
        return d

    def tick(self):
        return self.ok("tick", extra_env={"FACTORY_SERVE_PROC_DIR": str(self.proc)})

    def events(self):
        return self.env.sql("SELECT detail FROM events WHERE action='tmp-harness-swept'")

    def fake_pid(self, pid, fd_target=None, cwd=None):
        p = self.proc / str(pid)
        (p / "fd").mkdir(parents=True)
        (p / "cwd").symlink_to(cwd or self.env.home)
        if fd_target:
            (p / "fd" / "3").symlink_to(fd_target)
        return p

    def test_old_dir_removed_with_one_event(self):
        d = self.home_dir()
        self.tick()
        self.assertFalse(d.exists())
        ev = self.events()
        self.assertEqual(len(ev), 1)
        self.assertIn('"count": 1', ev[0]["detail"])
        self.assertIn('"kept_busy": 0', ev[0]["detail"])

    def test_recent_file_keeps_dir_and_no_event(self):
        d = self.home_dir()
        (d / "sub" / "new").write_text("fresh")
        t = time.time() - 600
        os.utime(d / "sub" / "new", (t, t))
        self.tick()
        self.assertTrue(d.exists())
        self.assertEqual(self.events(), [])

    def test_fd_inside_keeps_dir(self):
        d = self.home_dir()
        gone = self.home_dir("zz-test-factory-wxyz5678")
        self.fake_pid(4242, fd_target=d / "sub" / "f")
        self.tick()
        self.assertTrue(d.exists())
        self.assertFalse(gone.exists())
        self.assertIn('"kept_busy": 1', self.events()[0]["detail"])

    def test_cwd_inside_keeps_dir(self):
        d = self.home_dir()
        self.fake_pid(4243, cwd=d / "sub")
        self.tick()
        self.assertTrue(d.exists())

    @unittest.skipIf(os.getuid() == 0, "root reads any dir")
    def test_unreadable_fd_dir_removes_nothing(self):
        d = self.home_dir()
        p = self.fake_pid(4244)
        (p / "fd").chmod(0)
        try:
            self.tick()
            self.assertTrue(d.exists())
        finally:
            (p / "fd").chmod(0o700)

    def test_non_candidates_kept(self):
        outside = self.env.home / "outside"
        outside.mkdir()
        (outside / "keep").write_text("keep")
        t = time.time() - 7200
        os.utime(outside, (t, t))
        link = self.tmp / "zz-test-factory-abcd1234"
        link.symlink_to(outside)
        keep = [self.home_dir("zz-test-factory-x"), self.home_dir("zz-test-factoryabcd1234"),
                self.home_dir("zz-test-factory-abcd12345"),
                self.home_dir(parent=self.tmp / "nested")]
        self.tick()
        self.assertTrue(link.is_symlink())
        self.assertEqual((outside / "keep").read_text(), "keep")
        for d in keep:
            self.assertTrue(d.exists(), d)
        self.assertEqual(self.events(), [])

    def test_cap_500_per_pass(self):
        for i in range(600):
            d = self.tmp / f"zz-test-factory-{i:08d}"
            d.mkdir()
            os.utime(d, (time.time() - 7200,) * 2)
        left = lambda: len([x for x in self.tmp.iterdir() if x.name.startswith("zz-test-factory-")])
        self.tick()
        self.assertEqual(left(), 100)
        self.env.sql("DELETE FROM throttle WHERE key='tmpharness'")
        self.tick()
        self.assertEqual(left(), 0)
        self.assertEqual([('"count": 500' in e["detail"]) for e in self.events()].count(True), 1)

    def test_runs_at_most_hourly(self):
        self.home_dir()
        self.tick()
        d = self.home_dir("zz-test-factory-wxyz5678")
        self.tick()
        self.assertTrue(d.exists())
        self.assertEqual(len(self.events()), 1)
        self.assertEqual(len(self.env.sql("SELECT 1 FROM throttle WHERE key='tmpharness'")), 1)


if __name__ == "__main__":
    unittest.main()
