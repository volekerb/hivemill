"""G-027 SC4: refuse cursor spawns on models recorded over the Cursor pane limit (T-0878 state)."""
import json
import unittest

from harness import FactoryTestCase
from web import cursor_limits


class GateCursorPaneLimitTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal("G-001")
        e.seed_goal("G-CUR", title="zz-test cursor goal")
        e.sql("UPDATE goals SET org='cursor' WHERE id='G-CUR'")
        e.seed_team("g-001-a", goal="G-001", lead="lead-g-001-a")
        e.seed_team("g-cur-a", goal="G-CUR", lead="cursor-lead-g-cur-a")
        e.seed_task("T-0001", goal="G-CUR", team="g-cur-a", status="todo", pr_url=None)
        e.seed_agent("zz-test-director", "director", goal="G-001")
        fake = e.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho CURSOR_OK\n")
        fake.chmod(0o755)

    def _limits(self, state):
        self.env.sql(
            "INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
            (cursor_limits.CONFIG_KEY, json.dumps(state)),
        )

    def _no_agent_start(self, name):
        return [c for c in self.env.calls("herdr")
                if c["argv"][:2] == ["agent", "start"] and len(c["argv"]) > 2 and c["argv"][2] == name]

    def test_cursor_pm_refused_names_model_and_reset(self):
        self._limits({"grok-4.7-high": {"status": "limited", "reset": "2026-11-05"}})
        pm = self.ok("spawn", "pm", "--goal", "G-CUR", "--team", "g-cur-a", "--name", "cursor-pm-g-cur-a",
                     agent="zz-test-director")
        self.assertIn("QUEUED", pm.stdout)
        self.assertIn("grok-4.7-high", pm.stdout)
        self.assertIn("2026-11-05", pm.stdout)
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='cursor-pm-g-cur-a'")[0]["status"],
                         "queued")
        self.assertEqual(self._no_agent_start("cursor-pm-g-cur-a"), [])

        coder = self.ok("spawn", "coder", "--goal", "G-CUR", "--team", "g-cur-a", "--task", "T-0001",
                        "--name", "cursor-coder-t-0001", agent="zz-test-director")
        self.assertIn("SPAWNED", coder.stdout)
        self.assertNotIn("grok-4.7-high", coder.stdout)

        cl = self.ok("spawn", "pm", "--goal", "G-001", "--team", "g-001-a", "--name", "pm-g-001-a",
                     agent="zz-test-director")
        self.assertIn("SPAWNED", cl.stdout)
        self.assertNotIn("grok-4.7-high", cl.stdout)

    def test_cursor_not_refused_for_weekly_cap(self):
        self.env.write_usage(seven_day=95)
        p = self.ok("spawn", "pm", "--goal", "G-CUR", "--team", "g-cur-a", "--name", "cursor-pm-cap",
                    agent="zz-test-director")
        self.assertIn("SPAWNED", p.stdout)
        self.assertNotIn("weekly usage", p.stdout)

    def test_claude_still_refused_for_weekly_cap(self):
        self.env.write_usage(seven_day=95)
        p = self.ok("spawn", "pm", "--goal", "G-001", "--team", "g-001-a", "--name", "pm-cap",
                    agent="zz-test-director")
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("weekly usage", p.stdout)

    def test_mem_floor_still_refuses_both(self):
        self.ok("config", "min_mem_available_mb", "999999999", agent="zz-test-director")
        cur = self.ok("spawn", "pm", "--goal", "G-CUR", "--team", "g-cur-a", "--name", "cursor-pm-mem",
                      agent="zz-test-director")
        self.assertIn("QUEUED", cur.stdout)
        self.assertIn("MemAvailable", cur.stdout)
        cl = self.ok("spawn", "pm", "--goal", "G-001", "--team", "g-001-a", "--name", "pm-mem",
                     agent="zz-test-director")
        self.assertIn("QUEUED", cl.stdout)
        self.assertIn("MemAvailable", cl.stdout)

    def _live_lead(self, name, goal="G-001"):
        self.env.seed_agent(name, "lead", goal=goal)
        self.env.sql("UPDATE agents SET pane_id=? WHERE name=?", ("p-" + name, name))

    def test_max_live_leads_still_refuses_both(self):
        self.ok("config", "lead_parking", "1", agent="zz-test-director")
        self.ok("config", "max_live_agents", "18", agent="zz-test-director")
        for i in range(4):
            self._live_lead(f"zz-lead-{i}")
        self.env.set_herdr(agents=[{"name": f"zz-lead-{i}", "agent": "claude", "pane_id": f"p-zz-lead-{i}"}
                                   for i in range(4)])
        cur = self.ok("spawn", "lead", "--goal", "G-CUR", "--name", "cursor-lead-new", agent="zz-test-director")
        self.assertIn("QUEUED", cur.stdout)
        self.assertIn("max_live_leads", cur.stdout)
        cl = self.ok("spawn", "lead", "--goal", "G-001", "--name", "zz-lead-new", agent="zz-test-director")
        self.assertIn("QUEUED", cl.stdout)
        self.assertIn("max_live_leads", cl.stdout)

    def _live_agent(self, name, role="coder", goal="G-001"):
        self.env.seed_agent(name, role, goal=goal)
        self.env.sql("UPDATE agents SET pane_id=? WHERE name=?", ("p-" + name, name))
        self.env.set_herdr(agents=[{"name": name, "agent": "claude", "pane_id": "p-" + name}])

    def test_agent_cap_still_refuses_both(self):
        self.ok("config", "max_live_agents", "1", agent="zz-test-director")
        self._live_agent("zz-live-one")
        cur = self.ok("spawn", "pm", "--goal", "G-CUR", "--team", "g-cur-a", "--name", "cursor-pm-cap2",
                      agent="zz-test-director")
        self.assertIn("QUEUED", cur.stdout)
        self.assertIn("agent panes live", cur.stdout)
        cl = self.ok("spawn", "pm", "--goal", "G-001", "--team", "g-001-a", "--name", "pm-cap2",
                     agent="zz-test-director")
        self.assertIn("QUEUED", cl.stdout)
        self.assertIn("agent panes live", cl.stdout)

    def test_load_still_refuses_both(self):
        extra = {"FACTORY_LOADAVG": "100"}
        cur = self.ok("spawn", "pm", "--goal", "G-CUR", "--team", "g-cur-a", "--name", "cursor-pm-load",
                      agent="zz-test-director", extra_env=extra)
        self.assertIn("QUEUED", cur.stdout)
        self.assertIn("load1", cur.stdout)
        cl = self.ok("spawn", "pm", "--goal", "G-001", "--team", "g-001-a", "--name", "pm-load",
                     agent="zz-test-director", extra_env=extra)
        self.assertIn("QUEUED", cl.stdout)
        self.assertIn("load1", cl.stdout)


if __name__ == "__main__":
    unittest.main()
