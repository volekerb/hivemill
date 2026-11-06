"""T-0233: the tick sweeps stale <tmp>/zz-test-factory-<8 chars> harness homes (older than 1 h, no process inside, at most
500 per pass, hourly); everything else under <tmp> stays. Runs against FACTORY_TMP_DIR and a fake proc root only."""
import json
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

    def fake_pid(self, pid, fd_target=None, cwd=None, comm=None):
        p = self.proc / str(pid)
        (p / "fd").mkdir(parents=True)
        if comm is not None:
            (p / "comm").write_text(comm + "\n")
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
        p = self.fake_pid(4244, comm="bash")
        (p / "fd").chmod(0)
        try:
            self.tick()
            self.assertTrue(d.exists())
        finally:
            (p / "fd").chmod(0o700)

    def blocked(self):
        return [json.loads(r["detail"]) for r in self.env.sql("SELECT detail FROM events WHERE action='tmp-harness-blocked'")]

    def unreadable(self, pid, comm):
        """A same-uid pid whose fd dir gives EACCES (like a non-dumpable daemon)."""
        p = self.fake_pid(pid, comm=comm)
        os.chmod(p / "fd", 0)
        self.addCleanup(lambda: os.chmod(p / "fd", 0o700))
        return p

    @unittest.skipIf(os.getuid() == 0, "root reads any dir")
    def test_unreadable_listed_pids_do_not_block_sweep(self):
        d = self.home_dir()
        held = self.home_dir("zz-test-factory-wxyz5678")
        for i, comm in enumerate(("systemd", "(sd-pam)", "ssh-agent", "sshd-session")):
            self.unreadable(300 + i, comm)
        self.fake_pid(310, cwd=held / "sub")
        self.tick()
        self.assertFalse(d.exists())
        self.assertTrue(held.exists())
        self.assertEqual(self.blocked(), [])
        self.assertIn('"kept_busy": 1', self.events()[0]["detail"])

    @unittest.skipIf(os.getuid() == 0, "root reads any dir")
    def test_unreadable_unlisted_pid_keeps_all_and_is_named(self):
        d = self.home_dir()
        self.unreadable(320, "systemd")
        self.unreadable(321, "systemd-logind")
        self.tick()
        self.assertTrue(d.exists())
        self.assertEqual(self.events(), [])
        b = self.blocked()
        self.assertEqual(len(b), 1)
        self.assertEqual((b[0]["pid"], b[0]["comm"], b[0]["kept"]), ("321", "systemd-logind", 1))
        self.assertGreater(b[0]["mb"], -1)

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

    @unittest.skipIf(os.getuid() == 0, "root deletes anything")
    def test_undeletable_dir_does_not_block_the_rest(self):
        stuck = self.home_dir("zz-test-factory-aaaaaaaa")
        (stuck / "sub").chmod(0o555)
        other = self.home_dir("zz-test-factory-bbbbbbbb")
        try:
            self.tick()
            self.assertTrue(stuck.exists())
            self.assertFalse(other.exists())
            self.assertIn('"count": 1', self.events()[0]["detail"])
        finally:
            (stuck / "sub").chmod(0o755)

    @unittest.skipIf(os.getuid() == 0, "root reads anything")
    def test_unreadable_subdir_keeps_dir_and_others_still_swept(self):
        # mode-000 subdir holding a fresh file (unreadable), and a mode-600 subdir (listable, children not stat-able)
        locked = self.home_dir("zz-test-factory-aaaaaaaa")
        (locked / "hid").mkdir()
        (locked / "hid" / "fresh").write_text("new")
        (locked / "hid").chmod(0o000)
        nosearch = self.home_dir("zz-test-factory-bbbbbbbb")
        (nosearch / "sub").chmod(0o600)
        other = self.home_dir("zz-test-factory-cccccccc")
        try:
            self.tick()
            self.assertTrue((locked / "sub" / "f").exists())
            self.assertTrue(nosearch.exists())
            self.assertFalse(other.exists())
            self.assertIn('"count": 1', self.events()[0]["detail"])
            unm = self.env.sql("SELECT detail FROM events WHERE action='tmp-harness-unmeasured'")
            self.assertEqual(len(unm), 1)
            self.assertIn('"count": 2', unm[0]["detail"])
        finally:
            (locked / "hid").chmod(0o755)
            (nosearch / "sub").chmod(0o755)

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
