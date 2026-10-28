import unittest

from harness import SHA_B, FactoryTestCase, gates_summary

SPAWN = "2026-03-01T10:00:00+00:00"
BEFORE = "2026-03-01T09:00:00+00:00"
AFTER = "2026-03-01T11:00:00+00:00"


class JudgeDoneTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task(status="in_review")
        e.seed_agent("zz-test-judge", "judge", goal="G-001", task="T-0001", started=SPAWN)

    def event(self, action, ts):
        self.env.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                     (ts, "zz-test-judge", "task", "T-0001", action, "x"))

    def done(self):
        return self.env.run("done", "MERGED T-0001", agent="zz-test-judge")

    def assert_refused(self):
        p = self.done()
        self.assertEqual(p.returncode, 3, p.stdout + p.stderr)
        self.assertIn("still in_review with no merge/reject recorded since you spawned", p.stderr)
        self.assertIn("wait for `factory judge T-0001 --merge` to exit", p.stderr)
        self.assertIn("If you cannot run factory judge at all, msg your lead and wait; "
                      "the lead stops you with factory stop.", p.stderr)
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-test-judge'")[0]["status"], "live")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action LIKE 'done%'")[0]["c"], 0)

    def test_a_refused_without_outcome_event(self):
        self.assert_refused()

    def test_b_allowed_after_merged(self):
        self.event("merged", AFTER)
        self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0001'")
        self.ok("done", "MERGED T-0001", agent="zz-test-judge")
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-test-judge'")[0]["status"], "done")

    def test_c_allowed_after_blocked_since_spawn(self):
        for action in ("judge-blocked", "merge-failed", "judge-reject"):
            with self.subTest(action=action):
                self.env.sql("DELETE FROM events")
                self.env.sql("UPDATE agents SET status='live' WHERE name='zz-test-judge'")
                self.event(action, AFTER)
                self.ok("done", "REJECTED T-0001", agent="zz-test-judge")

    def test_d_blocked_before_spawn_does_not_count(self):
        self.event("judge-blocked", BEFORE)
        self.assert_refused()

    def test_task_already_done_allowed(self):
        self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0001'")
        self.ok("done", "MERGED T-0001", agent="zz-test-judge")


class VerdictShaTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.auto_sha = False
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        e.seed_agent("zz-test-reviewer", "reviewer", goal="G-001", task="T-0001")
        e.seed_agent("zz-test-qa", "qa", goal="G-001", task="T-0001")
        e.use_git_head()
        e.seed_pr()
        self.head = e.head_sha

    def verdicts(self):
        return self.env.sql("SELECT * FROM verdicts")

    def test_e_pass_without_sha_refused(self):
        p = self.refused(5, "verdict", "T-0001", "--pass", "--summary", "ok", agent="zz-test-reviewer")
        self.assertIn("--sha", p.stderr)
        self.assertIn(self.head, p.stderr)
        self.assertEqual(self.verdicts(), [])
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action LIKE '%-pass'")[0]["c"], 0)

    def test_f_pass_with_wrong_sha_refused_prefix_accepted(self):
        p = self.refused(5, "verdict", "T-0001", "--pass", "--sha", SHA_B[:7], "--summary", "ok", agent="zz-test-reviewer")
        self.assertIn("head moved", p.stderr)
        self.assertEqual(self.verdicts(), [])
        self.ok("verdict", "T-0001", "--pass", "--sha", self.head[:7], "--summary", "ok", agent="zz-test-reviewer")
        self.assertEqual(self.verdicts()[0]["head_sha"], self.head)
        self.ok("verdict", "T-0001", "--pass", "--sha", self.head, "--summary", gates_summary(), agent="zz-test-qa")
        self.assertEqual(self.verdicts()[1]["head_sha"], self.head)

    def test_bad_sha_format_refused(self):
        self.refused(5, "verdict", "T-0001", "--pass", "--sha", "abc", "--summary", "ok", agent="zz-test-reviewer")
        self.assertEqual(self.verdicts(), [])

    def test_g_fail_without_sha_accepted(self):
        self.ok("verdict", "T-0001", "--fail", "--summary", "no", agent="zz-test-qa")
        row = self.verdicts()[0]
        self.assertEqual((row["verdict"], row["head_sha"]), ("fail", self.head))

    def test_fail_with_head_prefix_stored_as_full_head(self):
        p = self.ok("verdict", "T-0001", "--fail", "--sha", self.head[:7], "--summary", "no", agent="zz-test-qa")
        self.assertNotIn("WARNING", p.stderr)
        self.assertEqual(self.verdicts()[0]["head_sha"], self.head)

    def test_fail_with_other_sha_warns_and_records(self):
        p = self.ok("verdict", "T-0001", "--fail", "--sha", SHA_B, "--summary", "no", agent="zz-test-qa")
        self.assertIn("WARNING", p.stderr)
        self.assertEqual(self.verdicts()[0]["head_sha"], SHA_B)


if __name__ == "__main__":
    unittest.main()
