"""T-1207: daily retro tick spawns cursor-retro once per day (retro/schedule.py)."""
import json
import os
import unittest

from harness import FactoryTestCase, REPO

D = "2026-10-26"


def at(hhmm, day=D):
    return f"{day}T{hhmm}:00"


class RetroScheduleTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        fake = self.env.stub_bin / "agent"  # cursor runtime: agent/cursor-agent not on PATH in CI
        fake.write_text("#!/bin/sh\necho CURSOR_OK\n")
        fake.chmod(0o755)
        self.ok("config", "retro_enabled", "1")
        self.ok("config", "retro_hour", "6")
        self.ok("config", "retro_max_minutes", "20")

    def tick(self, now, rc=0):
        p = self.env.run("tick", extra_env={"FACTORY_NOW": now})
        self.assertEqual(p.returncode, rc, f"tick {now}: rc={p.returncode}\n{p.stdout}\n{p.stderr}")
        return p

    def runs(self):
        return [dict(r) for r in self.env.sql("SELECT * FROM retro_runs ORDER BY id")]

    def starts(self):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]

    def agent(self):
        rows = self.env.sql("SELECT * FROM agents WHERE name='cursor-retro'")
        self.assertEqual(len(rows), 1)
        return dict(rows[0])

    def test_hour_six_spawns_once_with_cursor_runtime(self):
        self.tick(at("06:01"))
        self.assertEqual([a[2] for a in self.starts()], ["cursor-retro"])
        row = self.agent()
        self.assertEqual((row["role"], row["org"], row["model"]), ("retro", "cursor", "composer-2.5"))
        args = json.loads(row["spawn_args"])
        self.assertEqual(args.get("runtime"), "cursor")
        self.assertEqual(row["org"], "cursor")
        (run,) = self.runs()
        self.assertEqual((run["day"], run["status"]), (D, "running"))
        hooks = self.env.user_home / ".local" / "state" / "factory" / "cursor" / "cursor-retro" / ".cursor" / "hooks.json"
        self.assertTrue(hooks.is_file(), hooks)
        text = hooks.read_text()
        self.assertIn("cursor-guard-adapter.py", text)
        self.assertIn("--role retro", text)

    def test_second_tick_same_day_no_second_run(self):
        self.tick(at("06:01"))
        self.tick(at("06:30"))
        self.assertEqual(len(self.runs()), 1)
        self.assertEqual(len(self.starts()), 1)

    def test_disabled_does_not_spawn(self):
        self.ok("config", "retro_enabled", "0")
        self.tick(at("06:01"))
        self.assertEqual(self.runs(), [])
        self.assertEqual(self.starts(), [])

    def test_hour_five_does_not_spawn(self):
        self.tick(at("05:59"))
        self.assertEqual(self.runs(), [])

    def test_wall_budget_stops_run(self):
        self.tick(at("06:01"))
        self.env.sql("UPDATE agents SET started=? WHERE name='cursor-retro'", (at("05:40"),))
        self.tick(at("06:25"))
        self.assertEqual(self.agent()["status"], "stopped")
        (run,) = self.runs()
        self.assertEqual((run["status"], run["stop_reason"]), ("stopped", "wall"))

    def test_bad_hour_records_retro_failed_and_tick_returns(self):
        self.ok("config", "retro_hour", "99")
        self.tick(at("06:01"))
        self.assertEqual(self.runs(), [])
        self.assertTrue(self.env.sql("SELECT 1 FROM events WHERE action='retro-failed'"))

    def test_bad_max_minutes_records_retro_failed_and_does_not_spawn(self):
        self.ok("config", "retro_max_minutes", "0")
        self.tick(at("06:01"))
        self.assertEqual(self.runs(), [])
        self.assertEqual(self.starts(), [])
        self.assertTrue(self.env.sql("SELECT 1 FROM events WHERE action='retro-failed'"))

    def test_bad_max_minutes_stops_live_run(self):
        self.tick(at("06:01"))
        self.ok("config", "retro_max_minutes", "abc")
        self.tick(at("06:05"))
        self.assertEqual(self.agent()["status"], "stopped")
        (run,) = self.runs()
        self.assertEqual((run["status"], run["stop_reason"]), ("stopped", "config"))
        self.assertTrue(self.env.sql("SELECT 1 FROM events WHERE action='retro-failed'"))

    def test_deferred_while_other_agents_queued(self):
        self.env.seed_goal()
        self.env.seed_task(status="todo", pr_url=None)
        self.env.sql(
            "INSERT INTO agents(name,role,goal_id,team_id,task_id,status,spawn_args,queued_at,org) "
            "VALUES('claude-coder-t-0001','coder','G-001','g-001-a','T-0001','queued','{}','2026-10-26T05:00:00','claude')")
        self.tick(at("06:01"))
        self.assertEqual(self.runs(), [])
        self.assertEqual(self.starts(), [])

    def test_live_run_blocks_second_day_claim_as_skipped(self):
        self.tick(at("06:01"))
        row = self.agent()
        self.env.sql("UPDATE retro_runs SET status='done', ended=? WHERE day=?", (at("07:00"), D))
        self.env.sql("UPDATE agents SET status='live', started=? WHERE name='cursor-retro'",
                     (at("06:30", day="2026-10-27"),))
        self.env.set_herdr(agents=[{"name": "cursor-retro", "agent": "cursor", "pane_id": row["pane_id"],
                                    "agent_status": "working", "agent_session": {"value": "sess-retro"}}])
        self.tick(at("07:00", day="2026-10-27"))
        nxt = [r for r in self.runs() if r["day"] == "2026-10-27"]
        self.assertEqual(len(nxt), 1)
        self.assertEqual((nxt[0]["status"], nxt[0]["stop_reason"]), ("skipped", "previous run active"))


if __name__ == "__main__":
    unittest.main()
