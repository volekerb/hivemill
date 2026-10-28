import subprocess
import unittest

from harness import FactoryTestCase, git


class StatePushTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj", title="zz-test state push goal")
        self.bare = e.home / "origin.git"
        git("init", "-q", "--bare", "-b", "main", str(self.bare), cwd=e.home)
        git("remote", "add", "origin", str(self.bare), cwd=e.home_dir)
        git("commit", "-q", "--allow-empty", "-m", "base", cwd=e.home_dir)
        git("push", "-q", "-u", "origin", "main", cwd=e.home_dir)
        e.sql("DELETE FROM config WHERE key='state_push'")  # the harness turns it on; these tests want the shipped default
        self.refs_before = git("for-each-ref", cwd=self.bare)

    def origin_has_state(self):
        return subprocess.run(["git", "show-ref", "refs/heads/state"], cwd=self.bare, capture_output=True).returncode == 0

    def local_state(self):
        return git("rev-parse", "refs/heads/state", cwd=self.env.home_dir)

    def test_default_is_off(self):
        self.assertIn("state_push = 0", self.ok("config").stdout)

    def test_state_push_zero_keeps_state_local(self):
        self.ok("config", "git_push", "1")
        p = self.ok("export")
        self.assertIn("state push disabled (state_push=0)", p.stdout)
        self.assertFalse(self.origin_has_state())
        self.assertTrue(self.local_state())
        self.assertEqual(git("for-each-ref", cwd=self.bare), self.refs_before)

    def test_state_push_one_pushes(self):
        self.ok("config", "git_push", "1")
        self.ok("config", "state_push", "1")
        p = self.ok("export")
        self.assertNotIn("state push disabled", p.stdout)
        self.assertTrue(self.origin_has_state())
        self.assertEqual(git("rev-parse", "refs/heads/state", cwd=self.bare), self.local_state())

    def test_git_push_zero_blocks_state_push(self):
        self.ok("config", "state_push", "1")
        self.ok("export")
        self.assertFalse(self.origin_has_state())
        self.assertEqual(git("for-each-ref", cwd=self.bare), self.refs_before)
        self.assertTrue(self.local_state())

    def test_enabling_later_pushes_unpushed_state_commit(self):
        self.ok("config", "git_push", "1")
        self.ok("export")
        self.assertFalse(self.origin_has_state())
        self.ok("config", "state_push", "1")
        self.ok("export")
        self.assertEqual(git("rev-parse", "refs/heads/state", cwd=self.bare), self.local_state())


if __name__ == "__main__":
    unittest.main()
