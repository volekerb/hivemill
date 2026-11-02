"""factory prcheck (G-022 item 7): each of its checks has a failing and a passing case through the real CLI, and
`factory done` is gated on a pass for the current HEAD when config prcheck_required is 1."""
import fcntl
import json
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


if __name__ == "__main__":
    unittest.main()
