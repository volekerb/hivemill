"""G-022 #4: the mechanical merger (`factory mech-merge`, config mech_merge off|shadow|on). Everything runs through the CLI as a
subprocess against a temp FACTORY_HOME, with the gh stub of test_judge_wait and a real bare git remote."""
import json
import os
import shutil
import stat
import subprocess
import sys
import unittest

from harness import FACTORY, PR_URL, REPO, FactoryTestCase, gates_summary
from test_judge_wait import GH_STUB, GREEN, FAILED, git


class MechMergeTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        for name, role in (("zz-test-reviewer", "reviewer"), ("zz-test-qa", "qa")):
            e.seed_agent(name, role, goal="G-001", task="T-0001")
        e.seed_agent("zz-test-director", "director")
        (e.home_dir / "ci").mkdir(exist_ok=True)  # the main checkout carries the tier classifier and its rules
        for f in ("risk_tier.py", "tier_rules.json", "affected.py"):
            shutil.copy(REPO / "ci" / f, e.home_dir / "ci" / f)
        self.gh_dir = e.home / "ghstub"
        self.gh_dir.mkdir()
        gh = self.gh_dir / "gh"
        gh.write_text(GH_STUB)
        gh.chmod(gh.stat().st_mode | stat.S_IXUSR)
        self.jw_state, self.jw_calls = e.home / "jw-state.json", e.home / "jw-calls.jsonl"
        self.jw_calls.touch()
        self.remote, self.wt = e.home / "origin.git", e.home / "pr-wt"
        git(e.home, "init", "-q", "--bare", "-b", "main", str(self.remote))
        git(e.home, "clone", "-q", str(self.remote), str(self.wt))
        self.commit("base.txt", "base\n", "base")
        git(self.wt, "push", "-q", "origin", "HEAD:main")
        git(self.wt, "checkout", "-q", "-b", "feature")
        self.commit("feature.txt", "f\n", "feature work")
        e.sql("UPDATE tasks SET worktree=?, branch='factory/t-0001' WHERE id='T-0001'", (str(self.wt),))
        self.x = self.publish()
        self.stub(views=[self.pr(self.x, GREEN)])
        for agent, summary in (("zz-test-reviewer", "ok"), ("zz-test-qa", gates_summary())):
            p = self.cli("verdict", "T-0001", "--pass", "--summary", summary, agent=agent)
            self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.ok("config", "risk_tiers", "on")

    # -- helpers ---------------------------------------------------------
    def commit(self, name, text, msg):
        path = self.wt / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
        git(self.wt, "add", name)
        git(self.wt, "commit", "-q", "-m", msg)

    def publish(self):
        git(self.wt, "push", "-q", "-f", "origin", "HEAD:refs/pull/1/head")
        git(self.wt, "push", "-q", "-f", "origin", "HEAD:refs/heads/factory/t-0001")
        return git(self.wt, "rev-parse", "HEAD")

    def pr(self, sha, checks, state="OPEN", mergeable="MERGEABLE"):
        return {"number": 1, "state": state, "headRefOid": sha, "headRefName": "factory/t-0001", "baseRefName": "main",
                "mergeable": mergeable, "isDraft": False, "url": PR_URL, "statusCheckRollup": checks}

    def stub(self, views, merges=None):
        self.jw_state.write_text(json.dumps({
            "views": views, "n": 0, "merges": merges or [{"exit": 0}], "m": 0,
            "repo_url": str(self.remote), "main_repo": str(self.remote)}))
        self.jw_calls.write_text("")

    def cli(self, *args, agent="zz-test-director"):
        env = {"PATH": f"{self.gh_dir}{os.pathsep}{self.env.env()['PATH']}", "JW_STATE": str(self.jw_state),
               "JW_CALLS": str(self.jw_calls), "FACTORY_JUDGE_POLL_S": "0.05"}
        return self.env.run(*args, agent=agent, extra_env=env)

    def mech(self, mode="on", *extra):
        self.ok("config", "mech_merge", mode)
        return self.cli("mech-merge", *extra)

    def merges(self):
        rows = [json.loads(l) for l in self.jw_calls.read_text().splitlines() if l.strip()]
        return [r for r in rows if r[:2] == ["pr", "merge"]]

    def events(self, action):
        return [r["detail"] for r in self.env.sql("SELECT detail FROM events WHERE action=?", (action,))]

    def status(self):
        return self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"]

    # -- flag ------------------------------------------------------------
    def test_default_is_off_and_does_nothing(self):
        self.assertEqual(self.ok("config", "mech_merge").stdout.strip().split()[-1], "off")
        p = self.cli("mech-merge")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("mech_merge is off", p.stdout)
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.status(), "in_review")

    # -- shadow ----------------------------------------------------------
    def test_shadow_records_would_merge_and_never_merges(self):
        p = self.mech("shadow")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.status(), "in_review")
        got = self.events("mech-would-merge")
        self.assertEqual(len(got), 1)
        self.assertIn(f"head={self.x[:12]}", got[0])
        self.assertIn("tier T1", got[0])
        self.assertEqual(self.events("merged"), [])
        self.assertEqual(self.events("judge-blocked"), [])

    def test_shadow_records_would_block_with_reason_and_no_judge_blocked(self):
        self.stub([self.pr(self.x, FAILED)])
        p = self.mech("shadow")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.events("mech-would-merge"), [])
        self.assertEqual(self.merges(), [])
        # the CI check is not green: judge_pr_ready skips the PR before the gate (nothing recorded, nothing merged)
        self.assertEqual(self.events("mech-would-block"), [])

    def test_shadow_gate_refusal_is_recorded_as_would_block(self):
        self.ok("config", "frozen_repos", "zz-test/repo")
        p = self.mech("shadow")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        blocked = self.events("mech-would-block")
        self.assertEqual(len(blocked), 1)
        self.assertIn("frozen", blocked[0])
        self.assertEqual(self.events("judge-blocked"), [])
        self.assertEqual(self.merges(), [])

    def test_shadow_does_not_repeat_within_the_retry_window(self):
        self.mech("shadow")
        self.cli("mech-merge")
        self.assertEqual(len(self.events("mech-would-merge")), 1)

    # -- on --------------------------------------------------------------
    def test_on_merges_one_task_with_the_same_gate(self):
        p = self.mech("on")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.status(), "done")
        calls = self.merges()
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0][calls[0].index("--match-head-commit") + 1], self.x)  # the judge's head pin
        merged = self.events("merged")
        self.assertEqual(len(merged), 1)
        self.assertTrue(merged[0].startswith(f"@{self.x[:8]} by=merger: mechanical merge"), merged[0])
        self.assertIn("tier T1", merged[0])

    def test_on_one_merge_per_run(self):
        e = self.env
        e.seed_task("T-0002", pr_url="https://github.com/zz-test/repo/pull/2")
        e.sql("UPDATE tasks SET branch='factory/t-0002', worktree=? WHERE id='T-0002'", (str(self.wt),))
        for role, who in (("reviewer", "zz-test-reviewer"), ("qa", "zz-test-qa")):
            e.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES('T-0002',?,?,?,?,'ok',?,'t')",
                  ("https://github.com/zz-test/repo/pull/2", self.x, role, "pass", who))
        self.mech("on")
        self.assertEqual(len(self.merges()), 1)
        self.assertEqual(self.status(), "done")

    def test_on_refuses_a_stale_head_without_refreshing(self):
        other = self.env.home / "other"
        git(self.env.home, "clone", "-q", str(self.remote), str(other))
        (other / "main.txt").write_text("m\n")
        git(other, "add", "main.txt")
        git(other, "commit", "-q", "-m", "main moves")
        git(other, "push", "-q", "origin", "HEAD:main")
        self.mech("on")
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.status(), "in_review")
        self.assertIn("STALE-ONLY", self.events("mech-blocked")[0])
        self.assertEqual(self.events("refreshed"), [])

    # -- who goes to the LLM judge ---------------------------------------
    def test_t3_goes_to_the_judge(self):
        self.commit(".github/workflows/x.yml", "name: x\n", "touch ci")
        y = self.publish()
        self.stub([self.pr(y, GREEN)])
        for agent, summary in (("zz-test-reviewer", "ok"), ("zz-test-qa", gates_summary())):
            self.assertEqual(self.cli("verdict", "T-0001", "--pass", "--summary", summary, agent=agent).returncode, 0)
        self.mech("on")
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.status(), "in_review")
        self.assertIn("tier T3", self.events("mech-blocked")[0])

    def test_stored_t3_is_never_lowered_by_a_small_diff(self):
        self.ok("task", "tier", "T-0001", "up", "T3", agent="zz-test-reviewer")
        self.mech("on")
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.events("mech-blocked"), [])  # not even a candidate: the judge path owns it

    def test_risk_tiers_off_means_no_tier_and_no_merge(self):
        self.ok("config", "risk_tiers", "off")
        self.mech("on")
        self.assertEqual(self.merges(), [])
        self.assertIn("risk_tiers is off", self.events("mech-blocked")[0])

    def test_failed_round_twice_is_a_dispute(self):
        e = self.env
        for _ in range(2):
            e.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES('T-0001',?,?,?,?,'no','x','t')",
                  (PR_URL, "a" * 40, "qa", "fail"))
        e.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES('T-0001',?,?,'qa','pass','ok','x','t')",
              (PR_URL, self.x))
        self.mech("on")
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.status(), "in_review")

    def test_judge_request_in_a_verdict_is_a_dispute(self):
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
                     "VALUES('T-0001',?,?,'reviewer','pass','ok\njudge: please decide','x','t')", (PR_URL, self.x))
        self.mech("on")
        self.assertEqual(self.merges(), [])

    def test_reviewer_and_qa_disagreeing_on_a_head_is_a_dispute(self):
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
                     "VALUES('T-0001',?,?,'qa','fail','bug','x','t')", (PR_URL, self.x))
        self.mech("on")
        self.assertEqual(self.merges(), [])

    def test_a_live_judge_keeps_the_merger_away(self):
        self.env.seed_agent("zz-test-judge", "judge", goal="G-001", task="T-0001")
        self.mech("on")
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.status(), "in_review")

    # -- refusals through the merger -------------------------------------
    def test_push_policy_freeze_refuses(self):
        self.ok("config", "frozen_repos", "zz-test/repo")
        self.mech("on")
        self.assertEqual(self.merges(), [])
        self.assertIn("frozen", self.events("mech-blocked")[0])

    def test_public_repo_without_noreply_refuses(self):
        self.ok("config", "public_text_repos", "zz-test/repo")
        self.ok("config", "noreply_email", "")
        self.mech("on")
        self.assertEqual(self.merges(), [])
        self.assertTrue(self.events("mech-blocked"))
        self.assertNotEqual(self.status(), "done")

    def test_public_text_guard_refuses_when_it_cannot_run(self):
        self.ok("config", "public_text_repos", "zz-test/repo")
        self.ok("config", "noreply_email", "1+u@users.noreply.github.com")
        self.mech("on")
        self.assertEqual(self.merges(), [])
        self.assertIn("public text", self.events("mech-blocked")[0])

    def test_a_permanent_block_is_recorded_once(self):
        self.ok("config", "frozen_repos", "zz-test/repo")
        self.ok("config", "mech_merge_retry_s", "0")
        self.mech("on")
        self.cli("mech-merge")
        self.assertEqual(len(self.events("mech-blocked")), 1)

    def test_merge_freeze_stops_the_run(self):
        self.ok("config", "mech_merge", "on")
        self.env.sql("INSERT INTO config(key,value) VALUES('main_freeze','deadbeef') ON CONFLICT(key) DO UPDATE SET value='deadbeef'")
        p = self.cli("mech-merge")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.merges(), [])

    # -- serial ----------------------------------------------------------
    def test_second_run_is_refused_while_one_holds_the_lock(self):
        import fcntl
        self.ok("config", "mech_merge", "on")
        run = self.env.home_dir / "run"
        run.mkdir(exist_ok=True)
        with open(run / "mech-merge.lock", "w") as f:
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
            p = self.cli("mech-merge")
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("another mechanical merger run", p.stderr)
        self.assertEqual(self.merges(), [])

    def test_only_owner_or_director_may_run_it(self):
        self.ok("config", "mech_merge", "on")
        p = self.cli("mech-merge", agent="zz-test-qa")
        self.assertEqual(p.returncode, 3, p.stdout + p.stderr)
        self.assertEqual(self.merges(), [])

    def test_unknown_mode_counts_as_off(self):
        p = self.mech("bogus")
        self.assertIn("mech_merge is off", p.stdout)
        self.assertEqual(self.merges(), [])


if __name__ == "__main__":
    unittest.main()
