"""G-027 SC4: Claude 5h/7d usage caps must not queue or block cursor-runtime spawns."""
import json
import unittest
from pathlib import Path

from harness import FactoryTestCase


class GateCursorUsageTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_task(status="todo", pr_url=None)
        e.seed_agent("zz-test-director", "director", goal="G-001", team="g-001-a")
        e.seed_agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        e.write_usage(seven_day=95)
        fake = e.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho CURSOR_OK\n")
        fake.chmod(0o755)
        self.ok("config", "agent_runtimes", "*=cursor", agent="zz-test-director")

    def test_cursor_pm_and_coder_skip_weekly_usage_cap(self):
        pm = self.ok("spawn", "pm", "--name", "zz-cu-pm", "--goal", "G-001",
                     "--team", "g-001-a", agent="zz-test-director")
        self.assertIn("SPAWNED zz-cu-pm", pm.stdout)
        self.assertNotIn("weekly usage", pm.stdout)
        coder = self.ok("spawn", "coder", "--name", "zz-cu-coder", "--goal", "G-001",
                        "--team", "g-001-a", "--task", "T-0001", agent="zz-test-director")
        self.assertIn("SPAWNED zz-cu-coder", coder.stdout)
        self.assertNotIn("weekly usage", coder.stdout)

    def test_claude_pm_still_queued_on_weekly_usage(self):
        self.ok("config", "agent_runtimes", "", agent="zz-test-director")
        p = self.ok("spawn", "pm", "--name", "zz-cl-pm", "--goal", "G-001",
                    "--team", "g-001-a", agent="zz-test-director")
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("weekly usage", p.stdout)

    def test_cursor_pm_still_refused_on_mem_floor(self):
        self.ok("config", "min_mem_available_mb", "999999999", agent="zz-test-director")
        p = self.ok("spawn", "pm", "--name", "zz-cu-mem", "--goal", "G-001",
                    "--team", "g-001-a", agent="zz-test-director")
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("MemAvailable", p.stdout)

    def test_tick_dequeues_cursor_not_claude_when_usage_only_block(self):
        args = json.dumps({"role": "pm", "goal": "G-001", "team": "g-001-a", "task": None,
                           "name": "zz-cu-q", "cwd": str(Path(self.env.home)), "prompt": None, "model": None,
                           "resume": None, "fresh": False, "org": "claude"})
        self.env.sql("INSERT INTO agents(name,role,goal_id,team_id,status,spawn_args,queued_at) "
                     "VALUES('zz-cu-q','pm','G-001','g-001-a','queued',?, 't')", (args,))
        self.ok("tick")
        row = self.env.sql("SELECT status FROM agents WHERE name='zz-cu-q'")[0]
        self.assertEqual(row["status"], "live")

        self.ok("config", "agent_runtimes", "", agent="zz-test-director")
        args_cl = json.dumps({"role": "pm", "goal": "G-001", "team": "g-001-a", "task": None,
                              "name": "zz-cl-q", "cwd": str(Path(self.env.home)), "prompt": None, "model": None,
                              "resume": None, "fresh": False, "org": "claude"})
        self.env.sql("INSERT INTO agents(name,role,goal_id,team_id,status,spawn_args,queued_at) "
                     "VALUES('zz-cl-q','pm','G-001','g-001-a','queued',?, 't')", (args_cl,))
        self.ok("tick")
        row_cl = self.env.sql("SELECT status FROM agents WHERE name='zz-cl-q'")[0]
        self.assertEqual(row_cl["status"], "queued")


if __name__ == "__main__":
    unittest.main()
