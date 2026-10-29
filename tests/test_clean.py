import unittest

from harness import FactoryTestCase, git

URL1 = "https://github.com/zz-test/repo/pull/1"
URL2 = "https://github.com/zz-test/repo/pull/2"


class CleanTests(FactoryTestCase):
    """A real temp repo with a local bare origin; PR states come from the gh stub."""

    def setUp(self):
        super().setUp()
        e = self.env
        self.origin = e.home / "origin.git"
        self.repo = e.home / "repo"
        git("init", "-q", "--bare", "-b", "main", str(self.origin), cwd=e.home)
        git("clone", "-q", str(self.origin), str(self.repo), cwd=e.home)
        git("checkout", "-q", "-b", "main", cwd=self.repo)
        (self.repo / "README").write_text("x\n")
        git("add", "README", cwd=self.repo)
        git("commit", "-q", "-m", "init", cwd=self.repo)
        git("push", "-q", "-u", "origin", "main", cwd=self.repo)
        (e.home_dir / "worktrees").mkdir()
        e.seed_project("zz-test-proj")
        e.sql("UPDATE projects SET repo_path=? WHERE id='zz-test-proj'", (str(self.repo),))
        e.seed_goal(project="zz-test-proj")
        e.seed_agent("zz-test-cleaner", "cleaner", goal="G-001")
        e.seed_agent("zz-test-coder", "coder", goal="G-001", status="exited")

    def task(self, n=1, status="done", pr_state="MERGED", url=URL1, push=True, wt_parent=None):
        """Task T-000<n> with a worktree on factory/t-000<n> holding one commit (pushed to origin by default)."""
        e = self.env
        tid, branch = f"T-000{n}", f"factory/t-000{n}"
        wt = (wt_parent or e.home_dir / "worktrees") / f"G-001-{tid}"
        git("worktree", "add", "-q", "-b", branch, str(wt), "origin/main", cwd=self.repo)
        (wt / f"f{n}").write_text("work\n")
        git("add", f"f{n}", cwd=wt)
        git("commit", "-q", "-m", f"work {n}", cwd=wt)
        if push:
            git("push", "-q", "origin", branch, cwd=wt)
        sha = git("rev-parse", "HEAD", cwd=wt)
        e.seed_task(tid, status=status, pr_url=url if pr_state else None)
        e.sql("UPDATE tasks SET worktree=?, branch=? WHERE id=?", (str(wt), branch, tid))
        if pr_state:
            e.seed_pr(url, sha=sha, state=pr_state)
        return wt, branch, sha

    def clean(self, *args, agent="zz-test-cleaner"):
        return self.ok("clean", *args, agent=agent)

    def branches(self):
        return git("branch", "--list", "factory/*", cwd=self.repo)

    def test_clean_merged_removed(self):
        wt, branch, _ = self.task()
        p = self.clean()
        self.assertIn("removed", p.stdout)
        self.assertFalse(wt.exists())
        self.assertEqual(self.branches(), "")
        self.assertNotIn(str(wt), git("worktree", "list", cwd=self.repo))
        ev = self.env.sql("SELECT actor,detail FROM events WHERE entity='task' AND entity_id='T-0001' AND action='cleaned'")
        self.assertEqual(len(ev), 1)
        self.assertEqual(ev[0]["actor"], "zz-test-cleaner")
        self.assertIn(branch, ev[0]["detail"])
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")
        self.assertIn("1 removed", p.stdout)

    def test_clean_dry_run_noop(self):
        wt, branch, _ = self.task()
        before = git("worktree", "list", cwd=self.repo)
        p = self.clean("--dry-run")
        self.assertIn("would remove", p.stdout)
        self.assertTrue(wt.exists())
        self.assertIn(branch, self.branches())
        self.assertEqual(git("worktree", "list", cwd=self.repo), before)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action='cleaned'")[0]["c"], 0)

    def test_clean_unpushed_kept(self):
        wt, branch, sha = self.task()
        (wt / "extra").write_text("more\n")
        git("add", "extra", cwd=wt)
        git("commit", "-q", "-m", "local only", cwd=wt)
        p = self.clean()
        self.assertIn("unpushed commits", p.stdout)
        self.assertTrue(wt.exists())
        self.assertIn(branch, self.branches())

    def test_clean_unpushed_on_detached_worktree_head_kept(self):
        wt, branch, sha = self.task()
        git("checkout", "-q", "--detach", cwd=wt)
        (wt / "extra").write_text("more\n")
        git("add", "extra", cwd=wt)
        git("commit", "-q", "-m", "detached local", cwd=wt)
        self.assertIn("unpushed commits", self.clean().stdout)
        self.assertTrue(wt.exists())

    def test_clean_dirty_kept(self):
        wt, branch, _ = self.task()
        (wt / "f1").write_text("edited\n")
        p = self.clean()
        self.assertIn("dirty", p.stdout)
        self.assertEqual((wt / "f1").read_text(), "edited\n")
        self.assertIn(branch, self.branches())

    def test_clean_untracked_kept_but_pycache_allowed(self):
        wt, branch, _ = self.task()
        (wt / "notes.txt").write_text("keep me\n")
        self.assertIn("untracked files: notes.txt", self.clean().stdout)
        self.assertTrue((wt / "notes.txt").exists())
        (wt / "notes.txt").unlink()
        (wt / "__pycache__").mkdir()
        (wt / "__pycache__" / "x.pyc").write_text("c")
        self.clean()
        self.assertFalse(wt.exists())

    def test_clean_live_agent_kept(self):
        wt, branch, _ = self.task()
        self.env.seed_agent("zz-test-c2", "coder", goal="G-001", task="T-0001", status="queued")
        self.assertIn("agent live", self.clean().stdout)
        self.env.sql("UPDATE agents SET status='live' WHERE name='zz-test-c2'")
        self.assertIn("agent live", self.clean().stdout)
        self.assertTrue(wt.exists())
        self.assertIn(branch, self.branches())

    def test_clean_open_pr_kept(self):
        wt, branch, _ = self.task(pr_state="OPEN")
        self.assertIn("PR OPEN", self.clean().stdout)
        self.assertTrue(wt.exists())
        self.assertIn(branch, self.branches())

    def test_clean_active_task_kept(self):
        wt, branch, _ = self.task(status="in_review")
        self.assertIn("task in_review", self.clean().stdout)
        self.assertTrue(wt.exists())

    def test_clean_cancelled_rules(self):
        wt1, b1, _ = self.task(1, status="cancelled", pr_state="CLOSED")
        wt2, b2, _ = self.task(2, status="cancelled", pr_state=None, url=URL2, push=False)  # commit beyond base
        p = self.clean()
        self.assertFalse(wt1.exists())
        self.assertTrue(wt2.exists())
        self.assertIn("unpushed commits", p.stdout)
        # a cancelled task with no PR and nothing beyond base is removable
        git("reset", "-q", "--hard", "origin/main", cwd=wt2)
        self.clean()
        self.assertFalse(wt2.exists())

    def test_clean_outside_worktrees_untouched(self):
        outside = self.env.home / "elsewhere"
        outside.mkdir()
        wt, branch, _ = self.task(wt_parent=outside)
        p = self.clean()
        self.assertIn("outside worktrees/", p.stdout)
        self.assertTrue(wt.exists())
        self.assertIn(branch, self.branches())
        self.assertIn("outside worktrees/", self.clean("--dry-run").stdout)

    def test_clean_orphan_reported(self):
        orphan = self.env.home_dir / "worktrees" / "G-009-T-0099"
        orphan.mkdir()
        (orphan / "keep").write_text("x")
        p = self.clean()
        self.assertIn("orphan (left alone)", p.stdout)
        self.assertIn("total: 0 removed, 0 would remove, 0 skipped, 1 orphan(s)", p.stdout)  # the path cell is cut at 60 chars, so a long TMPDIR hides the name
        self.assertTrue((orphan / "keep").exists())

    def test_clean_role_refused(self):
        wt, branch, _ = self.task()
        p = self.refused(3, "clean", agent="zz-test-coder")
        self.assertIn("may not do this", p.stderr)
        self.assertTrue(wt.exists())
        self.refused(3, "clean", "--dry-run", agent="zz-test-coder")

    def test_clean_director_allowed(self):
        self.env.seed_agent("zz-test-director", "director")
        self.task()
        self.clean("--dry-run", agent="zz-test-director")

    def test_clean_survives_gh_failure_and_missing_repo(self):
        wt, branch, _ = self.task()
        self.env.set_gh(prs={})
        p = self.clean()
        self.assertIn("PR lookup failed", p.stdout)
        self.assertTrue(wt.exists())
        self.env.sql("UPDATE projects SET repo_path=NULL")
        self.assertIn("no project repo", self.clean().stdout)

    def test_clean_rerun_is_noop(self):
        self.task()
        self.assertIn("1 removed", self.clean().stdout)
        n = len(self.env.sql("SELECT 1 FROM events WHERE action='cleaned'"))
        p = self.clean()
        self.assertIn("0 removed", p.stdout)
        self.assertIn("nothing to remove", p.stdout)
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='cleaned'")), n)
        self.assertIn("0 would remove", self.clean("--dry-run").stdout)

    def test_clean_nothing_to_remove_skips_gh(self):
        wt, branch, _ = self.task()
        git("worktree", "remove", "--force", str(wt), cwd=self.repo)
        git("branch", "-D", branch, cwd=self.repo)
        before = len(self.env.calls("gh"))
        p = self.clean()
        self.assertIn("nothing to remove", p.stdout)
        self.assertIn("0 removed", p.stdout)
        self.assertEqual(len(self.env.calls("gh")), before)
        self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action='cleaned'"), [])

    def test_clean_partial_branch_delete_failure_reported(self):
        wt, branch, _ = self.task()
        (self.repo / ".git" / "refs" / "heads" / "factory" / "t-0001.lock").write_text("")
        p = self.clean()
        self.assertFalse(wt.exists())
        self.assertIn(branch, self.branches())
        self.assertIn("removed worktree; branch delete failed", p.stdout)
        self.assertIn("1 removed", p.stdout)
        ev = self.env.sql("SELECT detail FROM events WHERE entity='task' AND entity_id='T-0001' AND action='cleaned'")
        self.assertEqual(len(ev), 1)
        self.assertIn(str(wt), ev[0]["detail"])
        self.assertNotIn(branch, ev[0]["detail"])

    def set_cwd(self, name, cwd, status="live", task=None):
        self.env.seed_agent(name, "coder", goal="G-001", task=task, status=status)
        self.env.sql("UPDATE agents SET cwd=? WHERE name=?", (str(cwd), name))

    def test_clean_protects_cwd_of_live_agent_without_task(self):
        wt, branch, _ = self.task()
        self.set_cwd("zz-test-lead", wt / "sub")
        p = self.clean()
        self.assertIn("protected: cwd of agent zz-test-lead", p.stdout)
        self.assertTrue(wt.exists())
        self.assertIn(branch, self.branches())

    def test_clean_protects_cwd_of_done_agent(self):
        wt, branch, _ = self.task()
        self.set_cwd("zz-test-fin", wt, status="done", task="T-0001")
        self.assertIn("protected: cwd of agent zz-test-fin", self.clean().stdout)
        self.assertTrue(wt.exists())
        self.env.sql("UPDATE agents SET status='exited' WHERE name='zz-test-fin'")
        self.clean()
        self.assertFalse(wt.exists())

    def test_clean_protects_stopped_worktree(self):
        wt, branch, _ = self.task()
        self.set_cwd("zz-test-fin", wt, status="stopped", task="T-0001")  # force-stopped: the successor reuses it
        self.assertIn("protected: cwd of agent zz-test-fin", self.clean().stdout)
        self.assertTrue(wt.exists())
        self.env.sql("UPDATE agents SET status='exited' WHERE name='zz-test-fin'")
        self.clean()
        self.assertFalse(wt.exists())

    def test_clean_protects_cwd_via_symlink_path(self):
        wt, branch, _ = self.task()
        link = self.env.home / "link"
        link.symlink_to(self.env.home_dir / "worktrees")
        self.set_cwd("zz-test-sym", f"{link}/{wt.name}/")
        self.assertIn("protected: cwd of agent zz-test-sym", self.clean().stdout)
        self.assertTrue(wt.exists())

    def test_clean_protects_pane_cwd_from_herdr(self):
        wt, branch, _ = self.task()
        pane = {"pane_id": "p9", "tab_id": "t9", "workspace_id": "w9"}
        self.env.set_herdr(panes=[{**pane, "cwd": "/", "foreground_cwd": str(wt / "src")}])
        self.assertIn("protected: cwd of a live pane", self.clean().stdout)
        self.assertTrue(wt.exists())
        # a pane in a parent directory of the worktree does not hold it
        self.env.set_herdr(panes=[{**pane, "cwd": str(self.env.home_dir)}])
        self.clean()
        self.assertFalse(wt.exists())

    def test_clean_fails_closed_when_herdr_unreachable(self):
        wt, branch, _ = self.task()
        self.env.set_herdr(fail=True)
        p = self.refused(4, "clean", agent="zz-test-cleaner")
        self.assertIn("cannot verify live panes", p.stderr)
        self.refused(4, "clean", "--dry-run", agent="zz-test-cleaner")
        self.assertTrue(wt.exists())
        self.assertIn(branch, self.branches())

    def test_clean_dry_run_shows_protection_reason(self):
        wt, branch, _ = self.task()
        self.set_cwd("zz-test-lead", wt)
        p = self.clean("--dry-run")
        self.assertIn("protected: cwd of agent zz-test-lead", p.stdout)
        self.assertIn("0 would remove", p.stdout)

    def test_clean_relative_path_rejected(self):
        wt, branch, _ = self.task()
        self.env.sql("UPDATE tasks SET worktree='worktrees/x' WHERE id='T-0001'")
        p = self.clean()
        self.assertIn("recorded path not absolute", p.stdout)
        self.assertTrue(wt.exists())
        self.assertIn(branch, self.branches())


if __name__ == "__main__":
    unittest.main()
