"""G-021 T-0696: the git pre-push public-text guard that `factory spawn` installs in every task worktree
(ci/pre_push_public_text.py through the hook git runs, real `git push` to a local bare remote). The owner term is
built at run time so no file holds it."""
import os
import subprocess
import unittest

from harness import FactoryTestCase, git

TERM = "zebra" + "corp"


class PrePushTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj", title="zz-test pre-push goal")
        self.h = e.home_dir
        self.bare = e.home / "acme" / "pub.git"
        self.bare.parent.mkdir()
        git("init", "-q", "--bare", "-b", "main", str(self.bare), cwd=e.home)
        git("remote", "add", "origin", str(self.bare), cwd=self.h)
        git("commit", "-q", "--allow-empty", "-m", "base", cwd=self.h)
        git("push", "-q", "-u", "origin", "main", cwd=self.h)
        e.sql("UPDATE projects SET repo_path=? WHERE id='zz-test-proj'", (str(self.h),))
        e.seed_task(goal="G-001", status="in_progress")
        self.listfile = e.home / "owner-list.txt"
        self.listfile.write_text(TERM + "\n")
        e.sql("INSERT OR REPLACE INTO config(key, value) VALUES ('scrub_terms_file', ?)", (str(self.listfile),))
        self.set_public("acme/pub")
        with e.load_factory() as mod:
            con = mod.db()
            self.wt = mod.prepare_worktree(con, "T-0001", "coder")
            con.commit()

    def set_public(self, slugs):
        self.env.sql("INSERT OR REPLACE INTO config(key, value) VALUES ('public_text_repos', ?)", (slugs,))

    def commit(self, msg):
        git("commit", "-q", "--allow-empty", "-m", msg, cwd=self.wt)

    def push(self, ref="factory/t-0001"):
        env = {k: v for k, v in os.environ.items() if k not in ("SCRUB_TERMS_FILE", "FACTORY_AGENT")}
        env["FACTORY_HOME"] = str(self.h)
        return subprocess.run(["git", "push", "origin", f"HEAD:{ref}"], cwd=self.wt, capture_output=True, text=True,
                              env=env, timeout=60)

    def pushed(self):
        return subprocess.run(["git", "-C", str(self.bare), "branch", "--list", "factory/*"], capture_output=True,
                              text=True).stdout.strip()

    def test_hook_is_installed_in_the_new_worktree(self):
        hook = self.h / ".git" / "hooks" / "pre-push"
        self.assertTrue(os.access(hook, os.X_OK))
        self.assertIn("pre_push_public_text.py", hook.read_text())

    def test_hit_in_an_older_commit_of_the_range_is_refused(self):
        self.commit(f"add {TERM} notes")
        self.commit("fix the notes")
        p = self.push()
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("pre-push: refused (public text:", p.stderr)
        self.assertIn("owner-term", p.stderr)
        self.assertNotIn(TERM, p.stdout + p.stderr)
        self.assertEqual(self.pushed(), "")

    def test_hit_in_the_branch_name_is_refused(self):
        self.commit("clean")
        p = self.push(ref=f"factory/{TERM}-fix")
        self.assertNotEqual(p.returncode, 0)
        self.assertNotIn(TERM, p.stdout + p.stderr)

    def test_clean_range_is_pushed(self):
        self.commit("T-0001: add a thing")
        p = self.push()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("factory/t-0001", self.pushed())

    def test_non_public_remote_is_unaffected(self):
        self.set_public("someone/else")
        self.commit(f"add {TERM} notes")
        p = self.push()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("factory/t-0001", self.pushed())

    def test_missing_owner_list_fails_closed(self):
        self.env.sql("UPDATE config SET value=? WHERE key='scrub_terms_file'", (str(self.h / "no-such-list.txt"),))
        self.commit("clean")
        p = self.push()
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("pre-push: refused (cannot read terms file", p.stderr)
        self.assertEqual(self.pushed(), "")

    def hook_run(self, url, line=None):
        """The installed hook as git runs it: REMOTE URL args and one ref line on stdin."""
        head = git("rev-parse", "HEAD", cwd=self.wt)
        line = line or f"refs/heads/b {head} refs/heads/factory/b {'0' * 40}\n"
        env = {k: v for k, v in os.environ.items() if k not in ("SCRUB_TERMS_FILE", "FACTORY_AGENT")}
        return subprocess.run([str(self.h / ".git" / "hooks" / "pre-push"), "origin", url], cwd=self.wt, input=line,
                              capture_output=True, text=True, env=env, timeout=60)

    def test_userless_scp_url_of_a_public_repo_is_checked(self):
        self.commit(f"add {TERM} notes")
        p = self.hook_run("github.com:acme/pub")
        self.assertEqual(p.returncode, 1)
        self.assertIn("owner-term", p.stderr)
        self.assertNotIn(TERM, p.stderr)
        self.assertEqual(self.hook_run("git" + chr(64) + "github.com:other/repo.git").returncode, 0)

    def test_unparsable_remote_url_is_checked_not_skipped(self):
        self.commit(f"add {TERM} notes")
        self.assertEqual(self.hook_run("weird").returncode, 1)

    def test_no_base_fails_closed(self):
        self.commit("clean")
        git("update-ref", "-d", "refs/remotes/origin/main", cwd=self.wt)
        p = self.hook_run("acme/pub")
        self.assertEqual(p.returncode, 1)
        self.assertIn("no base to check the push against", p.stderr)

    def test_unreadable_factory_db_fails_closed(self):
        (self.h / "db" / "factory.db").rename(self.h / "db" / "gone.db")
        p = self.hook_run("acme/pub")
        self.assertEqual(p.returncode, 1)
        self.assertIn("factory config unreadable", p.stderr)

    def test_delete_push_is_not_scanned_and_empty_list_is_a_no_op(self):
        self.commit(f"add {TERM} notes")
        self.assertEqual(self.hook_run("acme/pub", line=f"(delete) {'0' * 40} refs/heads/factory/b {'1' * 40}\n").returncode, 0)
        self.set_public("")
        self.assertEqual(self.hook_run("acme/pub").returncode, 0)

    def test_a_reused_worktree_gets_the_hook_back(self):
        hook = self.h / ".git" / "hooks" / "pre-push"
        hook.unlink()
        with self.env.load_factory() as mod:
            con = mod.db()
            mod.prepare_worktree(con, "T-0001", "coder")
        self.assertTrue(hook.exists())


if __name__ == "__main__":
    unittest.main()
