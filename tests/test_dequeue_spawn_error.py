"""T-1184: a queued row whose start raises outside SystemExit must not end cmd_tick; one throttled event names the row and exception type."""
import datetime as dt
import json
import os
import unittest
from unittest import mock

from harness import FactoryTestCase

NOW = "2026-11-05T12:00:00"


def spawn_args(name, task):
    return json.dumps({"role": "pm", "goal": "G-001", "team": "g-001-a", "task": task, "name": name, "cwd": None,
                       "prompt": None, "model": None, "resume": None, "org": "claude"})


class DequeueSpawnError(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_team("g-001-a", lead="claude-lead")
        e.seed_task("T-0001", team="g-001-a", status="todo", pr_url=None)
        e.seed_task("T-0002", team="g-001-a", status="todo", pr_url=None)
        for i, (name, task) in enumerate((("claude-pm-t-0001", "T-0001"), ("claude-pm-t-0002", "T-0002")), 1):
            e.sql("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,spawn_args,queued_at,org) VALUES(?,?,?,?,?,?,?,?,?)",
                  (name, "pm", "G-001", "g-001-a", task, "queued", spawn_args(name, task), "2026-11-05T11:00:0%d" % i, "claude"))
        self.names = ("claude-pm-t-0001", "claude-pm-t-0002")
        self.dispatch_ran = 0

    def tick(self, at=NOW):
        calls = []

        def spawn(con, role, goal, team, task, name, *a, **k):
            calls.append(name)
            if len(calls) == 1:
                raise RuntimeError("boom")
            con.execute("UPDATE agents SET status='running' WHERE name=?", (name,))

        def dispatch(con):
            self.dispatch_ran += 1

        with mock.patch.dict(os.environ, {"FACTORY_NOW": at}), self.env.load_factory() as m, \
                mock.patch.object(m, "tick_lock", return_value=None), mock.patch.object(m, "_timed_spawn", spawn), \
                mock.patch.object(m, "dispatch_tick", dispatch):
            m.cmd_tick(mock.Mock(), locked=True)
        return calls

    def events(self):
        return self.env.sql("SELECT entity, entity_id, action, detail FROM events WHERE action='dequeue-spawn-error'")

    def test_runtime_error_on_first_row_still_starts_second_and_tick_finishes(self):
        calls = self.tick()
        self.assertEqual(calls, list(self.names))
        self.assertEqual(self.dispatch_ran, 1)
        got = {r["name"]: r["status"] for r in self.env.sql("SELECT name, status FROM agents WHERE name LIKE 'claude-pm-%'")}
        self.assertEqual(got, {self.names[0]: "queued", self.names[1]: "running"})

    def test_one_event_per_throttle_window_names_row_and_exception_type(self):
        for m in range(3):
            self.tick(at=(dt.datetime.fromisoformat(NOW) + dt.timedelta(minutes=m)).isoformat())
        ev = self.events()
        self.assertEqual(len(ev), 1)
        self.assertEqual((ev[0]["entity"], ev[0]["entity_id"], ev[0]["detail"]), ("task", "T-0001", "RuntimeError"))
        self.tick(at=(dt.datetime.fromisoformat(NOW) + dt.timedelta(minutes=61)).isoformat())
        self.assertEqual(len(self.events()), 2)
        self.tick(at=(dt.datetime.fromisoformat(NOW) + dt.timedelta(minutes=62)).isoformat())
        self.assertEqual(len(self.events()), 2)

    def test_agent_name_when_no_task_on_row(self):
        self.env.sql("DELETE FROM agents WHERE name LIKE 'claude-pm-%'")
        name = "claude-director"
        self.env.sql("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,spawn_args,queued_at,org) "
                     "VALUES(?,?,NULL,NULL,NULL,'queued',?,?,'claude')",
                     (name, "director", json.dumps({"role": "director", "goal": None, "team": None, "task": None,
                                                    "name": name, "cwd": None, "prompt": None, "model": None,
                                                    "resume": None, "org": "claude"}), "2026-11-05T11:00:03"))
        calls = []

        def spawn(*a, **k):
            calls.append(1)
            raise RuntimeError("boom")

        with mock.patch.dict(os.environ, {"FACTORY_NOW": NOW}), self.env.load_factory() as m, \
                mock.patch.object(m, "tick_lock", return_value=None), mock.patch.object(m, "_timed_spawn", spawn), \
                mock.patch.object(m, "dispatch_tick", lambda con: None):
            m.cmd_tick(mock.Mock(), locked=True)
        self.assertEqual(len(calls), 1)
        ev = self.events()
        self.assertEqual(len(ev), 1)
        self.assertEqual((ev[0]["entity"], ev[0]["entity_id"], ev[0]["detail"]), ("agent", name, "RuntimeError"))


if __name__ == "__main__":
    unittest.main()
