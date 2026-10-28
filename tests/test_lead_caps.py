"""T-0362 (G-014): lead cap (max_live_leads) + reserved worker slots in gate(), and task-level priority."""
import datetime as dt
import unittest

from harness import FactoryTestCase
from test_relaunch_idle import RelaunchIdleCase, LEAD, SESS, PANE


def at(n, mins=0):
    t = dt.datetime.now().astimezone() - dt.timedelta(minutes=mins, seconds=100 - n)
    return t.isoformat(timespec="seconds")


class CapBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal("G-001")
        e.seed_goal("G-003")
        e.sql("UPDATE goals SET priority=1 WHERE id='G-001'")
        e.sql("UPDATE goals SET priority=3 WHERE id='G-003'")
        e.seed_task(status="todo", pr_url=None)
        self.ok("config", "lead_parking", "1")
        self.ok("config", "max_live_agents", "18")
        self.listed = []

    def live(self, name, role, goal="G-001"):
        self.env.seed_agent(name, role, goal=goal)
        self.env.sql("UPDATE agents SET pane_id=? WHERE name=?", ("p-" + name, name))
        self.listed.append({"name": name, "agent": "claude", "pane_id": "p-" + name})
        self.env.set_herdr(agents=self.listed)

    def leads(self, n):
        for i in range(n):
            self.live(f"zz-lead-{i}", "lead")

    def status(self, name):
        return self.env.sql("SELECT status FROM agents WHERE name=?", (name,))[0]["status"]

    def spawn_lead(self, name="zz-lead-new"):
        return self.ok("spawn", "lead", "--goal", "G-001", "--name", name)

    def gate(self, **kw):
        with self.env.load_factory() as ns:
            con = ns.db()
            return ns.gate(con, **kw)


class LeadCapTests(CapBase):
    def test_fifth_lead_queues(self):
        self.leads(4)
        p = self.spawn_lead()
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("4/4 leads live (max_live_leads)", p.stdout)
        self.assertEqual(self.status("zz-lead-new"), "queued")

    def test_under_cap_spawns(self):
        self.leads(3)
        self.assertIn("SPAWNED", self.spawn_lead().stdout)

    def test_starting_lead_counts(self):
        self.leads(3)
        self.env.seed_agent("zz-lead-s", "lead", goal="G-001", status="starting")
        self.assertIn("4/4 leads live", self.spawn_lead().stdout)

    def test_reserved_worker_slots(self):
        self.ok("config", "reserved_worker_slots", "10")
        self.ok("config", "max_live_leads", "9")
        self.live("zz-boss", "boss")
        self.live("zz-director", "director")
        self.leads(6)
        p = self.spawn_lead()
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("8/8 HQ panes; 10 slots reserved for workers (reserved_worker_slots)", p.stdout)

    def test_workers_keep_the_reserve(self):
        self.ok("config", "reserved_worker_slots", "10")
        self.ok("config", "max_live_leads", "9")
        self.live("zz-boss", "boss")
        self.live("zz-director", "director")
        self.leads(6)
        for i in range(9):
            self.live(f"zz-w{i}", "coder")
        p = self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", "zz-new-coder")
        self.assertIn("SPAWNED", p.stdout)

    def test_team_new_lead_is_capped(self):
        self.leads(4)
        self.env.seed_agent("zz-director", "director")
        self.ok("team", "new", "--goal", "G-001", "--scope", "zz scope", agent="zz-director")
        self.assertEqual(self.status("lead-g-001-a"), "queued")

    def test_rollback_flag_off_has_no_cap(self):
        self.ok("config", "lead_parking", "0")
        self.leads(4)
        self.assertIn("SPAWNED", self.spawn_lead().stdout)

    def test_bad_config_uses_defaults(self):
        self.ok("config", "max_live_leads", "abc")
        self.ok("config", "reserved_worker_slots", "xyz")
        self.leads(4)
        ok, why, stats = self.gate(role="lead")
        self.assertFalse(ok)
        self.assertIn("4/4 leads live", why)
        self.assertEqual((stats["leads_cap"], stats["hq_cap"]), (4, 8))

    def test_own_slot_and_own_lead_skip_the_cap(self):
        self.leads(4)
        self.assertTrue(self.gate(role="lead", own_slot=True)[0])
        self.assertTrue(self.gate(role="lead", own_lead=True)[0])
        self.assertFalse(self.gate(role="lead")[0])

    def test_role_none_and_worker_see_the_same_gate(self):
        self.leads(4)
        for parking in ("1", "0"):
            self.ok("config", "lead_parking", parking)
            a, b = self.gate(), self.gate(role="coder")
            self.assertEqual((a[0], a[1]), (b[0], b[1]))
            self.assertTrue(a[0])

    def test_stats_for_the_dashboard(self):
        self.live("zz-boss", "boss")
        self.live("zz-director", "director")
        self.leads(2)
        self.live("zz-c", "coder")
        self.live("zz-q", "qa")
        s = self.gate()[2]
        self.assertEqual((s["leads"], s["leads_cap"], s["hq"], s["hq_cap"], s["workers"]), (2, 4, 4, 8, 2))


class SmallBoxClampTests(CapBase):
    def test_default_config_admits_first_lead_and_queues_second(self):
        # default max_live_agents=6, reserved_worker_slots=10 -> raw hq_cap -4, clamped to 3
        self.ok("config", "max_live_agents", "6")
        self.ok("config", "reserved_worker_slots", "10")
        self.live("zz-boss", "boss")
        self.live("zz-director", "director")
        ok, why, stats = self.gate(role="lead")
        self.assertTrue(ok, why)
        self.assertEqual(stats["hq_cap"], 3)
        self.assertIn("SPAWNED", self.spawn_lead("zz-lead-a").stdout)
        p = self.spawn_lead("zz-lead-b")
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("3/3 HQ panes", p.stdout)


