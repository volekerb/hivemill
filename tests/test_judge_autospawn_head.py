"""T-0969/T-1098: judge autospawn_tick only when reviewer and QA PASS hold on the PR head (verdict_pass_holds_for_pr_head, same as the merge gate)."""
import io
import subprocess
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

    def tick(self, head, carries=False, probe=None, gh=None):
        """Run judge_autospawn_tick with the PR at `head`; carries: what verdict_carries answers. Returns (spawned names, carry calls)."""
        spawned, carry_calls = [], []
        with self.env.load_factory() as f:
            con = f.db()
            f.gate = lambda con, **k: (True, "", {})
            f.digest_clock = lambda: f.dt.datetime.fromtimestamp(NOW, f.dt.timezone.utc)
            f._ci_wake_probe = probe or (lambda url, timeout: {"number": 1, "state": "OPEN", "headRefOid": head, "statusCheckRollup": GREEN,
                                                               "mergeStateStatus": "CLEAN"})
            f.judge_pr_ready = lambda con, url, behind_ok=False: (True, "CI green")
            f.gh_json = gh or (lambda *a, **k: {"url": "https://example.invalid/zz/repo"})
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

    def test_a_hung_gh_gives_that_task_no_judge_and_a_later_task_one(self):
        # T-1098 B2: TimeoutExpired from gh must not end the tick for the candidates after it
        self.env.seed_task("T-0836", team=TEAM, status="in_review")
        for tid in (T, "T-0836"):
            for role in ("reviewer", "qa"):
                self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)",
                             (tid, PR_URL, SHA_A, role, "pass", "ok", "zz", "t"))
        calls = []

        def gh(*a, **k):
            calls.append(1)
            if len(calls) == 1:
                raise subprocess.TimeoutExpired("gh", 1)
            return {"url": "https://example.invalid/zz/repo"}

        spawned, _ = self.tick(SHA_B, carries=True, gh=gh)
        self.assertEqual([n for n in spawned if "t-0835" in n], [])
        self.assertEqual(len([n for n in spawned if "t-0836" in n]), 1)
        self.assertIn("cannot verify", self.waits()[0]["detail"])

    def test_an_oserror_from_gh_gives_no_judge(self):
        self.verdict("reviewer", SHA_A)
        self.verdict("qa", SHA_A)

        def gh(*a, **k):
            raise OSError("gh not found")

        spawned, _ = self.tick(SHA_B, gh=gh)
        self.assertEqual(spawned, [])
        self.assertIn("cannot verify", self.waits()[0]["detail"])

    def test_an_unreadable_head_gives_no_judge(self):
        self.verdict("reviewer", SHA_B)
        self.verdict("qa", SHA_B)
        spawned, _ = self.tick(SHA_B, probe=lambda url, timeout: TimeoutError("gh pr view hung"))
        self.assertEqual(spawned, [])
        self.assertIn("cannot read the PR head", self.waits()[0]["detail"])

    def test_an_empty_head_sha_gives_no_judge(self):
        self.verdict("reviewer", "")
        self.verdict("qa", SHA_B)
        spawned, calls = self.tick(SHA_B)
        self.assertEqual(spawned, [])
        self.assertEqual(calls, [])
        self.assertIn("reviewer PASS is for ?", self.waits()[0]["detail"])

    def test_a_live_judge_costs_no_gh_call(self):
        self.verdict("reviewer", SHA_A)
        self.verdict("qa", SHA_A)
        self.env.seed_agent("judge-t-0835", "judge", goal="G-001", team=TEAM, task=T, status="live")
        probes = []
        self.tick(SHA_B, probe=lambda url, timeout: probes.append(url) or {"headRefOid": SHA_B})
        self.assertEqual(probes, [])

    def test_both_verdicts_on_the_current_head_start_a_judge(self):
        self.verdict("reviewer", SHA_B)
        self.verdict("qa", SHA_B)
        spawned, calls = self.tick(SHA_B)
        self.assertEqual(len(spawned), 1)
        self.assertIn("judge-t-0835", spawned[0])
        self.assertEqual(calls, [])  # no carry check needed when the heads match

    def test_both_on_head_spawns_even_when_gh_repo_view_would_hang(self):
        """Exact-head PASS must spawn without calling gh repo view (no 120s stall on match)."""
        self.verdict("reviewer", SHA_B)
        self.verdict("qa", SHA_B)
        gh_calls = []

        def gh(*a, **k):
            gh_calls.append(a)
            raise subprocess.TimeoutExpired("gh", 120)

        spawned, calls = self.tick(SHA_B, gh=gh)
        self.assertEqual(len(spawned), 1)
        self.assertEqual(calls, [])
        self.assertEqual(gh_calls, [])

    def test_latest_non_pass_verdict_counts_as_stale_not_fresh(self):
        """A newest row that is not PASS must block autospawn like the gate (no {role} PASS)."""
        self.verdict("reviewer", SHA_B)
        self.env.sql(
            "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)",
            (T, PR_URL, SHA_A, "qa", "fail", "nope", "zz", "t2"),
        )
        with self.env.load_factory() as f:
            con = f.db()
            t = con.execute("SELECT * FROM tasks WHERE id=?", (T,)).fetchone()
            f._review_probe = lambda url, timeout, con, clock: {
                "number": 1, "headRefOid": SHA_B, "state": "OPEN",
            }
            stale = f.judge_verdicts_stale(con, t, NOW)
        self.assertIsNotNone(stale)
        self.assertIn("no qa PASS", stale[1])
        kinds = []
        with self.env.load_factory() as f:
            con = f.db()
            carry = {}
            repo = "https://example.invalid/zz/repo"
            for role in ("reviewer", "qa"):
                kinds.append(
                    f.verdict_pass_holds_for_pr_head(con, T, role, SHA_B, 1, f.HOME, repo, "zz-test/repo", carry)[0]
                )
        self.assertEqual(kinds, ["holds", "missing"])

    def test_verdicts_that_carry_to_the_current_head_start_a_judge(self):
        self.verdict("reviewer", SHA_A)
        self.verdict("qa", SHA_A)
        spawned, calls = self.tick(SHA_B, carries=True)
        self.assertEqual(len(spawned), 1)
        self.assertEqual(calls, [(SHA_A, SHA_B)])  # one carry check for the shared old head

    def test_pure_main_merge_carry_matches_train_ready(self):
        """T-0969: verdict_pass_holds_for_pr_head is the shared rule — train_ready and autospawn stale agree on carry/no-carry."""
        self.verdict("reviewer", SHA_A)
        self.verdict("qa", SHA_A)
        repo_url, owner = "https://example.invalid/zz/repo", "zz-test/repo"
        with self.env.load_factory() as f:
            con = f.db()
            t = con.execute("SELECT * FROM tasks WHERE id=?", (T,)).fetchone()
            f._review_probe = lambda url, timeout, con, clock: {
                "number": 1, "headRefOid": SHA_B, "state": "OPEN", "statusCheckRollup": GREEN, "mergeStateStatus": "CLEAN"}
            f.pr_state = lambda url: {"number": 1, "headRefOid": SHA_B, "state": "OPEN", "isDraft": False, "baseRefName": "main",
                                      "statusCheckRollup": GREEN, "mergeable": "MERGEABLE"}
            f.gh_json = lambda *a, **k: {"url": repo_url}
            for carries in (False, True):
                f.verdict_carries = lambda *a, **k: (True, "", "m" * 40, []) if carries else (
                    False, "head moved by a real change", None, [])
                train_probs = f.train_ready(con, T, f.HOME)[4]
                stale = f.judge_verdicts_stale(con, t, NOW)
                carry = {}
                kinds = [f.verdict_pass_holds_for_pr_head(con, T, role, SHA_B, 1, f.HOME, repo_url, owner, carry)[0]
                         for role in ("reviewer", "qa")]
                if carries:
                    self.assertFalse(any("PASS is for" in p for p in train_probs), carries)
                    self.assertIsNone(stale, carries)
                    self.assertEqual(kinds, ["carry", "carry"], carries)
                else:
                    self.assertTrue(any("PASS is for" in p for p in train_probs), carries)
                    self.assertIsNotNone(stale, carries)
                    self.assertEqual(kinds, ["stale", "stale"], carries)


if __name__ == "__main__":
    unittest.main()
