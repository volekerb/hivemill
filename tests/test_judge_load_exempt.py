"""T-0612: a judge ignores the load spawn gate (it runs no local build); every other cap still holds it, and a coder
is still held by load."""
import os
import unittest

from harness import PR_URL
from test_dequeue import DequeueBase, at
import test_judge_gate as jg

HIGH = str(10 * (os.cpu_count() or 1))


class JudgeLoadExempt(DequeueBase):
    def setUp(self):
        super().setUp()
        self.env.seed_task("T-0002", pr_url=PR_URL, team="g-001-a")
        self.env.seed_pr(checks=jg.GREEN)

    queue_judge = jg.JudgeGate.queue_judge
    seed_live_judges = jg.JudgeGate.seed_live_judges

    def spawn_judge(self, **kw):
        return self.ok("spawn", "judge", "--name", "zz-jd", "--goal", "G-003", "--task", "T-0002", **kw).stdout

    def test_spawn_judge_starts_under_closed_load_gate(self):
        out = self.spawn_judge(extra_env={"FACTORY_LOADAVG": HIGH})
        self.assertNotIn("QUEUED", out)
        self.assertEqual(self.status("zz-jd"), "live")

    def test_spawn_coder_still_queues_under_closed_load_gate(self):
        out = self.ok("spawn", "coder", "--name", "zz-cx", "--goal", "G-003", extra_env={"FACTORY_LOADAVG": HIGH}).stdout
        self.assertIn("QUEUED", out)
        self.assertIn("load1", out)
        self.assertEqual(self.status("zz-cx"), "queued")

    def test_tick_dequeues_queued_judge_not_coder_under_closed_load_gate(self):
        self.queue_judge()
        self.queue("zz-c", "coder", "G-003", at(2))
        self.ok("config", "max_live_agents", "10")
        self.ok("tick", extra_env={"FACTORY_LOADAVG": HIGH})
        self.assertEqual((self.status("zz-jd"), self.status("zz-c")), ("live", "queued"))

    def test_tick_judge_starts_behind_queued_lead_and_director_under_load_only(self):
        self.queue_judge()
        self.queue("zz-l", "lead", "G-003", at(1))
        self.queue("zz-d", "director", "G-003", at(0))
        self.ok("config", "max_live_agents", "10")
        self.ok("tick", extra_env={"FACTORY_LOADAVG": HIGH})
        self.assertEqual((self.status("zz-jd"), self.status("zz-l"), self.status("zz-d")), ("live", "queued", "queued"))

    def test_tick_judge_stays_queued_behind_lead_when_another_cap_is_closed(self):
        self.queue_judge()
        self.queue("zz-l", "lead", "G-003", at(1))
        self.ok("config", "max_live_agents", "10")
        self.ok("config", "min_mem_available_mb", "999999999")
        self.ok("tick", extra_env={"FACTORY_LOADAVG": HIGH})
        self.assertEqual((self.status("zz-jd"), self.status("zz-l")), ("queued", "queued"))

    def test_judge_still_held_by_max_live_judges(self):
        self.seed_live_judges(3)
        self.queue_judge()
        self.ok("config", "max_live_agents", "10")
        self.ok("tick", extra_env={"FACTORY_LOADAVG": HIGH})
        self.assertEqual(self.status("zz-jd"), "queued")

    def test_judge_still_queues_at_max_live_agents(self):
        self.env.seed_agent("zz-other", "pm", goal="G-001", status="live", pane_id="p-zz-o", started=jg.now())
        self.ok("config", "max_live_agents", "1")
        out = self.spawn_judge(extra_env={"FACTORY_LOADAVG": HIGH})
        self.assertIn("QUEUED", out)
        self.assertIn("agent panes live", out)

    def test_judge_still_queues_below_memory_floor(self):
        self.ok("config", "min_mem_available_mb", "999999999")
        out = self.spawn_judge(extra_env={"FACTORY_LOADAVG": HIGH})
        self.assertIn("QUEUED", out)
        self.assertIn("MemAvailable", out)


if __name__ == "__main__":
    unittest.main()
