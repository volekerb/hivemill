"""G-022 #4: the mechanical merger (`factory mech-merge`, config mech_merge off|shadow|on). Everything runs through the CLI as a
subprocess against a temp FACTORY_HOME, with the gh stub of test_judge_wait and a real bare git remote."""
import json
import os
import shutil
import stat
import subprocess
import sys
import time
import unittest

from harness import FACTORY, PR_URL, REPO, FactoryTestCase, gates_summary
from test_gate_integration import seed_run
from test_judge_wait import GH_STUB, GREEN, FAILED, git

# st["run_list"] answers `gh run list` (the main push run check): {"exit": n} fails, {"raw": text} prints text, {"runs": [...]}
# prints the runs with headSha "MAIN" replaced by main's sha. Unset: the latest run is completed/success for main's head.
GH_MECH = GH_STUB.replace(
    'elif argv[:2] == ["repo", "view"]:',
    'elif argv[:2] == ["run", "list"]:\n'
    '    rl = st.get("run_list") or {"runs": [{"databaseId": 7, "status": "completed", "conclusion": "success", "headSha": "MAIN"}]}\n'
    '    if rl.get("exit"):\n'
    '        print("gh: boom", file=sys.stderr)\n'
    '        sys.exit(rl["exit"])\n'
    '    main = subprocess.run(["git", "-C", st["main_repo"], "rev-parse", "refs/heads/main"],\n'
    '                          capture_output=True, text=True, check=True).stdout.strip()\n'
    '    print(rl["raw"] if "raw" in rl else json.dumps([dict(r, headSha=main if r["headSha"] == "MAIN" else r["headSha"]) for r in rl["runs"]]))\n'
    'elif argv[:2] == ["repo", "view"]:')

