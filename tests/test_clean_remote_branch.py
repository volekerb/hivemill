import json
import subprocess
import sys
import unittest
from pathlib import Path

from harness import FactoryTestCase, git
from guard_impl import hook_run

HOOK = str(Path(__file__).resolve().parent.parent / "roles" / "hooks" / "worker-guard.py")


def run_hook(command, *flags):
    payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": command}})
    return hook_run(payload, [*flags])


class CleanRemoteBranchTests(FactoryTestCase):
    """The temp FACTORY_HOME is a git repo whose origin is a local bare repo; gh pr list comes from the gh stub."""

    def setUp(self):
        super().setUp()
        e = self.env
        self.origin = e.home / "origin.git"
        self.repo = e.home_dir
        git("init", "-q", "--bare", "-b", "main", str(self.origin), cwd=e.home)
        git("remote", "add", "origin", str(self.origin), cwd=self.repo)
        (self.repo / "README").write_text("x\n")
        git("add", "README", cwd=self.repo)
        git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init", cwd=self.repo)
        git("push", "-q", "origin", "main", cwd=self.repo)
        e.seed_project("zz-test-proj")
        e.seed_goal(project="zz-test-proj")
        e.seed_agent("zz-test-cleaner", "cleaner", goal="G-001")
        e.seed_agent("zz-test-coder", "coder", goal="G-001")
        e.seed_agent("zz-test-director", "director")

    def commit(self, tag):
        (self.repo / tag).write_text(tag)
        git("add", tag, cwd=self.repo)
        git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", tag, cwd=self.repo)
        return git("rev-parse", "HEAD", cwd=self.repo)

    def remote_branch(self, name="factory/t-0007", pull=1, pull_sha=None, prs=None, state="MERGED", cross=False):
        """Push a branch with one commit to origin, set refs/pull/<pull>/head, stub PRs. Returns its sha."""
        git("checkout", "-q", "-b", "tmp-" + name.replace("/", "-"), "main", cwd=self.repo)
        sha = self.commit(name.replace("/", "_"))
        git("push", "-q", "origin", f"HEAD:refs/heads/{name}", cwd=self.repo)
        git("checkout", "-q", "main", cwd=self.repo)
        git("branch", "-q", "-D", "tmp-" + name.replace("/", "-"), cwd=self.repo)
        if pull:
            git("--git-dir", str(self.origin), "update-ref", f"refs/pull/{pull}/head", pull_sha or sha, cwd=self.repo)
        if prs is None:
            prs = [self.pr(pull, sha, state, name, cross)]
        st = json.loads(self.env.gh_state.read_text())
        st.setdefault("pr_list", []).extend(prs)
        self.env.gh_state.write_text(json.dumps(st))
        return sha

    @staticmethod
    def pr(n, sha, state="MERGED", name="factory/t-0007", cross=False):
        return {"number": n, "state": state, "headRefOid": sha, "headRefName": name, "isCrossRepository": cross}

    def remote_sha(self, name):
        return git("--git-dir", str(self.origin), "rev-parse", "-q", "--verify", f"refs/heads/{name}", cwd=self.repo) \
            if self.exists(name) else None

    def exists(self, name):
        return bool(git("--git-dir", str(self.origin), "rev-parse", "-q", "--verify", f"refs/heads/{name}",
                        cwd=self.repo, check=False))

    def rm(self, *names, agent="zz-test-cleaner", extra=()):
        return self.env.run("clean", "--remote-branch", *names, *extra, agent=agent)

    def events(self, action):
        return self.env.sql("SELECT entity_id,detail,actor FROM events WHERE action=?", (action,))

    def assertRefused(self, p, text):
        self.assertEqual(p.returncode, 4, f"{p.stdout}\n{p.stderr}")
        self.assertIn(text, p.stdout)

    # -- name checks ------------------------------------------------------
    def test_refuses_main_state_and_non_factory_names(self):
        for name in ("main", "state", "HEAD", "feature/x", "factory", "factory/", "refs/heads/factory/x", "factory/a b"):
            p = self.rm(name)
            self.assertRefused(p, "not a factory/* branch")
        self.assertTrue(self.exists("main"))
        self.assertEqual(len(self.events("branch-delete-refused")), 8)

    def test_refuses_dotdot_name(self):
        self.remote_branch()
        self.assertRefused(self.rm("factory/../main"), "not a valid git branch name")
        self.assertRefused(self.rm("factory/x..y"), "not a valid git branch name")
        self.assertTrue(self.exists("main"))

    def test_refuses_missing_remote_branch(self):
        self.assertRefused(self.rm("factory/nope"), "remote branch does not exist")

    # -- PR checks --------------------------------------------------------
    def test_refuses_open_pr(self):
        self.remote_branch(state="OPEN")
        self.assertRefused(self.rm("factory/t-0007"), "OPEN PR")
        self.assertTrue(self.exists("factory/t-0007"))

    def test_refuses_two_prs_one_open(self):
        sha = self.remote_branch(prs=[])
        st = json.loads(self.env.gh_state.read_text())
        st["pr_list"] = [self.pr(1, sha, "MERGED"), self.pr(2, sha, "OPEN")]
        self.env.gh_state.write_text(json.dumps(st))
        git("--git-dir", str(self.origin), "update-ref", "refs/pull/1/head", sha, cwd=self.repo)
        p = self.rm("factory/t-0007")
        self.assertRefused(p, "OPEN PR")
        self.assertIn("[1, 2]", p.stdout)
        self.assertTrue(self.exists("factory/t-0007"))

    def test_two_closed_prs_the_one_matching_head_is_used(self):
        sha = self.remote_branch(pull=5, prs=[])
        old = self.commit("old")
        git("push", "-q", "origin", f"{old}:refs/heads/scratch-old", cwd=self.repo)
        git("--git-dir", str(self.origin), "update-ref", "refs/pull/9/head", old, cwd=self.repo)
        st = json.loads(self.env.gh_state.read_text())
        st["pr_list"] = [self.pr(5, sha, "CLOSED"), self.pr(9, old, "MERGED")]
        self.env.gh_state.write_text(json.dumps(st))
        p = self.ok_rm("factory/t-0007")
        self.assertIn("PR #5 CLOSED", p.stdout)
        self.assertFalse(self.exists("factory/t-0007"))
        self.assertEqual(json.loads(self.events("branch-deleted")[0]["detail"])["pr"], 5)

    def test_refuses_cross_repo_only_pr(self):
        self.remote_branch(cross=True)
        self.assertRefused(self.rm("factory/t-0007"), "no PR in this repo")
        self.assertTrue(self.exists("factory/t-0007"))

    def test_refuses_force_pushed_after_close(self):
        """The PR closed at an older head; the branch was force-pushed since, so unreviewed commits would be lost."""
        old = self.remote_branch(prs=[])
        git("push", "-q", "-f", "origin", f"{self.commit('later')}:refs/heads/factory/t-0007", cwd=self.repo)
        st = json.loads(self.env.gh_state.read_text())
        st["pr_list"] = [self.pr(1, old, "CLOSED")]
        self.env.gh_state.write_text(json.dumps(st))
        self.assertRefused(self.rm("factory/t-0007"), "no PR head equals the branch head")
        self.assertTrue(self.exists("factory/t-0007"))

    def test_refuses_pull_ref_differs(self):
        self.remote_branch(pull_sha=self.commit("other"))
        self.assertRefused(self.rm("factory/t-0007"), "differs from the branch head")
        self.assertTrue(self.exists("factory/t-0007"))

    def test_refuses_missing_pull_ref(self):
        self.remote_branch(pull=0, prs=[])
        sha = git("--git-dir", str(self.origin), "rev-parse", "refs/heads/factory/t-0007", cwd=self.repo)
        st = json.loads(self.env.gh_state.read_text())
        st["pr_list"] = [self.pr(3, sha)]
        self.env.gh_state.write_text(json.dumps(st))
        self.assertRefused(self.rm("factory/t-0007"), "refs/pull/3/head is missing")
        self.assertTrue(self.exists("factory/t-0007"))

    def test_gh_failure_and_bad_json_fail_closed(self):
        self.remote_branch()
        self.env.set_gh(pr_list_exit=1)
        self.assertRefused(self.rm("factory/t-0007"), "gh pr list failed")
        self.env.set_gh(pr_list_exit=0, pr_list_raw="not json")
        self.assertRefused(self.rm("factory/t-0007"), "unusable JSON")
        self.assertTrue(self.exists("factory/t-0007"))

    # -- in-use checks ----------------------------------------------------
    def test_refuses_branch_in_live_worktree(self):
        self.remote_branch()
        git("fetch", "-q", "origin", cwd=self.repo)
        git("worktree", "add", "-q", "-B", "factory/t-0007", str(self.env.home / "wt"), "origin/factory/t-0007", cwd=self.repo)
        self.assertRefused(self.rm("factory/t-0007"), "worktree has the branch")
        self.assertTrue(self.exists("factory/t-0007"))

    def test_refuses_non_terminal_task_without_worktree(self):
        self.remote_branch()
        self.env.seed_task("T-0007", status="blocked", pr_url=None)
        self.env.sql("UPDATE tasks SET branch='factory/t-0007' WHERE id='T-0007'")
        self.assertRefused(self.rm("factory/t-0007"), "task T-0007 (blocked)")
        self.assertTrue(self.exists("factory/t-0007"))

    def test_done_task_does_not_block(self):
        self.remote_branch()
        self.env.seed_task("T-0007", status="done", pr_url=None)
        self.env.sql("UPDATE tasks SET branch='factory/t-0007' WHERE id='T-0007'")
        self.ok_rm("factory/t-0007")
        self.assertFalse(self.exists("factory/t-0007"))

    # -- the action -------------------------------------------------------
    def ok_rm(self, *names, **kw):
        p = self.rm(*names, **kw)
        self.assertEqual(p.returncode, 0, f"{p.stdout}\n{p.stderr}")
        return p

    def test_deletes_valid_branch_logs_and_keeps_pull_ref(self):
        sha = self.remote_branch()
        p = self.ok_rm("factory/t-0007")
        self.assertIn("deleted", p.stdout)
        self.assertFalse(self.exists("factory/t-0007"))
        self.assertEqual(git("--git-dir", str(self.origin), "rev-parse", "refs/pull/1/head", cwd=self.repo), sha)
        ev = self.events("branch-deleted")
        self.assertEqual(len(ev), 1)
        self.assertEqual(ev[0]["actor"], "zz-test-cleaner")
        self.assertEqual(json.loads(ev[0]["detail"]), {"name": "factory/t-0007", "sha": sha, "pr": 1, "pr_state": "MERGED"})
        self.assertEqual(self.events("branch-delete-refused"), [])

    def test_director_may_delete_and_several_names_give_a_table(self):
        self.remote_branch("factory/t-0007", pull=1)
        self.remote_branch("factory/t-0008", pull=2, prs=[self.pr(2, "0" * 40, "CLOSED", "factory/t-0008")])
        p = self.rm("factory/t-0007", "factory/t-0008", "main", agent="zz-test-director")
        self.assertEqual(p.returncode, 4, p.stdout)
        self.assertIn("total: 1 ok, 2 refused", p.stdout)
        self.assertFalse(self.exists("factory/t-0007"))
        self.assertTrue(self.exists("factory/t-0008"))
        self.assertEqual(len(self.events("branch-delete-refused")), 2)

    def test_repeated_flag_accepted(self):
        self.remote_branch("factory/t-0007", pull=1)
        self.remote_branch("factory/t-0008", pull=2)
        self.ok_rm("factory/t-0007", extra=("--remote-branch", "factory/t-0008"))
        self.assertFalse(self.exists("factory/t-0007"))
        self.assertFalse(self.exists("factory/t-0008"))

    def test_dry_run_changes_nothing(self):
        self.remote_branch()
        p = self.ok_rm("factory/t-0007", extra=("--dry-run",))
        self.assertIn("would delete", p.stdout)
        self.assertTrue(self.exists("factory/t-0007"))
        self.assertEqual(self.events("branch-deleted"), [])
        p = self.rm("main", extra=("--dry-run",))
        self.assertRefused(p, "refused")
        self.assertEqual(self.events("branch-delete-refused"), [])

    def test_local_branch_equal_or_ancestor_deleted_ahead_kept(self):
        sha = self.remote_branch()
        git("branch", "factory/t-0007", sha, cwd=self.repo)
        self.assertIn("local branch deleted", self.ok_rm("factory/t-0007").stdout)
        self.assertEqual(git("branch", "--list", "factory/*", cwd=self.repo), "")
        sha = self.remote_branch("factory/t-0008", pull=2)
        git("checkout", "-q", "-b", "factory/t-0008", sha, cwd=self.repo)
        ahead = self.commit("ahead")
        git("checkout", "-q", "main", cwd=self.repo)
        p = self.ok_rm("factory/t-0008")
        self.assertIn("local branch kept", p.stdout)
        self.assertEqual(git("rev-parse", "factory/t-0008", cwd=self.repo), ahead)

    def test_lease_push_failure_is_refused_event_and_exit_4(self):
        """A pre-receive hook on the remote rejects the delete: refused with 'push failed', branch stays, local kept."""
        sha = self.remote_branch()
        git("branch", "factory/t-0007", sha, cwd=self.repo)
        hook = self.origin / "hooks" / "pre-receive"
        hook.write_text("#!/bin/sh\necho no >&2\nexit 1\n")
        hook.chmod(0o755)
        p = self.rm("factory/t-0007")
        self.assertRefused(p, "push failed")
        self.assertTrue(self.exists("factory/t-0007"))
        self.assertIn("factory/t-0007", git("branch", "--list", "factory/*", cwd=self.repo))
        self.assertEqual(len(self.events("branch-delete-refused")), 1)
        self.assertEqual(self.events("branch-deleted"), [])

    def test_lease_pins_head_against_a_concurrent_push(self):
        """The branch moves between the check and the push: the lease fails, nothing is deleted."""
        sha = self.remote_branch()
        moved = self.commit("moved")
        git("push", "-q", "-f", "origin", f"{moved}:refs/heads/factory/t-0007", cwd=self.repo)
        git("--git-dir", str(self.origin), "update-ref", "refs/pull/1/head", moved, cwd=self.repo)
        st = json.loads(self.env.gh_state.read_text())
        st["pr_list"] = [self.pr(1, moved, "MERGED")]
        self.env.gh_state.write_text(json.dumps(st))
        # direct call into the delete step with the stale sha
        with self.env.load_factory() as mod:
            self.assertIn("push failed", mod._rb_delete("factory/t-0007", sha))
        self.assertTrue(self.exists("factory/t-0007"))

    # -- stray branches (T-0408) ---------------------------------------------
    def stray_setup(self, stray="factory/T-0001", pr_branch="factory/t-0001", pr_state="MERGED", extra_commit=False):
        """A merged PR head (branch pr_branch, PR #1) and a stray branch at its parent (or one commit ahead of it)."""
        git("checkout", "-q", "-b", "tmp-s", "main", cwd=self.repo)
        base = self.commit("base")
        head = self.commit("head")
        git("push", "-q", "origin", f"{head}:refs/heads/{pr_branch}", f"{base}:refs/heads/{stray}", cwd=self.repo)
        if extra_commit:
            git("push", "-q", "-f", "origin", f"{self.commit('extra')}:refs/heads/{stray}", cwd=self.repo)
        git("checkout", "-q", "main", cwd=self.repo)
        git("branch", "-q", "-D", "tmp-s", cwd=self.repo)
        git("--git-dir", str(self.origin), "update-ref", "refs/pull/1/head", head, cwd=self.repo)
        st = json.loads(self.env.gh_state.read_text())
        st["pr_list"] = [self.pr(1, head, pr_state, pr_branch)]
        self.env.gh_state.write_text(json.dumps(st))
        if self.exists(pr_branch) and pr_branch != stray:  # the PR's own branch is gone from origin
            git("--git-dir", str(self.origin), "update-ref", "-d", f"refs/heads/{pr_branch}", cwd=self.repo)
        return base, head

    def test_stray_ancestor_of_merged_pr_head_deleted_with_evidence(self):
        base, head = self.stray_setup()
        p = self.ok_rm("factory/T-0001")
        self.assertIn(f"stray {base[:8]} ancestor of merged PR #1 head {head[:8]}", p.stdout)
        self.assertFalse(self.exists("factory/T-0001"))
        d = json.loads(self.events("branch-deleted")[0]["detail"])
        self.assertEqual((d["sha"], d["pr"], d["pr_head"]), (base, 1, head))

    def test_stray_equal_to_merged_pr_head_deleted(self):
        _, head = self.stray_setup()
        git("push", "-q", "origin", f"{head}:refs/heads/factory/T-0001", cwd=self.repo)
        self.ok_rm("factory/T-0001")
        self.assertFalse(self.exists("factory/T-0001"))

    def test_stray_with_extra_commit_refused(self):
        self.stray_setup(extra_commit=True)
        p = self.rm("factory/T-0001")
        self.assertRefused(p, "no PR in this repo has the branch as its head")
        self.assertTrue(self.exists("factory/T-0001"))

    def test_stray_with_open_pr_refused(self):
        self.stray_setup(pr_state="OPEN")
        self.assertRefused(self.rm("factory/T-0001"), "OPEN PR")
        self.assertTrue(self.exists("factory/T-0001"))

    def test_stray_of_a_different_task_refused(self):
        self.stray_setup(stray="factory/T-0002")
        self.assertRefused(self.rm("factory/T-0002"), "no PR in this repo")
        self.assertTrue(self.exists("factory/T-0002"))

    def test_stray_dry_run_lists_and_keeps_branch(self):
        base, head = self.stray_setup()
        p = self.ok_rm("factory/T-0001", extra=("--dry-run",))
        self.assertIn("would delete", p.stdout)
        self.assertIn(f"ancestor of merged PR #1 head {head[:8]}", p.stdout)
        self.assertTrue(self.exists("factory/T-0001"))
        self.assertEqual(self.events("branch-deleted"), [])

    # -- roles --------------------------------------------------------------
    def test_other_workers_exit_3(self):
        self.remote_branch()
        self.refused(3, "clean", "--remote-branch", "factory/t-0007", agent="zz-test-coder")
        self.assertTrue(self.exists("factory/t-0007"))

    # -- the hook ---------------------------------------------------------------
    def test_hook_allows_cleaner_and_hq_blocks_coder(self):
        cmd = "factory clean --remote-branch factory/x"
        self.assertEqual(run_hook(cmd, "--hq").returncode, 0)
        self.assertEqual(run_hook(cmd, "--role", "cleaner").returncode, 0)
        self.assertEqual(run_hook(cmd, "--role", "coder").returncode, 2)


if __name__ == "__main__":
    unittest.main()
