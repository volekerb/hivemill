"""T-0867 (owner M16580): a lead wake and a lead dequeue skip only the CPU-load gate; a coder at the same load stays
queued; the memory floor, max_live_leads and max_live_agents still hold a lead."""
import os
import unittest

from test_dequeue import DequeueBase, at
import test_lead_wake as lw
from test_judge_gate import now

HIGH = str(10 * (os.cpu_count() or 1))
ENV = {"FACTORY_LOADAVG": HIGH}


class LeadLoadExempt(DequeueBase):
    def setUp(self):
        super().setUp()
        self.ok("config", "max_live_agents", "10")

    def test_spawn_lead_starts_under_closed_load_gate(self):
        out = self.ok("spawn", "lead", "--name", "zz-ln", "--goal", "G-003", extra_env=ENV).stdout
        self.assertNotIn("QUEUED", out)
        self.assertEqual(self.status("zz-ln"), "live")

    def test_spawn_coder_still_queues_under_closed_load_gate(self):
        out = self.ok("spawn", "coder", "--name", "zz-cx", "--goal", "G-003", extra_env=ENV).stdout
        self.assertIn("QUEUED", out)
        self.assertIn("load1", out)

    def test_tick_dequeues_queued_lead_not_coder_under_closed_load_gate(self):
        self.queue("zz-ql", "lead", "G-003", at(1))
        self.queue("zz-c", "coder", "G-003", at(2))
        self.ok("tick", extra_env=ENV)
        self.assertEqual((self.status("zz-ql"), self.status("zz-c")), ("live", "queued"))

    def test_lead_below_memory_floor_stays_queued(self):
        self.ok("config", "min_mem_available_mb", "999999999")
        out = self.ok("spawn", "lead", "--name", "zz-ln", "--goal", "G-003", extra_env=ENV).stdout
        self.assertIn("QUEUED", out)
        self.assertIn("MemAvailable", out)
        self.queue("zz-ql", "lead", "G-003", at(1))
        self.ok("tick", extra_env=ENV)
        self.assertEqual(self.status("zz-ql"), "queued")

    def test_lead_at_max_live_agents_stays_queued(self):
        self.env.seed_agent("zz-other", "pm", goal="G-001", status="live", pane_id="p-zz-o", started=now())
        self.ok("config", "max_live_agents", "1")
        out = self.ok("spawn", "lead", "--name", "zz-ln", "--goal", "G-003", extra_env=ENV).stdout
        self.assertIn("QUEUED", out)
        self.assertIn("agent panes live", out)

    def test_lead_at_max_live_leads_stays_queued_at_high_load(self):
        self.ok("config", "lead_parking", "1")
        self.ok("config", "max_live_leads", "1")
        self.env.sql("UPDATE agents SET status='live', pane_id='p-zz-lead', started=? WHERE name='zz-lead'", (now(),))
        self.env.sql("UPDATE agents SET status='done' WHERE name='zz-lead-b'")
        out = self.ok("spawn", "lead", "--name", "zz-ln", "--goal", "G-003", extra_env=ENV).stdout
        self.assertIn("QUEUED", out)
        self.assertIn("max_live_leads", out)
        self.queue("zz-ql", "lead", "G-003", at(1))
        self.ok("tick", extra_env=ENV)
        self.assertEqual(self.status("zz-ql"), "queued")


class LeadWakeLoadExempt(lw.WakeBase):
    def test_tick_wake_starts_lead_under_closed_load_gate(self):
        self.mail()
        self.ok("tick", extra_env=ENV)
        self.assertEqual(self.lead(), "live")
        self.assertEqual(self.wakes()[0]["queued"], False)

    def test_team_wake_starts_lead_under_closed_load_gate(self):
        self.ok("team", "wake", lw.TEAM, extra_env=ENV)
        self.assertEqual(self.lead(), "live")

    def test_wake_below_memory_floor_stays_queued(self):
        self.ok("config", "min_mem_available_mb", "999999999")
        self.mail()
        self.ok("tick", extra_env=ENV)
        self.assertEqual(self.lead(), "queued")


if __name__ == "__main__":
    unittest.main()
