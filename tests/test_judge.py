import json
import unittest

from harness import PR_URL, SHA_A, SHA_B, FactoryTestCase, gates_summary

GREEN = [{"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}]


class JudgeTests(FactoryTestCase):
    INPROC = True
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        e.seed_agent("zz-test-reviewer", "reviewer", goal="G-001", task="T-0001")
        e.seed_agent("zz-test-qa", "qa", goal="G-001", task="T-0001")
        e.seed_agent("zz-test-judge", "judge", goal="G-001", task="T-0001")
        e.seed_agent("zz-test-director", "director")
        e.use_git_head()
        e.seed_pr(checks=GREEN)
        self.ok("config", "judge_ci_wait_s", "0")  # no waiting: these tests assert the immediate refusal (test_judge_wait.py covers the wait)

    def verdicts(self, reviewer=True, qa=True):
        if reviewer:
            self.ok("verdict", "T-0001", "--pass", "--summary", "ok", agent="zz-test-reviewer")
        if qa:
            self.ok("verdict", "T-0001", "--pass", "--summary", gates_summary(), agent="zz-test-qa")

    def judge(self, *extra):
        args = list(extra or ("--merge",))
        if "--reject" in args and "--cause" not in args:
            args += ("--cause", "other")
        return self.env.run("judge", "T-0001", *args, "--reason", "zz-test", agent="zz-test-judge")

    def assert_refused(self, text, p=None):
        p = p or self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("merge refused", p.stderr)
        self.assertIn(text, p.stderr)
        self.assertEqual([c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "merge"]], [])
        self.assertNotEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")
        ev = self.env.sql("SELECT detail FROM events WHERE action='judge-blocked'")
        self.assertTrue(ev and text in ev[-1]["detail"])

    # -- verdicts --------------------------------------------------------
    def test_judge_verdict_records_head_sha(self):
        p = self.ok("verdict", "T-0001", "--pass", "--summary", "ok", agent="zz-test-reviewer")
        self.assertIn(f"reviewer PASS @ {self.env.head_sha[:8]}", p.stdout)
        row = self.env.sql("SELECT * FROM verdicts")[0]
        self.assertEqual((row["head_sha"], row["role"], row["verdict"], row["pr_url"]), (self.env.head_sha, "reviewer", "pass", PR_URL))
        self.env.seed_pr(sha=SHA_B, checks=GREEN)
        self.ok("verdict", "T-0001", "--fail", "--cause", "bug", "--summary", "no", agent="zz-test-qa")
        row = self.env.sql("SELECT * FROM verdicts ORDER BY id DESC")[0]
        self.assertEqual((row["head_sha"], row["role"], row["verdict"]), (SHA_B, "qa", "fail"))

    def test_judge_verdict_needs_pr(self):
        self.env.seed_task("T-0002", pr_url=None)
        p = self.refused(1, "verdict", "T-0002", "--pass", "--summary", gates_summary(), agent="zz-test-qa")
        self.assertIn("no PR yet", p.stderr)

    # -- refusals --------------------------------------------------------
    def test_judge_missing_verdict(self):
        p = self.judge()
        self.assert_refused("no reviewer PASS", p)
        self.assertIn("no qa PASS", p.stderr)

    def test_judge_missing_qa_verdict(self):
        self.verdicts(qa=False)
        self.assert_refused("no qa PASS")

    def test_judge_missing_reviewer_verdict(self):
        self.verdicts(reviewer=False)
        self.assert_refused("no reviewer PASS")

    def test_judge_stale_sha(self):
        self.verdicts()
        self.env.seed_pr(sha=SHA_B, checks=GREEN)
        p = self.judge()
        self.assert_refused("head is now", p)
        self.assertIn(f"reviewer PASS is for {self.env.head_sha[:8]}, head is now {SHA_B[:8]}", p.stderr)
        self.assertIn(f"qa PASS is for {self.env.head_sha[:8]}, head is now {SHA_B[:8]}", p.stderr)

    def test_judge_stale_sha_only_one_role(self):
        self.verdicts(qa=False)
        self.env.seed_pr(sha=SHA_B, checks=GREEN)
        self.ok("verdict", "T-0001", "--pass", "--summary", gates_summary(), agent="zz-test-qa")
        p = self.judge()
        self.assert_refused("reviewer PASS is for", p)
        self.assertNotIn("qa PASS is for", p.stderr)

    def test_judge_red_ci(self):
        self.verdicts()
        self.env.seed_pr(checks=[{"name": "unittest", "status": "COMPLETED", "conclusion": "FAILURE"}])
        self.assert_refused("checks not green: unittest=FAILURE")

    def test_judge_pending_ci(self):
        self.verdicts()
        self.env.seed_pr(checks=[{"name": "unittest", "status": "IN_PROGRESS", "conclusion": None}])
        self.assert_refused("checks not green: unittest=IN_PROGRESS")

    def test_judge_pending_ci_empty_conclusion(self):
        self.verdicts()
        self.env.seed_pr(checks=[{"name": "unittest", "status": "QUEUED", "conclusion": ""}])
        self.assert_refused("checks not green")

    def test_judge_status_context_failure(self):
        self.verdicts()
        self.env.seed_pr(checks=[{"context": "ci/legacy", "state": "FAILURE"}])
        self.assert_refused("ci/legacy=FAILURE")

    def test_judge_one_red_among_green(self):
        self.verdicts()
        self.env.seed_pr(checks=GREEN + [{"name": "lint", "status": "COMPLETED", "conclusion": "CANCELLED"}])
        self.assert_refused("lint=CANCELLED")

    def test_judge_draft_refused(self):
        self.verdicts()
        self.env.seed_pr(draft=True, checks=GREEN)
        self.assert_refused("PR is a draft")

    def test_judge_conflicting_refused(self):
        self.verdicts()
        self.env.seed_pr(mergeable="CONFLICTING", checks=GREEN)
        self.assert_refused("PR has conflicts")

    def test_judge_closed_refused(self):
        self.verdicts()
        self.env.seed_pr(state="CLOSED", checks=GREEN)
        self.assert_refused("PR is CLOSED")

    def test_judge_merged_pr_refused(self):
        self.verdicts()
        self.env.seed_pr(state="MERGED", checks=GREEN)
        self.assert_refused("PR is MERGED")

    def test_judge_auto_merge_off_refused(self):
        self.env.sql("UPDATE projects SET auto_merge=0")
        self.verdicts()
        self.assert_refused("auto_merge=0")

    def test_judge_fail_after_pass_refused(self):
        self.verdicts()
        self.ok("verdict", "T-0001", "--fail", "--cause", "bug", "--summary", "regression", agent="zz-test-reviewer")
        self.assert_refused("no reviewer PASS")

    def test_judge_pass_after_fail_allowed(self):
        self.ok("verdict", "T-0001", "--fail", "--cause", "bug", "--summary", "bad", agent="zz-test-qa")
        self.verdicts()
        self.assertEqual(self.judge().returncode, 0)

    def test_judge_no_pr_refused(self):
        self.env.seed_task("T-0002", pr_url=None)
        p = self.refused(1, "judge", "T-0002", "--merge", "--reason", "r", agent="zz-test-judge")
        self.assertIn("has no PR", p.stderr)

    def test_judge_several_problems_listed_together(self):
        self.env.seed_pr(draft=True, mergeable="CONFLICTING", checks=[{"name": "x", "conclusion": "FAILURE"}])
        p = self.judge()
        for text in ("no reviewer PASS", "no qa PASS", "PR is a draft", "PR has conflicts", "checks not green"):
            self.assertIn(text, p.stderr)

    # -- merge -----------------------------------------------------------
    def test_judge_merge_ok(self):
        self.verdicts()
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn(f"T-0001 MERGED @ {self.env.head_sha[:8]}", p.stdout)
        merges = [c["argv"] for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "merge"]]
        self.assertEqual(len(merges), 1)
        self.assertEqual(merges[0][2], PR_URL)
        self.assertIn("--squash", merges[0])
        self.assertEqual(merges[0][merges[0].index("--match-head-commit") + 1], self.env.head_sha)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")
        v = self.env.sql("SELECT * FROM verdicts WHERE role='judge'")
        self.assertEqual([(r["verdict"], r["head_sha"], r["summary"]) for r in v], [("merge", self.env.head_sha, "zz-test")])
        self.assertEqual(self.env.sql("SELECT detail FROM events WHERE action='merged'")[0]["detail"], f"@{self.env.head_sha[:8]}")

    def test_judge_merge_failure_keeps_task_open(self):
        self.verdicts()
        self.env.set_gh(merge_exit=1, merge_stderr="boom")
        p = self.judge()
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertIn("gh pr merge failed: boom", p.stderr)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "in_review")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM verdicts WHERE role='judge'")[0]["c"], 0)
        self.assertEqual(self.env.sql("SELECT detail FROM events WHERE action='merge-failed'")[0]["detail"], "boom")

    def test_judge_required_check_missing(self):
        self.ok("config", "required_checks", "unittest", agent="zz-test-director")
        self.verdicts()
        self.env.seed_pr(checks=[])
        self.assert_refused("required check unittest missing")

    def test_judge_required_check_present_ok(self):
        self.ok("config", "required_checks", "unittest", agent="zz-test-director")
        self.verdicts()
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("MERGED", p.stdout)

    green_checks = [{"name": "build", "status": "COMPLETED", "conclusion": "SUCCESS"},
               {"name": "stale-ci", "status": "COMPLETED", "conclusion": "SUCCESS"}]

    def test_judge_project_required_checks_override_global(self):
        self.ok("config", "required_checks", "unittest", agent="zz-test-director")
        self.ok("project", "update", "zz-test-proj", "--required-checks", " build , ", agent="zz-test-director")
        self.assertEqual(self.env.sql("SELECT required_checks FROM projects")[0]["required_checks"], "build")
        self.verdicts()
        self.env.seed_pr(checks=self.green_checks)
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("MERGED", p.stdout)

    def test_judge_project_required_check_missing_refused(self):
        self.ok("project", "update", "zz-test-proj", "--required-checks", "build,lint", agent="zz-test-director")
        self.verdicts()
        self.env.seed_pr(checks=self.green_checks)
        self.assert_refused("required check lint missing")

    def test_judge_empty_project_value_keeps_global_gate(self):
        self.ok("config", "required_checks", "unittest", agent="zz-test-director")
        self.verdicts()
        self.env.seed_pr(checks=self.green_checks)
        self.assert_refused("required check unittest missing")

    def test_project_add_update_list_required_checks(self):
        self.ok("project", "add", "dib", "--required-checks", "build, stale-ci", agent="zz-test-director")
        out = self.ok("project", "list").stdout
        self.assertIn("REQUIRED_CHECKS", out)
        self.assertIn("build,stale-ci", out)
        self.ok("project", "update", "dib", "--required-checks", "", agent="zz-test-director")
        self.assertEqual(self.env.sql("SELECT required_checks FROM projects WHERE id='dib'")[0]["required_checks"], "")
        self.refused(1, "project", "update", "nope", "--required-checks", "x", agent="zz-test-director")
        self.refused(1, "project", "update", "dib", agent="zz-test-director")

    def test_judge_required_default_empty(self):
        self.assertIn("required_checks = \n", self.ok("config").stdout)
        self.verdicts()
        self.env.seed_pr(checks=[])
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_judge_merge_nonzero_but_merged(self):
        self.verdicts()
        self.env.set_gh(merge_exit=1, merge_stderr="failed to delete local branch", merge_sets_state="MERGED")
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn(f"T-0001 MERGED @ {self.env.head_sha[:8]}", p.stdout)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")
        v = self.env.sql("SELECT verdict FROM verdicts WHERE role='judge'")
        self.assertEqual([r["verdict"] for r in v], ["merge"])
        ev = self.env.sql("SELECT detail FROM events WHERE action='merge-post-error'")
        self.assertEqual([r["detail"] for r in ev], ["failed to delete local branch"])

    def test_judge_merge_failed_and_pr_unreadable(self):
        self.verdicts()
        self.env.set_gh(merge_exit=1, merge_stderr="boom", merge_removes_pr=True)
        p = self.judge()
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertIn("gh pr merge failed: boom", p.stderr)
        self.assertEqual(self.env.sql("SELECT detail FROM events WHERE action='merge-failed'")[0]["detail"], "boom")
        self.assertNotEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")

    def test_judge_merge_failed(self):
        self.verdicts()
        self.env.set_gh(merge_exit=1, merge_stderr="boom")
        p = self.judge()
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertNotEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action='merge-post-error'")[0]["c"], 0)

    def test_judge_reject_sets_in_progress(self):
        p = self.judge("--reject")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn(f"REJECTED @ {self.env.head_sha[:8]}", p.stdout)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "in_progress")
        v = self.env.sql("SELECT verdict,head_sha FROM verdicts WHERE role='judge'")
        self.assertEqual([(r["verdict"], r["head_sha"]) for r in v], [("reject", self.env.head_sha)])
        self.assertEqual([c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "merge"]], [])

    def test_judge_reject_activates_a_planning_goal_once(self):
        self.env.sql("UPDATE goals SET status='planning' WHERE id='G-001'")
        self.assertEqual(self.judge("--reject").returncode, 0)
        self.assertEqual(self.env.sql("SELECT status FROM goals WHERE id='G-001'")[0]["status"], "active")
        ev = self.env.sql("SELECT detail FROM events WHERE action='goal-auto-active'")
        self.assertEqual(len(ev), 1)
        self.assertIn("judge reject", ev[0]["detail"])

    def test_judge_reject_leaves_a_done_goal_alone(self):
        self.env.sql("UPDATE goals SET status='done' WHERE id='G-001'")
        self.assertEqual(self.judge("--reject").returncode, 0)
        self.assertEqual(self.env.sql("SELECT status FROM goals WHERE id='G-001'")[0]["status"], "done")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action='goal-auto-active'")[0]["c"], 0)

    # -- T-1590: bare-id PR titles -----------------------------------------
    def set_title(self, title):
        st = json.loads(self.env.gh_state.read_text())["prs"][PR_URL]
        self.env.set_gh(prs={PR_URL: {**st, "title": title}})

    def test_judge_refuses_a_bare_id_title(self):
        self.verdicts()
        self.set_title("T-0001")
        self.assert_refused("is only the task id")

    def test_judge_accepts_a_described_title(self):
        self.verdicts()
        self.set_title("T-0001: fix the thing")
        p = self.judge()
        self.assertNotIn("is only the task id", p.stderr)


