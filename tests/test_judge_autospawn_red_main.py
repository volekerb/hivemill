"""T-1153: the tick starts no judge while the red-main freeze refuses merges; it starts one once main is green again."""
import io
import json
import unittest
from contextlib import redirect_stdout

from harness import PR_URL, SHA_A, FactoryTestCase

TEAM, LEAD, T = "g-001-a", "zz-lead", "T-0835"
GREEN = [{"__typename": "CheckRun", "name": "unit", "status": "COMPLETED", "conclusion": "SUCCESS"}]
NOW = 1_800_000_000.0
RED = json.dumps({"run": "9", "sha": "x", "url": "https://example.invalid/run/9", "since": "t"})


class JudgeRedMainTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(TEAM, lead=LEAD)
        self.env.seed_agent(LEAD, "lead", goal="G-001", team=TEAM, status="exited")
        self.env.seed_task(T, team=TEAM, status="in_review")
        for role in ("reviewer", "qa"):
            self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)",
                         (T, PR_URL, SHA_A, role, "pass", "ok", "zz", "t"))

    def red(self, on=True):
        self.env.sql("DELETE FROM config WHERE key='red_main:zz-test/repo'")
        if on:
            self.env.sql("INSERT INTO config(key,value) VALUES('red_main:zz-test/repo',?)", (RED,))

    def tick(self, head=SHA_A, at=NOW):
        spawned = []
        with self.env.load_factory() as f:
            con = f.db()
            f._ci_remote_head = lambda branch, batch=None: head
            f.gate = lambda con, **k: (True, "", {})
            f.digest_clock = lambda: f.dt.datetime.fromtimestamp(at, f.dt.timezone.utc)
            f._ci_wake_probe = lambda url, timeout: {"number": 1, "state": "OPEN", "headRefOid": SHA_A, "statusCheckRollup": GREEN,
                                                     "mergeStateStatus": "CLEAN"}
            f.judge_pr_ready = lambda con, url, behind_ok=False: (True, "CI green")

            def fake_spawn(con, role, goal=None, team=None, task=None, name=None, **kw):
                spawned.append(name)
                con.execute("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,'live','t')",
                            (name, role, goal, team, task))

            f.spawn = fake_spawn
            with redirect_stdout(io.StringIO()):
                f.judge_autospawn_tick(con)
            con.commit()
        return spawned

    def waits(self):
        return self.env.sql("SELECT detail FROM events WHERE entity='task' AND entity_id=? AND action='judge-autospawn-wait'", (T,))

    def test_frozen_red_main_starts_no_judge_and_logs_one_wait(self):
        self.env.run("config", "red_main_freeze", "1")
        self.red()
        self.assertEqual([self.tick() for _ in range(3)], [[], [], []])
        self.assertEqual(len(self.waits()), 1)
        self.assertIn("red main: run 9", self.waits()[0]["detail"])

    def test_new_pr_head_logs_a_wait_per_head_not_per_red_run(self):
        self.env.run("config", "red_main_freeze", "1")
        self.red()
        for head in (SHA_A, SHA_A, "b" * 40):
            self.assertEqual(self.tick(head), [])
        self.assertEqual(len(self.waits()), 2)

    def test_new_red_run_on_the_same_head_logs_no_extra_wait(self):
        self.env.run("config", "red_main_freeze", "1")
        self.red()
        self.tick()
        self.env.sql("UPDATE config SET value=? WHERE key='red_main:zz-test/repo'", (RED.replace('"9"', '"10"'),))
        self.assertEqual(self.tick(), [])
        self.assertEqual(len(self.waits()), 1)

    def test_unreadable_head_starts_no_judge_and_logs_one_wait_per_hour(self):
        self.env.run("config", "red_main_freeze", "1")
        self.red()
        self.assertEqual([self.tick(None) for _ in range(3)], [[], [], []])
        self.assertEqual(len(self.waits()), 1)

    def test_green_main_again_starts_the_judge(self):
        self.env.run("config", "red_main_freeze", "1")
        self.red()
        self.assertEqual(self.tick(), [])
        self.red(False)
        self.assertEqual(len(self.tick()), 1)

    def test_freeze_off_with_a_red_row_starts_the_judge(self):
        self.env.run("config", "red_main_freeze", "0")
        self.red()
        self.assertEqual(len(self.tick()), 1)
        self.assertEqual(self.waits(), [])


if __name__ == "__main__":
    unittest.main()
