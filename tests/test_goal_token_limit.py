"""G-015 T-1117 (SC3, F-74 L3): a goal at its token limit gets no new coder or pm spawn. Direct `factory spawn` runs as a
subprocess in a temp FACTORY_HOME; the tick cases run a real cmd_tick in-process (tests/test_owner_alert_feed_tick.py)."""
import builtins
import os
import unittest
from pathlib import Path
from unittest import mock

from harness import FactoryTestCase

NOW = {"FACTORY_NOW": "2026-11-05T12:00:00"}
LEAD = "claude-lead-g-001-a"


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_team("g-001-a", lead=LEAD)
        e.seed_task(team="g-001-a", status="todo", pr_url=None)
        e.seed_agent(LEAD, "lead", goal="G-001", team="g-001-a")

    def limit(self, n, gid="G-001"):
        self.env.sql("UPDATE goals SET token_limit=? WHERE id=?", (n, gid))

    def tally(self, used, gid="G-001"):
        self.env.sql("INSERT OR REPLACE INTO goal_token_tally(goal_id,input,output,cache_read,cache_write_5m,cache_write_1h,"
                     "cost,computed_at) VALUES(?,?,0,0,0,0,0,'t')", (gid, used))

    def spawn(self, role, name=None):
        return self.env.run("spawn", role, "--task", "T-0001", "--name", name or f"claude-{role}-x", agent=LEAD)

    def agent_rows(self, name):
        return self.env.sql("SELECT status FROM agents WHERE name=?", (name,))

    def assert_refused(self, role):
        p = self.spawn(role)
        self.assertEqual(p.returncode, 3, f"{role}: {p.stdout}\n{p.stderr}")
        for part in ("G-001", "tally 1000", "limit 1000", "factory goal edit G-001 --token-limit N"):
            self.assertIn(part, p.stderr)
        self.assertEqual(self.agent_rows(f"claude-{role}-x"), [])  # no row, queued or other
        return p

    def assert_not_limit_refused(self, role, name=None):
        p = self.spawn(role, name)
        self.assertNotIn("token limit", p.stdout + p.stderr, f"{role}: {p.stdout}\n{p.stderr}")


class DirectSpawn(Base):
    def test_coder_and_pm_over_limit_are_refused_exit_3_no_row(self):
        self.limit(1000)
        self.tally(1500)
        for role in ("coder", "pm"):
            p = self.spawn(role)
            self.assertEqual(p.returncode, 3, f"{role}: {p.stdout}\n{p.stderr}")
            for part in ("G-001", "tally 1500", "limit 1000", "factory goal edit G-001 --token-limit N"):
                self.assertIn(part, p.stderr)
            self.assertEqual(self.agent_rows(f"claude-{role}-x"), [])
        ev = self.env.sql("SELECT detail FROM events WHERE entity_id='T-0001' AND action='goal-token-limit-refused'")
        self.assertEqual(len(ev), 2)

    def test_tally_equal_to_the_limit_refuses_a_coder(self):
        self.limit(1000)
        self.tally(1000)
        self.assert_refused("coder")

    def test_tally_just_under_the_limit_is_allowed(self):
        self.limit(1000)
        self.tally(999)
        self.assert_not_limit_refused("coder")

    def test_other_roles_are_not_refused_at_the_limit(self):
        self.limit(1000)
        self.tally(5000)
        for role in ("lead", "judge", "reviewer", "qa", "cleaner"):
            with self.subTest(role=role):
                self.assert_not_limit_refused(role)

    def test_no_limit_or_no_tally_row_is_allowed(self):
        self.tally(5000)  # a tally but no limit
        self.assert_not_limit_refused("coder")
        self.limit(1000)
        self.env.sql("DELETE FROM goal_token_tally")  # a limit but no figure: fails open on purpose
        self.assert_not_limit_refused("pm")

    def test_cursor_goal_over_limit_is_allowed(self):
        self.env.sql("UPDATE goals SET org='cursor' WHERE id='G-001'")
        self.limit(1000)
        self.tally(5000)
        for role in ("coder", "pm"):
            with self.subTest(role=role):
                self.assert_not_limit_refused(role, f"cursor-{role}-x")  # the name prefix carries the org

    def test_cursor_prefixed_name_on_a_claude_goal_does_not_escape(self):
        self.limit(1000)
        self.tally(1000)
        p = self.spawn("coder", "cursor-coder-x")
        self.assertEqual(p.returncode, 3, f"{p.stdout}\n{p.stderr}")
        self.assertIn("token limit", p.stderr)
        self.assertEqual(self.agent_rows("cursor-coder-x"), [])

    def test_retry_of_a_task_in_progress_is_refused(self):
        self.env.sql("UPDATE tasks SET status='in_progress' WHERE id='T-0001'")
        self.limit(1000)
        self.tally(1000)
        self.assert_refused("coder")


class InProcess(Base):
    def test_spawn_path_opens_no_log(self):
        self.limit(1000)
        self.tally(1000)
        real_open = builtins.open

        def guarded(f, *a, **k):
            if str(f).endswith(".jsonl"):
                raise AssertionError(f"opened a log: {f}")
            return real_open(f, *a, **k)

        with self.env.load_factory() as m, mock.patch.object(builtins, "open", guarded), \
                mock.patch.object(Path, "open", lambda self, *a, **k: guarded(str(self), *a, **k)):
            con = m.db()
            with self.assertRaises(SystemExit) as cm:
                m.spawn(con, "coder", "G-001", "g-001-a", "T-0001", "claude-coder-x")
        self.assertEqual(cm.exception.code, 3)


