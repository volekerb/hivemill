"""T-1098: the tick starts a judge only when the latest reviewer AND QA PASS both hold for the PR's current head (or carry to it)."""
import io
import unittest
from contextlib import redirect_stdout

from harness import PR_URL, SHA_A, SHA_B, FactoryTestCase

TEAM, LEAD, T = "g-001-a", "zz-lead", "T-0835"
GREEN = [{"__typename": "CheckRun", "name": "unit", "status": "COMPLETED", "conclusion": "SUCCESS"}]
NOW = 1_800_000_000.0


class JudgeHeadTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(TEAM, lead=LEAD)
        self.env.seed_agent(LEAD, "lead", goal="G-001", team=TEAM, status="exited")
        self.env.seed_task(T, team=TEAM, status="in_review")

    def verdict(self, role, sha):
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)",
                     (T, PR_URL, sha, role, "pass", "ok", "zz", "t"))

    def tick(self, head, carries=False):
        """Run judge_autospawn_tick with the PR at `head`; carries: what verdict_carries answers. Returns (spawned names, carry calls)."""
        spawned, carry_calls = [], []
        with self.env.load_factory() as f:
            con = f.db()
            f.gate = lambda con, **k: (True, "", {})
            f.digest_clock = lambda: f.dt.datetime.fromtimestamp(NOW, f.dt.timezone.utc)
            f._ci_wake_probe = lambda url, timeout: {"number": 1, "state": "OPEN", "headRefOid": head, "statusCheckRollup": GREEN,
                                                    "mergeStateStatus": "CLEAN"}
            f.judge_pr_ready = lambda con, url, behind_ok=False: (True, "CI green")
            f.gh_json = lambda *a, **k: {"url": "https://example.invalid/zz/repo"}
            f.verdict_carries = lambda repo, x, y, n, url, owner: carry_calls.append((x, y)) or (
                (True, "", "m" * 40, []) if carries else (False, "head moved by a real change", None, []))

            def fake_spawn(con, role, goal=None, team=None, task=None, name=None, **kw):
                spawned.append(name)
                con.execute("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,'live','t')",
                            (name, role, goal, team, task))

            f.spawn = fake_spawn
            with redirect_stdout(io.StringIO()):
                f.judge_autospawn_tick(con)
            con.commit()
        return spawned, carry_calls

    def waits(self):
        return self.env.sql("SELECT detail FROM events WHERE entity='task' AND entity_id=? AND action='judge-autospawn-wait'", (T,))

    def test_verdicts_on_an_older_head_that_do_not_carry_start_no_judge(self):
        self.verdict("reviewer", SHA_A)
        self.verdict("qa", SHA_A)
        spawned, calls = self.tick(SHA_B)
        self.assertEqual(spawned, [])
        self.assertEqual(calls, [(SHA_A, SHA_B)])
        self.assertEqual(len(self.waits()), 1)
        self.assertIn("reviewer PASS is for aaaaaaaa, head is bbbbbbbb", self.waits()[0]["detail"])

    def test_only_one_verdict_on_the_current_head_starts_no_judge(self):
        self.verdict("reviewer", SHA_B)  # the new head
        self.verdict("qa", SHA_A)  # still the old one: QA is running on the new head
        spawned, _ = self.tick(SHA_B)
        self.assertEqual(spawned, [])
        self.assertIn("qa PASS is for aaaaaaaa", self.waits()[0]["detail"])

    def test_the_wait_event_is_logged_once_per_reason(self):
        self.verdict("reviewer", SHA_A)
        self.verdict("qa", SHA_A)
        self.tick(SHA_B)
        self.tick(SHA_B)
        self.assertEqual(len(self.waits()), 1)

    def test_both_verdicts_on_the_current_head_start_a_judge(self):
        self.verdict("reviewer", SHA_B)
        self.verdict("qa", SHA_B)
        spawned, calls = self.tick(SHA_B)
        self.assertEqual(len(spawned), 1)
        self.assertIn("judge-t-0835", spawned[0])
        self.assertEqual(calls, [])  # no carry check needed when the heads match

    def test_verdicts_that_carry_to_the_current_head_start_a_judge(self):
        self.verdict("reviewer", SHA_A)
        self.verdict("qa", SHA_A)
        spawned, calls = self.tick(SHA_B, carries=True)
        self.assertEqual(len(spawned), 1)
        self.assertEqual(calls, [(SHA_A, SHA_B)])  # one carry check for the shared old head


if __name__ == "__main__":
    unittest.main()
