"""T-1135: a queued non-evaluator row whose start dies (die(3), missing binary) must not end the tick: later phases run,
one event and one alert per row; at the retry cap the row stays queued, retried every 10 minutes (M26136). Real cmd_tick in-process, temp FACTORY_HOME."""
import json
import os
import unittest
from unittest import mock

from harness import FactoryTestCase

LEAD = "claude-lead-g-001-a"


class TickStartFailure(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_team("g-001-a", lead=LEAD)
        e.seed_task(team="g-001-a", status="todo", pr_url=None)
        e.seed_agent(LEAD, "lead", goal="G-001", team="g-001-a")
        self.name = "claude-coder-t-0001"
        e.sql("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,spawn_args,queued_at,org) VALUES(?,?,?,?,?,?,?,?,?)",
              (self.name, "coder", "G-001", "g-001-a", "T-0001", "queued",
               '{"role":"coder","goal":"G-001","team":"g-001-a","task":"T-0001","name":"%s","cwd":null,'
               '"prompt":null,"model":null,"resume":null,"org":"claude"}' % self.name, "2026-11-05T11:00:00", "claude"))
        self.later = 0

    def tick(self, start_dies=True, gate_closed=False, at="2026-11-05T12:00:00"):
        self.attempts = getattr(self, "attempts", 0)

        def die3(*a, **k):
            self.attempts += 1
            raise SystemExit(3)

        def works(con, role, goal, team, task, name, *a, **k):
            self.attempts += 1
            con.execute("UPDATE agents SET status='running' WHERE name=?", (name,))

        def later(con):
            self.later += 1

        with mock.patch.dict(os.environ, {"FACTORY_NOW": at}), self.env.load_factory() as m, mock.patch.object(m, "tick_lock", return_value=None), \
                mock.patch.object(m, "dispatch_tick", later):
            if start_dies:
                ctx = mock.patch.object(m, "_spawn_start", die3)  # the process boundary: pane/binary start
                ctx.start()
                self.addCleanup(ctx.stop)
            else:
                ctx = mock.patch.object(m, "_timed_spawn", works)
                ctx.start()
            if gate_closed:
                g = mock.patch.object(m, "gate", return_value=(False, "load", {}))
                g.start()
                self.addCleanup(g.stop)
            m.cmd_tick(mock.Mock(), locked=True)
            mock.patch.stopall()

    def status(self):
        return self.env.sql("SELECT status FROM agents WHERE name=?", (self.name,))[0]["status"]

    def count(self, action):
        return len(self.env.sql("SELECT 1 FROM events WHERE entity_id=? AND action=?", (self.name, action)))

    def alerts(self, role="coder"):
        return self.env.sql("SELECT recipient, body FROM messages WHERE subject LIKE ?", (f"%queued {role}%",))

    def requeue_as(self, role, team):
        """Turn the queued row into a queued `role` row named name (team may be NULL)."""
        self.env.sql("UPDATE agents SET role=?, team_id=?, task_id=NULL, spawn_args=json_set(spawn_args,'$.role',?,"
                     "'$.team',?,'$.task',NULL) WHERE name=?", (role, team, role, team, self.name))

    def test_dying_start_does_not_end_the_tick(self):
        self.tick()
        self.assertEqual(self.later, 1, "a later tick phase (dispatch) did not run")
        self.assertEqual(self.status(), "queued")

    def test_three_ticks_one_event_one_alert_to_lead(self):
        for _ in range(3):
            self.tick()
        self.assertEqual(self.later, 3)
        self.assertEqual(self.count("spawn-start-failed"), 1)
        self.assertEqual([a["recipient"] for a in self.alerts()], [LEAD])
        self.assertEqual(self.status(), "queued")

    def test_no_lead_alerts_the_director(self):
        self.env.sql("DELETE FROM agents WHERE name=?", (LEAD,))
        self.env.sql("UPDATE agents SET team_id=NULL, spawn_args=json_set(spawn_args,'$.team',NULL) WHERE name=?", (self.name,))
        self.tick()
        got = [a["recipient"] for a in self.alerts()]
        self.assertEqual(len(got), 1)
        self.assertIn("director", got[0])

    def test_failing_lead_row_alerts_the_director_not_itself(self):  # reviewer B1
        self.env.sql("DELETE FROM agents WHERE name=?", (LEAD,))
        self.env.sql("UPDATE teams SET lead_agent=? WHERE id='g-001-a'", (self.name,))
        self.requeue_as("lead", "g-001-a")
        self.tick()
        got = [a["recipient"] for a in self.alerts("lead")]
        self.assertEqual(len(got), 1)
        self.assertIn("director", got[0])
        self.assertNotIn(self.name, got)

    def test_failing_director_row_alerts_the_boss_not_itself(self):  # reviewer B1
        self.requeue_as("director", None)
        self.tick()
        got = [a["recipient"] for a in self.alerts("director")]
        self.assertEqual(len(got), 1)
        self.assertIn("boss", got[0])
        self.assertNotIn(self.name, got)

    def test_row_no_longer_queued_gets_no_event_and_no_alert(self):  # reviewer B2
        self.env.sql("UPDATE agents SET status='cancelled' WHERE name=?", (self.name,))
        with self.env.load_factory() as m:
            con = m.db()
            row = con.execute("SELECT * FROM agents WHERE name=?", (self.name,)).fetchone()
            m.start_failed_queued(con, row, {"role": "coder", "team": "g-001-a", "goal": "G-001", "task": "T-0001"},
                                  SystemExit(3))
            con.commit()
            con.close()
        self.assertEqual(self.count("spawn-start-failed"), 0)
        self.assertEqual(self.alerts(), [])

    def test_lead_gone_between_resolve_and_send_falls_back_to_the_director(self):
        self.env.sql("UPDATE teams SET lead_agent=NULL WHERE id='g-001-a'")
        self.tick()
        got = [a["recipient"] for a in self.alerts()]
        self.assertEqual(len(got), 1)
        self.assertIn("director", got[0])

    def at(self, minute):
        return "2026-11-05T12:%02d:00" % minute

    def test_past_the_cap_row_stays_queued_and_retries_every_10_minutes(self):  # M26136
        for i in range(5):
            self.tick(at=self.at(i))
        self.assertEqual(self.attempts, 5)
        self.assertEqual(self.status(), "queued")
        self.assertEqual(self.count("dequeued"), 0)
        self.assertEqual(len(self.alerts()), 2)  # failure 1 and the cap: the last
        for m in (5, 9, 13):  # inside 10 min of the failure at 12:04: not attempted
            self.tick(at=self.at(m))
        self.assertEqual(self.attempts, 5)
        self.tick(at=self.at(14))  # 10 min after: attempted again, fails again, silent
        self.assertEqual(self.attempts, 6)
        self.tick(at=self.at(20))
        self.assertEqual(self.attempts, 6)
        self.assertEqual(self.status(), "queued")
        self.assertEqual(self.count("spawn-start-failed"), 1)
        self.assertEqual(len(self.alerts()), 2)

    def test_start_that_works_after_the_cap_starts_the_rows(self):  # M26136
        other = "claude-coder-t-0002"
        self.env.sql("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,spawn_args,queued_at,org) "
                     "SELECT ?,role,goal_id,team_id,task_id,status,replace(spawn_args,?,?),queued_at,org FROM agents "
                     "WHERE name=?", (other, self.name, other, self.name))
        for i in range(5):
            self.tick(at=self.at(i))
        self.assertEqual(self.status(), "queued")
        self.tick(start_dies=False, at=self.at(14))
        got = {r["name"]: r["status"] for r in self.env.sql("SELECT name, status FROM agents WHERE name LIKE 'claude-coder-%'")}
        self.assertEqual(got, {self.name: "running", other: "running"})

    def test_gate_refusal_leaves_the_row_queued_without_failure_handling(self):
        for _ in range(6):
            self.tick(gate_closed=True)
        self.assertEqual(self.status(), "queued")
        self.assertEqual(self.count("spawn-start-failed"), 0)
        self.assertEqual(self.alerts(), [])
        self.assertEqual(self.later, 6)

    def real_tick(self, start_ok=False, gate_refuses_coder=False, at="2026-11-05T12:00:00"):
        """Like tick(), but spawn() always runs for real; only the start (_spawn_start) and, optionally, the gate are stubbed.
        The gate refuses only role=="coder": the tick's own gate(con) has role None and passes, spawn()'s gate refuses (B3)."""
        self.attempts = getattr(self, "attempts", 0)

        def start(con, role, goal, team, task, name, cwd, prompt, model, resume, pane, existing, args, stats, *a, **k):
            self.attempts += 1
            if not start_ok:
                raise SystemExit(3)
            # what the real start writes: the row goes live with the args it was handed
            con.execute("UPDATE agents SET status='live', pane_id='p-1', spawn_args=? WHERE name=?", (json.dumps(args), name))

        def gate(con, *a, role=None, **k):
            return (False, "review reserve", {}) if gate_refuses_coder and role == "coder" else (True, "", {"live": 0})

        with mock.patch.dict(os.environ, {"FACTORY_NOW": at}), self.env.load_factory() as m, \
                mock.patch.object(m, "tick_lock", return_value=None), mock.patch.object(m, "dispatch_tick", lambda con: None), \
                mock.patch.object(m, "_spawn_start", start), mock.patch.object(m, "gate", gate):
            m.cmd_tick(mock.Mock(), locked=True)

    def spawn_args(self):
        return json.loads(self.env.sql("SELECT spawn_args FROM agents WHERE name=?", (self.name,))[0]["spawn_args"])

    def test_gate_requeue_in_spawn_keeps_the_failure_count(self):  # B3, acceptance 11
        for i in range(5):
            self.real_tick(at=self.at(i))
        self.assertEqual(self.attempts, 5)
        self.assertEqual(len(self.alerts()), 2)
        events = self.count("spawn-start-failed")
        for m in (14, 25, 40):  # window passed each time: the tick attempts, spawn()'s gate refuses and requeues
            self.real_tick(gate_refuses_coder=True, at=self.at(m))
        self.assertEqual(self.attempts, 5)
        self.assertEqual(self.status(), "queued")
        self.assertEqual(self.spawn_args()["spawn_failures"], 5)
        self.assertIn("spawn_failed_at", self.spawn_args())
        self.real_tick(at=self.at(55))  # gate open again: the failure is attempt 6, still no new alert or event
        self.assertEqual(self.attempts, 6)
        self.assertEqual(len(self.alerts()), 2)
        self.assertEqual(self.count("spawn-start-failed"), events)

    def test_gate_refusal_after_a_failure_does_not_restart_the_episode(self):  # B3
        self.real_tick(at=self.at(0))
        self.real_tick(gate_refuses_coder=True, at=self.at(1))
        self.assertEqual(self.spawn_args()["spawn_failures"], 1)
        self.real_tick(at=self.at(2))
        self.assertEqual(self.spawn_args()["spawn_failures"], 2)
        self.assertEqual(len(self.alerts()), 1)

    def test_start_ok_after_fail_and_gate_refusal_clears_the_keys(self):  # B3, acceptance 12
        self.real_tick(at=self.at(0))
        self.real_tick(gate_refuses_coder=True, at=self.at(1))
        self.real_tick(start_ok=True, at=self.at(2))
        self.assertEqual(self.status(), "live")
        got = self.spawn_args()
        self.assertNotIn("spawn_failures", got)
        self.assertNotIn("spawn_failed_at", got)

    def test_cancelled_row_with_the_keys_respawned_counts_from_zero(self):  # B3, acceptance 12
        self.real_tick(at=self.at(0))
        self.env.sql("UPDATE agents SET status='cancelled' WHERE name=?", (self.name,))
        self.assertIn("spawn_failures", self.spawn_args())
        with mock.patch.dict(os.environ, {"FACTORY_NOW": self.at(3)}), self.env.load_factory() as m, \
                mock.patch.object(m, "gate", lambda con, *a, **k: (False, "load", {})):
            con = m.db()
            m.spawn(con, "coder", "G-001", "g-001-a", "T-0001", self.name, None, None, None, None, org="claude")
            con.commit()
            con.close()
        self.assertEqual(self.status(), "queued")
        self.assertNotIn("spawn_failures", self.spawn_args())

    def test_goalless_cursor_rows_alert_their_own_org(self):  # B4, acceptance 13
        for role, want in (("director", "cursor-boss"), ("staff-engineer", "cursor-director")):
            name = f"cursor-{role}"
            self.env.sql("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,spawn_args,queued_at,org) "
                         "VALUES(?,?,NULL,NULL,NULL,'queued',?,?,'cursor')",
                         (name, role, json.dumps({"role": role, "goal": None, "team": None, "task": None, "name": name,
                                                  "cwd": None, "prompt": None, "model": None, "resume": None,
                                                  "org": "cursor"}), "2026-11-05T11:00:00"))
        self.real_tick()
        got = {r["recipient"] for r in self.env.sql("SELECT recipient FROM messages WHERE subject LIKE '%queued director%' "
                                                    "OR subject LIKE '%queued staff-engineer%'")}
        self.assertEqual(got, {"cursor-boss", "cursor-director"})


if __name__ == "__main__":
    unittest.main()
