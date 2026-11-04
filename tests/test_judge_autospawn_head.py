"""T-0969: judge_autospawn_tick spawns only when the latest reviewer and QA verdicts are PASS on the PR's current head
(judge gate rule: a verdict at another sha counts only if verdict_carries says it is a pure origin/main merge)."""
import io
import json
import unittest
from contextlib import redirect_stdout

from harness import PR_URL, SHA_A, SHA_B, FactoryTestCase
from harness import git as harness_git

T = "T-0969"
TEAM, LEAD = "g-001-a", "zz-lead"
GREEN = [{"__typename": "CheckRun", "name": "unit", "status": "COMPLETED", "conclusion": "SUCCESS"}]
WAIT = "judge-autospawn-wait"


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(TEAM, lead=LEAD)
        self.env.seed_agent(LEAD, "lead", goal="G-001", team=TEAM, status="exited")
        self.env.seed_task(T, team=TEAM, status="in_review")
        self.env.seed_pr(sha=SHA_A, checks=GREEN)

    def verdict(self, role, sha, verdict="pass"):
        self.env.sql(
            "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)",
            (T, PR_URL, sha, role, verdict, "ok", "zz", "t"))

    def tick(self):
        calls = []
        with self.env.load_factory() as f:
            con = f.db()
            f.gate = lambda con, **k: (True, "", {})

            def fake_spawn(con, role, goal=None, team=None, task=None, name=None, **kw):
                calls.append(task)
                con.execute("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,?,'t')",
                            (name, role, goal, team, task, "live"))

            f.spawn = fake_spawn
            with redirect_stdout(io.StringIO()):
                f.judge_autospawn_tick(con)
            con.commit()
        return calls

    def events(self):
        return [r["detail"] for r in self.env.sql(
            "SELECT detail FROM events WHERE entity='task' AND entity_id=? AND action=? ORDER BY id", (T, WAIT))]


class StaleVerdictTests(Base):
    def test_both_pass_on_head_spawns_judge(self):
        self.verdict("reviewer", SHA_A)
        self.verdict("qa", SHA_A)
        self.assertEqual(self.tick(), [T])
        self.assertEqual(self.events(), [])

    def test_qa_pass_on_older_sha_no_judge(self):
        self.verdict("reviewer", SHA_A)
        self.verdict("qa", SHA_B)
        self.assertEqual(self.tick(), [])
        self.assertEqual(self.events(), [f"head={SHA_A[:12]}: qa PASS is for {SHA_B[:8]}"])

    def test_reviewer_pass_on_older_sha_no_judge(self):
        self.verdict("reviewer", SHA_B)
        self.verdict("qa", SHA_A)
        self.assertEqual(self.tick(), [])
        self.assertEqual(self.events(), [f"head={SHA_A[:12]}: reviewer PASS is for {SHA_B[:8]}"])

    def test_stale_event_once_per_task_and_head(self):
        self.verdict("reviewer", SHA_A)
        self.verdict("qa", SHA_B)
        for _ in range(3):
            self.assertEqual(self.tick(), [])
        self.assertEqual(len(self.events()), 1)
        self.env.seed_pr(sha="c" * 40, checks=GREEN)  # the head moved: a new head gets its own event
        self.assertEqual(self.tick(), [])
        self.assertEqual(len(self.events()), 2)

    def test_head_lookup_failure_no_judge_one_event(self):
        self.verdict("reviewer", SHA_A)
        self.verdict("qa", SHA_A)
        with self.env.load_factory() as f:
            con = f.db()
            f.judge_hold = lambda *a, **k: None  # CI readiness is cached elsewhere; isolate the head lookup
            def boom(url):
                raise SystemExit(1)
            f.pr_state = boom
            f.gate = lambda con, **k: (True, "", {})
            f.spawn = lambda *a, **k: self.fail("judge spawned without a known head")
            with redirect_stdout(io.StringIO()):
                for _ in range(3):
                    f.judge_autospawn_tick(con)
            con.commit()
        self.assertEqual(self.events(), ["head unknown: gh lookup failed"])
        self.assertEqual(self.tick(), [T])  # retried next tick: gh works again, verdicts are on the head


class MergedMainTests(Base):
    """A head that only merged origin/main keeps the PASS verdicts, exactly as the judge gate decides."""

    def setUp(self):
        super().setUp()
        e = self.env
        self.remote, self.wt = e.home / "origin.git", e.home / "pr-wt"
        harness_git("init", "-q", "--bare", "-b", "main", str(self.remote), cwd=e.home)
        harness_git("clone", "-q", str(self.remote), str(self.wt), cwd=e.home)
        self.commit("base.txt", "1\n")
        harness_git("push", "-q", "origin", "HEAD:main", cwd=self.wt)
        harness_git("checkout", "-q", "-b", "feature", cwd=self.wt)
        self.commit("feature.txt", "f\n")
        e.sql("UPDATE tasks SET worktree=? WHERE id=?", (str(self.wt), T))
        e.set_gh(repo_url=str(self.remote))
        self.x = self.publish()
        self.verdict("reviewer", self.x)
        self.verdict("qa", self.x)

    def commit(self, name, text):
        (self.wt / name).write_text(text)
        harness_git("add", name, cwd=self.wt)
        harness_git("commit", "-q", "-m", name, cwd=self.wt)

    def publish(self):
        harness_git("push", "-q", "-f", "origin", "HEAD:refs/pull/1/head", cwd=self.wt)
        sha = harness_git("rev-parse", "HEAD", cwd=self.wt)
        self.env.seed_pr(sha=sha, checks=GREEN)
        return sha

    def main_commit(self):
        other = self.env.home / "other"
        harness_git("clone", "-q", str(self.remote), str(other), cwd=self.env.home)
        (other / "m.txt").write_text("m\n")
        harness_git("add", "m.txt", cwd=other)
        harness_git("commit", "-q", "-m", "main moves", cwd=other)
        harness_git("push", "-q", "origin", "HEAD:main", cwd=other)

    def test_verdicts_on_the_same_head_spawn(self):
        self.assertEqual(self.tick(), [T])

    def test_head_that_only_merged_main_spawns(self):
        self.main_commit()
        harness_git("fetch", "-q", "origin", "main", cwd=self.wt)
        harness_git("merge", "-q", "--no-edit", "origin/main", cwd=self.wt)
        self.assertNotEqual(self.publish(), self.x)
        self.assertEqual(self.tick(), [T])
        self.assertEqual(self.events(), [])

    def test_head_with_new_work_after_the_verdict_does_not_spawn(self):
        self.commit("more.txt", "new work\n")
        self.publish()
        self.assertEqual(self.tick(), [])
        self.assertEqual(len(self.events()), 1)

    def test_merge_with_new_work_does_not_spawn(self):
        self.main_commit()
        harness_git("fetch", "-q", "origin", "main", cwd=self.wt)
        harness_git("merge", "-q", "--no-edit", "origin/main", cwd=self.wt)
        self.commit("more.txt", "new work\n")
        self.publish()
        self.assertEqual(self.tick(), [])


if __name__ == "__main__":
    unittest.main()
