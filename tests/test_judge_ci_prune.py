"""T-0839: the tick step tick_judge_ci_prune deletes the judge_pr_ready cache rows (config key judge_ci:<pr_url>) that no
judge can still use. Every prune case runs the real `factory tick` against a temp FACTORY_HOME, with the clock fixed by
FACTORY_NOW; gh is the stub in tests/stubs."""
import argparse
import datetime as dt
import json
import unittest
from unittest import mock

from harness import FactoryTestCase

NOW_ISO = "2026-10-31T12:00:00+00:00"
NOW = dt.datetime.fromisoformat(NOW_ISO).timestamp()
DAY = 86400
URL = "https://github.com/zz-test/repo/pull/%d"
GREEN = [{"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS", "workflowName": "ci"}]


def cache(at, ready=True, why="CI green"):
    return json.dumps({"at": at, "ready": ready, "why": why})


class JudgeCiPruneTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()

    def task(self, n, status, url=True):
        self.env.seed_task(f"T-{n:04d}", status=status, pr_url=URL % n if url else None)
        return URL % n

    def put(self, url, value):
        self.env.sql("INSERT INTO config(key,value) VALUES(?,?)", ("judge_ci:" + url, value))

    def keys(self):
        return {r["key"] for r in self.env.sql("SELECT key FROM config WHERE key LIKE 'judge\\_ci:%' ESCAPE '\\'")}

    def tick(self):
        p = self.env.run("tick", extra_env={"FACTORY_NOW": NOW_ISO})
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        return p

    def events(self, action):
        return self.env.sql("SELECT * FROM events WHERE action=?", (action,))

    # -- prune rules ------------------------------------------------------
    def test_row_of_done_task_is_deleted(self):
        url = self.task(1, "done")
        self.put(url, cache(NOW - 60))
        self.tick()
        self.assertEqual(self.keys(), set())

    def test_row_of_cancelled_task_is_deleted(self):
        url = self.task(1, "cancelled")
        self.put(url, cache(NOW - 60))
        self.tick()
        self.assertEqual(self.keys(), set())

    def test_row_without_a_task_is_deleted(self):
        self.task(1, "in_review")  # another PR's task
        self.put(URL % 99, cache(NOW - 60))
        self.tick()
        self.assertEqual(self.keys(), set())

    def test_row_older_than_seven_days_is_deleted_even_for_an_open_task(self):
        url = self.task(1, "in_review")
        self.put(url, cache(NOW - 7 * DAY - 60))
        self.tick()
        self.assertEqual(self.keys(), set())

    def test_row_of_open_in_review_task_younger_than_seven_days_is_kept(self):
        url = self.task(1, "in_review")
        self.put(url, cache(NOW - 7 * DAY + 3600))
        self.tick()
        self.assertEqual(self.keys(), {"judge_ci:" + url})

    def test_unparsable_value_is_deleted_without_a_crash(self):
        url = self.task(1, "in_review")
        self.put(url, "{not json")
        self.put(URL % 2, json.dumps({"ready": True}))  # no "at"
        self.put(URL % 3, json.dumps(["list"]))
        self.tick()
        self.assertEqual(self.keys(), set())

    def test_a_pr_shared_by_a_done_and_an_open_task_is_kept(self):
        self.env.seed_task("T-0001", status="done", pr_url=URL % 1)
        self.env.seed_task("T-0002", status="in_review", pr_url=URL % 1)
        self.put(URL % 1, cache(NOW - 60))
        self.tick()
        self.assertEqual(self.keys(), {"judge_ci:" + URL % 1})

    def test_only_judge_ci_rows_are_touched(self):
        others = {"judge_ci_wait_s": "2400", "judge_cix:" + URL % 9: "x", "xjudge_ci:" + URL % 9: "x",
                  "judge_ci": "x", "last_judge_gh_fail_ts": "1.0", "mech_merge": "off"}
        for k, v in others.items():
            self.env.sql("INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (k, v))
        self.put(URL % 5, "{not json")  # a row that does get pruned, in the same tick
        self.tick()
        self.assertEqual(self.keys(), set())
        for k, v in others.items():
            self.assertEqual(self.env.sql("SELECT value FROM config WHERE key=?", (k,))[0]["value"], v, k)

    def test_pruned_rows_are_logged_once(self):
        self.put(URL % 1, cache(NOW - 60))
        self.put(URL % 2, cache(NOW - 60))
        self.tick()
        ev = self.events("judge-ci-pruned")
        self.assertEqual([e["detail"] for e in ev], ["2"])

    # -- cache behaviour is unchanged -------------------------------------
    def test_pruned_open_task_row_is_cached_again_by_the_next_judge_pr_ready(self):
        url = self.task(1, "in_review", url=False)
        self.env.sql("UPDATE tasks SET pr_url=? WHERE id='T-0001'", (url,))
        self.env.seed_pr(url=url, checks=GREEN)
        self.put(url, cache(NOW - 8 * DAY, ready=False, why="old answer"))
        self.tick()
        self.assertEqual(self.keys(), set())
        with self.env.load_factory() as mod:
            con = mod.db()
            self.assertEqual(mod.judge_pr_ready(con, url), (True, "CI green"))
            con.commit()
        row = self.env.sql("SELECT value FROM config WHERE key=?", ("judge_ci:" + url,))
        self.assertEqual(len(row), 1)
        self.assertEqual((json.loads(row[0]["value"])["ready"], json.loads(row[0]["value"])["why"]), (True, "CI green"))

    # -- a failing step never stops the tick -------------------------------
    def test_a_raising_prune_step_is_one_event_and_the_tick_goes_on(self):
        later = mock.Mock()
        with self.env.load_factory() as mod:
            with mock.patch.object(mod, "tick_judge_ci_prune", side_effect=RuntimeError("boom")), \
                    mock.patch.object(mod, "tick_gate", later):
                mod.cmd_tick(argparse.Namespace(), locked=True)
        later.assert_called_once()  # a step that runs after the prune step
        ev = self.events("judge-ci-prune-error")
        self.assertEqual(len(ev), 1)
        self.assertIn("boom", ev[0]["detail"])
        self.assertEqual((ev[0]["entity"], ev[0]["entity_id"]), ("tick", "judge-ci-prune"))


if __name__ == "__main__":
    unittest.main()
