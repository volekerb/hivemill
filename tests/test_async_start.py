"""T-0734 (G-016 P0-4a): tick_async_start. Flag 0 keeps the old start/sleep/prompt sequence; flag 1 returns from
_spawn_start without waiting for the TUI and the tick's start_confirm phase sends the kickoff. Subprocess runs on a temp home."""
import json
import time
import unittest

from harness import FactoryTestCase
from test_sandbox_spawn import prep, spawn_args

NAME = "zz-pm-async"


class AsyncStartBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team("g-001-a", lead="zz-lead")
        self.env.seed_agent("zz-director", "director")

    def spawn(self, role="pm"):
        return self.env.run("spawn", role, "--goal", "G-001", "--team", "g-001-a", "--name", NAME, agent="zz-director")

    def herdr_calls(self):
        return [c["argv"] for c in self.env.calls("herdr")]

    def prompts(self):
        return [a for a in self.herdr_calls() if a[:2] == ["agent", "prompt"] and a[2] == NAME]

    def row(self):
        return self.env.sql("SELECT * FROM agents WHERE name=?", (NAME,))[0]

    def events(self, action):
        return self.env.sql("SELECT detail FROM events WHERE entity_id=? AND action=?", (NAME, action))

    def go_async(self, **herdr):
        self.ok("config", "tick_async_start", "1")
        self.env.set_herdr(slow_list=True, **herdr)  # herdr has not registered the new agent yet


class FlagOff(AsyncStartBase):
    def test_default_is_zero(self):
        self.assertEqual(self.ok("config", "tick_async_start").stdout.strip().split()[-1], "0")

    def test_old_call_sequence_pinned(self):
        p = self.spawn()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        calls = self.herdr_calls()
        starts = [a for a in calls if a[:2] == ["agent", "start"]]
        self.assertEqual(len(starts), 1)
        self.assertEqual(starts[0][starts[0].index("--timeout") + 1], "90000")
        self.assertLess(calls.index(starts[0]), calls.index(self.prompts()[0]))
        self.assertEqual(len(self.prompts()), 1)
        self.assertIsNone(self.row()["start_pending"])
        self.ok("tick")
        self.assertEqual(len(self.prompts()), 1)


class FastPath(AsyncStartBase):
    def test_spawn_returns_pending_with_kickoff_in_inbox(self):
        self.go_async()
        t = time.time()
        p = self.spawn()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        start = [a for a in self.herdr_calls() if a[:2] == ["agent", "start"]][0]
        self.assertEqual(start[start.index("--timeout") + 1], "1")
        self.assertEqual(self.prompts(), [])
        r = self.row()
        self.assertEqual(r["status"], "live")
        self.assertAlmostEqual(r["start_pending"], t, delta=60)
        self.assertEqual(self.events("start-uncertain"), [])
        kick = self.env.sql("SELECT body FROM messages WHERE recipient=? AND sender='factory'", (NAME,))
        self.assertEqual(len(kick), 1)

    def test_sandbox_spawn_stays_synchronous(self):
        self.env.sql("DELETE FROM teams")  # prep() seeds its own goal and team
        self.env.sql("DELETE FROM goals")
        prep(self.env)  # sandbox stubs and token files from test_sandbox_spawn; the flag on must not change this path
        self.go_async()
        self.ok("config", "sandbox_roles", "reviewer")
        p = self.env.run(*spawn_args(self.env, "reviewer", NAME))
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        start = [a for a in self.herdr_calls() if a[:2] == ["agent", "start"]][0]
        self.assertEqual(start[start.index("--timeout") + 1], "90000")
        self.assertEqual(len(self.prompts()), 1)  # kickoff sent inside _spawn_start, after the escape check
        r = self.row()
        self.assertIsNone(r["start_pending"])
        self.assertIn('"sandbox": true', r["spawn_args"])


class Confirm(AsyncStartBase):
    def ready(self, status="idle"):
        r = self.row()
        self.env.set_herdr(agents=[{"name": NAME, "agent": "claude", "pane_id": r["pane_id"], "agent_status": status,
                                    "agent_session": {"value": "s"}}])

    def test_ready_agent_gets_one_kickoff(self):
        self.go_async()
        self.spawn()
        self.env.sql("UPDATE agents SET flagless=1, flagless_since='x' WHERE name=?", (NAME,))
        self.ready()
        self.ok("tick")
        self.assertEqual(len(self.prompts()), 1)
        kick = self.env.sql("SELECT body FROM messages WHERE recipient=? AND sender='factory'", (NAME,))[0]["body"]
        self.assertEqual(self.prompts()[0][3], kick)
        r = self.row()
        self.assertIsNone(r["start_pending"])
        self.assertIsNotNone(r["last_nudge"])
        self.ok("tick")
        self.assertEqual(len(self.prompts()), 1)

    def test_not_ready_waits_then_start_uncertain(self):
        self.go_async()
        self.spawn()
        self.env.sql("UPDATE agents SET flagless=1, flagless_since='x' WHERE name=?", (NAME,))
        self.ok("tick")  # younger than 90 s, never listed
        self.assertEqual(self.events("start-uncertain"), [])
        self.assertEqual(self.prompts(), [])
        self.assertIsNotNone(self.row()["start_pending"])
        self.env.sql("UPDATE agents SET start_pending=? WHERE name=?", (time.time() - 100, NAME))
        self.ok("tick")
        self.assertEqual(len(self.events("start-uncertain")), 1)
        self.assertEqual(self.prompts(), [])
        r = self.row()
        self.assertIsNone(r["start_pending"])
        self.assertEqual(r["flagless"], 1)

    def test_flagless_cleared_only_on_confirm(self):
        self.go_async()
        self.env.seed_agent(NAME, "pm", goal="G-001", team="g-001-a", status="exited")
        self.env.sql("UPDATE agents SET flagless=1, flagless_since='x' WHERE name=?", (NAME,))
        self.spawn()
        self.assertEqual(self.row()["flagless"], 1)
        self.assertEqual(self.events("flagless-cleared"), [])
        self.ready("working")
        self.ok("tick")
        self.assertEqual(self.row()["flagless"], 0)
        self.assertEqual(len(self.events("flagless-cleared")), 1)
        self.assertEqual(len(self.prompts()), 1)


if __name__ == "__main__":
    unittest.main()