class RelaunchCapTests(RelaunchIdleCase):
    def test_relaunch_of_a_live_lead_is_not_capped(self):
        self.ok("config", "lead_parking", "1")
        rows = [("idle", LEAD, SESS, PANE)]
        for i in range(3):
            self.env.seed_agent(f"zz-lead-{i}", "lead", goal="G-001", pane_id=f"pl{i}")
            rows.append(("idle", f"zz-lead-{i}", f"sess-{i}", f"pl{i}"))
        self.herdr_agents(*rows)
        p = self.ok("relaunch", LEAD, "--fresh", agent="zz-director")
        self.assertNotIn("max_live_leads", p.stdout + p.stderr)
        self.assertEqual(len(self.starts()), 1)
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name=?", (LEAD,))[0]["status"], "live")


class QueueOrderTests(CapBase):
    def queue(self, name, role, goal, queued_at, task=None):
        self.env.set_herdr(fail=True)
        args = ["spawn", role, "--name", name, "--goal", goal] + (["--task", task] if task else [])
        self.assertIn("QUEUED", self.ok(*args).stdout)
        self.env.sql("UPDATE agents SET queued_at=? WHERE name=?", (queued_at, name))
        self.env.set_herdr(fail=False, agents=self.listed)

    def test_priority_order_and_no_blocking(self):
        self.leads(4)
        self.queue("zz-lead-p3", "lead", "G-003", at(1))
        self.queue("zz-lead-p1", "lead", "G-001", at(2))
        self.queue("zz-coder", "coder", "G-003", at(3), task="T-0001")
        self.env.sql("UPDATE agents SET status='done' WHERE name='zz-lead-0'")
        self.ok("tick")
        self.assertEqual(self.status("zz-lead-p1"), "live")
        self.assertEqual(self.status("zz-lead-p3"), "queued")
        self.assertEqual(self.status("zz-coder"), "live")


class TaskPriorityTests(CapBase):
    def setUp(self):
        super().setUp()
        self.env.sql("UPDATE goals SET priority=2 WHERE id='G-001'")
        self.env.seed_task("T-0002", status="todo", pr_url=None)

    def queue(self, name, task, queued_at):
        self.env.set_herdr(fail=True)
        self.assertIn("QUEUED", self.ok("spawn", "coder", "--name", name, "--goal", "G-001", "--task", task).stdout)
        self.env.sql("UPDATE agents SET queued_at=? WHERE name=?", (queued_at, name))
        self.env.set_herdr(fail=False)

    def first(self, winner, loser):
        self.ok("config", "max_live_agents", "1")
        self.ok("tick")
        self.assertEqual((self.status(winner), self.status(loser)), ("live", "queued"))

    def test_task_priority_lifts_the_row(self):
        self.ok("task", "update", "T-0001", "--priority", "1", agent=self.director())
        self.queue("zz-a-plain", "T-0002", at(1))
        self.queue("zz-b-prio", "T-0001", at(2))
        self.first("zz-b-prio", "zz-a-plain")

    def test_unset_priority_keeps_queued_at_order(self):
        self.queue("zz-a-first", "T-0002", at(1))
        self.queue("zz-b-second", "T-0001", at(2))
        self.first("zz-a-first", "zz-b-second")

    def test_task_priority_never_lowers_the_goal_priority(self):
        self.env.sql("UPDATE goals SET priority=1 WHERE id='G-001'")
        self.ok("task", "update", "T-0001", "--priority", "3", agent=self.director())
        self.queue("zz-a-plain", "T-0002", at(1))
        self.queue("zz-b-low", "T-0001", at(2))
        self.first("zz-a-plain", "zz-b-low")  # both P1 (min), queued_at decides

    def test_aging_cap_still_beats_a_fresh_p1_task(self):
        self.ok("task", "update", "T-0001", "--priority", "1", agent=self.director())
        self.queue("zz-old", "T-0002", at(1, mins=250))
        self.queue("zz-fresh", "T-0001", at(2))
        self.first("zz-old", "zz-fresh")

    def director(self):
        if not self.env.sql("SELECT 1 FROM agents WHERE name='zz-director'"):
            self.env.seed_agent("zz-director", "director")
        return "zz-director"

    def test_worker_refused_director_and_lead_allowed(self):
        self.env.seed_agent("zz-coder", "coder", goal="G-001", task="T-0001")
        self.env.seed_agent("zz-lead", "lead", goal="G-001")
        p = self.env.run("task", "update", "T-0001", "--priority", "1", agent="zz-coder")
        self.assertNotEqual(p.returncode, 0)
        self.assertIsNone(self.env.sql("SELECT priority FROM tasks WHERE id='T-0001'")[0]["priority"])
        self.ok("task", "update", "T-0001", "--priority", "1", agent=self.director())
        self.ok("task", "update", "T-0001", "--priority", "2", agent="zz-lead")
        self.assertEqual(self.env.sql("SELECT priority FROM tasks WHERE id='T-0001'")[0]["priority"], 2)
        self.assertIn("priority: 2", self.ok("task", "show", "T-0001").stdout)

    def test_out_of_range_refused(self):
        self.refused(2, "task", "update", "T-0001", "--priority", "5", agent=self.director())


if __name__ == "__main__":
    unittest.main()
