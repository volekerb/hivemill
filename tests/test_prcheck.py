"""factory prcheck (G-022 item 7): each of its checks has a failing and a passing case through the real CLI, and
`factory done` is gated on a pass for the current HEAD when config prcheck_required is 1."""
import fcntl
import json
import os
import threading
from pathlib import Path
import unittest

from harness import PR_URL, FactoryTestCase, git, pr_body

REPO = Path(__file__).resolve().parent.parent
BRANCH = "factory/t-0001"
NOREPLY = "1+volekerb@users.noreply.github.com"  # the owner's own noreply: the only noreply identity tests may use
AFFECTED_STUB = ("import os, sys\nprint(os.environ.get('AFF_OUT', 'Ran 3 tests in 0.1s'))\n"
                 "sys.exit(int(os.environ.get('AFF_RC', '0')))\n")
TESTS_FILE = "import unittest\n\nclass T(unittest.TestCase):\n    def test_a(self):\n        pass\n\n    def test_b(self):\n        pass\n"


def commit(cwd, name, text="x\n", email=NOREPLY, msg="work"):
    (cwd / name).parent.mkdir(parents=True, exist_ok=True)
    (cwd / name).write_text(text)
    git("add", "-A", cwd=cwd)
    git("commit", "-qm", msg, cwd=cwd, env={"GIT_AUTHOR_EMAIL": email, "GIT_COMMITTER_EMAIL": email})


# -- T-0947: `## Test evidence` needs a literal command line plus an output line ------------------------------
# Test evidence sections of merged PRs #326, #322, #321, #319 and #329, verbatim (they must keep passing).
EVIDENCE_FIXTURES = {
    '326': "`python3 -m unittest tests.test_guard_diff.SettingsCompareTests` -> Ran 13, OK (incl. new test_b4_deleted_template_is_loosened_with_repo_subdir, retry fix for B1: ls-tree --full-tree; fails with --full-tree removed, passes with it). Not run: the full module or the real corpus (load rules). Test count (merge-tree of origin/main + HEAD): 5678; origin/main: 5666. No `def test_` removed or renamed vs merge-base; no existing test changed.\nWith origin/main's guard_diff.py: Ran 12, FAILED (failures=9) (the 3 passes are the no-noise, legacy and no-base-dir cases).",
    '322': '- `cd tests && python3 -m unittest test_worker_guard_flags_read test_guard_impl_coverage`: Ran 10, OK.\n- Hook change removed: new module Ran 5, FAILED (failures=90).\n- def test_ count: merged tree 5666, origin/main 5661.\n- guard-diff and unittest shards: from PR CI.',
    '321': '`python3 -m unittest tests.test_judge_hold_flag -v` → Ran 13 tests, OK (three consecutive runs).\n\n`python3 ci/affected.py --base origin/main --quick --run` → FULL needed: rely on CI (main changed bin/factory_cli.py).\n\nRegressions fail on 96b1939f: swapped `bin/factory_cli.py` to that rev, `test_historical_fails_latest_pass_spawns_when_hold_clear` and `test_exited_judge_row_does_not_block_autospawn` → FAIL.\n\nTest count: origin/main 5666 `def test_`, branch 5679 (+13 in new file).',
    '319': "PR CI run 41709408705 on 3ba705752, all jobs green:\n- `unittest-shard (8)` job, module tests.test_guard_shim (28 of 28 tests in that block) -> `Ran 28 tests in 6.060s`, `OK`\n- `GUARD_IMPL=diff python3 ci/guard_impl_unittest.py run` (guard-diff job; first module tests.test_worker_guard) -> `Ran 41 tests in 47.828s`, `OK`\n- go job: go vet, gofmt, `go test -race` clean\n- Test count (`git grep -E 'def test_' <tree> | wc -l`): main 9685854d7 5697; PR merged into main 5725; 0 removed vs merge-base. C4 done (unmapped shim exits 1).\nSelf-review: 0 findings (attempt 2 diff read in full; Python and Go rules identical, message only). Reviewer PASS on b1923434 (M19931).",
    '329': '`python3 -m unittest discover -s tests -p test_research_skills.py` and the flow, role and plugins modules: all OK. Merged 5682 tests vs main 5679. PR CI green. Details below.',
}


