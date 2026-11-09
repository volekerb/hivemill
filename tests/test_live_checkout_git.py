"""T-1197: the live factory toplevel (FACTORY_HOME / ~/factory) must stay on main; sync_pull and git_run refuse moves."""

import subprocess
import unittest

from harness import FactoryTestCase, git


class LiveCheckoutGitTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj", title="zz-test live checkout goal")
        e.seed_agent("zz-test-director", "director")
        self.h = e.home_dir
        self.bare = e.home / "origin.git"
        git("init", "-q", "--bare", "-b", "main", str(self.bare), cwd=e.home)
        git("remote", "add", "origin", str(self.bare), cwd=self.h)
        git("commit", "-q", "--allow-empty", "-m", "base", cwd=self.h)
        git("push", "-q", "-u", "origin", "main", cwd=self.h)
        self.ok("config", "git_push", "1")
        self.ok("config", "push_interval_s", "0")
        self.wt = e.home / "worktrees" / "G-zz-0001"
        git("worktree", "add", "-q", "-b", "factory/t-zz-0001", str(self.wt), "origin/main", cwd=self.h)

    def test_sync_pull_refuses_detached_head_and_does_not_move_it(self):
        git("checkout", "-q", "--detach", "HEAD", cwd=self.h)
        before = git("rev-parse", "HEAD", cwd=self.h)
        self.assertEqual(subprocess.run(["git", "symbolic-ref", "-q", "HEAD"], cwd=self.h, capture_output=True).returncode, 1)
        with self.env.load_factory() as mod:
            con = mod.db()
            try:
                self.assertFalse(mod.sync_pull(con))
            finally:
                con.close()
        self.assertEqual(git("rev-parse", "HEAD", cwd=self.h), before)
        failed = self.env.sql("SELECT detail FROM events WHERE action='sync-failed'")
        self.assertTrue(any("HEAD is not main" in (r["detail"] or "") for r in failed), failed)

    def test_git_run_refuses_checkout_switch_reset_on_live_toplevel(self):
        head = git("rev-parse", "HEAD", cwd=self.h)
        with self.env.load_factory() as mod:
            for sub, extra in (("checkout", ("-q", "--detach", "HEAD~0")), ("switch", ("-q", "main",)),
                               ("reset", ("--hard", "HEAD"))):
                with self.subTest(sub=sub):
                    p = mod.git_run(self.h, sub, *extra)
                    self.assertNotEqual(p.returncode, 0, p.stderr)
                    self.assertIn("live factory checkout", p.stderr)
                    self.assertEqual(git("rev-parse", "HEAD", cwd=self.h), head)

    def test_git_run_allows_same_commands_in_a_worktree(self):
        with self.env.load_factory() as mod:
            p = mod.git_run(self.wt, "checkout", "-q", "-B", "factory/t-zz-0001", "origin/main")
            self.assertEqual(p.returncode, 0, p.stderr)

    def test_git_run_allows_reset_keep_on_main_for_tick(self):
        git("commit", "-q", "--allow-empty", "-m", "state: scratch", cwd=self.h)
        with self.env.load_factory() as mod:
            p = mod.git_run(self.h, "reset", "-q", "--keep", "origin/main")
            self.assertEqual(p.returncode, 0, p.stderr)


if __name__ == "__main__":
    unittest.main()
