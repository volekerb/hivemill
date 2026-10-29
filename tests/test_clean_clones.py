import datetime as dt
import os
import unittest

from harness import FactoryTestCase, git

URL = "https://github.com/zz-test/repo/pull/1"


def ago(days):
    return (dt.datetime.now().astimezone() - dt.timedelta(days=days)).isoformat(timespec="seconds")


class CloneCleanBase(FactoryTestCase):
    """A real temp repo with a local bare origin; task dirs are standalone `git clone --shared` clones."""

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
        self.wts = e.home_dir / "worktrees"
        self.wts.mkdir()
        e.seed_project("zz-test-proj")
        e.sql("UPDATE projects SET repo_path=? WHERE id='zz-test-proj'", (str(self.repo),))
        e.seed_goal(project="zz-test-proj")
        e.seed_agent("zz-test-cleaner", "cleaner", goal="G-001")
        e.seed_agent("zz-test-coder", "coder", goal="G-001", status="exited")

    def clone_task(self, status="done", pr_state="MERGED", push=True, name="G-001-T-0001"):
        """T-0001 with a standalone clone on factory/t-0001 holding one commit (pushed to origin by default)."""
        e = self.env
        wt, branch = self.wts / name, "factory/t-0001"
        git("-c", "protocol.file.allow=always", "clone", "-q", "--shared", "--no-checkout", str(self.repo), str(wt), cwd=e.home)
        git("remote", "set-url", "origin", str(self.origin), cwd=wt)
        git("fetch", "-q", "origin", cwd=wt)
        git("checkout", "-q", "-b", branch, "origin/main", cwd=wt)
        (wt / "f1").write_text("work\n")
        git("add", "f1", cwd=wt)
        git("-c", "user.name=zz", "-c", "user.email=zz@example.invalid", "commit", "-q", "-m", "work", cwd=wt)
        if push:
            git("push", "-q", "origin", branch, cwd=wt)
        sha = git("rev-parse", "HEAD", cwd=wt)
        e.seed_task("T-0001", status=status, pr_url=URL)
        e.sql("UPDATE tasks SET worktree=?, branch=? WHERE id='T-0001'", (str(wt), branch))
        if pr_state:
            e.seed_pr(URL, sha=sha, state=pr_state)
        return wt, branch

    def clean(self, *args):
        return self.ok("clean", *args, agent="zz-test-cleaner")

    def refused_events(self):
        return self.env.sql("SELECT detail FROM events WHERE action='git-config-refused'")


class CloneCleanTests(CloneCleanBase):
    def test_clone_merged_done_removed(self):
        wt, _ = self.clone_task()
        p = self.clean()
        self.assertFalse(wt.exists())
        self.assertIn(str(wt), p.stdout)
        self.assertIn("removed", p.stdout)
        self.assertIn("1 removed", p.stdout)
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='cleaned' AND entity_id='T-0001'")), 1)

    def test_clone_dry_run_keeps(self):
        wt, _ = self.clone_task()
        self.assertIn("would remove", self.clean("--dry-run").stdout)
        self.assertTrue(wt.exists())

    def test_clone_blocked_task_not_done(self):
        wt, _ = self.clone_task(status="in_review")
        self.assertIn("task in_review", self.clean().stdout)
        self.assertTrue(wt.exists())

    def test_clone_blocked_branch_not_merged(self):
        wt, _ = self.clone_task(pr_state="OPEN")
        self.assertIn("PR OPEN", self.clean().stdout)
        self.assertTrue(wt.exists())

    def test_clone_blocked_uncommitted_tracked_change(self):
        wt, _ = self.clone_task()
        (wt / "f1").write_text("changed\n")
        self.assertIn("dirty", self.clean().stdout)
        self.assertTrue(wt.exists())

    def test_clone_blocked_unpushed_commit(self):
        wt, _ = self.clone_task()
        (wt / "extra").write_text("more\n")
        git("add", "extra", cwd=wt)
        git("-c", "user.name=zz", "-c", "user.email=zz@example.invalid", "commit", "-q", "-m", "local only", cwd=wt)
        self.assertIn("unpushed commits", self.clean().stdout)
        self.assertTrue(wt.exists())

    def test_clone_blocked_unpushed_commit_on_other_branch(self):
        wt, _ = self.clone_task()
        git("checkout", "-q", "-b", "wip", cwd=wt)
        (wt / "extra").write_text("more\n")
        git("add", "extra", cwd=wt)
        git("-c", "user.name=zz", "-c", "user.email=zz@example.invalid", "commit", "-q", "-m", "wip only", cwd=wt)
        git("checkout", "-q", "factory/t-0001", cwd=wt)
        p = self.clean()
        self.assertIn("unpushed commits", p.stdout)
        self.assertTrue(wt.exists())

    def test_clone_blocked_stash_only_in_clone(self):
        wt, _ = self.clone_task()
        (wt / "f1").write_text("stashed\n")
        git("-c", "user.name=zz", "-c", "user.email=zz@example.invalid", "stash", "push", "-q", cwd=wt)
        p = self.clean()
        self.assertIn("unpushed commits", p.stdout)
        self.assertTrue(wt.exists())

    def test_clone_blocked_live_pane_cwd_inside(self):
        wt, _ = self.clone_task()
        pane = {"pane_id": "p9", "tab_id": "t9", "workspace_id": "w9"}
        self.env.set_herdr(panes=[{**pane, "cwd": "/", "foreground_cwd": str(wt / "sub")}])
        self.assertIn("protected: cwd of a live pane", self.clean().stdout)
        self.assertTrue(wt.exists())

    def test_clone_blocked_planted_fsmonitor_never_runs(self):
        wt, _ = self.clone_task()
        marker = self.env.home / "fsmonitor-ran"
        hook = self.env.home / "fsm.sh"
        hook.write_text(f"#!/bin/sh\ntouch {marker}\n")
        hook.chmod(0o755)
        with open(wt / ".git" / "config", "a") as f:
            f.write(f"[core]\n\tfsmonitor = {hook}\n")
        p = self.clean()
        self.assertIn("git config refused", p.stdout)
        self.assertTrue(wt.exists())
        self.assertFalse(marker.exists())
        self.assertTrue(any("core.fsmonitor" in r["detail"] for r in self.refused_events()))

    def test_clone_outside_worktrees_recorded_path_never_removed(self):
        wt, _ = self.clone_task()
        outside = self.env.home / "elsewhere"
        wt.rename(outside)
        self.env.sql("UPDATE tasks SET worktree=? WHERE id='T-0001'", (str(outside),))
        self.assertIn("outside worktrees/", self.clean().stdout)
        self.assertTrue(outside.exists())

    def test_clone_symlink_under_worktrees_pointing_outside_never_removed(self):
        wt, _ = self.clone_task()
        outside = self.env.home / "elsewhere"
        wt.rename(outside)
        wt.symlink_to(outside)
        self.assertIn("outside worktrees/", self.clean().stdout)
        self.assertTrue(outside.exists())
        self.assertTrue((outside / "f1").exists())

    def test_clone_symlink_to_sibling_dir_in_worktrees_not_followed(self):
        wt, _ = self.clone_task(name="G-001-T-0009")
        link = self.wts / "G-001-T-0001"
        link.symlink_to(wt)
        self.env.sql("UPDATE tasks SET worktree=? WHERE id='T-0001'", (str(link),))
        self.assertIn("outside worktrees/", self.clean().stdout)
        self.assertTrue((wt / "f1").exists())