class PrTitleTickTests(FactoryTestCase):
    NOW = "2026-10-31T12:00:00+00:00"

    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_task("T-0001", status="in_review", pr_url=PR_URL)
        self.env.sql("UPDATE tasks SET title=? WHERE id='T-0001'", ("[SC2][SC4] PR titles enforced " + "x" * 80,))

    def prs(self, *rows):
        self.env.set_gh(pr_list=[{"number": n, "url": f"https://github.com/zz-test/repo/pull/{n}", "headRefName": br,
                                  "headRefOid": "a" * 40, "title": title} for n, br, title in rows])

    def tick(self):
        p = self.env.run("tick", extra_env={"FACTORY_NOW": self.NOW})
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def edits(self):
        return [c["argv"] for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "edit"]]

    def test_bare_id_is_renamed_and_logged(self):
        self.prs((1, "factory/t-0001", "T-0001"))
        self.tick()
        new = ("T-0001: PR titles enforced " + "x" * 80)[:72]
        self.assertEqual(self.edits(), [["pr", "edit", "https://github.com/zz-test/repo/pull/1", "--title", new]])
        ev = self.env.sql("SELECT detail FROM events WHERE action='pr-title-renamed'")
        self.assertEqual(len(ev), 1)
        d = json.loads(ev[0]["detail"])
        self.assertEqual((d["old"], d["new"]), ("T-0001", new))
        self.tick()  # same head: handled once
        self.assertEqual(len(self.edits()), 1)

    def test_mid_title_tag_is_kept(self):
        self.env.sql("UPDATE tasks SET title='Add foo [SC2] bar' WHERE id='T-0001'")
        self.prs((1, "factory/t-0001", "T-0001 "))
        self.tick()
        self.assertEqual(self.edits()[0][-1], "T-0001: Add foo [SC2] bar")

    def test_described_title_is_untouched(self):
        self.prs((1, "factory/t-0001", "T-0001: already fine"))
        self.tick()
        self.assertEqual(self.edits(), [])
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action='pr-title-renamed'")[0]["c"], 0)

    def test_non_factory_branch_is_untouched(self):
        self.prs((1, "feature/x", "T-0001"))
        self.tick()
        self.assertEqual(self.edits(), [])


