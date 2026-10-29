import subprocess
import unittest
from pathlib import Path

from harness import FactoryTestCase, git

BRANCH = "factory/t-0001"


class DoneGuardTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        base = e.home / "git"
        base.mkdir()
        self.origin, self.main, self.wt = base / "origin.git", base / "main", base / "wt"
        git("init", "-q", "--bare", "-b", "main", str(self.origin), cwd=base)
        git("clone", "-q", str(self.origin), str(self.main), cwd=base)
        for d in (self.main,):
            git("config", "user.email", "zz@example.invalid", cwd=d)
            git("config", "user.name", "zz", cwd=d)
        (self.main / "a.txt").write_text("a\n")
        git("add", ".", cwd=self.main)
        git("commit", "-qm", "base", cwd=self.main)
        git("push", "-q", "origin", "main", cwd=self.main)
        git("worktree", "add", "-q", "-b", BRANCH, str(self.wt), "origin/main", cwd=self.main)
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_team(lead="zz-test-lead")
        e.seed_task(status="in_progress", team="g-001-a")
        e.sql("UPDATE tasks SET worktree=?, branch=? WHERE id='T-0001'", (str(self.wt), BRANCH))
        e.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", task="T-0001")
        e.seed_agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")

    def agent_status(self, name="zz-test-coder"):
        return self.env.sql("SELECT status FROM agents WHERE name=?", (name,))[0]["status"]

    def nothing_changed(self):
        self.assertEqual(self.agent_status(), "live")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE sender='zz-test-coder'")[0]["c"], 0)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action LIKE 'done%'")[0]["c"], 0)

    def commit(self, name="b.txt"):
        (self.wt / name).write_text("x\n")
        git("add", name, cwd=self.wt)
        git("commit", "-qm", "work", cwd=self.wt)

    def push(self):
        git("push", "-q", "-u", "origin", "HEAD", cwd=self.wt)

    def lead_msgs(self):
        return self.env.sql("SELECT subject, body FROM messages WHERE recipient='zz-test-lead'")

    def test_clean_and_pushed_succeeds(self):
        self.commit()
        self.push()
        self.ok("done", "all good", agent="zz-test-coder")
        self.assertEqual(self.agent_status(), "done")

    def push_as(self, name):
        git("push", "-q", "origin", f"HEAD:refs/heads/{name}", cwd=self.wt)

    def test_recorded_lowercase_branch_pushed_no_finding(self):
        self.commit()
        self.push()
        p = self.ok("done", "x", agent="zz-test-coder")
        self.assertNotIn("pushed to ", p.stderr)

    def test_upper_case_variant_pushed_passes_with_warning(self):
        self.commit()
        self.push_as("factory/T-0001")
        p = self.ok("done", "x", agent="zz-test-coder")
        self.assertEqual(self.agent_status(), "done")
        lines = [l for l in p.stderr.splitlines() if l.startswith("pushed to ")]
        self.assertEqual(len(lines), 1)
        self.assertIn(BRANCH, lines[0])
        self.assertEqual(self.env.sql("SELECT branch FROM tasks WHERE id='T-0001'")[0]["branch"], BRANCH)

    def test_case_variant_behind_head_is_unpushed(self):
        self.commit()
        self.push_as("factory/T-0001")
        self.commit("c.txt")
        p = self.refused(3, "done", "x", agent="zz-test-coder")
        self.assertIn("is not on origin/", p.stderr)
        self.assertNotIn("pushed to ", p.stderr)
        self.nothing_changed()

    def test_two_case_variants_named_in_finding(self):
        self.commit()
        self.push_as("factory/T-0001")
        self.push_as("Factory/T-0001")
        p = self.refused(3, "done", "x", agent="zz-test-coder")
        self.assertIn("origin/factory/T-0001", p.stderr)
        self.assertIn("origin/Factory/T-0001", p.stderr)
        self.nothing_changed()

    def test_other_task_branch_is_unpushed(self):
        self.commit()
        self.push_as("factory/t-0002")
        p = self.refused(3, "done", "x", agent="zz-test-coder")
        self.assertIn("is not on origin/", p.stderr)
        self.nothing_changed()

    def test_upstream_picks_among_two_variants(self):
        self.commit()
        self.push_as("Factory/T-0001")
        git("push", "-q", "-u", "origin", "HEAD:refs/heads/factory/T-0001", cwd=self.wt)
        p = self.ok("done", "x", agent="zz-test-coder")
        self.assertEqual(self.agent_status(), "done")
        self.assertEqual(len([l for l in p.stderr.splitlines() if l.startswith("pushed to ")]), 1)

    def test_recorded_branch_behind_head_wins_over_variant(self):
        self.commit()
        self.push()
        self.commit("c.txt")
        self.push_as("factory/T-0001")
        p = self.refused(3, "done", "x", agent="zz-test-coder")
        self.assertIn("is not on origin/factory/t-0001", p.stderr)
        self.nothing_changed()

    def test_modified_tracked_file_refused(self):
        (self.wt / "a.txt").write_text("changed\n")
        p = self.refused(3, "done", "x", agent="zz-test-coder")
        self.assertIn("a.txt", p.stderr)
        self.assertIn("commit (a WIP commit is fine) and push", p.stderr)
        self.nothing_changed()

    def test_staged_file_refused(self):
        (self.wt / "new.txt").write_text("n\n")
        git("add", "new.txt", cwd=self.wt)
        p = self.refused(3, "done", "x", agent="zz-test-coder")
        self.assertIn("new.txt", p.stderr)
        self.nothing_changed()

    def test_untracked_only_warns(self):
        (self.wt / "scratch.txt").write_text("s\n")
        p = self.ok("done", "x", agent="zz-test-coder")
        self.assertIn("untracked", p.stderr)
        self.assertIn("scratch.txt", p.stderr)
        self.assertEqual(self.agent_status(), "done")

    def test_committed_not_pushed_refused(self):
        self.commit()
        p = self.refused(3, "done", "x", agent="zz-test-coder")
        self.assertIn("push your commits (git push -u origin HEAD)", p.stderr)
        self.nothing_changed()

    def test_new_commit_after_push_refused(self):
        self.commit()
        self.push()
        self.commit("c.txt")
        self.refused(3, "done", "x", agent="zz-test-coder")
        self.nothing_changed()

    def test_no_commits_beyond_base_succeeds(self):
        self.ok("done", "spec impossible", agent="zz-test-coder")
        self.assertEqual(self.agent_status(), "done")

    def test_unreachable_origin_still_works(self):
        self.commit()
        self.push()
        self.origin.rename(self.origin.with_name("gone.git"))  # any fetch would now fail
        self.ok("done", "x", agent="zz-test-coder")

    def test_force_dirty_records_event_and_tells_lead(self):
        (self.wt / "a.txt").write_text("changed\n")
        self.ok("done", "--force", "x", agent="zz-test-coder")
        self.assertEqual(self.agent_status(), "done")
        ev = self.env.sql("SELECT detail FROM events WHERE action='done-forced-unclean'")
        self.assertEqual(len(ev), 1)
        self.assertIn("a.txt", ev[0]["detail"])
        self.assertTrue(any("closed with --force" in m["body"] and "a.txt" in m["body"] for m in self.lead_msgs()))

    def test_force_unpushed(self):
        self.commit()
        self.ok("done", "--force", "x", agent="zz-test-coder")
        ev = self.env.sql("SELECT detail FROM events WHERE action='done-forced-unclean'")
        self.assertEqual(len(ev), 1)
        self.assertIn("not on origin", ev[0]["detail"])

    def test_failing_checks_warn_only(self):
        self.env.seed_pr(checks=[{"name": "unit", "conclusion": "FAILURE"}])
        p = self.ok("done", "x", agent="zz-test-coder")
        self.assertIn("WARNING", p.stderr)
        self.assertIn("unit", p.stderr)
        self.assertTrue(any("CI at done" in m["body"] for m in self.lead_msgs()))

    def test_pending_checks_note(self):
        self.env.seed_pr(checks=[{"name": "unit", "status": "IN_PROGRESS", "conclusion": ""}])
        p = self.ok("done", "x", agent="zz-test-coder")
        self.assertIn("pending", p.stderr)
        self.assertTrue(any("CI at done" in m["body"] for m in self.lead_msgs()))

    def test_gh_failure_is_unknown(self):
        # no PR registered in the stub: `gh pr view` exits 1
        p = self.ok("done", "x", agent="zz-test-coder")
        self.assertIn("CI state unknown", p.stderr)
        self.assertTrue(any("CI at done: unknown" in m["body"] for m in self.lead_msgs()))

    def test_green_ci_adds_nothing(self):
        self.env.seed_pr(checks=[{"name": "unit", "conclusion": "SUCCESS"}])
        self.ok("done", "x", agent="zz-test-coder")
        self.assertFalse(any("CI at done" in m["body"] for m in self.lead_msgs()))

    def fake_gh(self, script):
        """A gh that runs `script`, first on PATH for one run."""
        d = self.env.home / "fakegh"
        d.mkdir(exist_ok=True)
        (d / "gh").write_text("#!/bin/sh\n" + script + "\n")
        (d / "gh").chmod(0o755)
        return {"PATH": f"{d}:{self.env.env()['PATH']}"}

    def no_gh(self):
        """PATH with git only, so `gh` is not found."""
        d = self.env.home / "nogh"
        d.mkdir(exist_ok=True)
        (d / "git").symlink_to(subprocess.run(["which", "git"], capture_output=True, text=True).stdout.strip())
        return {"PATH": str(d)}

    def check_gh_broken(self, extra_env):
        p = self.ok("done", "x", agent="zz-test-coder", extra_env=extra_env)
        self.assertIn("CI state unknown", p.stderr)
        self.assertNotIn("Traceback", p.stderr)
        self.assertEqual(self.agent_status(), "done")
        self.assertTrue(any("CI at done: unknown" in m["body"] for m in self.lead_msgs()))

    def check_gh_broken_force(self, extra_env):
        (self.wt / "a.txt").write_text("changed\n")
        p = self.ok("done", "--force", "x", agent="zz-test-coder", extra_env=extra_env)
        self.assertNotIn("Traceback", p.stderr)
        self.assertEqual(self.agent_status(), "done")
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='done-forced-unclean'")), 1)

    def test_gh_non_json_is_unknown(self):
        self.check_gh_broken(self.fake_gh("echo not-json"))

    def test_gh_non_json_with_force(self):
        self.check_gh_broken_force(self.fake_gh("echo not-json"))

    def test_gh_missing_is_unknown(self):
        self.check_gh_broken(self.no_gh())

    def test_gh_missing_with_force(self):
        self.check_gh_broken_force(self.no_gh())

    def test_gh_timeout_is_unknown(self):
        with self.env.load_factory() as mod:
            def hang(cmd, *a, **kw):
                if cmd[0] == "gh":
                    raise subprocess.TimeoutExpired(cmd, 120)
                return subprocess.run(cmd, *a, **kw)
            mod.subprocess = type("S", (), {"run": staticmethod(hang), "TimeoutExpired": subprocess.TimeoutExpired,
                                            "PIPE": subprocess.PIPE})
            con = mod.db()
            row = con.execute("SELECT * FROM agents WHERE name='zz-test-coder'").fetchone()
            for force in (False, True):
                self.assertEqual(mod.coder_done_findings(con, row, force)[3], "unknown")

    def test_refusal_does_not_ask_gh(self):
        (self.wt / "a.txt").write_text("changed\n")
        marker = self.env.home / "gh-called"
        p = self.refused(3, "done", "x", agent="zz-test-coder", extra_env=self.fake_gh(f"touch {marker}; echo {{}}"))
        self.assertFalse(marker.exists())
        self.nothing_changed()

    def test_force_message_subject_is_short(self):
        (self.wt / "a.txt").write_text("changed\n")
        self.ok("done", "--force", "x", agent="zz-test-coder")
        self.assertTrue(any(m["subject"].endswith("closed with --force") for m in self.lead_msgs()))

    def test_qa_in_dirty_worktree_unchanged(self):
        self.env.seed_agent("zz-test-qa", "qa", goal="G-001", team="g-001-a", task="T-0001")
        (self.wt / "a.txt").write_text("changed\n")
        self.ok("done", "x", agent="zz-test-qa")
        self.assertEqual(self.agent_status("zz-test-qa"), "done")

    def test_coder_without_worktree_on_disk_unchanged(self):
        self.env.sql("UPDATE tasks SET worktree=?", (str(self.wt) + "-missing",))
        self.ok("done", "x", agent="zz-test-coder")


if __name__ == "__main__":
    unittest.main()
