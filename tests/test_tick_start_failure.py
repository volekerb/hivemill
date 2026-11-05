"""T-1135: a queued non-evaluator row whose start dies (die(3), missing binary) must not end the tick: later phases run,
one event and one alert per row, a cancel at the retry cap. Real cmd_tick in-process, temp FACTORY_HOME."""
import os
import unittest
from unittest import mock

from harness import FactoryTestCase

NOW = {"FACTORY_NOW": "2026-11-05T12:00:00"}
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

    def tick(self, start_dies=True, gate_closed=False):
        def die3(*a, **k):
            raise SystemExit(3)

        def later(con):
            self.later += 1

        with mock.patch.dict(os.environ, NOW), self.env.load_factory() as m, mock.patch.object(m, "tick_lock", return_value=None), \
                mock.patch.object(m, "dispatch_tick", later):
            if start_dies:
                ctx = mock.patch.object(m, "_spawn_start", die3)  # the process boundary: pane/binary start
                ctx.start()
                self.addCleanup(ctx.stop)
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

    def test_cap_cancels_the_row_with_one_more_alert(self):
        for _ in range(5):
            self.tick()
        self.assertEqual(self.status(), "cancelled")
        self.assertEqual(self.count("dequeued"), 1)
        self.assertEqual(len(self.alerts()), 2)
        self.tick()  # cancelled: no more attempts, events or alerts
        self.assertEqual(self.count("spawn-start-failed"), 1)
        self.assertEqual(len(self.alerts()), 2)

    def test_gate_refusal_leaves_the_row_queued_without_failure_handling(self):
        for _ in range(6):
            self.tick(gate_closed=True)
        self.assertEqual(self.status(), "queued")
        self.assertEqual(self.count("spawn-start-failed"), 0)
        self.assertEqual(self.alerts(), [])
        self.assertEqual(self.later, 6)


if __name__ == "__main__":
    unittest.main()