# st["race_sql"] = [db, sql] runs when the gate reads origin/main: the other run's merge, landing mid-gate
GH_RACE = GH_MECH.replace(
    'print(subprocess.run(["git", "-C"',
    'if st.get("race_sql"):\n        import sqlite3\n        c = sqlite3.connect(st["race_sql"][0]); c.execute(st["race_sql"][1]); c.commit()\n    '
    'print(subprocess.run(["git", "-C"')


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
        gh.write_text(GH_MECH)
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

    def main_run(self, **run):
        """The latest main push run the gh stub reports (defaults: completed, headSha = main); run_list=... via raw=/exit=."""
        st = json.loads(self.jw_state.read_text())
        st["run_list"] = run.pop("run_list", None) or {"runs": [dict({"databaseId": 7, "status": "completed", "conclusion": "success",
                                                                       "headSha": "MAIN"}, **run)]}
        self.jw_state.write_text(json.dumps(st))

    def assert_held(self, p, text):
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("T-0001: blocked", p.stdout)
        ev = self.events("mech-blocked")
        self.assertEqual(len(ev), 1, ev)
        self.assertIn("main push run" if "main push run" in text else text, ev[0])
        self.assertIn(text, ev[0])
        self.assertIn("hold", ev[0])
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.status(), "in_review")

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

    def _behind_ok_main_moves(self, name):
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('stale_strict:zz-test/repo',?)",
                     (json.dumps({"at": time.time(), "strict": False}),))
        other = self.env.home / "other"
        git(self.env.home, "clone", "-q", str(self.remote), str(other))
        (other / name).parent.mkdir(parents=True, exist_ok=True)
        (other / name).write_text("m\n")
        git(other, "add", name)
        git(other, "commit", "-q", "-m", "main moves")
        git(other, "push", "-q", "origin", "HEAD:main")

    def test_behind_ok_with_tests_yml_changed_on_main_is_refused_for_refresh(self):
        # T-1000: behind_ok must not let a head that predates a CI change merge; the mech merger refuses, never refreshes
        self._behind_ok_main_moves(".github/workflows/tests.yml")
        self.mech("on")
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.status(), "in_review")
        self.assertIn("tests.yml changed on main since the merge-base", self.events("mech-blocked")[0])
        self.assertEqual(self.events("refreshed"), [])

    def test_behind_ok_without_tests_yml_change_still_merges(self):
        self._behind_ok_main_moves("main.txt")
        p = self.mech("on")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)
        self.assertEqual(self.status(), "done")
        self.assertIn("tests.yml unchanged on main since the merge-base", self.events("merged")[0])

    def test_behind_ok_with_a_file_changed_by_main_and_the_pr_is_refused_for_refresh(self):
        # T-1025: main edits a file the PR also edits (different lines, merge-tree clean): refused, never refreshed
        self.commit("base.txt", "PR\nb\nc\n", "pr edits base")
        self.x = self.publish()
        self.stub(views=[self.pr(self.x, GREEN)])
        for agent, summary in (("zz-test-reviewer", "ok"), ("zz-test-qa", gates_summary())):
            self.assertEqual(self.cli("verdict", "T-0001", "--pass", "--summary", summary, agent=agent).returncode, 0)
        self._behind_ok_main_moves("base.txt")
        self.mech("on")
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.status(), "in_review")
        self.assertIn("main and the PR both changed base.txt", self.events("mech-blocked")[0])
        self.assertEqual(self.events("refreshed"), [])

    def _judge_candidates(self):
        with self.env.load_factory() as f:
            con = f.db()
            return [r["id"] for r in f.judge_autospawn_candidates(con)]

    def test_a_mech_refused_overlap_pr_keeps_its_judge_route_after_the_retry_window(self):
        # T-1025 B1: T0-T2 overlap PR refused by the merger; 10+ minutes later (judge slots full) it must still reach the judge
        self.commit("base.txt", "PR\nb\nc\n", "pr edits base")
        self.x = self.publish()
        self.stub(views=[self.pr(self.x, GREEN)])
        for agent, summary in (("zz-test-reviewer", "ok"), ("zz-test-qa", gates_summary())):
            self.assertEqual(self.cli("verdict", "T-0001", "--pass", "--summary", summary, agent=agent).returncode, 0)
        self._behind_ok_main_moves("base.txt")
        self.mech("on")
        self.assertEqual(len(self.events("mech-blocked")), 1)
        self.assertIn("T-0001", self._judge_candidates())
        self.env.sql("UPDATE events SET ts='2020-01-01T00:00:00+00:00' WHERE action='mech-blocked'")
        self.assertIn("T-0001", self._judge_candidates())

    def test_a_task_the_merger_has_not_refused_stays_with_the_merger(self):
        self.ok("config", "mech_merge", "on")
        self.assertNotIn("T-0001", self._judge_candidates())

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

    # -- judge hold on the task (G-022 T-1012) ----------------------------
    def test_judge_hold_stops_the_mech_merge_and_clear_restores_it(self):
        self.ok("task", "update", "T-0001", "--judge-hold", "owner wants to look")
        p = self.mech("on")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.status(), "in_review")
        self.ok("task", "update", "T-0001", "--judge-hold-clear")
        p = self.cli("mech-merge")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.status(), "done")
        self.assertEqual(len(self.merges()), 1)

    def test_judge_hold_set_after_the_candidate_list_stops_the_merge(self):
        hook = ('if argv[:2] == ["pr", "view"]:\n    if st.get("hold_db"):\n        import sqlite3\n'
                '        c = sqlite3.connect(st["hold_db"])\n'
                '        c.execute("UPDATE tasks SET judge_hold_reason=\'late hold\' WHERE id=\'T-0001\'")\n        c.commit()\n')
        stub = GH_MECH.replace('if argv[:2] == ["pr", "view"]:\n', hook, 1)
        self.assertNotEqual(stub, GH_MECH)
        (self.gh_dir / "gh").write_text(stub)
        st = json.loads(self.jw_state.read_text())
        st["hold_db"] = str(self.env.db_path)
        self.jw_state.write_text(json.dumps(st))
        p = self.mech("on")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.status(), "in_review")
        self.assertEqual(self.events("judge-hold"), ["late hold"])

    # -- QA live-run marker (roles/qa.md Q7) ------------------------------
    def qa_verdict(self, summary, verdict="pass"):
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
                     "VALUES('T-0001',?,?,'qa',?,?,'x','t')", (PR_URL, self.x, verdict, summary))

    def live_events(self):
        return [d for a in ("mech-blocked", "mech-would-block") for d in self.events(a) if "live-run pending" in d]

    def assert_live_run_held(self, summary, mode="on"):
        self.qa_verdict(summary)
        p = self.mech(mode)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("T-0001: blocked", p.stdout)
        self.assertEqual(len(self.live_events()), 1)
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.status(), "in_review")

    def assert_live_run_not_held(self, summary, older=None):
        if older:
            self.qa_verdict(older)
        self.qa_verdict(summary)
        p = self.mech("on")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.live_events(), [])
        self.assertEqual(len(self.merges()), 1)

    def test_live_run_pending_on_latest_qa_pass_holds(self):
        self.assert_live_run_held("ok\nlive-run: pending AC2 rerun of the scan against the live factory")

    def test_live_run_pending_is_case_and_space_tolerant(self):
        self.assert_live_run_held("ok\n  Live-Run:   PENDING AC3")

    def test_live_run_pending_holds_in_shadow_as_would_block(self):
        self.qa_verdict("ok\nlive-run: pending AC1 x")
        self.assertIn("T-0001: blocked", self.mech("shadow").stdout)
        self.assertEqual(len(self.events("mech-would-block")), 1)
        self.assertEqual(self.events("mech-blocked"), [])

    def test_live_run_done_without_pending_does_not_hold(self):
        self.assert_live_run_not_held("ok\nlive-run: done AC2 exit=0 scan rerun live")

    def test_live_run_older_pending_latest_without_does_not_hold(self):
        self.assert_live_run_not_held("ok again", older="ok\nlive-run: pending AC2 rerun")

    def test_live_run_pending_bullet_quote_backtick_forms_hold(self):
        for form in ("- live-run: pending AC2 x", "* live-run: pending AC2 x",
                     "`live-run: pending AC2 x`", "> live-run: pending AC2 x"):
            with self.subTest(form=form):
                self.env.sql("DELETE FROM events WHERE action IN ('mech-blocked','mech-would-block')")
                self.env.sql("DELETE FROM verdicts WHERE role='qa'")
                self.qa_verdict(f"ok\n{form}")
                p = self.mech("on")
                self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
                self.assertIn("T-0001: blocked", p.stdout)
                self.assertGreaterEqual(len(self.live_events()), 1)
                self.assertEqual(self.merges(), [])

    def test_live_run_pending_numbered_checkbox_bold_forms_hold(self):
        for form in ("1. live-run: pending AC2 x", "- [ ] live-run: pending AC2 x", "**live-run:** pending AC2 x",
                     "`live-run:` pending AC2 x", "2) live-run: pending AC2 x", "- [x] **live-run:** pending AC2 x"):
            with self.subTest(form=form):
                self.env.sql("DELETE FROM events WHERE action IN ('mech-blocked','mech-would-block')")
                self.env.sql("DELETE FROM verdicts WHERE role='qa'")
                self.qa_verdict(f"ok\n{form}")
                p = self.mech("on")
                self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
                self.assertIn("T-0001: blocked", p.stdout)
                self.assertGreaterEqual(len(self.live_events()), 1)
                self.assertEqual(self.merges(), [])

    def test_live_run_done_numbered_and_bold_do_not_hold(self):
        for form in ("1. live-run: done AC2 exit=0", "**live-run:** done AC2 exit=0"):
            with self.subTest(form=form):
                self.env.sql("DELETE FROM verdicts WHERE role='qa'")
                self.assert_live_run_not_held(f"ok\n{form}")

    def test_live_run_pending_mid_line_does_not_hold(self):
        self.assert_live_run_not_held("ok\nnotes: live-run: pending AC2")

    def test_live_run_latest_fail_is_not_a_candidate(self):
        self.qa_verdict("live-run: pending AC2 x", "pass")
        self.qa_verdict("bug", "fail")
        self.mech("on")
        self.assertEqual(self.live_events(), [])
        self.assertEqual(self.merges(), [])

    def test_live_run_entry_point_blocks_before_any_gh_call(self):
        self.qa_verdict("ok\nlive-run: pending AC2 rerun against the live factory")
        self.ok("config", "mech_merge", "on")
        self.jw_calls.write_text("")
        p = self.env.run("mech-merge", "--task", "T-0001", agent="zz-test-director", inproc=False, extra_env={
            "PATH": f"{self.gh_dir}{os.pathsep}{self.env.env()['PATH']}", "JW_STATE": str(self.jw_state), "JW_CALLS": str(self.jw_calls)})
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("T-0001: blocked", p.stdout)
        self.assertEqual(self.status(), "in_review")
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.jw_calls.read_text(), "")  # DB-only check: no gh call at all
        self.assertIn("live-run pending (live-run: pending AC2 rerun", self.live_events()[0])

    def test_live_run_marker_is_accepted_by_factory_verdict(self):
        p = self.cli("verdict", "T-0001", "--pass", "--sha", self.x, "--summary",
                     "ok\nlive-run: pending AC1 x\n" + gates_summary(), agent="zz-test-qa")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        got = self.env.sql("SELECT summary FROM verdicts WHERE task_id='T-0001' AND role='qa' ORDER BY id DESC LIMIT 1")
        self.assertIn("live-run: pending AC1 x", got[0]["summary"])

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
        self.ok("config", "noreply_email", "1+volekerb@users.noreply.github.com")
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

    def test_a_held_per_task_merge_lock_stops_the_merge(self):
        import fcntl
        self.ok("config", "mech_merge", "on")
        run = self.env.home_dir / "run"
        run.mkdir(exist_ok=True)
        with open(run / "merge-T-0001.lock", "w") as f:  # a judge is merging this task right now
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
            p = self.cli("mech-merge")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.events("merged"), [])
        self.assertEqual(self.status(), "in_review")
        self.assertIn("another judge or mechanical merger is merging T-0001", "; ".join(self.events("mech-blocked")))

    # -- late loser: the other run merged and released the lock before this one took it ---
    GATE_VIEWS = {"mech": 2, "judge": 1}  # PR reads the gate makes before it takes the per-task lock; the next is the recheck's

    def race(self, via, **effect):
        """The other run merged and released the lock before this one took it: either the task is already done (set while
        the gate's last PR read is served) or the recheck's PR read answers MERGED."""
        (self.gh_dir / "gh").write_text(GH_RACE)
        views = [self.pr(self.x, GREEN)] * self.GATE_VIEWS[via]
        views.append(self.pr(self.x, GREEN, state="MERGED" if effect.get("pr_merged") else "OPEN"))
        self.stub(views=views)
        if effect.get("task_done"):
            st = json.loads(self.jw_state.read_text())
            st["race_sql"] = [str(self.env.db_path), "UPDATE tasks SET status='done' WHERE id='T-0001'"]
            self.jw_state.write_text(json.dumps(st))
        return self.mech("on") if via == "mech" else self.cli(
            "judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge")

    def assert_late_loser_did_nothing(self, p, text):
        self.assertIn(text, p.stdout + p.stderr)
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.events("merged"), [])
        self.assertEqual(self.env.sql("SELECT COUNT(*) AS n FROM verdicts WHERE role='judge'")[0]["n"], 0)

    def late_loser_cases(self):
        self.env.seed_agent("zz-test-judge", "judge")
        for via in ("mech", "judge"):
            for effect, text in (({"pr_merged": True}, "no longer OPEN"), ({"task_done": True}, "no longer in_review")):
                yield via, effect, text

    def test_late_loser_sees_the_merge_under_the_lock_and_does_nothing(self):
        for via, effect, text in self.late_loser_cases():
            with self.subTest(via=via, effect=effect):
                self.env.sql("UPDATE tasks SET status='in_review' WHERE id='T-0001'")
                self.env.sql("DELETE FROM events WHERE action='mech-blocked'")
                p = self.race(via, **effect)
                if via == "mech":  # the merger records a refusal, it does not die
                    self.assertIn(text, "; ".join(self.events("mech-blocked")))
                else:
                    self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
                self.assert_late_loser_did_nothing(p, "" if via == "mech" else text)

    def test_late_loser_on_the_landed_path_records_nothing(self):
        self.env.seed_agent("zz-test-judge", "judge")
        git(self.wt, "fetch", "-q", "origin", "main")  # the squash landed on main (this run's merge answer was lost)
        sq = git(self.wt, "commit-tree", "HEAD^{tree}", "-p", "origin/main", "-m", "feature work (#1)")
        git(self.wt, "push", "-q", "origin", f"{sq}:refs/heads/main")
        for effect, text in (({"pr_merged": True}, "no longer OPEN"), ({"task_done": True}, "no longer in_review")):
            with self.subTest(effect=effect):
                p = self.race("judge", **effect)
                self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
                self.assert_late_loser_did_nothing(p, text)
                self.assertEqual([l for l in self.jw_calls.read_text().splitlines() if '"close"' in l], [])

    def test_lock_is_released_after_a_merge(self):
        import fcntl
        self.assertEqual(self.mech("on").returncode, 0)
        with open(self.env.home_dir / "run" / "merge-T-0001.lock", "w") as f:
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)  # raises if the merger still held it
        self.assertEqual(len(self.events("merged")), 1)

    def test_only_owner_or_director_may_run_it(self):
        self.ok("config", "mech_merge", "on")
        p = self.cli("mech-merge", agent="zz-test-qa")
        self.assertEqual(p.returncode, 3, p.stdout + p.stderr)
        self.assertEqual(self.merges(), [])

    def test_unknown_mode_counts_as_off(self):
        p = self.mech("bogus")
        self.assertIn("mech_merge is off", p.stdout)
        self.assertEqual(self.merges(), [])

    # -- main push run (T-0917) ------------------------------------------
    def test_main_run_success_for_main_head_reaches_the_gate(self):
        self.main_run()
        p = self.mech("on")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)
        self.assertEqual(self.status(), "done")

    def test_main_run_in_progress_holds(self):
        for status in ("in_progress", "queued"):
            self.main_run(status=status, conclusion="")
            self.assert_held(self.mech("on"), f"main push run 7 still {status}")
            self.env.sql("DELETE FROM events WHERE action='mech-blocked'")

    def test_main_run_failure_holds(self):
        for concl in ("failure", "cancelled"):
            self.main_run(conclusion=concl)
            self.assert_held(self.mech("on"), f"main push run 7 {concl}")
            self.env.sql("DELETE FROM events WHERE action='mech-blocked'")

    def test_main_run_for_another_sha_holds(self):
        self.main_run(headSha="0" * 40)
        main = git(self.wt, "rev-parse", "HEAD~1")
        self.assert_held(self.mech("on"), f"no push run yet for main {main[:8]}")

    def test_main_run_gh_error_and_bad_json_hold(self):
        for rl in ({"exit": 1}, {"raw": "not json"}, {"raw": "[]"}):
            self.main_run(run_list=rl)
            self.assert_held(self.mech("on"), "cannot read main push run")
            self.env.sql("DELETE FROM events WHERE action='mech-blocked'")

    def fix_task(self):
        self.ok("config", "red_main_freeze", "1")
        self.env.sql("INSERT INTO config(key,value) VALUES('red_main:zz-test/repo', ?)",
                     (json.dumps({"run": "7", "sha": "abc", "url": "u", "since": "x", "task": "T-0001"}),))

    def test_main_run_fix_task_passes_a_failed_run(self):
        self.fix_task()
        self.main_run(conclusion="failure")
        p = self.mech("on")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)

    def test_main_run_fix_task_holds_when_failure_is_for_another_sha(self):
        self.fix_task()
        self.main_run(conclusion="failure", headSha="0" * 40)
        main = git(self.wt, "rev-parse", "refs/remotes/origin/main")
        self.assert_held(self.mech("on"), f"no push run yet for main {main[:8]}")

    def test_main_run_fix_task_still_waits_for_a_running_run(self):
        self.fix_task()
        self.main_run(status="in_progress", conclusion="")
        self.assert_held(self.mech("on"), "main push run 7 still in_progress")

    def test_main_run_hold_in_shadow_is_a_would_block(self):
        self.main_run(conclusion="failure")
        p = self.mech("shadow")
        self.assertIn("T-0001: blocked", p.stdout)
        self.assertEqual(len(self.events("mech-would-block")), 1)
        self.assertEqual(self.merges(), [])

    def test_main_run_hold_creates_no_red_main_record(self):
        self.main_run(conclusion="failure")
        self.mech("on")
        self.assertEqual(self.env.sql("SELECT key FROM config WHERE key LIKE 'red_main:%'"), [])

    def test_main_run_entry_point_holds_in_progress(self):
        self.main_run(status="in_progress", conclusion="")
        self.ok("config", "mech_merge", "on")
        p = self.env.run("mech-merge", "--task", "T-0001", agent="zz-test-director", inproc=False, extra_env={
            "PATH": f"{self.gh_dir}{os.pathsep}{self.env.env()['PATH']}", "JW_STATE": str(self.jw_state), "JW_CALLS": str(self.jw_calls)})
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("T-0001: blocked", p.stdout)
        self.assertEqual(self.status(), "in_review")
        self.assertEqual(self.merges(), [])
        ev = self.events("mech-blocked")
        self.assertEqual(len(ev), 1)
        self.assertIn("main push run", ev[0])
        self.assertIn("hold", ev[0])

    # -- T-1308: local gate without tests.yml on main ------------------------------------------
    def test_main_run_skipped_for_local_gate_without_tests_yml(self):
        main = git(self.wt, "rev-parse", "refs/remotes/origin/main")
        self.env.sql("UPDATE projects SET gate_cmd='true', gh_repo='zz-test/repo' WHERE id='zz-test-proj'")
        seed_run(self.env, self.x, main=main)
        self.main_run(run_list={"exit": 1})
        p = self.mech("on")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("main-run check skipped: project zz-test-proj has no CI workflow", p.stderr)
        self.assertEqual(self.events("mech-blocked"), [])
        self.assertEqual(len(self.merges()), 1)
        self.assertEqual(self.status(), "done")

    def test_main_run_github_project_still_held_without_workflow(self):
        self.env.sql("UPDATE projects SET gh_repo='zz-test/repo', gate_cmd='' WHERE id='zz-test-proj'")
        self.main_run(run_list={"exit": 1})
        self.assert_held(self.mech("on"), "cannot read main push run")
        blocked = "; ".join(self.events("mech-blocked"))
        self.assertNotIn("main-run check skipped", blocked)


if __name__ == "__main__":
    unittest.main()
