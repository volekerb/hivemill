"""G-022 T-1029: flag judge_wait_main_run. `factory judge --merge` waits while the tests.yml push run for the current main
head is not completed+success; the named red-main fix task may merge on a failed run. Temp FACTORY_HOME, the tests/stubs gh."""
import json

from harness import REPO, FactoryTestCase
import test_public_repo_safety as base

URL = "https://github.com/acme/priv/pull/1"


class JudgeMainRunTests(FactoryTestCase):
    judge_setup = base.FrozenTests.judge_setup
    pr_repo = base.FrozenTests.pr_repo
    bare = base.Base.bare

    def setUp(self):
        super().setUp()
        (self.env.home_dir / "ci").symlink_to(REPO / "ci")
        self.ok("config", "git_push", "1")
        self.judge_setup(URL, "acme/priv")
        self.env.sql("UPDATE projects SET gh_repo='acme/priv' WHERE id='zz-test-proj'")
        self.main = base.run_git(self.env.home / "remotes" / "acme" / "priv.git", "rev-parse", "refs/heads/main")

    def run_json(self, status, conclusion=None, sha=None):
        return {"databaseId": 7, "status": status, "conclusion": conclusion, "headSha": sha or self.main,
                "createdAt": "2026-11-04T10:00:00Z", "url": "https://github.com/acme/priv/actions/runs/7"}

    def runs(self, *runs, **kw):
        self.env.set_gh(runs={"acme/priv": list(runs)}, **kw)

    def merge(self):
        return self.env.run("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge")

    def merges(self):
        return [c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "merge"]]

    def refused(self, text):
        p = self.merge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn(text, p.stderr)
        self.assertEqual(self.merges(), [])
        ev = self.env.sql("SELECT detail FROM events WHERE action='judge-blocked'")
        self.assertTrue(any("judge_wait_main_run" in (r["detail"] or "") for r in ev), ev)

    def test_flag_default_off_ignores_main_run(self):
        self.runs(self.run_json("in_progress"))
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)
        self.assertEqual([c for c in self.env.calls("gh") if c["argv"][:2] == ["run", "list"]], [])

    def test_in_progress_run_refuses(self):
        self.ok("config", "judge_wait_main_run", "1")
        self.runs(self.run_json("in_progress"))
        self.refused("main run is in_progress")

    def test_failed_run_refuses(self):
        self.ok("config", "judge_wait_main_run", "1")
        self.runs(self.run_json("completed", "failure"))
        self.refused("concluded failure")

    def test_no_run_for_head_refuses_even_if_older_run_green(self):
        self.ok("config", "judge_wait_main_run", "1")
        self.runs(self.run_json("completed", "success", sha="e" * 40))
        self.refused("has not started")

    def test_gh_failure_refuses(self):
        self.ok("config", "judge_wait_main_run", "1")
        self.runs(self.run_json("completed", "success"), run_list_exit=1)
        self.refused("main run unknown")

    def test_green_run_at_head_merges(self):
        self.ok("config", "judge_wait_main_run", "1")
        self.runs(self.run_json("completed", "success"))
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)

    def set_fix_task(self):
        self.env.sql("INSERT INTO config(key,value) VALUES('red_main:acme/priv', ?)",
                     (json.dumps({"run": 7, "sha": self.main, "url": "", "since": "", "task": "T-0001"}),))

    def test_named_fix_task_merges_on_failed_run(self):
        self.ok("config", "judge_wait_main_run", "1")
        self.set_fix_task()
        self.runs(self.run_json("completed", "failure"))
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)

    def test_named_fix_task_waits_while_run_in_progress(self):
        self.ok("config", "judge_wait_main_run", "1")
        self.set_fix_task()
        self.runs(self.run_json("in_progress"))
        self.refused("main run is in_progress")
