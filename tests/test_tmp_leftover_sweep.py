"""T-1146: over tmp_sweep_pct the tick also removes stale leftovers with a listed prefix (guard-diff-, guard-diff-seed-,
factory-screenshots-, freshbox-, zz-test-<name>-, zz-rv-, tmp<8>, tmp.<10>) directly under <tmp>: older than 24 h by newest
mtime in the tree, our uid, not an unfinished agent's cwd, not held by a process of ours (cwd or fd), symlinks never
followed; a locked tree is counted, not an error; one tmp-swept event per run, at most one run per throttle window.
Runs against FACTORY_TMP_DIR and a fake proc root only."""
import json
import os
import time
import unittest

from harness import FactoryTestCase

OVER = "1000000000,150000000"    # 85%: over the sweep line, under the gate line
ROOMY = "1000000000,500000000"   # 50%


class LeftoverSweep(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.tmp = self.env.home / "faketmp"
        self.tmp.mkdir(exist_ok=True)
        self.proc = self.env.home / "fakeproc"
        self.proc.mkdir()

    def fake_pid(self, pid, fd_target=None, cwd=None):
        p = self.proc / str(pid)
        (p / "fd").mkdir(parents=True)
        (p / "cwd").symlink_to(cwd or self.env.home)
        if fd_target:
            (p / "fd" / "3").symlink_to(fd_target)
        return p

    def leftover(self, name="guard-diff-abcd1234", age_h=48, inner_age_h=None, kb=50):
        d = self.tmp / name
        d.mkdir()
        f = d / "blob"
        f.write_text("x" * 1000 * kb)
        t = time.time() - (age_h if inner_age_h is None else inner_age_h) * 3600
        os.utime(f, (t, t))
        t = time.time() - age_h * 3600
        os.utime(d, (t, t))
        return d

    def tick(self, statvfs=OVER):
        return self.ok("tick", extra_env={"FACTORY_TMP_STATVFS": statvfs, "FACTORY_SERVE_PROC_DIR": str(self.proc)})

    def unthrottle(self):
        self.env.sql("DELETE FROM throttle WHERE key='tmpleftover'")

    def events(self):
        return self.env.sql("SELECT detail FROM events WHERE action='tmp-swept'")

    def test_stale_listed_prefixes_removed(self):
        ds = [self.leftover(n) for n in ("guard-diff-abcd1234", "guard-diff-seed-abcd1234",
                                         "factory-screenshots-abcd1234", "freshbox-abcd1234",
                                         "zz-test-persist-abcd1234", "zz-rv-abcd1234", "tmpabcd1234",
                                         "tmp.AbCd123456")]
        self.tick()
        for d in ds:
            self.assertFalse(d.exists(), d.name)

    def test_fresh_kept(self):
        d = self.leftover(age_h=2)
        self.tick()
        self.assertTrue(d.exists())

    def test_old_dir_with_fresh_file_inside_kept(self):
        d = self.leftover(age_h=48, inner_age_h=1)
        self.tick()
        self.assertTrue((d / "blob").exists())

    def test_unlisted_prefix_kept(self):
        keep = [self.leftover(n) for n in ("corp-abcd1234", "gd-abcd1234", "go-build123456", "claude-1000",
                                           "cursor-agent-abcd1234", "guard-diff-shared-1000", "zz-other-abcd1234",
                                           "tmpfoo-notes", "tmp.short", "tmp-abcd1234", "tmpABCD1234", "zz-rv-abcd12345")]
        self.tick()
        for d in keep:
            self.assertTrue(d.exists(), d.name)

    def test_other_uid_kept(self):
        # chown needs root: run the sweep as a uid that differs from the dir's owner instead
        d = self.leftover()
        other = os.getuid() + 1
        with self.env.load_factory() as f:
            f.os = type("OtherUid", (), {"__getattr__": lambda s, n: getattr(os, n), "getuid": staticmethod(lambda: other)})()
            self.assertEqual(f._sweep_leftovers(f.db(), time.time()), ([], 0))
        self.assertTrue(d.exists())

    def test_symlink_not_followed(self):
        target = self.tmp / "elsewhere"
        target.mkdir()
        (target / "precious").write_text("keep")
        old = time.time() - 48 * 3600
        os.utime(target, (old, old))
        os.utime(target / "precious", (old, old))
        link = self.tmp / "guard-diff-linkabcd"
        link.symlink_to(target)
        self.tick()
        self.assertTrue(link.is_symlink())
        self.assertTrue((target / "precious").exists())

    def test_live_agent_cwd_kept(self):
        d = self.leftover()
        inner = d / "wt"
        inner.mkdir()
        old = time.time() - 48 * 3600
        os.utime(inner, (old, old))
        os.utime(d, (old, old))
        self.env.seed_agent("zz-test-c1", "coder", goal="G-001", status="live")
        self.env.sql("UPDATE agents SET cwd=? WHERE name='zz-test-c1'", (str(inner),))
        self.tick()
        self.assertTrue(d.exists())
        self.env.sql("UPDATE agents SET status='exited' WHERE name='zz-test-c1'")
        self.unthrottle()
        self.tick()
        self.assertFalse(d.exists())

    def test_under_sweep_line_nothing_removed(self):
        d = self.leftover()
        self.tick(ROOMY)
        self.assertTrue(d.exists())
        self.assertEqual(self.events(), [])

    def test_one_event_per_run_with_mb(self):
        for i in range(3):
            self.leftover(f"freshbox-abcd123{i}", kb=1000)
        self.tick()
        ev = self.events()
        self.assertEqual(len(ev), 1)
        detail = json.loads(ev[0]["detail"])
        self.assertEqual(detail["leftovers"], 3)
        self.assertEqual(detail["dirs"], 0)
        self.assertEqual(detail["locked"], 0)
        self.assertGreaterEqual(detail["mb"], 2.8)

    def test_plain_files_of_the_tmp_shapes_removed_when_stale(self):
        old = time.time() - 48 * 3600
        names = ("tmp.AbCd123456", "tmpabcd1234")
        for n in names:
            (self.tmp / n).write_text("x")
            os.utime(self.tmp / n, (old, old))
        fresh = self.tmp / "tmp.ZyXw987654"
        fresh.write_text("x")
        self.tick()
        for n in names:
            self.assertFalse((self.tmp / n).exists(), n)
        self.assertTrue(fresh.exists())

    def test_claude_and_cursor_agent_dirs_not_removed(self):
        keep = [self.leftover(n) for n in ("claude-1000", "claude-abcd1234", "cursor-agent-abcd1234", "cursor-agent-1")]
        self.tick()
        for d in keep:
            self.assertTrue(d.exists(), d.name)

    def test_held_tmp_dir_kept_by_cwd_or_fd(self):
        a = self.leftover("tmpabcd1234")
        b = self.leftover("tmp.AbCd123456")
        c = self.leftover("tmpzzzzzzzz")
        self.fake_pid(101, cwd=a)
        self.fake_pid(102, fd_target=b / "blob")
        self.tick()
        self.assertTrue(a.exists())
        self.assertTrue(b.exists())
        self.assertFalse(c.exists())

    def test_plain_file_held_open_kept(self):
        old = time.time() - 48 * 3600
        held, free = self.tmp / "tmp.AbCd123456", self.tmp / "tmp.ZyXw987654"
        for f in (held, free):
            f.write_text("x")
            os.utime(f, (old, old))
        self.fake_pid(104, fd_target=held)
        self.tick()
        self.assertTrue(held.exists())
        self.assertFalse(free.exists())

    def test_unreadable_pid_keeps_everything(self):
        d = self.leftover("tmpabcd1234")
        p = self.fake_pid(103)
        os.chmod(p / "fd", 0)
        try:
            self.tick()
        finally:
            os.chmod(p / "fd", 0o700)
        self.assertTrue(d.exists())
        self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action='tmp-sweep-failed'"), [])

    @unittest.skipIf(os.getuid() == 0, "root reads mode-000 dirs")
    def test_locked_subdir_counted_not_failed(self):
        d = self.leftover("zz-rv-abcd1234")
        locked = d / "scratch" / "locked"
        locked.mkdir(parents=True)
        old = time.time() - 48 * 3600
        for q in (locked, d / "scratch", d):
            os.utime(q, (old, old))
        os.chmod(locked, 0)
        self.addCleanup(lambda: os.chmod(locked, 0o700))
        good = self.leftover("tmpzzzzzzzz")
        self.tick()
        self.unthrottle()
        self.tick()
        self.assertTrue(d.exists())
        self.assertFalse(good.exists())
        self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action='tmp-sweep-failed'"), [])
        ev = [json.loads(r["detail"]) for r in self.events()]
        self.assertEqual(len(ev), 2)
        self.assertEqual(ev[0]["locked"], 1)
        self.assertEqual(ev[1]["locked"], 1)

    def test_second_tick_inside_throttle_window_walks_nothing(self):
        self.tick()
        d = self.leftover()
        self.tick()
        self.assertTrue(d.exists())
        self.unthrottle()
        self.tick()
        self.assertFalse(d.exists())


if __name__ == "__main__":
    unittest.main()
