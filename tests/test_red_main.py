"""G-022 T-0623: red-main monitor and merge freeze. The tick reads the latest completed tests.yml push run on main
(stub gh `run list`); red freezes cmd_judge merges with the run id, messages the director once and files a revert task;
green clears; red_main_freeze=0 (the default) changes nothing. Temp FACTORY_HOME, the tests/stubs gh."""
import unittest

from harness import REPO, FactoryTestCase
import test_public_repo_safety as base

RED = {"databaseId": 901, "status": "completed", "conclusion": "failure", "createdAt": "2026-10-29T10:00:00Z", "headSha": "b" * 40, "url": "https://github.com/acme/priv/actions/runs/901"}
GOOD = {"databaseId": 902, "status": "completed", "conclusion": "success", "createdAt": "2026-01-01T00:00:00Z", "headSha": "c" * 40, "url": "https://github.com/acme/priv/actions/runs/902"}
NEWER_GOOD = dict(GOOD, createdAt="2026-11-01T00:00:00Z")  # newer than RED: the newest commit is green
RUNNING = {"databaseId": 903, "status": "in_progress", "conclusion": None, "headSha": "d" * 40, "url": ""}
# T-1592: an older *pending* main run that a newer merge superseded (one shared push concurrency group)
CANCELLED = {"databaseId": 906, "status": "completed", "conclusion": "cancelled", "createdAt": "2026-10-29T11:00:00Z", "headSha": "e" * 40, "url": "https://github.com/acme/priv/actions/runs/906"}
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
        self.runs(NEWER_GOOD, RED)
        self.tick()
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)

    def test_green_clears_on_next_tick_while_red_despite_interval(self):
        self.ok("config", "red_main_freeze", "1")
        self.ok("config", "red_main_interval_s", "3600")
        self.runs(RED)
        self.tick()
        self.assertEqual(self.merge().returncode, 4)
        self.runs(NEWER_GOOD, RED)
        self.tick()
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)
        self.assertTrue(self.env.sql("SELECT 1 FROM events WHERE action='red-main-clear'"))

    def test_green_main_stays_throttled(self):
        self.ok("config", "red_main_freeze", "1")
        self.ok("config", "red_main_interval_s", "3600")
        self.runs(GOOD)
        self.tick()
        self.tick()
        lists = [c for c in self.env.calls("gh") if c["argv"][:2] == ["run", "list"]]
        self.assertEqual(len(lists), 1, lists)

    def test_red_rechecked_every_tick_no_duplicate_alerts(self):
        self.ok("config", "red_main_freeze", "1")
        self.ok("config", "red_main_interval_s", "3600")
        self.runs(RED)
        self.tick()
        self.runs(RUNNING, RED)
        self.tick()
        self.tick()
        lists = [c for c in self.env.calls("gh") if c["argv"][:2] == ["run", "list"]]
        self.assertEqual(len(lists), 3, lists)
        self.assertEqual(len(self.director_mail()), 1)
        self.assertEqual(len(self.env.sql("SELECT id FROM tasks WHERE title LIKE 'Red main:%'")), 1)

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

    def red_main_events(self):
        return self.env.sql("SELECT action, detail FROM events WHERE entity='redmain' AND action IN ('red-main','red-main-clear')")

    def test_cancelled_run_after_green_is_not_red(self):
        """T-1592: green, then a cancelled (superseded) run, then a newer pending or green run: never red, nothing filed."""
        self.ok("config", "red_main_freeze", "1")
        self.ok("config", "red_main_interval_s", "0")
        newer_green = dict(GOOD, databaseId=907, createdAt="2026-10-29T12:00:00Z", headSha="f" * 40)
        for runs in ((CANCELLED, GOOD), (RUNNING, CANCELLED, GOOD), (dict(RUNNING, status="queued"), CANCELLED, GOOD),
                     (newer_green, CANCELLED, GOOD)):
            with self.subTest(runs=[r["databaseId"] for r in runs]):
                self.runs(*runs)
                self.tick()
                self.assertEqual(self.env.sql("SELECT key FROM config WHERE key GLOB 'red_main:*'"), [])
                self.assertEqual(self.red_main_events(), [])
                self.assertEqual(self.director_mail(), [])
                self.assertEqual(self.env.sql("SELECT id FROM tasks WHERE title LIKE 'Red main:%'"), [])
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)

    def test_cancelled_run_after_red_is_not_green(self):
        """T-1592: a cancelled run newer than a red one clears nothing: the freeze stays, with no second alert or task."""
        self.ok("config", "red_main_freeze", "1")
        self.ok("config", "red_main_interval_s", "0")
        self.runs(RED)
        self.tick()
        for runs in ((CANCELLED, RED), (RUNNING, CANCELLED, RED)):
            with self.subTest(runs=[r["databaseId"] for r in runs]):
                self.runs(*runs)
                self.tick()
                self.assertEqual([e["action"] for e in self.red_main_events()], ["red-main"])
                self.assertEqual(len(self.director_mail()), 1)
                self.assertEqual(len(self.env.sql("SELECT id FROM tasks WHERE title LIKE 'Red main:%'")), 1)
        p = self.merge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("red main: run 901", p.stderr)
        self.assertEqual(self.merges(), [])

    def test_only_cancelled_runs_leave_the_state_unknown(self):
        """T-1592: with nothing but cancelled and pending runs there is no information: no state, no green mark, no event."""
        self.ok("config", "red_main_freeze", "1")
        self.ok("config", "red_main_interval_s", "0")
        self.runs(RUNNING, CANCELLED, dict(CANCELLED, databaseId=905))
        self.tick()
        self.assertEqual(self.env.sql("SELECT key FROM config WHERE key GLOB 'red_main*:*'"), [])
        self.assertEqual(self.red_main_events(), [])
        self.assertEqual(self.director_mail(), [])

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

    def test_second_red_run_names_stored_task_not_by_hand(self):
        self.ok("config", "red_main_freeze", "1")
        self.ok("config", "red_main_interval_s", "0")
        self.runs(RED)
        self.tick()
        first = self.env.sql("SELECT id FROM tasks WHERE title LIKE 'Red main:%'")
        self.assertEqual(len(first), 1, first)
        self.runs(dict(RED, databaseId=904, url="https://github.com/acme/priv/actions/runs/904"))
        self.tick()
        mail = self.env.sql("SELECT subject, body FROM messages WHERE subject LIKE 'RED MAIN%' ORDER BY id")  # the tick may vanish the seeded director
        self.assertEqual(len(mail), 2, [dict(m) for m in mail])
        body = mail[1]["body"]
        self.assertIn("904", mail[1]["subject"])
        self.assertIn(first[0]["id"], body)
        self.assertIn("still open", body)
        self.assertNotIn("by hand", body)
        self.assertEqual(len(self.env.sql("SELECT id FROM tasks WHERE title LIKE 'Red main:%'")), 1)

    def test_second_red_run_with_closed_stored_task_asks_for_new_one(self):
        self.ok("config", "red_main_freeze", "1")
        self.ok("config", "red_main_interval_s", "0")
        self.runs(RED)
        self.tick()
        first = self.env.sql("SELECT id FROM tasks WHERE title LIKE 'Red main:%'")
        for st, rid in (("done", 904), ("cancelled", 905)):
            self.env.sql("UPDATE tasks SET status=? WHERE id=?", (st, first[0]["id"]))
            self.runs(dict(RED, databaseId=rid))
            self.tick()
            body = self.env.sql("SELECT body FROM messages WHERE subject LIKE 'RED MAIN%' ORDER BY id DESC LIMIT 1")[0]["body"]
            self.assertIn(first[0]["id"], body)
            self.assertIn(st, body)
            self.assertIn("new fix task is needed", body)
            self.assertIn("factory redmain fix acme/priv NEW_TASK_ID", body)
            self.assertNotIn(f"factory redmain fix acme/priv {first[0]['id']}", body)
            self.assertNotIn("<repo>", body)
            self.assertNotIn("<task>", body)
            self.assertNotIn("still open", body)
            self.assertNotIn("earlier red run", body)
        self.assertEqual(len(self.env.sql("SELECT id FROM tasks WHERE title LIKE 'Red main:%'")), 1)

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

    # T-1636: queued runs crowd the window; the newest completed run's verdict decides.
    @staticmethod
    def queued(n):
        return [{"databaseId": 950 + i, "status": "queued", "conclusion": None, "createdAt": f"2026-11-15T19:0{i}:00Z",
                 "headSha": "e" * 40, "url": ""} for i in range(n)]

    def test_green_clears_red_behind_queued_runs(self):
        self.ok("config", "red_main_freeze", "1")
        self.ok("config", "red_main_interval_s", "0")
        self.runs(RED)
        self.tick()
        self.assertEqual(self.merge().returncode, 4)
        newer = dict(GOOD, createdAt="2026-10-30T10:00:00Z")
        self.runs(*self.queued(6), newer, RED)
        self.tick()
        self.assertTrue(self.env.sql("SELECT 1 FROM events WHERE action='red-main-clear'"))
        p = self.merge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_red_newest_completed_behind_queued_keeps_freeze(self):
        self.ok("config", "red_main_freeze", "1")
        self.ok("config", "red_main_interval_s", "0")
        older_green = dict(GOOD, createdAt="2026-10-01T10:00:00Z")
        self.runs(*self.queued(3), RED, older_green)
        self.tick()
        self.assertEqual(self.merge().returncode, 4)
        with self.env.load_factory() as f:
            self.assertEqual(f.red_main_state(f.db(), "acme/priv")["run"], 901)

    def test_latest_reads_window_beyond_five_and_picks_newest_created(self):
        """Fails on a --limit 5 window (the green run sits behind 6 queued runs); also picks by createdAt, not list order."""
        import json
        import subprocess
        from unittest import mock
        older_red = dict(RED, createdAt="2026-10-01T10:00:00Z")
        newer_green = dict(GOOD, createdAt="2026-10-30T10:00:00Z")
        listing = self.queued(6) + [older_red, newer_green]  # red listed before the newer green

        def fake_gh(argv, **kw):
            n = int(argv[argv.index("--limit") + 1])
            return subprocess.CompletedProcess(argv, 0, json.dumps(listing[:n]), "")
        with self.env.load_factory() as f, mock.patch.object(f.subprocess, "run", fake_gh):
            run = f._red_main_latest("acme/priv")
        self.assertIsNotNone(run)
        self.assertEqual(run["databaseId"], 902)


if __name__ == "__main__":
    unittest.main()
