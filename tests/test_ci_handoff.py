"""T-1003 (G-018, T-0980 task B): config coder_ci_handoff (off|resume|fresh). With it on, a coder's `factory done` at PR open
accepts a pushed head while CI is pending or red (the cheap prcheck parts still run), the tick starts the first-round reviewer
and QA only on a green head, and the merge gate is untouched (docs/prcheck.md, docs/review-autospawn.md)."""
import unittest

from harness import PR_URL, FactoryTestCase, git, pr_body
import test_judge_wait as tjw
import test_prcheck as tp
import test_review_autospawn as tra

FLAG = "coder_ci_handoff"
PENDING = [tp.PrcheckTests.check("a"), tp.PrcheckTests.check("b", "", "IN_PROGRESS")]
RED = [tp.PrcheckTests.check("a"), tp.PrcheckTests.check("b", "FAILURE")]


def own_tests_only(cls):
    """Reuse a test class's setUp and helpers without rerunning its inherited tests."""
    for base in cls.__mro__[1:]:
        for name in vars(base):
            if name.startswith("test_") and name not in vars(cls):
                setattr(cls, name, None)
    return cls


@own_tests_only
class DoneHandoff(tp.PrcheckTests):
    """`factory done` as a subprocess against a real bare origin, worktree and the gh stub (tests/test_prcheck.py setup)."""

    def start(self, mode="resume", checks=PENDING, status="in_review"):
        self.ok("config", "prcheck_required", "1")
        if mode:
            self.ok("config", FLAG, mode)
        self.env.sql("UPDATE tasks SET status=?, pr_url=? WHERE id='T-0001'", (status, PR_URL))
        self.pushed()
        self.ci_pr(checks)

    def test_flag_on_pushed_pending_done_exits_0(self):
        for mode in ("resume", "fresh"):
            with self.subTest(mode=mode):
                self.start(mode)
                p = self.done()
                self.assertEqual(p.returncode, 0, p.stderr)
                self.assertIn("accepted via CI handoff", p.stderr)
                self.assertEqual(self.agent_status(), "done")
                self.env.sql("UPDATE agents SET status='live' WHERE name='zz-test-coder'")

    def test_flag_on_red_head_done_exits_0(self):
        self.start(checks=RED)
        p = self.done()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.agent_status(), "done")

    def test_flag_off_same_state_exits_3_with_todays_message(self):
        for mode in ("off", None):
            with self.subTest(mode=mode):
                self.start(mode)
                p = self.done()
                self.assertEqual(p.returncode, 3, p.stderr)
                self.assertIn("CI not all green", p.stderr)
                self.assertIn("no passing `factory prcheck`", p.stderr)
                self.assertEqual(self.agent_status(), "live")

    def test_flag_on_unpushed_commit_exits_3(self):
        self.start()
        (self.wt / "more.txt").write_text("x\n")
        git("add", "more.txt", cwd=self.wt)
        git("commit", "-q", "-m", "more", cwd=self.wt)
        p = self.done()
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("is not on origin", p.stderr)
        self.assertEqual(self.agent_status(), "live")

    def test_flag_on_pr_head_behind_local_head_exits_3(self):
        self.start()
        self.ci_pr(PENDING, sha="c" * 40)  # the PR does not carry the local HEAD
        p = self.done()
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("push first", p.stderr)

    def test_flag_on_body_fail_exits_3(self):
        self.start()
        self.env.seed_pr(url=PR_URL, sha=self.head(), checks=PENDING, body=pr_body().replace("`python3 -m unittest` -> OK", "it works"))
        p = self.done()
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("body FAIL", p.stderr)
        self.assertEqual(self.agent_status(), "live")

    def test_flag_on_public_text_fail_exits_3(self):
        self.start()
        terms = self.env.home / "terms.txt"
        terms.write_text("zebracorp\n")
        self.ok("config", "public_text_repos", "zz-test/repo")
        tp.commit(self.wt, "notes.txt", "contact zebracorp support\n")
        self.pushed()
        self.ci_pr(PENDING)
        p = self.env.run("done", "summary", agent="zz-test-coder", extra_env={"SCRUB_TERMS_FILE": str(terms)})
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("public-text FAIL", p.stderr)
        self.assertEqual(self.agent_status(), "live")

    def test_done_summary_carries_handoff_line_for_the_lead(self):
        self.start()
        self.assertEqual(self.done().returncode, 0)
        body = self.env.sql("SELECT body FROM messages WHERE recipient='zz-test-lead' AND kind='report' ORDER BY id DESC")[0]["body"]
        self.assertIn("CI pending, tick starts review at green, do not spawn reviewer.", body)

    def test_green_ci_summary_has_no_handoff_line(self):
        self.start(checks=[self.check("a"), self.check("b")])
        self.assertEqual(self.done().returncode, 0)
        body = self.env.sql("SELECT body FROM messages WHERE recipient='zz-test-lead' AND kind='report' ORDER BY id DESC")[0]["body"]
        self.assertNotIn("tick starts review at green", body)


