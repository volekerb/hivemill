"""T-1146: over tmp_sweep_pct the tick also removes stale leftovers with a listed prefix (guard-diff-, guard-diff-seed-,
factory-screenshots-, freshbox-) directly under <tmp>: older than 24 h by newest mtime in the tree, our uid, not an
unfinished agent's cwd, symlinks never followed; one tmp-swept event per run. Runs against FACTORY_TMP_DIR only."""
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
        return self.ok("tick", extra_env={"FACTORY_TMP_STATVFS": statvfs})

    def events(self):
        return self.env.sql("SELECT detail FROM events WHERE action='tmp-swept'")

    def test_stale_listed_prefixes_removed(self):
        ds = [self.leftover(n) for n in ("guard-diff-abcd1234", "guard-diff-seed-abcd1234",
                                         "factory-screenshots-abcd1234", "freshbox-abcd1234")]
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
                                           "cursor-agent-abcd1234", "guard-diff-shared-1000", "zz-other-abcd1234")]
        self.tick()
        for d in keep:
            self.assertTrue(d.exists(), d.name)

    def test_other_uid_kept(self):
        # chown needs root: run the sweep as a uid that differs from the dir's owner instead
        d = self.leftover()
        other = os.getuid() + 1
        with self.env.load_factory() as f:
            f.os = type("OtherUid", (), {"__getattr__": lambda s, n: getattr(os, n), "getuid": staticmethod(lambda: other)})()
            self.assertEqual(f._sweep_leftovers(f.db(), time.time()), [])
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
        self.assertGreaterEqual(detail["mb"], 2.8)


if __name__ == "__main__":
    unittest.main()
