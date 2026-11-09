"""T-1198: the tick heals LIVE_HOME before sync_pull (origin URL and detached HEAD)."""

import subprocess
import unittest
from unittest import mock

from harness import FactoryTestCase, git

GOOD = "https://github.com/example-org/factory.git"
BAD = "https://github.com/evil/archived.git"


class LiveCheckoutHealTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj", title="zz-test heal goal")
        e.seed_agent("zz-test-director", "director")
        e.seed_agent("zz-test-boss", "boss")
        self.h = e.home_dir
        self.bare = e.home / "origin.git"
        git("init", "-q", "--bare", "-b", "main", str(self.bare), cwd=e.home)
        git("remote", "add", "origin", str(self.bare), cwd=self.h)
        git("commit", "-q", "--allow-empty", "-m", "base", cwd=self.h)
        git("push", "-q", "-u", "origin", "main", cwd=self.h)
        self.factory_url = str(self.bare)
        self.env.sql("UPDATE config SET value=? WHERE key='factory_remote_url'", (self.factory_url,))
        git("config", "remote.origin.url", self.factory_url, cwd=self.h)
        self.ok("config", "git_push", "1")

    def heal_events(self):
        return self.env.sql("SELECT detail FROM events WHERE action='live-checkout-heal'")

    def heal_reports(self):
        return self.env.sql("SELECT recipient, subject FROM messages WHERE sender='tick' AND subject LIKE 'live checkout heal%'")

    def fail_alerts(self):
        return self.env.sql("SELECT recipient, subject FROM messages WHERE sender='tick' AND subject LIKE 'live checkout heal failed%'")

    def run_heal(self):
        with self.env.load_factory() as mod:
            mod.LIVE_HOME = str(self.h)
            with mock.patch.object(mod, "home_is_live", return_value=True):
                con = mod.db()
                try:
                    mod.tick_live_checkout_heal(con)
                finally:
                    con.close()
        return mod

    def test_origin_healed_from_factory_remote_url(self):
        self.ok("config", "factory_remote_url", GOOD)
        git("config", "remote.origin.url", BAD, cwd=self.h)
        self.run_heal()
        self.assertEqual(git("config", "remote.origin.url", cwd=self.h), GOOD)
        ev = self.heal_events()
        self.assertEqual(len(ev), 1)
        self.assertIn("remote.origin.url", ev[0]["detail"])
        reps = self.heal_reports()
        self.assertEqual({r["recipient"] for r in reps}, {"zz-test-director", "zz-test-boss"})

    def test_empty_factory_remote_url_does_not_heal_or_tofu(self):
        self.env.sql("UPDATE config SET value='' WHERE key='factory_remote_url'")
        git("config", "remote.origin.url", BAD, cwd=self.h)
        with self.env.load_factory() as mod:
            mod.LIVE_HOME = str(self.h)
            with mock.patch.object(mod, "home_is_live", return_value=True):
                with mock.patch.object(mod, "factory_remote_url", wraps=mod.factory_remote_url) as tofu:
                    con = mod.db()
                    try:
                        mod.tick_live_checkout_heal(con)
                    finally:
                        con.close()
                    tofu.assert_not_called()
        self.assertEqual(git("config", "remote.origin.url", cwd=self.h), BAD)
        self.assertEqual(self.heal_events(), [])
        self.assertTrue(self.fail_alerts())
        self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action='tofu'"), [])

    def test_detached_clean_head_switched_with_untracked_kept(self):
        git("checkout", "-q", "--detach", "HEAD", cwd=self.h)
        (self.h / "scratch.txt").write_text("stay\n")
        before = git("rev-parse", "HEAD", cwd=self.h)
        self.run_heal()
        head = subprocess.run(["git", "symbolic-ref", "-q", "HEAD"], cwd=self.h, capture_output=True).stdout
        self.assertEqual(head.decode().strip(), "refs/heads/main")
        self.assertTrue((self.h / "scratch.txt").is_file())
        self.assertEqual(git("rev-parse", "HEAD", cwd=self.h), git("rev-parse", "main", cwd=self.h))
        ev = self.heal_events()
        self.assertEqual(len(ev), 1)
        self.assertIn("HEAD", ev[0]["detail"])
        self.assertEqual({r["recipient"] for r in self.heal_reports()}, {"zz-test-director", "zz-test-boss"})

    def test_detached_dirty_tracked_not_switched(self):
        (self.h / "dirty.txt").write_text("x\n")
        git("add", "dirty.txt", cwd=self.h)
        git("commit", "-q", "-m", "track dirty", cwd=self.h)
        git("checkout", "-q", "--detach", "HEAD", cwd=self.h)
        (self.h / "dirty.txt").write_text("changed\n")
        head_before = git("rev-parse", "HEAD", cwd=self.h)
        with self.env.load_factory() as mod:
            mod.LIVE_HOME = str(self.h)
            calls = []
            real = mod._live_heal_git

            def spy(repo, *args, **kw):
                calls.append(args)
                return real(repo, *args, **kw)

            with mock.patch.object(mod, "home_is_live", return_value=True):
                with mock.patch.object(mod, "_live_heal_git", spy):
                    con = mod.db()
                    try:
                        mod.tick_live_checkout_heal(con)
                    finally:
                        con.close()
        self.assertEqual(git("rev-parse", "HEAD", cwd=self.h), head_before)
        self.assertNotIn(tuple(["switch", "main"]), calls)
        self.assertNotIn(tuple(["checkout", "main"]), calls)
        self.assertNotIn(tuple(["reset"]), [(a[0],) if a else () for a in calls])
        self.assertEqual(self.heal_events(), [])
        self.assertEqual({r["recipient"] for r in self.fail_alerts()}, {"zz-test-director", "zz-test-boss"})

    def test_sync_pull_ff_only_does_not_move_main_back_when_origin_behind(self):
        git("commit", "-q", "--allow-empty", "-m", "ahead", cwd=self.h)
        ahead = git("rev-parse", "HEAD", cwd=self.h)
        self.assertNotEqual(ahead, git("rev-parse", "origin/main", cwd=self.h))
        with self.env.load_factory() as mod:
            mod.LIVE_HOME = str(self.h)
            with mock.patch.object(mod, "home_is_live", return_value=True):
                con = mod.db()
                try:
                    mod.tick_live_checkout_heal(con)
                    mod.sync_pull(con, push_ok=True)
                finally:
                    con.close()
        self.assertEqual(git("rev-parse", "main", cwd=self.h), ahead)
        self.assertNotEqual(git("rev-parse", "main", cwd=self.h), git("rev-parse", "origin/main", cwd=self.h))

    def test_detached_clean_refused_filter_does_not_switch(self):
        script = self.env.home / "clean.sh"
        script.write_text("#!/bin/sh\necho clean\n")
        script.chmod(0o755)
        git("config", "filter.x.clean", str(script), cwd=self.h)
        git("checkout", "-q", "--detach", "HEAD", cwd=self.h)
        head_before = git("rev-parse", "HEAD", cwd=self.h)
        sym_before = subprocess.run(
            ["git", "symbolic-ref", "-q", "HEAD"], cwd=self.h, capture_output=True,
        ).stdout.decode().strip()
        self.assertEqual(sym_before, "")
        with self.env.load_factory() as mod:
            mod.LIVE_HOME = str(self.h)
            calls = []
            real = mod._live_heal_git

            def spy(repo, *args, **kw):
                calls.append(args)
                return real(repo, *args, **kw)

            with mock.patch.object(mod, "home_is_live", return_value=True):
                with mock.patch.object(mod, "_live_heal_git", spy):
                    con = mod.db()
                    try:
                        mod.tick_live_checkout_heal(con)
                    finally:
                        con.close()
        self.assertEqual(git("rev-parse", "HEAD", cwd=self.h), head_before)
        sym_after = subprocess.run(
            ["git", "symbolic-ref", "-q", "HEAD"], cwd=self.h, capture_output=True,
        ).stdout.decode().strip()
        self.assertEqual(sym_after, "")
        self.assertNotIn(tuple(["switch", "main"]), calls)
        self.assertEqual(self.heal_events(), [])
        alerts = self.fail_alerts()
        self.assertEqual({r["recipient"] for r in alerts}, {"zz-test-director", "zz-test-boss"})
        self.assertTrue(any("filter.x.clean" in r["subject"] for r in alerts))

    def test_other_refused_config_keys_not_removed(self):
        git("config", "--file", f"{self.h}/.git/config", "include.path", "/tmp/evil", cwd=self.h)
        self.run_heal()
        self.assertIn("/tmp/evil", (self.h / ".git" / "config").read_text())
        with self.env.load_factory() as mod:
            mod.LIVE_HOME = str(self.h)
            with mock.patch.object(mod, "home_is_live", return_value=True):
                con = mod.db()
                try:
                    self.assertFalse(mod.sync_pull(con))
                finally:
                    con.close()
        refused = self.env.sql("SELECT detail FROM events WHERE action='git-config-refused'")
        self.assertTrue(refused)
        self.assertIn("include", refused[-1]["detail"])

    def test_temp_home_does_not_heal_or_touch_live_db(self):
        """A temp FACTORY_HOME tick must not heal LIVE_HOME or record heal-fail keys."""
        decoy = self.env.home / "decoy-live-factory"
        decoy.mkdir()
        git("init", "-q", "-b", "main", cwd=decoy)
        git("commit", "-q", "--allow-empty", "-m", "decoy", cwd=decoy)
        git("config", "remote.origin.url", BAD, cwd=decoy)
        self.ok("config", "factory_remote_url", GOOD)
        with self.env.load_factory() as mod:
            mod.LIVE_HOME = str(decoy)
            self.assertFalse(mod.home_is_live())
            con = mod.db()
            try:
                mod.tick_live_checkout_heal(con)
            finally:
                con.close()
        rows = self.env.sql("SELECT 1 FROM config WHERE key='last_live_checkout_heal_fail_ts'")
        self.assertEqual(rows, [])
        self.assertEqual(self.heal_events(), [])
        self.assertEqual(self.fail_alerts(), [])
        self.assertEqual(git("config", "remote.origin.url", cwd=decoy), BAD)


if __name__ == "__main__":
    unittest.main()
