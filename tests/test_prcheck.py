"""factory prcheck (G-022 item 7): each of its checks has a failing and a passing case through the real CLI, and
`factory done` is gated on a pass for the current HEAD when config prcheck_required is 1."""
import json
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


if __name__ == "__main__":
    unittest.main()