# G-058 fix 1 (T-1831): verdict classes. Only a code_defect FAIL (and evidence_missing on a risky path) blocks the
# merge gate; env_gap files exactly one box-fix task; the class is stored and shown.
class VerdictClassTests(FactoryTestCase):
    INPROC = True

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal()
        e.seed_task()
        e.seed_pr()
        e.seed_agent("zz-test-qa", "qa", goal="G-001", task="T-0001")
        e.seed_agent("zz-test-reviewer", "reviewer", goal="G-001", task="T-0001")
        self.ok("verdict", "T-0001", "--pass", "--summary", "ok", "--sha", SHA_A, agent="zz-test-reviewer")

    def qa_fail(self, cls=None, summary="cannot run go here"):
        args = ["verdict", "T-0001", "--fail", "--cause", "other", "--summary", summary]
        return self.ok(*args, *(["--class", cls] if cls else []), agent="zz-test-qa")

    def classes(self):
        return [r["verdict_class"] for r in self.env.sql("SELECT verdict_class FROM verdicts ORDER BY id")]

    def holds(self, role, risky=False):
        with self.env.load_factory() as f:
            con = f.db()
            f.pr_touches_risky = lambda url, head: risky
            return f.verdict_pass_holds_for_pr_head(con, "T-0001", role, SHA_A, 1, None, None, "zz/repo", {})[0]

    def test_class_is_stored_and_shown(self):
        self.qa_fail("env_gap")
        self.assertEqual(self.classes(), [None, "env_gap"])
        self.assertIn("[env_gap]", self.ok("task", "show", "T-0001").stdout)

    def test_fail_without_class_is_code_defect(self):
        self.qa_fail()
        self.assertEqual(self.classes(), [None, "code_defect"])

    def test_bad_class_and_pass_with_class_refused(self):
        self.refused(2, "verdict", "T-0001", "--fail", "--cause", "other", "--class", "bogus", "--summary", "x", agent="zz-test-qa")
        self.refused(2, "verdict", "T-0001", "--pass", "--class", "nit", "--summary", gates_summary(), "--sha", SHA_A, agent="zz-test-qa")
        self.assertEqual(self.classes(), [None])

    def test_env_gap_and_nit_do_not_block_the_gate(self):
        for cls in ("env_gap", "nit"):
            self.qa_fail(cls, summary=f"finding {cls}")
            self.assertEqual(self.holds("qa"), "holds", cls)
        self.assertEqual(self.holds("reviewer"), "holds")

    def test_code_defect_blocks_the_gate(self):
        self.qa_fail("code_defect")
        self.assertEqual(self.holds("qa"), "missing")

    def test_evidence_missing_blocks_only_on_a_risky_path(self):
        self.qa_fail("evidence_missing")
        self.assertEqual(self.holds("qa", risky=False), "holds")
        self.assertEqual(self.holds("qa", risky=True), "missing")

    def test_risky_paths(self):
        with self.env.load_factory() as f:
            for p in ("ci/shard.py", ".github/workflows/tests.yml", "roles/hooks/worker-guard", "go/internal/judge/gate.go",
                      "docs/mech_merge.md", "bin/credentials.py"):
                self.assertTrue(f.path_is_risky(p), p)
            for p in ("docs/notes.md", "go/internal/web/views/x.go", "tests/test_x.py"):
                self.assertFalse(f.path_is_risky(p), p)

    def test_non_blocking_fail_is_not_a_dispute_and_candidate(self):
        self.qa_fail("env_gap")
        with self.env.load_factory() as f:
            con = f.db()
            self.assertIsNone(f.mech_dispute(con, "T-0001"))
            self.assertTrue(f.verdict_role_ok(con, "T-0001", "qa"))
        self.qa_fail("code_defect", summary="real bug")
        with self.env.load_factory() as f:
            self.assertFalse(f.verdict_role_ok(f.db(), "T-0001", "qa"))

    def test_env_gap_files_exactly_one_task_on_repeat(self):
        self.qa_fail("env_gap", summary="cannot run go here\nno toolchain")
        self.qa_fail("env_gap", summary="Cannot  run go here\nother detail")
        tasks = self.env.sql("SELECT title, role, goal_id FROM tasks WHERE title LIKE 'Box fix:%'")
        self.assertEqual(len(tasks), 1)
        self.assertEqual((tasks[0]["role"], tasks[0]["goal_id"]), ("coder", "G-001"))
        self.qa_fail("env_gap", summary="a different finding")
        self.assertEqual(len(self.env.sql("SELECT 1 FROM tasks WHERE title LIKE 'Box fix:%'")), 2)

    def test_other_classes_file_no_task(self):
        self.qa_fail("code_defect")
        self.qa_fail("nit", summary="n")
        self.assertEqual(self.env.sql("SELECT 1 FROM tasks WHERE title LIKE 'Box fix:%'"), [])


if __name__ == "__main__":
    unittest.main()