@own_tests_only
class ReviewWaitsForGreen(tra.Base):
    """review_candidates path: with the flag on, no reviewer or QA row while the head is pending or red; both start on green."""

    def step(self, rollup):
        calls = []
        with self.env.load_factory() as f:
            con = f.db()
            f._ci_wake_probe = lambda url, timeout: {"number": 1, "state": "OPEN", "headRefOid": tra.SHA_A, "statusCheckRollup": rollup}
            f._ci_remote_heads = lambda: None

            def fake_spawn(con, role, goal=None, team=None, task=None, name=None, prompt=None, **kw):
                calls.append((role, name))
                con.execute("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,'live','t')",
                            (name, role, goal, team, task))

            f.spawn = fake_spawn
            f.gate = lambda con, **k: (True, "", {})
            f.review_autospawn_tick(con)
            con.commit()
        return calls

    def setUp(self):
        super().setUp()
        self.on()
        self.task(pr_url=PR_URL)

    def rows(self):
        return self.env.sql("SELECT name FROM agents WHERE task_id='T-0001' ORDER BY name")

    def test_flag_on_pending_or_red_head_starts_nothing_green_starts_both(self):
        self.ok("config", FLAG, "resume")
        green, red = tra.GREEN, [dict(tra.GREEN[0], conclusion="FAILURE")]
        for rollup in (tra.PENDING, red, []):
            self.assertEqual(self.step(rollup), [])
            self.assertEqual(self.rows(), [])
        self.assertEqual(self.step(green), [("reviewer", "reviewer-t-0001"), ("qa", "qa-t-0001")])

    def test_flag_off_pending_head_still_starts_review_at_pr_open(self):
        self.assertEqual(len(self.step(tra.PENDING)), 2)


class Kickoff(FactoryTestCase):
    def kick(self, role="coder"):
        with self.env.load_factory() as f:
            return f.coder_ci_handoff_kickoff(f.db(), role, "base")

    def test_coder_kickoff_flag_on_says_do_not_wait(self):
        self.assertEqual(self.kick(), "base")
        self.ok("config", FLAG, "fresh")
        self.assertIn("do not wait for CI", self.kick())
        self.assertIn("factory done", self.kick())
        self.assertEqual(self.kick("reviewer"), "base")
        self.ok("config", FLAG, "off")
        self.assertEqual(self.kick(), "base")


@own_tests_only
class MergeGateWithFlag(tjw.JudgeWaitTests):
    """The existing merge-gate refusals of tests/test_judge_wait.py, run with the flag on: a red or pending head is still refused."""

    def test_red_head_still_refused(self):
        self.ok("config", FLAG, "resume")
        self.stub([self.pr(self.x, tjw.FAILED)])
        self.assert_blocked(self.judge(), "checks not green: unittest=FAILURE")

    def test_pending_head_past_the_wait_still_refused(self):
        self.ok("config", FLAG, "fresh")
        self.ok("config", "judge_ci_wait_s", "1")
        self.stub([self.pr(self.x, tjw.PENDING)])
        self.assert_blocked(self.judge(poll="0.3"), "checks not green: unittest=IN_PROGRESS (waited ")


if __name__ == "__main__":
    unittest.main()
