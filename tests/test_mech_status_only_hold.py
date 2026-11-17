"""T-1125: mech_candidates skips a task while judge_status_only_hold applies."""
import unittest

from harness import PR_URL, SHA_A, VALID_PR_BODY, FactoryTestCase, gates_summary

TEAM = "g-001-a"
T = "T-1125"
JUDGE = "zz-test-judge"
CODER = "zz-test-coder"
HELD = "status-only: [head ready] "


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(TEAM)
        e.seed_task(T, team=TEAM, status="in_review", pr_url=PR_URL)
        e.sql("UPDATE tasks SET assignee=? WHERE id=?", (CODER, T))
        e.seed_agent(CODER, "coder", goal="G-001", team=TEAM, task=T)
        e.seed_agent(JUDGE, "judge", goal="G-001", task=T, status="exited")
        e.seed_agent("zz-test-reviewer", "reviewer", goal="G-001", task=T)
        e.seed_agent("zz-test-qa", "qa", goal="G-001", task=T)
        e.use_git_head()
        e.seed_pr(body=VALID_PR_BODY)
        self.ok("config", "mech_merge_retry_s", "0")

    def seed_verdict(self, role, verdict, summary="ok", sha=SHA_A):
        self.env.sql(
            "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
            "VALUES(?,?,?,?,?,?,?,?)",
            (T, PR_URL, sha, role, verdict, summary, "zz", "t"),
        )

    def rq_pass(self):
        for role in ("reviewer", "qa"):
            self.ok("verdict", T, "--pass", "--summary", gates_summary() if role == "qa" else "ok",
                    agent=f"zz-test-{role}")

    def mech_ids(self):
        with self.env.load_factory() as f:
            con = f.db()
            return [t["id"] for t in f.mech_candidates(con)]

    def status_only_hold(self, reason="verdict missing on head"):
        self.env.sql(
            "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
            "VALUES(?,?,?,?,?,?,?,?)",
            (T, PR_URL, SHA_A, "judge", "reject", HELD + reason, JUDGE, "t"),
        )


class MechStatusOnlyHoldTests(Base):
    def test_hold_skips_mech_candidate_with_rq_pass(self):
        self.rq_pass()
        self.assertIn(T, self.mech_ids())
        self.status_only_hold()
        self.assertNotIn(T, self.mech_ids())

    def test_newer_reviewer_verdict_lifts_hold(self):
        self.rq_pass()
        self.status_only_hold()
        self.assertNotIn(T, self.mech_ids())
        self.seed_verdict("reviewer", "pass", "fresh pass")
        self.assertIn(T, self.mech_ids())

    def test_newer_qa_verdict_lifts_hold(self):
        self.rq_pass()
        self.status_only_hold()
        self.assertNotIn(T, self.mech_ids())
        self.seed_verdict("qa", "pass", "fresh pass")
        self.assertIn(T, self.mech_ids())

    def test_full_reject_not_mech_candidate(self):
        self.rq_pass()
        p = self.ok("judge", T, "--reject", "--cause", "unclear_spec", "--reason", "scope", agent=JUDGE)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id=?", (T,))[0]["status"], "in_progress")
        self.assertNotIn(T, self.mech_ids())

    def test_c4_without_hold_check_task_is_candidate(self):
        """Removing judge_status_only_hold from mech_candidates would include T after a status-only hold."""
        self.rq_pass()
        self.status_only_hold()
        with self.env.load_factory() as f:
            con = f.db()
            orig = f.judge_status_only_hold
            f.judge_status_only_hold = lambda con, task_id: False
            try:
                ids = [t["id"] for t in f.mech_candidates(con)]
            finally:
                f.judge_status_only_hold = orig
        self.assertIn(T, ids)


class GreenReviewGateCandidateTests(Base):
    """T-1802: judge_review_gate=green puts a task with no reviewer/QA verdict on the mech and autospawn candidate lists;
    a FAIL on an older head does not drop it (the merge gate refuses a FAIL on the current head); passpass (default) still
    needs both PASS."""

    def autospawn_ids(self):
        with self.env.load_factory() as f:
            con = f.db()
            return [t["id"] for t in f.judge_autospawn_candidates(con)]

    def set_green(self):
        self.ok("config", "judge_review_gate", "green")

    def test_passpass_default_needs_both_pass(self):
        self.assertNotIn(T, self.mech_ids())
        self.assertNotIn(T, self.autospawn_ids())

    def test_green_lists_task_without_verdicts(self):
        self.set_green()
        self.assertIn(T, self.mech_ids())
        self.assertIn(T, self.autospawn_ids())

    def test_green_lists_task_with_fail_on_an_older_head(self):
        self.set_green()
        self.seed_verdict("reviewer", "fail", "bug", sha="0" * 40)
        self.assertIn(T, self.mech_ids())
        self.assertIn(T, self.autospawn_ids())


if __name__ == "__main__":
    unittest.main()