class TickCase(Base):
    def tick(self, *patches):
        with mock.patch.dict(os.environ, NOW), self.env.load_factory() as m, \
                mock.patch.object(m, "tick_lock", return_value=None):
            for p in patches:
                p(m)
            m.cmd_tick(mock.Mock(), locked=True)

    def queue_coder(self, name="claude-coder-t-0001", org="claude"):
        self.env.sql("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,spawn_args,queued_at,org) "
                     "VALUES(?,?,?,?,?,?,?,?,?)",
                     (name, "coder", "G-001", "g-001-a", "T-0001", "queued",
                      '{"role":"coder","goal":"G-001","team":"g-001-a","task":"T-0001","name":"%s","cwd":null,'
                      '"prompt":null,"model":null,"resume":null,"org":"%s"}' % (name, org), "2026-11-05T11:00:00", org))
        return name

    def test_queued_row_is_cancelled_once_alerted_once_and_not_retried(self):
        self.limit(1000)
        self.tally(1000)
        name = self.queue_coder()
        spy = []

        def patch(m):
            real = m._timed_spawn

            def wrapped(con, role, *a, **k):
                spy.append(a)
                return real(con, role, *a, **k)
            p = mock.patch.object(m, "_timed_spawn", wrapped)
            p.start()
            self.addCleanup(p.stop)

        self.tick(patch)
        self.assertEqual(self.agent_rows(name)[0]["status"], "cancelled")
        deq = self.env.sql("SELECT detail FROM events WHERE entity_id=? AND action='dequeued'", (name,))
        self.assertEqual(len(deq), 1)
        self.assertIn('"by": "goal-token-limit"', deq[0]["detail"])
        mail = self.env.sql("SELECT recipient, body FROM messages WHERE subject LIKE '%queued spawn%'")
        self.assertEqual([r["recipient"] for r in mail], [LEAD])
        self.assertIn("tally 1000", mail[0]["body"])
        first_calls = len(spy)
        self.assertEqual(first_calls, 1)
        self.tick(patch)  # second tick: no spawn attempt, no second event, no second alert
        self.assertEqual(len(spy), first_calls)
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE entity_id=? AND action='dequeued'", (name,))), 1)
        self.assertEqual(len(self.env.sql("SELECT 1 FROM messages WHERE subject LIKE '%queued spawn%'")), 1)

    def test_cursor_goal_queued_coder_is_not_cancelled_for_the_limit(self):
        self.env.sql("UPDATE goals SET org='cursor' WHERE id='G-001'")
        self.limit(1000)
        self.tally(1000)
        name = self.queue_coder("cursor-coder-t-0001", "cursor")
        started = []

        def patch(m):  # stub the start: the test must not need a cursor binary on PATH
            p = mock.patch.object(m, "_timed_spawn", lambda con, role, *a, **k: started.append((role, a)))
            p.start()
            self.addCleanup(p.stop)

        self.tick(patch)
        self.assertEqual(len(started), 1)  # the cursor row was looked at and started, not skipped
        self.assertEqual(started[0][0], "coder")
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE entity_id=? AND action='dequeued'", (name,))), 0)

    def test_gate_queued_relaunch_at_limit_is_cancelled(self):
        """A relaunch queued while the gate was closed loses the relaunch exemption: dequeue passes no relaunch=True."""
        self.limit(1000)
        self.tally(1000)
        name = "claude-coder-t-0001"
        args = ('{"role":"coder","goal":"G-001","team":"g-001-a","task":"T-0001","name":"%s","cwd":null,'
                '"prompt":null,"model":null,"resume":"sess-old","fresh":false,"org":"claude"}' % name)
        self.env.sql("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,spawn_args,queued_at,session_id,org) "
                     "VALUES(?,?,?,?,?,?,?,?,?,?)",
                     (name, "coder", "G-001", "g-001-a", "T-0001", "queued", args, "2026-11-05T11:00:00", "sess-old", "claude"))
        self.tick()
        self.assertEqual(self.agent_rows(name)[0]["status"], "cancelled")
        deq = self.env.sql("SELECT detail FROM events WHERE entity_id=? AND action='dequeued'", (name,))
        self.assertEqual(len(deq), 1)
        self.assertIn('"by": "goal-token-limit"', deq[0]["detail"])

    def test_tick_warns_the_lead_at_80_percent_and_survives_a_warn_failure(self):
        self.limit(1000)
        self.tally(800)
        self.tick()
        warn = self.env.sql("SELECT recipient FROM messages WHERE subject LIKE 'token limit 80%'")
        self.assertIn(LEAD, [r["recipient"] for r in warn])
        self.assertEqual([r["recipient"] for r in warn].count(LEAD), 1)
        # a raising warn step (the hook swallows it) must not stop the rest of the tick: the step after it still runs
        after = []

        def patch(m):
            boom = mock.patch.object(m, "tally_tokens", side_effect=RuntimeError("boom"))  # raises inside goal_token_warn_tick
            step = mock.patch.object(m, "_pt_emit", side_effect=lambda *a, **k: after.append(1))
            boom.start(), step.start()
            self.addCleanup(boom.stop), self.addCleanup(step.stop)

        self.tick(patch)
        self.assertTrue(after)


if __name__ == "__main__":
    unittest.main()
