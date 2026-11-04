"""T-0967: the tick alerts the director once when a tracked file of the live checkout is dirty (no repair)."""
import subprocess
import unittest
from unittest import mock

from harness import FactoryTestCase, git


class LiveTreeAlertTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_agent("zz-test-director", "director")
        self.home = e.home_dir
        names = ("a.txt", "b.txt", "with space.txt", "dätei-ü.txt", "exports/gen.md", "state/gen.json")
        for name in names:
            p = self.home / name
            p.parent.mkdir(exist_ok=True)
            p.write_text("v1\n")
        git("add", "--", *names, cwd=self.home)  # not -A: db/ in the temp home is not tracked in the real checkout
        git("-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "-q", "-m", "seed", cwd=self.home)
        self.head = git("rev-parse", "HEAD", cwd=self.home)

    def alerts(self):
        return self.env.sql("SELECT * FROM messages WHERE sender='tick' AND kind='alert' AND subject LIKE 'live checkout%'")

    def hook(self, n=1):
        with self.env.load_factory() as mod:
            con = mod.db()
            for _ in range(n):
                mod.live_tree_dirty_alert(con)
            con.commit()

    def status(self):
        return git("status", "--porcelain", "--untracked-files=no", cwd=self.home)

    def test_modified_tracked_file_alerts_once(self):
        (self.home / "a.txt").write_text("v2\n")
        self.hook(2)
        rows = self.alerts()
        self.assertEqual(len(rows), 1)
        self.assertIn("a.txt", rows[0]["body"])
        self.assertIn("does not repair", rows[0]["body"])
        self.assertEqual(rows[0]["recipient"], "zz-test-director")

    def test_deleted_tracked_file_alerts_once(self):
        (self.home / "a.txt").unlink()
        self.hook(2)
        rows = self.alerts()
        self.assertEqual(len(rows), 1)
        self.assertIn(" a.txt", rows[0]["body"])

    def test_staged_change_alerts(self):
        (self.home / "a.txt").write_text("v2\n")
        git("add", "a.txt", cwd=self.home)
        self.hook()
        self.assertEqual(len(self.alerts()), 1)

    def test_untracked_file_only_no_alert(self):
        (self.home / "new.txt").write_text("x\n")
        self.hook()
        self.assertEqual(self.alerts(), [])

    def test_exports_and_state_only_no_alert(self):
        (self.home / "exports" / "gen.md").write_text("v2\n")
        (self.home / "state" / "gen.json").unlink()
        self.hook()
        self.assertEqual(self.alerts(), [])

    def test_rearm_changed_set_and_same_path_new_content(self):
        a = self.home / "a.txt"
        a.write_text("v2\n")
        self.hook()
        a.write_text("v3\n")  # new content, same path: no new alert
        self.hook()
        self.assertEqual(len(self.alerts()), 1)
        (self.home / "b.txt").write_text("v2\n")  # different path set: alerts again
        self.hook()
        self.assertEqual(len(self.alerts()), 2)
        git("checkout", "--", "a.txt", "b.txt", cwd=self.home)  # clean: clears the key
        self.hook()
        self.assertEqual(len(self.alerts()), 2)
        b = self.home / "b.txt"
        b.write_text("v9\n")  # dirty again with a path reported before: re-armed
        self.hook()
        self.assertEqual(len(self.alerts()), 3)

    def test_no_repair(self):
        (self.home / "a.txt").write_text("changed\n")
        (self.home / "b.txt").unlink()
        before = self.status()
        self.hook(2)
        self.assertEqual(len(self.alerts()), 1)
        self.assertEqual((self.home / "a.txt").read_text(), "changed\n")
        self.assertFalse((self.home / "b.txt").exists())
        self.assertEqual(self.status(), before)
        self.assertEqual(git("rev-parse", "HEAD", cwd=self.home), self.head)

    def test_spaces_and_non_ascii_paths_exact(self):
        (self.home / "with space.txt").write_text("v2\n")
        (self.home / "dätei-ü.txt").write_text("v2\n")
        self.hook()
        body = self.alerts()[0]["body"]
        self.assertIn(" M with space.txt\n", body)
        self.assertIn(" M dätei-ü.txt\n", body)

    def test_over_twenty_paths_capped_with_count(self):
        for i in range(25):
            (self.home / f"f{i:02d}.txt").write_text("v1\n")
        git("add", "--", *[f"f{i:02d}.txt" for i in range(25)], cwd=self.home)
        git("-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "-q", "-m", "many", cwd=self.home)
        for i in range(25):
            (self.home / f"f{i:02d}.txt").write_text("v2\n")
        self.hook()
        body = self.alerts()[0]["body"]
        self.assertEqual(sum(1 for l in body.splitlines() if l.startswith("   M f")), 20)
        self.assertIn("and 5 more", body)
        self.assertIn("25 path(s)", body)

    def test_git_failure_one_event_per_error_no_alert(self):
        (self.home / "a.txt").write_text("v2\n")
        bad = subprocess.CompletedProcess([], 128, b"", b"fatal: boom")
        with self.env.load_factory() as mod:
            con = mod.db()
            real = mod.git_run
            fake = lambda bad_: (lambda repo, *a, **k: bad_ if a and a[0] == "status" else real(repo, *a, **k))  # noqa: E731
            with mock.patch.object(mod, "git_run", fake(bad)):
                mod.live_tree_dirty_alert(con)
                mod.live_tree_dirty_alert(con)
            bad2 = subprocess.CompletedProcess([], 128, b"", b"fatal: other")
            with mock.patch.object(mod, "git_run", fake(bad2)):
                mod.live_tree_dirty_alert(con)
            con.commit()
        self.assertEqual(self.alerts(), [])
        self.assertEqual(len(self.env.sql("SELECT * FROM events WHERE action='live-tree-check-failed'")), 2)

    def test_git_config_refusal_no_alert_no_exception(self):
        (self.home / "a.txt").write_text("v2\n")
        with self.env.load_factory() as mod:
            con = mod.db()
            with mock.patch.object(mod, "git_config_refusal", return_value=["core.fsmonitor"]):
                mod.live_tree_dirty_alert(con)
            con.commit()
        self.assertEqual(self.alerts(), [])

    def test_wired_into_cmd_tick(self):
        (self.home / "a.txt").write_text("v2\n")
        with self.env.load_factory() as mod:
            with mock.patch.object(mod, "tick_lock", return_value=None):
                mod.cmd_tick(mock.Mock(), locked=True)
                mod.cmd_tick(mock.Mock(), locked=True)
        rows = self.alerts()
        self.assertEqual(len(rows), 1)
        self.assertIn("a.txt", rows[0]["body"])

    def test_tick_survives_hook_exception(self):
        with self.env.load_factory() as mod:
            with mock.patch.object(mod, "tick_lock", return_value=None), \
                    mock.patch.object(mod, "live_tree_dirty_alert", side_effect=OSError("boom")):
                mod.cmd_tick(mock.Mock(), locked=True)
        self.assertEqual(len(self.env.sql("SELECT * FROM events WHERE action='live-tree-alert-failed'")), 1)


if __name__ == "__main__":
    unittest.main()
