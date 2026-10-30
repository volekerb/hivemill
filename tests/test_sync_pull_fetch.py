"""G-016 T-0724: sync_pull fetches origin/main once per call. On main it ran two identical fetches (one in
_migrate_state_commits, one before the merge)."""
import contextlib
import unittest
from unittest import mock

from harness import FactoryTestCase, git

IDENT = ("-c", "user.name=t", "-c", "user.email=t@example.com")


class SyncPullFetchTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.h = self.env.home_dir
        self.origin = self.env.home / "origin.git"
        git("init", "-q", "--bare", "-b", "main", str(self.origin), cwd=self.env.home)
        git("remote", "add", "origin", str(self.origin), cwd=self.h)
        git(*IDENT, "commit", "-q", "--allow-empty", "-m", "base", cwd=self.h)
        git("push", "-q", "-u", "origin", "main", cwd=self.h)

    def push_from_other(self, name):
        other = self.env.home / f"other-{name}"
        git("clone", "-q", str(self.origin), str(other), cwd=self.env.home, local_clone_of=self.origin)
        git(*IDENT, "commit", "-q", "--allow-empty", "-m", name, cwd=other)
        git("push", "-q", "origin", "main", cwd=other)

    def run_sync(self, f, **patches):
        """sync_pull once; returns (result, number of git fetch subprocesses)."""
        real = f.git_run
        calls = []

        def spy(repo, *args, **kw):
            calls.append(args)
            return real(repo, *args, **kw)

        with contextlib.ExitStack() as stack:
            stack.enter_context(mock.patch.object(f, "git_run", spy))
            for name, val in patches.items():
                stack.enter_context(mock.patch.object(f, name, val))
            ok = f.sync_pull(f.db(), push_ok=True)
        return ok, sum(1 for a in calls if a and a[0] == "fetch")

    def test_normal_path_does_one_fetch(self):
        """Exactly 1 git fetch per sync_pull (main did 2)."""
        with self.env.load_factory() as f:
            ok, fetches = self.run_sync(f)
        self.assertTrue(ok)
        self.assertEqual(fetches, 1)

    def test_state_commits_dropped_and_new_origin_commit_fast_forwarded_in_one_call(self):
        (self.h / "exports").mkdir()
        (self.h / "exports" / "a.md").write_text("x\n")
        git("add", "exports/a.md", cwd=self.h)
        git(*IDENT, "commit", "-q", "-m", "state: old code", cwd=self.h)
        self.push_from_other("new-on-origin")
        with self.env.load_factory() as f:
            ok, fetches = self.run_sync(f)
        self.assertTrue(ok)
        self.assertEqual(fetches, 1)
        self.assertEqual(git("rev-parse", "main", cwd=self.h), git("rev-parse", "main", cwd=self.origin))
        self.assertEqual(git("log", "-1", "--format=%s", cwd=self.h), "new-on-origin")
        self.assertNotIn("state: old code", git("log", "--format=%s", cwd=self.h))

    def test_frozen_path_fetches_once_and_returns_false(self):
        self.push_from_other("new-on-origin")
        before = git("rev-parse", "main", cwd=self.h)
        with self.env.load_factory() as f:
            ok, fetches = self.run_sync(f, _sync_target=lambda con: None)
        self.assertFalse(ok)
        self.assertEqual(fetches, 1)
        self.assertEqual(git("rev-parse", "main", cwd=self.h), before)  # no merge when frozen
        self.assertEqual(git("rev-parse", "origin/main", cwd=self.h), git("rev-parse", "main", cwd=self.origin))

    def test_failed_fetch_reports_and_does_not_merge(self):
        self.push_from_other("new-on-origin")
        before = git("rev-parse", "main", cwd=self.h)
        git("remote", "set-url", "origin", str(self.env.home / "missing.git"), cwd=self.h)
        with self.env.load_factory() as f:
            ok, fetches = self.run_sync(f)
        self.assertFalse(ok)
        self.assertEqual(fetches, 1)
        self.assertEqual(git("rev-parse", "main", cwd=self.h), before)
        events = self.env.sql("SELECT detail FROM events WHERE action='sync-failed'")
        self.assertEqual(len(events), 1)
        self.assertIn("fetch", events[0]["detail"].lower())


if __name__ == "__main__":
    unittest.main()