def evidence_body(section):
    return pr_body().replace("`python3 -m unittest` -> OK", section)


class PrcheckTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        (e.home_dir / "ci").symlink_to(REPO / "ci")  # the public-text guard is loaded from the main checkout
        base = e.home / "git"
        base.mkdir()
        self.origin, self.main, self.wt = base / "origin.git", base / "main", base / "wt"
        git("init", "-q", "--bare", "-b", "main", str(self.origin), cwd=base)
        git("clone", "-q", str(self.origin), str(self.main), cwd=base)
        commit(self.main, "ci/affected.py", AFFECTED_STUB, msg="base")
        commit(self.main, "tests/test_x.py", TESTS_FILE, msg="base tests")
        git("push", "-q", "origin", "main", cwd=self.main)
        git("worktree", "add", "-q", "-b", BRANCH, str(self.wt), "origin/main", cwd=self.main)
        commit(self.wt, "work.txt")
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_team(lead="zz-test-lead")
        e.seed_task(status="in_progress", team="g-001-a")
        e.sql("UPDATE tasks SET worktree=?, branch=? WHERE id='T-0001'", (str(self.wt), BRANCH))
        e.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", task="T-0001")
        e.seed_agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        e.seed_pr(url=PR_URL, body=pr_body())

    def prcheck(self, code, *extra, env=None):
        return self.refused(code, "prcheck", *extra, agent="zz-test-coder", extra_env=env)

    def head(self):
        return git("rev-parse", "HEAD", cwd=self.wt)

    def advance_main(self, name="m.txt", text="m\n"):
        git("fetch", "-q", "origin", cwd=self.main)
        commit(self.main, name, text, msg="main moves")
        git("push", "-q", "origin", "main", cwd=self.main)

    def events(self):
        return [json.loads(r["detail"]) for r in self.env.sql(
            "SELECT detail FROM events WHERE entity='task' AND action='prcheck' ORDER BY id")]

    # -- everything green ------------------------------------------------
    def test_all_pass_records_head(self):
        p = self.prcheck(0)
        for name in ("fresh", "identity", "body", "public-text", "affected", "tier"):
            self.assertIn(f"PASS {name}:", p.stdout)
        self.assertIn("ran 3 tests", p.stdout)
        self.assertEqual(self.events(), [{"head": self.head(), "ok": True, "failed": []}])

    def test_non_coder_refused(self):
        p = self.env.run("prcheck", agent="zz-test-lead")
        self.assertEqual(p.returncode, 3, p.stderr)

    def test_dirty_tracked_file_refused(self):
        (self.wt / "work.txt").write_text("changed\n")
        p = self.prcheck(3)
        self.assertIn("uncommitted", p.stderr)
        self.assertEqual(self.events(), [])

    # -- 1 fresh -----------------------------------------------------------
    def test_fresh_behind_but_clean_passes(self):
        self.advance_main()
        p = self.prcheck(0)
        self.assertRegex(p.stdout, r"PASS fresh: behind origin/main by 1; a merge is clean")

    def test_fresh_conflict_fails(self):
        self.advance_main("work.txt", "other\n")
        p = self.prcheck(1)
        self.assertIn("FAIL fresh: merging origin/main conflicts (work.txt)", p.stdout)
        self.assertEqual(self.events()[0]["failed"], ["fresh"])

    # -- 2 identity ----------------------------------------------------------
    def test_identity_real_email_fails(self):
        commit(self.wt, "more.txt", email="real.person@example.com")
        p = self.prcheck(1)
        self.assertIn("FAIL identity: non-noreply identity", p.stdout)
        self.assertIn("author+committer", p.stdout)

    # -- 3 body ---------------------------------------------------------------
    def test_body_missing_sections_fails(self):
        self.env.seed_pr(url=PR_URL, body="just words\n")
        p = self.prcheck(1)
        self.assertIn("FAIL body:", p.stdout)
        self.assertIn("missing section heading", p.stdout)

    def test_body_without_test_command_fails(self):
        self.env.seed_pr(url=PR_URL, body=pr_body().replace("`python3 -m unittest` -> OK", "it works"))
        p = self.prcheck(1)
        self.assertIn("no command in backticks", p.stdout)

    def test_body_no_pr_fails(self):
        self.env.sql("UPDATE tasks SET pr_url=NULL WHERE id='T-0001'")
        p = self.prcheck(1)
        self.assertIn("no PR for this task yet", p.stdout)

    def test_body_gh_failure_fails_closed(self):
        p = self.prcheck(1, "--pr", "https://github.com/zz-test/repo/pull/99")
        self.assertIn("cannot read the body", p.stdout)

    def test_test_count_drop_needs_tests_removed_line(self):
        git("rm", "-q", "tests/test_x.py", cwd=self.wt)
        commit(self.wt, "tests/test_y.py", "import unittest\n\nclass T(unittest.TestCase):\n    def test_a(self):\n        pass\n")
        p = self.prcheck(1)
        self.assertIn("the test count drops (2 at the merge base with origin/main, 1 on HEAD)", p.stdout)
        self.env.seed_pr(url=PR_URL, body=pr_body().replace("## Why", "Tests removed: test_b duplicated test_a\n\n## Why"))
        p = self.prcheck(0)
        self.assertIn("PASS body:", p.stdout)

    def test_tests_added_on_main_are_not_a_drop(self):
        self.advance_main("tests/test_z.py", TESTS_FILE)
        p = self.prcheck(0)
        self.assertIn("PASS body:", p.stdout)

    # -- 4 public text ----------------------------------------------------------
    def test_public_text_hit_fails_and_clean_passes(self):
        terms = self.env.home / "terms.txt"
        terms.write_text("zebracorp\n")
        tenv = {"SCRUB_TERMS_FILE": str(terms)}
        self.ok("config", "public_text_repos", "zz-test/repo")
        commit(self.wt, "notes.txt", "contact zebracorp support\n")
        p = self.prcheck(1, env=tenv)
        self.assertIn("FAIL public-text:", p.stdout)
        self.assertNotIn("zebracorp", p.stdout)
        git("reset", "-q", "--hard", "HEAD~1", cwd=self.wt)
        p = self.prcheck(0, env=tenv)
        self.assertIn("PASS public-text: no hits", p.stdout)

    def test_public_text_without_terms_fails_closed(self):
        self.ok("config", "public_text_repos", "zz-test/repo")
        p = self.prcheck(1)
        self.assertIn("FAIL public-text:", p.stdout)
        self.assertIn("no owner terms", p.stdout)

    # -- 5 affected ----------------------------------------------------------------
    def test_affected_red_fails(self):
        p = self.prcheck(1, env={"AFF_RC": "1", "AFF_OUT": "FAILED (failures=1)"})
        self.assertIn("FAIL affected: ci/affected.py --run exited 1", p.stdout)

    def test_affected_zero_tests_fails(self):
        p = self.prcheck(1, env={"AFF_OUT": "Ran 0 tests in 0.0s"})
        self.assertIn("reported no tests run", p.stdout)

    # -- 5b affected holds a test slot (T-0876) ----------------------------------------
    def hold_slots(self, n):
        d = self.env.user_home / ".cache" / "factory-test-slots"
        d.mkdir(parents=True, exist_ok=True)
        files = []
        for i in range(n):
            f = open(d / f"slot-{i}.lock", "a+")
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
            files.append(f)
            self.addCleanup(f.close)
        return files

    def stub_marker(self):
        """Make the base ci/affected.py stub leave a marker (so a test sees whether it ran) and record slot-0's lock state."""
        probe = ("import fcntl, os\nopen(os.environ['AFF_MARK'], 'a').write('ran\\n')\n"
                 "f = open(os.path.expanduser('~/.cache/factory-test-slots/slot-0.lock'), 'a+')\n"
                 "try:\n    fcntl.flock(f, fcntl.LOCK_SH | fcntl.LOCK_NB)\n    open(os.environ['AFF_MARK'], 'a').write('slot0=free\\n')\n"
                 "except BlockingIOError:\n    open(os.environ['AFF_MARK'], 'a').write('slot0=held\\n')\n")
        commit(self.wt, "ci/affected.py", probe + AFFECTED_STUB, msg="probe")

    def test_affected_waits_when_all_slots_busy_and_gives_up(self):
        self.ok("config", "test_slots", "2")
        self.hold_slots(2)
        mark = self.env.home / "mark"
        self.stub_marker()
        p = self.prcheck(1, env={"AFF_MARK": str(mark), "FACTORY_PRCHECK_SLOT_WAIT_S": "1", "FACTORY_PRCHECK_POLL_S": "0.1"})
        self.assertIn("waiting for a test slot (2/2 busy", p.stdout)
        self.assertIn("FAIL affected: no test slot free", p.stdout)
        self.assertIn("NOT a pass", p.stdout)
        self.assertFalse(mark.exists(), "the affected run must not start while every slot is busy")
        self.assertEqual(self.events()[-1]["failed"], ["affected"])

    def test_affected_runs_once_a_slot_frees_and_holds_it(self):
        self.ok("config", "test_slots", "2")
        files = self.hold_slots(2)
        mark = self.env.home / "mark"
        self.stub_marker()
        threading.Timer(1.0, files[0].close).start()  # closing the file drops its flock: slot 0 frees up
        p = self.prcheck(0, env={"AFF_MARK": str(mark), "FACTORY_PRCHECK_SLOT_WAIT_S": "30", "FACTORY_PRCHECK_POLL_S": "0.1"})
        self.assertIn("waiting for a test slot", p.stdout)
        self.assertIn("PASS affected:", p.stdout)
        self.assertEqual(mark.read_text().splitlines()[0], "ran")

    # -- slots drop to 1 while load1 >= the load cap (owner M16816, T-0903) ----------------------
    def cap(self):
        return 1.25 * (os.cpu_count() or 1)

    def test_below_load_cap_every_configured_slot_is_usable(self):
        self.ok("config", "test_slots", "2")
        self.hold_slots(1)  # slot 0 busy, slot 1 free: a second suite runs at normal load
        mark = self.env.home / "mark"
        self.stub_marker()
        p = self.prcheck(0, env={"AFF_MARK": str(mark), "FACTORY_LOADAVG": str(self.cap() - 0.5)})
        self.assertIn("PASS affected:", p.stdout)
        self.assertEqual(mark.read_text().splitlines()[0], "ran")

    def test_at_or_above_load_cap_a_second_suite_is_refused_without_waiting(self):
        self.ok("config", "test_slots", "2")
        self.hold_slots(1)  # one suite already runs; slot 1 is free but the cap leaves one slot
        mark = self.env.home / "mark"
        self.stub_marker()
        p = self.prcheck(1, env={"AFF_MARK": str(mark), "FACTORY_LOADAVG": str(self.cap()), "FACTORY_PRCHECK_SLOT_WAIT_S": "30"})
        self.assertIn("FAIL affected: not run: load1 ", p.stdout)
        self.assertIn(">= cap ", p.stdout)
        self.assertIn("one local suite box-wide (M16816); rely on PR CI", p.stdout)
        self.assertNotIn("waiting for a test slot", p.stdout)
        self.assertFalse(mark.exists(), "the affected run must not start for a second suite under load")

    def test_at_load_cap_the_first_suite_still_runs(self):
        self.ok("config", "test_slots", "2")
        mark = self.env.home / "mark"
        self.stub_marker()
        p = self.prcheck(0, env={"AFF_MARK": str(mark), "FACTORY_LOADAVG": str(self.cap() + 4)})
        self.assertIn("PASS affected:", p.stdout)
        self.assertEqual(mark.read_text().splitlines()[0], "ran")

    def test_load_refusal_passes_when_ci_is_green_on_the_head(self):
        self.hold_slots(1)
        mark = self.env.home / "mark"
        self.stub_marker()  # commits, so seed the PR head after it
        self.pushed()
        self.ci_pr([self.check("a"), self.check("b")])
        p = self.prcheck(0, env={"AFF_MARK": str(mark), "FACTORY_LOADAVG": str(self.cap() + 4)})
        self.assertIn("PASS affected: not run: load1 ", p.stdout)
        self.assertIn("CI is green on the PR head", p.stdout)
        self.assertFalse(mark.exists())

    def test_load_refusal_fails_when_ci_is_not_green(self):
        self.hold_slots(1)
        self.pushed()
        self.ci_pr([self.check("a"), self.check("b", "FAILURE")])
        p = self.prcheck(1, env={"FACTORY_LOADAVG": str(self.cap() + 4)})
        self.assertIn("FAIL affected: not run: load1 ", p.stdout)
        self.assertIn("CI is not green on the head yet", p.stdout)

    # -- gate_fast_projects: never run affected locally, CI on the head stands in (owner M16816, T-0924) ----
    def test_gate_fast_project_never_runs_affected_and_passes_on_green_ci(self):
        self.ok("config", "gate_fast_projects", "zz-test-proj")
        mark = self.env.home / "mark"
        self.stub_marker()
        self.pushed()
        self.ci_pr([self.check("a"), self.check("b")])
        p = self.prcheck(0, env={"AFF_MARK": str(mark)})
        self.assertIn("PASS affected: not run locally (gate_fast_projects, M16816); CI is green on the PR head", p.stdout)
        self.assertFalse(mark.exists(), "ci/affected.py must not be spawned for a gate_fast project")

    def test_gate_fast_project_fails_and_says_wait_for_ci_when_not_green(self):
        self.ok("config", "gate_fast_projects", "zz-test-proj")
        mark = self.env.home / "mark"
        self.stub_marker()
        self.pushed()
        self.ci_pr([self.check("a"), self.check("b", "FAILURE")])
        p = self.prcheck(1, env={"AFF_MARK": str(mark)})
        self.assertIn("FAIL affected: not run locally (gate_fast_projects, M16816); CI is not green on the head yet", p.stdout)
        self.assertIn("push, wait for CI, rerun prcheck", p.stdout)
        self.assertFalse(mark.exists())

    def test_gate_fast_project_takes_no_test_slot(self):
        self.ok("config", "gate_fast_projects", "zz-test-proj")
        self.hold_slots(2)  # every slot busy: a slot-taking run would wait/fail with "no test slot free"
        self.pushed()
        self.ci_pr([self.check("a"), self.check("b")])
        p = self.prcheck(0, env={"FACTORY_PRCHECK_SLOT_WAIT_S": "0"})
        self.assertNotIn("no test slot free", p.stdout)
        self.assertIn("PASS affected: not run locally", p.stdout)

    def test_non_gate_fast_project_still_runs_affected(self):
        self.ok("config", "gate_fast_projects", "some-other-proj")
        mark = self.env.home / "mark"
        self.stub_marker()
        p = self.prcheck(0, env={"AFF_MARK": str(mark)})
        self.assertIn("PASS affected: ci/affected.py --run ok", p.stdout)
        self.assertEqual(mark.read_text().splitlines()[0], "ran")

    def test_affected_run_holds_slot_visible_to_res(self):
        self.ok("config", "test_slots", "2")
        mark = self.env.home / "mark"
        self.stub_marker()
        # the stub records into the marker file whether slot-0 was flocked while it ran
        p = self.prcheck(0, env={"AFF_MARK": str(mark), "AFF_OUT": "Ran 3 tests in 0.1s"})
        self.assertIn("PASS affected:", p.stdout)
        self.assertEqual(mark.read_text(), "ran\nslot0=held\n")
        self.assertIn("slots 0/2", self.ok("res").stdout)  # released again once prcheck exits

    # -- 6 tier ----------------------------------------------------------------------
    def test_tier_is_printed(self):
        p = self.prcheck(0)
        self.assertRegex(p.stdout, r"PASS tier: tier: ")

    # -- done gating -------------------------------------------------------------------
    def done(self, *extra):
        return self.env.run("done", *extra, "summary", agent="zz-test-coder")

    def agent_status(self):
        return self.env.sql("SELECT status FROM agents WHERE name='zz-test-coder'")[0]["status"]

    def pushed(self):
        git("push", "-q", "-u", "origin", "HEAD", cwd=self.wt)

    def test_flag_default_off_done_allowed(self):
        self.pushed()
        p = self.done()
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_done_refused_without_prcheck(self):
        self.ok("config", "prcheck_required", "1")
        self.pushed()
        for extra in ((), ("--force",)):
            p = self.done(*extra)
            self.assertEqual(p.returncode, 3, p.stderr)
            self.assertIn("no passing `factory prcheck`", p.stderr)
        self.assertEqual(self.agent_status(), "live")

    def test_done_allowed_after_pass(self):
        self.ok("config", "prcheck_required", "1")
        self.pushed()
        self.prcheck(0)
        p = self.done("--force")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.agent_status(), "done")

    def test_done_refused_when_head_moved_after_pass(self):
        self.ok("config", "prcheck_required", "1")
        self.prcheck(0)
        commit(self.wt, "late.txt")
        self.pushed()
        p = self.done()
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("no passing `factory prcheck` on the current HEAD", p.stderr)

    def test_done_refused_when_latest_prcheck_failed(self):
        self.ok("config", "prcheck_required", "1")
        self.prcheck(0)
        self.pushed()
        self.prcheck(1, env={"AFF_RC": "1", "AFF_OUT": "Ran 1 test in 0s"})
        p = self.done()
        self.assertEqual(p.returncode, 3, p.stderr)

    def test_done_without_pr_not_gated(self):
        self.ok("config", "prcheck_required", "1")
        self.env.sql("UPDATE tasks SET pr_url=NULL WHERE id='T-0001'")
        self.pushed()
        p = self.done()
        self.assertEqual(p.returncode, 0, p.stderr)

    # -- done accepts green CI on the PR head instead of the local affected run (T-0873) -----------
    def ci_pr(self, checks, sha=None, body=None):
        self.env.seed_pr(url=PR_URL, sha=sha or self.head(), checks=checks, body=body if body is not None else pr_body())

    @staticmethod
    def check(name="test", concl="SUCCESS", status="COMPLETED"):
        return {"__typename": "CheckRun", "name": name, "workflowName": "ci", "status": status, "conclusion": concl,
                "startedAt": "2026-01-01T00:00:00Z"}

    def test_done_accepts_all_green_ci_on_head_without_affected_run(self):
        self.ok("config", "prcheck_required", "1")
        self.pushed()
        self.ci_pr([self.check("a"), self.check("b")])
        p = self.done()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("accepted via green CI", p.stderr)
        self.assertEqual(self.events(), [])  # no local prcheck (so no affected run) happened

    def test_done_refused_when_a_check_fails_or_pends(self):
        self.ok("config", "prcheck_required", "1")
        self.pushed()
        for bad in (self.check("b", "FAILURE"), self.check("b", "", "IN_PROGRESS")):
            self.ci_pr([self.check("a"), bad])
            p = self.done()
            self.assertEqual(p.returncode, 3, p.stderr)
            self.assertIn("CI not all green", p.stderr)
        self.assertEqual(self.agent_status(), "live")

    def test_done_accepted_for_merged_pr_with_worktree_gone(self):
        """T-0923: the task's PR is MERGED and the worktree was removed: nothing left to prcheck, done succeeds."""
        self.ok("config", "prcheck_required", "1")
        self.pushed()
        self.env.seed_pr(url=PR_URL, state="MERGED", body=pr_body())
        git("worktree", "remove", "--force", str(self.wt), cwd=self.main)
        self.assertFalse(self.wt.exists())
        p = self.done()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.agent_status(), "done")

    def test_done_refused_for_merged_pr_of_another_branch(self):
        """A coder-set pr_url pointing at an unrelated merged PR must not skip the gate."""
        self.ok("config", "prcheck_required", "1")
        self.pushed()
        self.env.seed_pr(url=PR_URL, state="MERGED", body=pr_body())
        st = json.loads(self.env.gh_state.read_text())
        st["prs"][PR_URL]["headRefName"] = "factory/t-9999"
        self.env.gh_state.write_text(json.dumps(st))
        git("worktree", "remove", "--force", str(self.wt), cwd=self.main)
        p = self.done()
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("cannot be read", p.stderr)

    def test_done_still_refused_for_open_pr_with_worktree_gone(self):
        self.ok("config", "prcheck_required", "1")
        self.pushed()
        git("worktree", "remove", "--force", str(self.wt), cwd=self.main)
        p = self.done()
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("cannot be read", p.stderr)
        self.assertEqual(self.agent_status(), "live")

    def test_done_refused_when_pr_head_is_not_local_head(self):
        self.ok("config", "prcheck_required", "1")
        self.pushed()
        self.ci_pr([self.check()], sha="b" * 40)
        p = self.done("--force")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("!= local HEAD", p.stderr)

    def test_done_refused_when_pr_has_no_checks(self):
        self.ok("config", "prcheck_required", "1")
        self.pushed()
        self.ci_pr([])
        p = self.done()
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("no CI checks", p.stderr)

    def test_green_ci_still_enforces_cheap_parts(self):
        self.ok("config", "prcheck_required", "1")
        commit(self.wt, "more.txt", email="real.person@example.com")
        self.pushed()
        self.ci_pr([self.check()])
        p = self.done()
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("identity FAIL", p.stderr)
        self.ci_pr([self.check()], body="just words\n")
        git("reset", "-q", "--hard", "HEAD~1", cwd=self.wt)
        git("push", "-q", "-f", "origin", "HEAD", cwd=self.wt)
        self.ci_pr([self.check()], body="just words\n")
        p = self.done()
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("body FAIL", p.stderr)

    def test_green_ci_refuses_when_behind_main_conflicting(self):
        self.ok("config", "prcheck_required", "1")
        self.advance_main("work.txt", "other\n")
        self.pushed()
        self.ci_pr([self.check()])
        p = self.done()
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("fresh FAIL", p.stderr)

    # -- T-0947: Test evidence needs a command line plus an output line
    def body_result(self, section, code):
        self.env.seed_pr(url=PR_URL, body=evidence_body(section))
        return self.prcheck(code)

    def test_evidence_count_without_command_fails(self):  # acceptance 1 (T-0937 shape)
        p = self.body_result("Ran 5 in the new module, OK.", 1)
        self.assertIn("no command in backticks", p.stdout)
        self.assertIn("e.g. `python3 -m unittest", p.stdout)

    def test_evidence_backticked_file_name_is_not_a_command(self):  # acceptance 2 (T-0928 shape)
        p = self.body_result("`test_ci_corpus_step.py`: Ran 7, OK.", 1)
        self.assertIn("no command in backticks", p.stdout)

    def test_evidence_command_without_output_fails(self):  # acceptance 3
        p = self.body_result("`python3 -m unittest tests.test_x`", 1)
        self.assertIn("no output line", p.stdout)
        self.assertIn("Ran 7, OK", p.stdout)

    def test_evidence_command_only_inside_details_fails(self):  # acceptance 3
        body = pr_body().replace("`python3 -m unittest` -> OK", "Ran 5, OK.").replace(
            "</details>", "`python3 -m unittest tests.test_x` -> Ran 5, OK\n</details>")
        self.env.seed_pr(url=PR_URL, body=body)
        self.assertIn("no command in backticks", self.prcheck(1).stdout)

    def test_evidence_real_sections_pass(self):  # acceptance 4
        for num, section in EVIDENCE_FIXTURES.items():
            with self.subTest(pr=num):
                self.assertIn("PASS body:", self.body_result(section, 0).stdout)

    def test_evidence_fenced_block_passes(self):  # acceptance 5
        p = self.body_result("```\n$ python3 -m unittest tests.test_x\nRan 7 tests in 0.1s\nOK\n```", 0)
        self.assertIn("PASS body:", p.stdout)

    def test_evidence_fenced_block_without_output_fails(self):
        p = self.body_result("```\npython3 -m unittest tests.test_x\n```", 1)
        self.assertIn("no output line", p.stdout)

    def test_evidence_ci_only_line_passes(self):  # acceptance 5 (#319 form)
        p = self.body_result("PR CI run 1 on abc1234: `unittest-shard (8)` job, `python3 -m unittest tests.test_x` -> "
                             "`Ran 28 tests in 6.060s`, `OK`", 0)
        self.assertIn("PASS body:", p.stdout)

    def test_evidence_chained_command_and_var_prefix_pass(self):
        for cmd in ("`cd go && test -z x && go test -race ./...` -> ok", "`FOO=1 ci/affected.py --quick` -> rc=0"):
            with self.subTest(cmd=cmd):
                self.assertIn("PASS body:", self.body_result(cmd, 0).stdout)


if __name__ == "__main__":
    unittest.main()
