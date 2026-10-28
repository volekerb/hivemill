import json
import unittest

from harness import PR_URL, FactoryTestCase, gates_summary, git

GREEN = [{"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}]
SQUASH = "feature (#1)"
POLL_ENV = {"FACTORY_JUDGE_MERGE_POLL_S": "0", "FACTORY_JUDGE_MERGE_POLLS": "3"}  # no real sleeps, bounded polling


class LandedTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        for name, role in (("zz-test-reviewer", "reviewer"), ("zz-test-qa", "qa"), ("zz-test-judge", "judge"),
                           ("zz-test-coder", "coder")):
            e.seed_agent(name, role, goal="G-001", task="T-0001")
        e.seed_agent("zz-test-director", "director")
        e.use_git_head()
        e.seed_pr(checks=GREEN)
        self.ok("config", "judge_ci_wait_s", "0")
        self.wt = e.home / "head-wt"
        self.remote = e.home / "head-origin.git"

    def verdicts(self):
        self.ok("verdict", "T-0001", "--pass", "--summary", "ok", agent="zz-test-reviewer")
        self.ok("verdict", "T-0001", "--pass", "--summary", gates_summary(), agent="zz-test-qa")

    def judge(self, *extra, agent="zz-test-judge"):
        return self.env.run("judge", "T-0001", *(extra or ("--merge",)), "--reason", "zz-test", agent=agent,
                            extra_env=POLL_ENV)

    def land(self, msg=SQUASH, same_tree=True):
        """A squash commit on the remote main: the PR head's tree (or a different one) on top of main."""
        git("fetch", "-q", "origin", "main", cwd=self.wt)
        if same_tree:
            sha = git("commit-tree", "HEAD^{tree}", "-p", "origin/main", "-m", msg, cwd=self.wt)
        else:
            git("checkout", "-q", "--detach", "origin/main", cwd=self.wt)
            (self.wt / "other.txt").write_text("other\n")
            git("add", "other.txt", cwd=self.wt)
            git("commit", "-q", "-m", msg, cwd=self.wt)
            sha = git("rev-parse", "HEAD", cwd=self.wt)
            git("checkout", "-q", "feature", cwd=self.wt)
        git("push", "-q", "origin", f"{sha}:refs/heads/main", cwd=self.wt)
        return sha

    def main_sha(self):
        return git("rev-parse", "refs/heads/main", cwd=self.remote)

    def task_status(self):
        return self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"]

    def events(self, action):
        return [r["detail"] for r in self.env.sql("SELECT detail FROM events WHERE action=?", (action,))]

    def gh_calls(self, *prefix):
        return [c["argv"] for c in self.env.calls("gh") if c["argv"][:len(prefix)] == list(prefix)]

    def assert_recorded(self, main):
        self.assertEqual(self.task_status(), "done")
        self.assertEqual(self.events("merged"), [f"@{main[:8]}"])
        self.assertEqual(self.events("merge-failed"), [])
        self.assertEqual([r["verdict"] for r in self.env.sql("SELECT verdict FROM verdicts WHERE role='judge'")], ["merge"])

    # -- acceptance 1: a 504 where the squash landed ----------------------
    def test_504_then_pr_state_merged_records_real_main_sha(self):
        self.verdicts()
        self.env.set_gh(merge_exit=1, merge_stderr="HTTP 504: 504 Gateway Timeout", merge_lands_msg=SQUASH,
                        merge_lands_after_views=1)
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        main = self.main_sha()
        self.assertIn(f"T-0001 MERGED @ {main[:8]}", p.stdout)
        self.assert_recorded(main)
        self.assertEqual(len(self.gh_calls("pr", "merge")), 1)
        self.assertEqual(self.gh_calls("pr", "close"), [])  # GitHub already reads MERGED

    def test_504_pr_still_open_but_main_has_the_squash_records_and_closes(self):
        self.verdicts()
        self.env.set_gh(merge_exit=1, merge_stderr="HTTP 504: 504 Gateway Timeout", merge_lands_msg=SQUASH)
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        main = self.main_sha()
        self.assert_recorded(main)
        closes = self.gh_calls("pr", "close")
        self.assertEqual(len(closes), 1)
        self.assertIn(main, closes[0][closes[0].index("--comment") + 1])
        self.assertEqual(len(self.gh_calls("pr", "merge")), 1)

    def test_504_main_commit_for_another_pr_is_not_a_landing(self):
        self.verdicts()
        self.env.set_gh(merge_exit=1, merge_stderr="HTTP 504: 504 Gateway Timeout", merge_lands_msg="feature (#2)")
        p = self.judge()
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertEqual(len(self.events("merge-failed")), 1)

    # -- acceptance 2: a real failure is still merge-failed ---------------
    def test_504_nothing_landed_is_merge_failed_with_bounded_polling(self):
        self.verdicts()
        self.env.set_gh(merge_exit=1, merge_stderr="HTTP 504: 504 Gateway Timeout")
        p = self.judge()
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertIn("gh pr merge failed: HTTP 504", p.stderr)
        self.assertEqual(self.task_status(), "in_review")
        self.assertEqual(self.events("merge-failed"), ["HTTP 504: 504 Gateway Timeout"])
        merge_at = [i for i, c in enumerate(self.env.calls("gh")) if c["argv"][:2] == ["pr", "merge"]][0]
        views = [c for c in self.env.calls("gh")[merge_at:] if c["argv"][:2] == ["pr", "view"]]
        self.assertEqual(len(views), 3)  # FACTORY_JUDGE_MERGE_POLLS, no more
        self.assertEqual(self.gh_calls("pr", "close"), [])

    def test_non_5xx_failure_is_not_polled_even_if_main_has_a_squash(self):
        self.verdicts()
        self.env.set_gh(merge_exit=1, merge_stderr="boom", merge_lands_msg=SQUASH)
        p = self.judge()
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertEqual(self.events("merge-failed"), ["boom"])
        merge_at = [i for i, c in enumerate(self.env.calls("gh")) if c["argv"][:2] == ["pr", "merge"]][0]
        self.assertEqual(len([c for c in self.env.calls("gh")[merge_at:] if c["argv"][:2] == ["pr", "view"]]), 1)

    # -- acceptance 3: already on main before any refresh -----------------
    def test_already_on_main_never_refreshes_pushes_or_squashes_again(self):
        self.verdicts()
        main = self.land()
        refs_before = git("for-each-ref", "--format=%(refname) %(objectname)", cwd=self.remote)
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn(f"T-0001 MERGED @ {main[:8]}", p.stdout)
        self.assert_recorded(main)
        self.assertEqual(self.gh_calls("pr", "merge"), [])
        self.assertEqual(self.events("refreshed"), [])
        self.assertEqual(git("for-each-ref", "--format=%(refname) %(objectname)", cwd=self.remote), refs_before)
        self.assertEqual(len(self.gh_calls("pr", "close")), 1)

    def test_stale_and_not_on_main_is_still_refused_as_before(self):
        self.verdicts()
        self.land(same_tree=False)
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertEqual(self.gh_calls("pr", "merge"), [])
        self.assertNotEqual(self.task_status(), "done")

    # -- acceptance 4: --record-merged ------------------------------------
    def record(self, sha, agent="zz-test-judge"):
        return self.env.run("judge", "T-0001", "--record-merged", sha, "--reason", "zz-test squash landed",
                            agent=agent, extra_env=POLL_ENV)

    def assert_not_recorded(self, p, text):
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("record refused", p.stderr)
        self.assertIn(text, p.stderr)
        self.assertNotEqual(self.task_status(), "done")
        self.assertEqual(self.gh_calls("pr", "close"), [])
        self.assertEqual(self.env.sql("SELECT count(*) c FROM verdicts WHERE role='judge'")[0]["c"], 0)

    def test_record_merged_success_closes_pr_with_pointer(self):
        main = self.land()
        p = self.record(main)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn(f"T-0001 MERGED @ {main[:8]}", p.stdout)
        self.assert_recorded(main)
        closes = self.gh_calls("pr", "close")
        self.assertEqual(len(closes), 1)
        self.assertEqual(closes[0][2], PR_URL)
        self.assertIn(main, closes[0][closes[0].index("--comment") + 1])
        self.assertEqual(self.gh_calls("pr", "merge"), [])
        rec = json.loads(self.events("merge-recorded")[0])
        self.assertEqual((rec["main"], rec["reason"]), (main, "zz-test squash landed"))

    def test_record_merged_accepts_a_short_sha_and_a_head_already_in_main(self):
        git("push", "-q", "origin", "HEAD:refs/heads/main", cwd=self.wt)  # the PR head itself reaches main too
        main = self.land()
        p = self.record(main[:10])
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.task_status(), "done")

    def test_record_merged_refuses_wrong_subject(self):
        main = self.land(msg="feature (#2)")
        self.assert_not_recorded(self.record(main), 'must end with "(#1)"')

    def test_record_merged_refuses_sha_not_on_main(self):
        self.land()
        self.assert_not_recorded(self.record(self.env.head_sha), "is not on origin/main")

    def test_record_merged_refuses_non_empty_diff(self):
        main = self.land(same_tree=False)
        self.assert_not_recorded(self.record(main), "is not empty")

    def test_record_merged_refuses_unknown_sha(self):
        self.assert_not_recorded(self.record("0123456789abcdef"), "is not a commit")

    def test_record_merged_refuses_done_task(self):
        main = self.land()
        self.ok("judge", "T-0001", "--record-merged", main, "--reason", "r", agent="zz-test-judge")
        p = self.record(main)
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("already done", p.stderr)

    def test_record_merged_close_failure_records_nothing(self):
        main = self.land()
        self.env.set_gh(close_exit=1)
        p = self.record(main)
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertNotEqual(self.task_status(), "done")

    def test_record_merged_empty_or_blank_sha_is_refused_and_never_merges(self):
        self.verdicts()
        for bad in ("", "   "):
            p = self.record(bad)
            self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
            self.assertIn("--record-merged needs a commit sha", p.stderr)
            self.assertNotIn("MERGED", p.stdout)
            self.assertEqual(self.gh_calls("pr", "merge"), [])
            self.assertEqual(self.gh_calls("pr", "close"), [])
            self.assertNotEqual(self.task_status(), "done")

    def test_pr_number_text_in_error_is_not_a_5xx(self):
        self.verdicts()
        self.env.set_gh(merge_exit=1, merge_stderr="could not merge #512", merge_lands_msg=SQUASH)
        p = self.judge()
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        merge_at = [i for i, c in enumerate(self.env.calls("gh")) if c["argv"][:2] == ["pr", "merge"]][0]
        self.assertEqual(len([c for c in self.env.calls("gh")[merge_at:] if c["argv"][:2] == ["pr", "view"]]), 1)

    def test_already_on_main_path_emits_verdict_carried(self):
        self.verdicts()
        wt = self.wt  # main moves, the PR head is a pure origin/main merge: both PASSes are for the older head
        git("fetch", "-q", "origin", "main", cwd=wt)
        git("checkout", "-q", "-b", "tmp", "origin/main", cwd=wt)
        (wt / "main.txt").write_text("m\n")
        git("add", "main.txt", cwd=wt)
        git("commit", "-q", "-m", "main moves", cwd=wt)
        git("push", "-q", "origin", "HEAD:main", cwd=wt)
        git("checkout", "-q", "feature", cwd=wt)
        git("merge", "-q", "--no-edit", "origin/main", cwd=wt)
        git("push", "-q", "-f", "origin", "HEAD:refs/pull/1/head", cwd=wt)
        self.env.seed_pr(sha=git("rev-parse", "HEAD", cwd=wt), checks=GREEN)
        main = self.land()
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assert_recorded(main)
        self.assertEqual(len(self.events("verdict-carried")), 2)  # reviewer + qa

    def test_record_merged_roles(self):
        main = self.land()
        for agent in ("zz-test-coder", "zz-test-qa", "zz-test-reviewer"):
            p = self.record(main, agent=agent)
            self.assertEqual(p.returncode, 3, f"{agent}: {p.stdout}{p.stderr}")
            self.assertNotEqual(self.task_status(), "done")
        p = self.record(main, agent="zz-test-director")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.task_status(), "done")


if __name__ == "__main__":
    unittest.main()