class SandboxDirTests(CloneCleanBase):
    def setUp(self):
        super().setUp()
        self.root = self.env.user_home / ".local" / "state" / "factory" / "workers"
        self.root.mkdir(parents=True)

    def agent(self, name, status, ended):
        self.env.seed_agent(name, "coder", goal="G-001", status=status)
        self.env.sql("UPDATE agents SET ended=? WHERE name=?", (ended, name))
        (self.root / name).mkdir()
        (self.root / name / "gitconfig").write_text("x\n")
        return self.root / name

    def test_sandbox_dir_of_agent_ended_8_days_ago_removed(self):
        d = self.agent("zz-test-old", "exited", ago(8))
        p = self.clean()
        self.assertFalse(d.exists())
        self.assertIn("sandbox dir of an ended agent", p.stdout)

    def test_sandbox_dirs_kept_when_young_live_queued_starting_unknown_or_ended_null(self):
        keep = [self.agent("zz-test-young", "exited", ago(2)),
                self.agent("zz-test-live", "live", None),
                self.agent("zz-test-queued", "queued", None),
                self.agent("zz-test-starting", "starting", None),
                self.agent("zz-test-nullend", "exited", None),
                self.agent("zz-test-oldlive", "live", ago(30))]
        unknown = self.root / "zz-test-unknown"
        unknown.mkdir()
        self.clean()
        for d in keep + [unknown]:
            self.assertTrue(d.exists(), d)

    def test_sandbox_dir_kept_while_live_pane_cwd_inside(self):
        d = self.agent("zz-test-old", "exited", ago(8))
        pane = {"pane_id": "p9", "tab_id": "t9", "workspace_id": "w9"}
        self.env.set_herdr(panes=[{**pane, "cwd": "/", "foreground_cwd": str(d / "claude")}])
        self.clean()
        self.assertTrue(d.exists())

    def test_sandbox_symlink_entry_not_followed(self):
        target = self.env.home / "precious"
        target.mkdir()
        (target / "keep").write_text("x\n")
        self.env.seed_agent("zz-test-sym", "coder", goal="G-001", status="exited")
        self.env.sql("UPDATE agents SET ended=? WHERE name='zz-test-sym'", (ago(30),))
        (self.root / "zz-test-sym").symlink_to(target)
        self.clean()
        self.assertTrue((target / "keep").exists())

    def test_sandbox_dry_run_keeps(self):
        d = self.agent("zz-test-old", "exited", ago(8))
        self.assertIn("would remove", self.clean("--dry-run").stdout)
        self.assertTrue(d.exists())


if __name__ == "__main__":
    unittest.main()
