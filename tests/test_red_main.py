"""G-022 T-0623: red-main monitor and merge freeze. The tick reads the latest completed tests.yml push run on main
(stub gh `run list`); red freezes cmd_judge merges with the run id, messages the director once and files a revert task;
green clears; red_main_freeze=0 (the default) changes nothing. Temp FACTORY_HOME, the tests/stubs gh."""
import unittest

from harness import REPO, FactoryTestCase
import test_public_repo_safety as base

RED = {"databaseId": 901, "status": "completed", "conclusion": "failure", "createdAt": "2026-10-29T10:00:00Z", "headSha": "b" * 40, "url": "https://github.com/acme/priv/actions/runs/901"}
GOOD = {"databaseId": 902, "status": "completed", "conclusion": "success", "createdAt": "2026-01-01T00:00:00Z", "headSha": "c" * 40, "url": "https://github.com/acme/priv/actions/runs/902"}
RUNNING = {"databaseId": 903, "status": "in_progress", "conclusion": None, "headSha": "d" * 40, "url": ""}
URL = "https://github.com/acme/priv/pull/1"


class RedMainTests(FactoryTestCase):
    judge_setup = base.FrozenTests.judge_setup
    pr_repo = base.FrozenTests.pr_repo
    bare = base.Base.bare
    refs = base.Base.refs

    def setUp(self):
        super().setUp()
        (self.env.home_dir / "ci").symlink_to(REPO / "ci")
        self.ok("config", "git_push", "1")
        self.judge_setup(URL, "acme/priv")
        self.env.sql("UPDATE projects SET gh_repo='acme/priv' WHERE id='zz-test-proj'")
        self.env.seed_agent("zz-test-director", "director")

    def runs(self, *runs):
        self.env.set_gh(runs={"acme/priv": list(runs)})

    def tick(self):
        return self.ok("tick")

    def merge(self):
        return self.env.run("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge")

    def merges(self):
        return [c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "merge"]]

    def director_mail(self):
        return self.env.sql("SELECT subject, body FROM messages WHERE recipient='zz-test-director' AND subject LIKE 'RED MAIN%'")

    def test_default_off_ignores_red_main(self):
        self.runs(RED)
        self.tick()
        self.assertEqual(self.director_mail(), [])
        self.assertEqual([c for c in self.env.calls("gh") if c["argv"][:2] == ["run", "list"]], [])
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)

    def test_red_run_refuses_merge_with_run_id(self):
        self.ok("config", "red_main_freeze", "1")
        self.runs(RED)
        self.tick()
        p = self.merge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("red main: run 901", p.stderr)
        self.assertEqual(self.merges(), [])

    def test_one_director_message_and_revert_task_per_red_run(self):
        self.ok("config", "red_main_freeze", "1")
        self.ok("config", "red_main_interval_s", "0")
        self.runs(RED)
        self.tick()
        self.tick()
        mail = self.director_mail()
        self.assertEqual(len(mail), 1, mail)
        self.assertIn("901", mail[0]["subject"])
        tasks = self.env.sql("SELECT id, title, goal_id FROM tasks WHERE title LIKE 'Red main:%'")
        self.assertEqual(len(tasks), 1, tasks)
        self.assertIn("run 901", tasks[0]["title"])

    def test_green_run_clears_freeze(self):
        self.ok("config", "red_main_freeze", "1")
        self.ok("config", "red_main_interval_s", "0")
        self.runs(RED)
        self.tick()
        self.runs(GOOD, RED)
        self.tick()
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)

    def test_running_or_unreadable_run_keeps_state(self):
        self.ok("config", "red_main_freeze", "1")
        self.ok("config", "red_main_interval_s", "0")
        self.runs(RED)
        self.tick()
        self.runs(RUNNING, RED)  # the newest completed run is still the red one
        self.tick()
        self.env.set_gh(run_list_exit=1)  # gh trouble: the freeze stays
        self.tick()
        p = self.merge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("red main: run 901", p.stderr)
        self.assertEqual(len(self.director_mail()), 1)

    def test_other_repo_red_does_not_block(self):
        self.ok("config", "red_main_freeze", "1")
        self.env.set_gh(runs={"acme/other": [RED]})
        self.tick()
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_revert_task_may_merge_during_freeze(self):
        self.ok("config", "red_main_freeze", "1")
        self.runs(RED)
        self.tick()
        tid = self.env.sql("SELECT id FROM tasks WHERE title LIKE 'Red main:%'")[0]["id"]
        with self.env.load_factory() as f:
            con = f.db()
            self.assertIsNone(f.red_main_refusal(con, ["acme/priv"], tid))
            self.assertIn("run 901", f.red_main_refusal(con, ["acme/priv"], "T-0001"))
            self.assertIn("run 901", f.red_main_refusal(con, ["acme/priv"]))

    def test_corrupt_state_keeps_freeze(self):
        self.ok("config", "red_main_freeze", "1")
        self.env.sql("INSERT INTO config(key,value) VALUES('red_main:acme/priv','[]')")
        p = self.merge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("red main", p.stderr)

    def test_revert_task_goes_to_the_exact_repo_project(self):
        self.env.seed_project("zz-other")
        self.env.sql("UPDATE projects SET gh_repo='acme/private' WHERE id='zz-other'")
        self.env.seed_goal("G-002", project="zz-other")
        self.ok("config", "red_main_freeze", "1")
        self.runs(RED)
        self.tick()
        self.assertEqual(self.env.sql("SELECT goal_id FROM tasks WHERE title LIKE 'Red main:%'")[0]["goal_id"], "G-001")

    def test_revert_task_names_pr_merged_while_green_poll_was_running(self):
        self.ok("config", "red_main_freeze", "1")
        self.ok("config", "red_main_interval_s", "0")
        self.runs(GOOD)
        self.tick()
        self.env.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('2026-06-01T12:00:00+00:00','judge','task','T-0001','merged','@abc')")
        self.runs(RUNNING, GOOD)
        self.tick()
        self.runs(RED, GOOD)
        self.tick()
        spec = self.env.sql("SELECT spec FROM tasks WHERE title LIKE 'Red main:%'")[0]["spec"]
        self.assertIn("T-0001", spec)
        self.assertNotIn("unknown", spec)

    def hive_setup(self):
        """T-0669: the repo's own project has no goal; the active goal sits under another project but its task has a PR there."""
        self.env.sql("UPDATE projects SET gh_repo='acme/home' WHERE id='zz-test-proj'")
        self.env.seed_project("zz-hive")
        self.env.sql("UPDATE projects SET gh_repo='acme/priv' WHERE id='zz-hive'")
        self.ok("config", "red_main_freeze", "1")
        self.runs(RED)

    def test_red_run_files_task_under_goal_working_in_that_repo(self):
        self.hive_setup()
        self.tick()
        tasks = self.env.sql("SELECT id, goal_id FROM tasks WHERE title LIKE 'Red main:%'")
        self.assertEqual([t["goal_id"] for t in tasks], ["G-001"], tasks)
        self.assertIn("filed", self.director_mail()[0]["body"])
        self.assertNotIn("No active goal", self.director_mail()[0]["body"])

    def test_no_goal_anywhere_still_alerts_director(self):
        self.hive_setup()
        self.env.sql("UPDATE tasks SET pr_url='https://github.com/acme/elsewhere/pull/1'")
        self.tick()
        self.assertEqual(self.env.sql("SELECT id FROM tasks WHERE title LIKE 'Red main:%'"), [])
        self.assertIn("No active goal", self.director_mail()[0]["body"])

    def test_director_names_fix_task_and_it_may_merge(self):
        self.hive_setup()
        self.env.sql("UPDATE tasks SET pr_url='https://github.com/acme/elsewhere/pull/1'")
        self.tick()  # no goal: the stored task is empty (the deadlock)
        self.env.sql("UPDATE tasks SET pr_url=?", (URL,))
        self.env.seed_task(tid="T-0002", pr_url="https://github.com/acme/priv/pull/2")
        p = self.env.run("redmain", "fix", "acme/priv", "T-0002", agent="zz-test-director")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.env.sql("SELECT action FROM events WHERE entity='redmain' AND action='red-main-fix-task'")[0]["action"], "red-main-fix-task")
        with self.env.load_factory() as f:
            con = f.db()
            self.assertIsNone(f.red_main_refusal(con, ["acme/priv"], "T-0002"))
            self.assertIn("run 901", f.red_main_refusal(con, ["acme/priv"], "T-0001"))

    def test_redmain_fix_refusals(self):
        self.hive_setup()
        self.tick()
        p = self.env.run("redmain", "fix", "acme/priv", "T-0001", agent="zz-test-judge")
        self.assertEqual(p.returncode, 3, p.stdout + p.stderr)
        p = self.env.run("redmain", "fix", "acme/priv", "T-9999", agent="zz-test-director")
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        p = self.env.run("redmain", "fix", "acme/other", "T-0001", agent="zz-test-director")
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("not frozen", p.stderr)

    def test_flag_off_after_red_lifts_refusal(self):
        self.ok("config", "red_main_freeze", "1")
        self.runs(RED)
        self.tick()
        self.ok("config", "red_main_freeze", "0")
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)


if __name__ == "__main__":
    unittest.main()
