import json
import os
import time
import unittest
from pathlib import Path
from unittest import mock

from harness import MODELS, FactoryTestCase


class GateTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_task(status="todo", pr_url=None)

    def spawn(self, name="zz-test-c1"):
        return self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", name)

    def assert_queued(self, p, text, name="zz-test-c1"):
        self.assertIn("QUEUED", p.stdout)
        self.assertIn(text, p.stdout)
        rows = self.env.sql("SELECT status FROM agents WHERE name=?", (name,))
        self.assertEqual([r["status"] for r in rows], ["queued"])
        self.assertEqual([c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]], [])

    def test_gate_herdr_unreachable(self):
        self.env.set_herdr(fail=True)
        p = self.spawn()
        self.assert_queued(p, "herdr unreachable")

    def live_factory(self, *names):
        """DB-live factory agents (started long ago) that herdr also lists."""
        for n in names:
            self.env.seed_agent(n, "coder", goal="G-001")
            self.env.sql("UPDATE agents SET pane_id=? WHERE name=?", ("p-" + n, n))
        self.env.set_herdr(agents=[{"name": n, "agent": "claude", "pane_id": "p-" + n} for n in names])

    def test_gate_cap(self):
        self.ok("config", "max_live_agents", "2")
        self.live_factory("zz-test-x1", "zz-test-x2")
        self.assert_queued(self.spawn(), "agent panes live")

    def test_gate_cap_ignores_non_claude_panes(self):
        self.ok("config", "max_live_agents", "2")
        self.env.seed_agent("zz-test-x1", "coder", goal="G-001")
        self.env.seed_agent("zz-test-x2", "coder", goal="G-001")
        self.env.set_herdr(agents=[{"name": "zz-test-x1", "agent": "claude", "pane_id": "p1"},
                                   {"name": "zz-test-x2", "agent": "codex", "pane_id": "p2"}])
        # both are listed by herdr, so both count whatever their agent type
        self.assert_queued(self.spawn(), "2/2 agent panes live")

    def test_gate_ignores_foreign_claude(self):
        self.ok("config", "max_live_agents", "2")
        self.live_factory("zz-test-x1")
        agents = json.loads(self.env.herdr_state.read_text())["agents"]
        agents += [{"name": f"zz-test-f{i}", "agent": "claude", "pane_id": f"pf{i}"} for i in range(3)]
        self.env.set_herdr(agents=agents)
        self.assertIn("spawn gate: OPEN", self.ok("res").stdout)
        self.assertIn("SPAWNED", self.spawn().stdout)

    def test_gate_counts_factory_live(self):
        self.ok("config", "max_live_agents", "2")
        self.live_factory("zz-test-x1", "zz-test-x2")
        self.assertIn("spawn gate: CLOSED — 2/2 agent panes live", self.ok("res").stdout)

    def test_gate_counts_by_pane_id(self):
        self.ok("config", "max_live_agents", "1")
        self.env.seed_agent("zz-test-x1", "coder", goal="G-001")
        self.env.sql("UPDATE agents SET pane_id='p9' WHERE name='zz-test-x1'")
        self.env.set_herdr(agents=[{"name": "renamed", "agent": "claude", "pane_id": "p9"}])
        out = self.ok("res").stdout
        self.assertIn("CLOSED — 1/1", out)
        self.assertIn("other claude panes 0", out)

    def test_gate_lost_not_counted(self):
        self.ok("config", "max_live_agents", "1")
        self.env.seed_agent("zz-test-x1", "coder", goal="G-001", started="2026-01-01T00:00:00+00:00")
        out = self.ok("res").stdout
        self.assertIn("spawn gate: OPEN", out)
        self.assertIn("agents live 0/1", out)

    def test_gate_grace_counted(self):
        import datetime as dt
        self.ok("config", "max_live_agents", "1")
        recent = (dt.datetime.now().astimezone() - dt.timedelta(seconds=60)).isoformat()
        self.env.seed_agent("zz-test-x1", "coder", goal="G-001", started=recent)
        out = self.ok("res").stdout
        self.assertIn("CLOSED — 1/1 agent panes live", out)

    def test_tick_dequeue_no_overspawn(self):
        self.ok("config", "max_live_agents", "2")
        self.live_factory("zz-test-x1")
        self.env.set_herdr(fail=True)
        for i in range(3):
            self.assertIn("QUEUED", self.spawn(f"zz-test-q{i}").stdout)
        self.env.set_herdr(fail=False)
        self.ok("tick")
        starts = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]
        self.assertEqual(len(starts), 1)
        rows = self.env.sql("SELECT status FROM agents WHERE name LIKE 'zz-test-q%' ORDER BY name")
        self.assertEqual(sorted(r["status"] for r in rows), ["live", "queued", "queued"])

    def test_tick_dequeue_no_overspawn_slow_listing(self):
        # herdr is slow to list a newly started agent: the 180 s startup grace must still count it
        self.ok("config", "max_live_agents", "2")
        self.live_factory("zz-test-x1")
        self.env.set_herdr(fail=True)
        for i in range(3):
            self.assertIn("QUEUED", self.spawn(f"zz-test-q{i}").stdout)
        self.env.set_herdr(fail=False, slow_list=True)
        self.ok("tick")
        starts = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]
        self.assertEqual(len(starts), 1)
        self.assertEqual([a["name"] for a in json.loads(self.env.herdr_state.read_text())["agents"]], ["zz-test-x1"])
        rows = self.env.sql("SELECT status FROM agents WHERE name LIKE 'zz-test-q%' ORDER BY name")
        self.assertEqual(sorted(r["status"] for r in rows), ["live", "queued", "queued"])
        self.ok("tick")  # the unlisted agent is still within grace: not reaped as lost, nothing more starts
        self.assertEqual(len([c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]), 1)
        rows = self.env.sql("SELECT status FROM agents WHERE name LIKE 'zz-test-q%' ORDER BY name")
        self.assertEqual(sorted(r["status"] for r in rows), ["live", "queued", "queued"])

    def test_res_reports_other(self):
        self.ok("config", "max_live_agents", "2")
        self.live_factory("zz-test-x1")
        agents = json.loads(self.env.herdr_state.read_text())["agents"]
        agents += [{"name": f"zz-test-f{i}", "agent": "claude", "pane_id": f"pf{i}"} for i in range(3)]
        agents.append({"name": "zz-test-cx", "agent": "codex", "pane_id": "pcx"})
        self.env.set_herdr(agents=agents)
        out = self.ok("res").stdout
        self.assertIn("agents live 1/2 · queued 0 · other claude panes 3 (counted in host total)", out)

    def test_gate_mem(self):
        self.ok("config", "min_mem_available_mb", "999999999")
        self.assert_queued(self.spawn(), "MemAvailable")

    def test_gate_ignores_the_demo_mem_pin(self):
        """$FACTORY_MEM_AVAILABLE_MB only pins what `factory serve` shows; it never opens or breaks the spawn gate."""
        self.ok("config", "min_mem_available_mb", "999999999")
        for pin in ("99999999999", "not-a-number"):
            with self.subTest(pin=pin):
                self.assert_queued(self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", "zz-test-c1",
                                           extra_env={"FACTORY_MEM_AVAILABLE_MB": pin}), "MemAvailable")

    def test_serve_mem_fn_honours_the_pin(self):
        import sys
        sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "bin"))
        self.addCleanup(sys.path.pop, 0)
        import factory_cli
        # the live /proc/meminfo reader is mocked with a fixed value: no second live read to race the first
        with mock.patch.object(factory_cli, "mem_available_mb", return_value=4242) as live:
            with mock.patch.dict("os.environ", {"FACTORY_MEM_AVAILABLE_MB": "3900"}):
                self.assertEqual(factory_cli.serve_mem_available_mb(), 3900)
                live.assert_not_called()
            with mock.patch.dict("os.environ", {"FACTORY_MEM_AVAILABLE_MB": "junk"}):
                self.assertEqual(factory_cli.serve_mem_available_mb(), 4242)
                live.assert_called_once_with()

    def test_gate_usage(self):
        self.env.write_usage(five_hour=95)
        self.assert_queued(self.spawn(), "5h usage")

    def test_gate_usage_weekly(self):
        self.env.write_usage(seven_day=95)
        self.assert_queued(self.spawn(), "weekly usage")

    def test_gate_usage_stale_is_ignored(self):
        self.env.write_usage(five_hour=95, seven_day=95, age_s=5 * 3600)
        self.assertIn("SPAWNED", self.spawn().stdout)

    def test_gate_open_spawns(self):
        p = self.spawn()
        self.assertIn("SPAWNED zz-test-c1", p.stdout)
        row = self.env.sql("SELECT * FROM agents WHERE name='zz-test-c1'")[0]
        self.assertEqual((row["status"], row["role"], row["model"]), ("live", "coder", MODELS["coder"]))
        msgs = self.env.sql("SELECT * FROM messages WHERE recipient='zz-test-c1'")
        self.assertEqual(len(msgs), 1)
        self.assertTrue(msgs[0]["subject"].startswith("kickoff"))
        self.assertEqual(msgs[0]["sender"], "factory")
        self.assertIn("factory task show T-0001", msgs[0]["body"])
        starts = [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]
        self.assertEqual(len(starts), 1)
        self.assertEqual(starts[0][starts[0].index("--model") + 1], MODELS["coder"])
        self.assertEqual(self.env.sql("SELECT assignee FROM tasks WHERE id='T-0001'")[0]["assignee"], "zz-test-c1")

    def test_gate_open_uses_role_model(self):
        self.env.seed_team()
        self.ok("spawn", "reviewer", "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001", "--name", "zz-test-r1")
        starts = [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]
        self.assertEqual(starts[0][starts[0].index("--model") + 1], MODELS["reviewer"])
        self.assertNotEqual(MODELS["reviewer"], MODELS["coder"])

    def test_gate_duplicate_live_name_refused(self):
        self.spawn()
        p = self.refused(1, "spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", "zz-test-c1")
        self.assertIn("already live", p.stderr)

    def test_res_output(self):
        p = self.ok("res")
        self.assertIn("spawn gate: OPEN", p.stdout)
        self.env.set_herdr(fail=True)
        p = self.ok("res")
        self.assertIn("spawn gate: CLOSED", p.stdout)
        self.assertIn("herdr unreachable", p.stdout)


class UsageReopenTests(FactoryTestCase):
    """T-1641: after an at-cap reading, a stale, unreadable or single below-cap sample keeps the Claude queue closed."""

    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_task(status="todo", pr_url=None)

    def write_reading(self, five, seven, updated_at):
        f = self.env.user_home / ".claude" / "usage-limits.json"
        f.parent.mkdir(exist_ok=True)
        f.write_text(json.dumps({"five_hour_pct": five, "seven_day_pct": seven, "updated_at": updated_at}))

    def hold(self):
        rows = self.env.sql("SELECT ts FROM throttle WHERE key='usage-reopen-streak'")
        self.assertLessEqual(len(rows), 1)
        return json.loads(rows[0]["ts"]) if rows else None

    def spawn_pm(self, name):
        return self.ok("spawn", "pm", "--goal", "G-001", "--name", name)

    def assert_queued_row(self, p, text, name):
        self.assertIn("QUEUED", p.stdout)
        self.assertIn(text, p.stdout)
        self.assertNotIn("SPAWNED", p.stdout)
        rows = self.env.sql("SELECT status FROM agents WHERE name=?", (name,))
        self.assertEqual([r["status"] for r in rows], ["queued"])

    def statuses(self, *names):
        out = {}
        for name in names:
            rows = self.env.sql("SELECT status FROM agents WHERE name=?", (name,))
            self.assertEqual(len(rows), 1, name)
            out[name] = rows[0]["status"]
        return out

    def test_owner_caps_and_reopen_default(self):
        self.assertEqual(self.ok("config", "max_five_hour_pct").stdout.strip(), "92")
        self.assertEqual(self.ok("config", "max_seven_day_pct").stdout.strip(), "92")
        self.assertEqual(self.ok("config", "usage_reopen_samples").stdout.strip(), "3")

    def test_replay_2351_stale_then_one_dip_stays_closed_until_three(self):
        # 23:51 drain: 90 fresh, then a stale reading, then one fresh 89, must not start the queue.
        now = time.time()
        self.ok("config", "max_seven_day_pct", "90")
        self.ok("config", "max_live_agents", "20")
        self.ok("config", "scout_enabled", "0")
        self.ok("config", "retro_enabled", "0")
        names = ["zz-a-over", "zz-a-stale", "zz-a-one", "zz-a-again"]
        self.write_reading(10, 90, now)
        self.assert_queued_row(self.spawn_pm(names[0]), "weekly usage 90% ≥ 90%", names[0])
        self.assertEqual(self.hold()["n"], 0)
        self.assertTrue(self.hold()["sample"])

        self.write_reading(10, 90, now - 5 * 3600)
        stale = self.spawn_pm(names[1])
        self.assert_queued_row(stale, "weekly usage stale:", names[1])
        self.assertIn("90%", stale.stdout)
        self.assertEqual(self.hold()["n"], 0)

        self.write_reading(10, 89, now + 1)
        one = self.spawn_pm(names[2])
        self.assert_queued_row(one, "1/3 consecutive fresh readings", names[2])
        self.assertIn("89%", one.stdout)
        again = self.spawn_pm(names[3])
        self.assert_queued_row(again, "1/3 consecutive fresh readings", names[3])
        self.assertEqual(self.hold()["n"], 1)

        self.ok("tick")
        self.assertEqual(self.statuses(*names), {n: "queued" for n in names})
        self.assertEqual(self.hold()["n"], 1)

        self.write_reading(10, 89, now + 2)
        self.ok("tick")
        self.assertEqual(self.statuses(*names), {n: "queued" for n in names})
        self.assertEqual(self.hold()["n"], 2)

        self.write_reading(10, 89, now + 3)
        self.ok("tick")
        self.assertEqual(self.statuses(*names), {n: "live" for n in names})
        self.assertIsNone(self.hold())

    def test_unreadable_after_over_cap_stays_closed(self):
        now = time.time()
        self.ok("config", "max_seven_day_pct", "90")
        self.write_reading(10, 91, now)
        self.assert_queued_row(self.spawn_pm("zz-b-over"), "weekly usage 91% ≥ 90%", "zz-b-over")
        f = self.env.user_home / ".claude" / "usage-limits.json"
        f.write_text("not json")
        bad = self.spawn_pm("zz-b-bad")
        self.assert_queued_row(bad, "weekly usage unreadable", "zz-b-bad")
        f.write_text(json.dumps({"five_hour_pct": None, "seven_day_pct": None, "updated_at": now + 1}))
        none = self.spawn_pm("zz-b-none")
        self.assert_queued_row(none, "weekly usage unreadable", "zz-b-none")
        self.assertIsNotNone(self.hold())

    def test_cold_start_stale_without_over_cap_stays_open(self):
        now = time.time()
        self.write_reading(95, 95, now - 5 * 3600)
        p = self.spawn_pm("zz-c-open")
        self.assertIn("SPAWNED zz-c-open", p.stdout)
        self.assertNotIn("QUEUED", p.stdout)
        self.assertNotIn("holding the queue", p.stdout)
        self.assertIsNone(self.hold())
        rows = self.env.sql("SELECT status FROM agents WHERE name='zz-c-open'")
        self.assertEqual(rows[0]["status"], "live")

    def test_cursor_runtime_ignores_the_hold(self):
        now = time.time()
        self.ok("config", "max_seven_day_pct", "90")
        self.env.seed_team(lead="zz-test-lead")
        self.env.seed_agent("zz-test-director", "director", goal="G-001", team="g-001-a")
        fake = self.env.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho CURSOR_OK\n")
        fake.chmod(0o755)
        self.write_reading(10, 90, now)
        self.ok("config", "agent_runtimes", "*=cursor")
        first = self.spawn_pm("zz-d-cursor")
        self.assertIn("SPAWNED zz-d-cursor", first.stdout)
        self.assertIn("runtime=cursor", first.stdout)
        self.assertNotIn("weekly usage", first.stdout)
        self.assertIsNone(self.hold())  # a cursor spawn does not arm the latch

        self.ok("config", "agent_runtimes", "")
        self.write_reading(10, 90, now)  # same at-cap sample
        held = self.spawn_pm("zz-d-claude")
        self.assert_queued_row(held, "weekly usage 90% ≥ 90%", "zz-d-claude")
        self.assertEqual(self.hold()["n"], 0)

        self.ok("config", "agent_runtimes", "*=cursor")
        still = self.spawn_pm("zz-d-cursor2")
        self.assertIn("SPAWNED zz-d-cursor2", still.stdout)
        self.assertNotIn("weekly usage", still.stdout)
        self.assertEqual(self.hold()["n"], 0)

        self.write_reading(10, 90, now - 5 * 3600)
        stale_cursor = self.spawn_pm("zz-d-cursor3")
        self.assertIn("SPAWNED zz-d-cursor3", stale_cursor.stdout)
        self.assertNotIn("weekly usage", stale_cursor.stdout)
        self.ok("config", "agent_runtimes", "")
        stale_claude = self.spawn_pm("zz-d-claude2")
        self.assert_queued_row(stale_claude, "weekly usage stale:", "zz-d-claude2")
        self.assertIn("90%", stale_claude.stdout)

        self.write_reading(10, 89, now + 1)
        self.ok("config", "agent_runtimes", "*=cursor")
        dip_cursor = self.spawn_pm("zz-d-cursor4")
        self.assertIn("SPAWNED zz-d-cursor4", dip_cursor.stdout)
        self.assertNotIn("weekly usage", dip_cursor.stdout)
        self.ok("config", "agent_runtimes", "")
        dip_claude = self.spawn_pm("zz-d-claude3")
        self.assert_queued_row(dip_claude, "1/3 consecutive fresh readings", "zz-d-claude3")
        self.assertEqual(self.hold()["n"], 1)

    def test_at_cap_reading_latches_when_an_earlier_cap_closes_first(self):
        now = time.time()
        self.ok("config", "max_live_agents", "1")
        self.ok("config", "max_seven_day_pct", "90")
        self.env.seed_agent("zz-full", "coder", goal="G-001")
        self.env.sql("UPDATE agents SET pane_id=? WHERE name=?", ("p-zz-full", "zz-full"))
        self.env.set_herdr(agents=[{"name": "zz-full", "agent": "claude", "pane_id": "p-zz-full"}])
        self.write_reading(10, 90, now)
        blocked = self.spawn_pm("zz-arm")
        self.assert_queued_row(blocked, "agent panes live", "zz-arm")
        self.assertEqual(self.hold()["n"], 0)
        self.ok("config", "max_live_agents", "6")
        self.write_reading(10, 90, now - 5 * 3600)
        stale = self.spawn_pm("zz-arm-stale")
        self.assert_queued_row(stale, "weekly usage stale:", "zz-arm-stale")
        self.assertIn("90%", stale.stdout)

    def test_infinite_updated_at_stays_fresh_and_queues_at_cap(self):
        self.ok("config", "max_seven_day_pct", "90")
        f = self.env.user_home / ".claude" / "usage-limits.json"
        f.parent.mkdir(exist_ok=True)
        f.write_text('{"five_hour_pct": 10, "seven_day_pct": 95, "updated_at": Infinity}')
        blocked = self.spawn_pm("zz-inf")
        self.assert_queued_row(blocked, "weekly usage 95% ≥ 90%", "zz-inf")
        self.assertEqual(self.hold()["n"], 0)  # the at-cap reading armed the latch

    def test_reopen_count_follows_usage_reopen_samples(self):
        now = time.time()
        self.ok("config", "max_seven_day_pct", "90")
        self.ok("config", "usage_reopen_samples", "2")
        self.write_reading(10, 90, now)
        self.assert_queued_row(self.spawn_pm("zz-n-over"), "weekly usage 90% ≥ 90%", "zz-n-over")
        self.write_reading(10, 89, now + 1)
        self.assert_queued_row(self.spawn_pm("zz-n-one"), "1/2 consecutive fresh readings", "zz-n-one")
        self.write_reading(10, 89, now + 2)
        opened = self.spawn_pm("zz-n-two")
        self.assertIn("SPAWNED zz-n-two", opened.stdout)
        self.assertNotIn("QUEUED", opened.stdout)
        self.assertIsNone(self.hold())


class TotalClaudeCapTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_task(status="todo", pr_url=None)

    def procs(self, n, first=100):
        for i in range(n):
            self.env.fake_claude(first + i)

    def spawn(self, name="zz-test-c1"):
        return self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", name)

    def live(self, *names):
        for n in names:
            self.env.seed_agent(n, "coder", goal="G-001", pane_id="p-" + n)
        self.env.set_herdr(agents=[{"name": n, "agent": "claude", "pane_id": "p-" + n} for n in names])

    def test_gate_closed_at_total_claude_cap(self):
        self.live("zz-test-x1", "zz-test-x2", "zz-test-x3")
        self.procs(10)  # 3 factory + 7 non-factory
        p = self.spawn()
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("10/10 claude processes on host (incl. non-factory)", p.stdout)
        self.assertEqual([c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]], [])

    def test_gate_open_below_total_claude_cap(self):
        self.live("zz-test-x1", "zz-test-x2", "zz-test-x3")
        self.procs(9)
        self.assertIn("SPAWNED", self.spawn().stdout)

    def test_gate_total_counts_starting_spawn_without_process(self):
        import datetime as dt
        self.procs(9)
        recent = (dt.datetime.now().astimezone() - dt.timedelta(seconds=10)).isoformat()
        self.env.seed_agent("zz-test-x1", "coder", goal="G-001", started=recent)
        out = self.ok("res").stdout
        self.assertIn("CLOSED — 10/10 claude processes on host", out)

    def test_total_claude_skips_child_claude_procs(self):
        self.env.fake_claude(100, ppid=1)
        self.env.fake_claude(101, ppid=100)  # child of a counted claude
        self.env.fake_claude(102, ppid=101)  # grandchild
        self.env.fake_claude(103, argv=("node", "/usr/bin/claude"))  # basename of argv[1]
        with self.env.load_factory() as mod:
            got = mod.total_claude_procs()
            self.assertEqual((got["total"], got["pids"]), (2, [100, 103]))
            self.assertEqual(got["argv"], {100: ["claude"], 103: ["node", "/usr/bin/claude"]})

    def test_claude_procs_matches_session_id_flag(self):
        self.env.fake_claude(100, argv=("claude", "--session-id", "sid-a", "--model", "m"))
        self.env.fake_claude(101, argv=("claude", "--session-id=sid-b"))
        self.env.fake_claude(102, argv=("claude", "--resume", "sid-c"))
        self.env.fake_claude(103, argv=("claude", "--model", "sid-a"))
        with self.env.load_factory() as mod:
            self.assertEqual([p for p, _ in mod.claude_procs("sid-a")], [100])
            self.assertEqual([p for p, _ in mod.claude_procs("sid-b")], [101])
            self.assertEqual([p for p, _ in mod.claude_procs("sid-c")], [102])
            self.assertEqual(mod.claude_procs("sid-d"), [])

    def test_gate_fresh_spawn_with_process_counted_once(self):
        self.procs(8)
        self.assertIn("SPAWNED", self.spawn("zz-test-c1").stdout)
        sid = self.env.sql("SELECT session_id FROM agents WHERE name='zz-test-c1'")[0]["session_id"]
        self.env.fake_claude(200, argv=("claude", "--model", "m", "--settings", "/x/settings/coder.json",
                                        "--session-id", sid))
        out = self.ok("res").stdout
        self.assertIn("spawn gate: OPEN", out)
        self.assertIn("claude processes 9/10 on host (factory 1 · other 8)", out)

    def test_res_split_counts_fresh_and_resumed_factory_procs(self):
        for n, sid in (("zz-test-x1", "sid-fresh"), ("zz-test-x2", "sid-resumed")):
            self.env.seed_agent(n, "coder", goal="G-001")
            self.env.sql("UPDATE agents SET pane_id=?, session_id=? WHERE name=?", ("p-" + n, sid, n))
        self.env.set_herdr(agents=[{"name": n, "agent": "claude", "pane_id": "p-" + n}
                                   for n in ("zz-test-x1", "zz-test-x2")])
        self.env.fake_claude(300, argv=("claude", "--model", "m", "--session-id", "sid-fresh"))  # fresh spawn
        self.env.fake_claude(301, argv=("claude", "--resume", "sid-resumed"))  # resumed
        self.env.fake_claude(302, argv=("claude", "--model", "m"))  # the owner's own pane
        out = self.ok("res").stdout
        self.assertIn("claude processes 3/10 on host (factory 2 · other 1)", out)

    def start_argv(self):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]][0]

    def queue_one(self, name="zz-test-c1"):
        self.env.set_herdr(fail=True)
        self.assertIn("QUEUED", self.spawn(name).stdout)
        self.assertIsNone(self.env.sql("SELECT session_id FROM agents WHERE name=?", (name,))[0]["session_id"])
        self.env.set_herdr(fail=False)

    def test_spawn_fresh_passes_and_stores_session_id(self):
        self.assertIn("SPAWNED", self.spawn().stdout)
        start = self.start_argv()
        sid = start[start.index("--session-id") + 1]
        self.assertRegex(sid, r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
        self.assertNotIn("--resume", start)
        self.assertEqual(self.env.sql("SELECT session_id FROM agents WHERE name='zz-test-c1'")[0]["session_id"], sid)

    def test_spawn_resume_has_no_session_id_flag(self):
        self.queue_one()
        self.env.sql("UPDATE agents SET session_id='sess-old', spawn_args=json_set(spawn_args,'$.resume','sess-old') "
                     "WHERE name='zz-test-c1'")
        self.ok("tick")
        start = self.start_argv()
        self.assertEqual(start[start.index("--resume") + 1], "sess-old")
        self.assertNotIn("--session-id", start)
        self.assertEqual(self.env.sql("SELECT session_id FROM agents WHERE name='zz-test-c1'")[0]["session_id"], "sess-old")

    def test_spawn_resume_clears_flagless(self):
        self.queue_one()
        self.env.sql("UPDATE agents SET session_id='sess-old', flagless=1, flagless_since='2026-10-23T10:00:00+00:00', "
                     "spawn_args=json_set(spawn_args,'$.resume','sess-old') WHERE name='zz-test-c1'")
        self.ok("tick")
        row = self.env.sql("SELECT status, flagless, flagless_since FROM agents WHERE name='zz-test-c1'")[0]
        self.assertEqual((row["status"], row["flagless"], row["flagless_since"]), ("live", 0, None))
        ev = self.env.sql("SELECT detail FROM events WHERE action='flagless-cleared'")
        self.assertEqual([json.loads(e["detail"]) for e in ev], [{"via": "spawn"}])

    def test_queued_spawn_gets_session_id_on_start(self):
        self.queue_one()
        self.ok("tick")
        start = self.start_argv()
        sid = start[start.index("--session-id") + 1]
        row = self.env.sql("SELECT status, session_id FROM agents WHERE name='zz-test-c1'")[0]
        self.assertEqual((row["status"], row["session_id"]), ("live", sid))

    def test_tick_does_not_clobber_stored_session_id(self):
        self.env.seed_agent("zz-test-x1", "coder", goal="G-001", pane_id="p-x1")
        self.env.sql("UPDATE agents SET session_id='sid-stored' WHERE name='zz-test-x1'")
        agent = {"name": "zz-test-x1", "agent": "claude", "pane_id": "p-x1", "agent_status": "idle"}
        for sess in (None, {"value": ""}, {"value": None}):
            self.env.set_herdr(agents=[dict(agent, **({} if sess is None else {"agent_session": sess}))])
            self.ok("tick")
            self.assertEqual(self.env.sql("SELECT session_id FROM agents WHERE name='zz-test-x1'")[0]["session_id"],
                             "sid-stored")
        self.assertEqual(self.env.sql("SELECT * FROM events WHERE action='session-mismatch'"), [])
        # a different non-empty value: the listing wins, and the mismatch is recorded
        self.env.set_herdr(agents=[dict(agent, agent_session={"value": "sid-other"})])
        self.ok("tick")
        self.assertEqual(self.env.sql("SELECT session_id FROM agents WHERE name='zz-test-x1'")[0]["session_id"], "sid-other")
        ev = self.env.sql("SELECT detail FROM events WHERE action='session-mismatch'")
        self.assertEqual([json.loads(e["detail"]) for e in ev],
                         [{"name": "zz-test-x1", "stored": "sid-stored", "herdr": "sid-other"}])

    def test_gate_total_unknown_does_not_close(self):
        gone = str(self.env.home / "no-such-proc")
        p = self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", "zz-test-c1",
                    extra_env={"FACTORY_PROC_DIR": gone})
        self.assertIn("SPAWNED", p.stdout)
        out = self.ok("res", extra_env={"FACTORY_PROC_DIR": gone}).stdout
        self.assertIn("claude processes unknown", out)

    def test_res_shows_host_claude_total(self):
        self.live("zz-test-x1")
        self.env.sql("UPDATE agents SET session_id='sess-x1' WHERE name='zz-test-x1'")
        self.env.fake_claude(100, argv=("claude", "--resume", "sess-x1"))
        self.procs(3, first=101)
        out = self.ok("res").stdout
        self.assertIn("claude processes 4/10 on host (factory 1 · other 3)", out)
        self.assertIn("(counted in host total)", out)

    def test_dequeue_stops_at_total_cap(self):
        self.procs(9)  # room for exactly one more
        self.env.set_herdr(fail=True)
        for i in range(2):
            self.assertIn("QUEUED", self.spawn(f"zz-test-q{i}").stdout)
        self.env.set_herdr(fail=False)
        self.ok("tick")
        starts = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]
        self.assertEqual(len(starts), 1)
        rows = self.env.sql("SELECT status FROM agents WHERE name LIKE 'zz-test-q%'")
        self.assertEqual(sorted(r["status"] for r in rows), ["live", "queued"])


class UsageReadTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_task(status="todo", pr_url=None)

    def write_raw(self, text):
        f = self.env.user_home / ".claude" / "usage-limits.json"
        f.parent.mkdir(exist_ok=True)
        f.write_text(text)

    def test_usage_info_retries_partial_json(self):
        good = json.dumps({"five_hour_pct": 41, "seven_day_pct": 12, "updated_at": time.time()})
        reads = iter(['{"five_hour_pct": 4', good])
        with self.env.load_factory() as mod:
            fake = mock.Mock()
            fake.read_text.side_effect = lambda: next(reads)
            with mock.patch.object(mod, "USAGE_FILE", fake):
                u = mod.usage()
        self.assertEqual((u["five_hour"], u["seven_day"], u["fresh"]), (41.0, 12.0, True))
        self.assertNotIn("unreadable", u)

    def test_usage_recorded_payload_parses_to_its_values(self):
        # a payload as ~/.claude/statusline-usage.sh writes it from Claude Code's rate_limits
        self.write_raw('{"five_hour_pct":"42","five_hour_resets_at":"1793000000","seven_day_pct":"93",'
                       '"seven_day_resets_at":"1793500000","updated_at":%d}' % int(time.time()))
        with self.env.load_factory() as mod:
            u = mod.usage()
            self.assertEqual((u["five_hour"], u["seven_day"], u["fresh"]), (42.0, 93.0, True))
            self.assertEqual(mod.usage_text(u), "claude usage 5h 42% · week 93%")
        out = self.ok("res").stdout
        self.assertIn("claude usage 5h 42% · week 93%", out)

    def test_usage_missing_file_is_stale_never_zero(self):
        f = self.env.user_home / ".claude" / "usage-limits.json"
        self.assertFalse(f.exists())
        with self.env.load_factory() as mod:
            u = mod.usage()
            self.assertEqual((u["five_hour"], u["seven_day"], u["fresh"]), (None, None, False))
            self.assertEqual(mod.usage_text(u), "claude usage stale since never (no usage file)")
        line = [l for l in self.ok("res").stdout.splitlines() if "claude usage" in l][0]
        self.assertEqual(line.strip(), "claude usage stale since never (no usage file)")
        self.assertNotIn("0%", line)

    def test_usage_old_file_says_stale_since_time_not_numbers(self):
        self.env.write_usage(five_hour=99, seven_day=97, age_s=5 * 3600)
        line = [l for l in self.ok("res").stdout.splitlines() if "claude usage" in l][0]
        self.assertRegex(line, r"claude usage stale since \d{4}-\d\d-\d\d \d\d:\d\d")
        self.assertNotIn("99%", line)
        self.assertNotIn("97%", line)

    def test_usage_info_unreadable_shown(self):
        self.write_raw('{"five_hour_pct": 4')
        out = self.ok("res").stdout
        self.assertIn("claude usage unreadable", out)
        self.assertNotIn("0%", "\n".join(l for l in out.splitlines() if "claude usage" in l))  # not the tmp line's percentages
        with self.env.load_factory() as mod:
            self.assertEqual(mod.usage(), {"five_hour": None, "seven_day": None, "fresh": False, "age_min": -1,
                                           "unreadable": True, "five_hour_resets_at": None,
                                           "seven_day_resets_at": None, "updated_at": None})

    def test_usage_overflowing_reset_keeps_percentages(self):
        for bad in ("1e400", "inf", "-inf", "nan"):
            self.write_raw(json.dumps({"five_hour_pct": 41, "seven_day_pct": 12, "updated_at": time.time(),
                                       "five_hour_resets_at": bad, "seven_day_resets_at": "1793000000"}))
            with self.env.load_factory() as mod:
                u = mod.usage()
            self.assertNotIn("unreadable", u, bad)
            self.assertEqual((u["five_hour"], u["seven_day"], u["fresh"]), (41.0, 12.0, True), bad)
            self.assertEqual((u["five_hour_resets_at"], u["seven_day_resets_at"]), (None, 1793000000), bad)

    def test_res_usage_line_with_overflowing_resets(self):
        for bad in ("1e400", "inf"):
            self.write_raw(json.dumps({"five_hour_pct": 41, "seven_day_pct": 12, "updated_at": time.time(),
                                       "five_hour_resets_at": bad, "seven_day_resets_at": bad}))
            p = self.ok("res")
            self.assertIn("claude usage 5h 41% · week 12%\n", p.stdout, bad)
            self.assertNotIn("claude usage unreadable", p.stdout, bad)
            self.assertNotIn("stale", p.stdout, bad)

    spawn = GateTests.spawn
    assert_queued = GateTests.assert_queued

    def write_pcts(self, five, seven):
        self.write_raw(json.dumps({"five_hour_pct": five, "seven_day_pct": seven, "updated_at": time.time()}))

    def test_empty_five_hour_still_closes_gate_on_weekly(self):
        self.ok("config", "max_seven_day_pct", "98")
        self.write_pcts("", "99")
        self.assert_queued(self.spawn(), "weekly usage 99% ≥ 98%")
        self.assertIn("claude usage 5h n/a · week 99%\n", self.ok("res").stdout)

    def test_empty_weekly_still_closes_gate_on_five_hour(self):
        self.ok("config", "max_five_hour_pct", "92")
        self.write_pcts("95", "")
        self.assert_queued(self.spawn(), "5h usage 95% ≥ 92%")
        self.assertIn("claude usage 5h 95% · week n/a\n", self.ok("res").stdout)

    def test_partial_reading_below_cap_leaves_gate_open(self):
        self.write_pcts("", "50")
        p = self.spawn()
        self.assertNotIn("usage", p.stdout)
        self.assertIn("claude usage 5h n/a · week 50%\n", self.ok("res").stdout)

    def test_both_empty_is_unreadable(self):
        self.write_pcts("", "")
        with self.env.load_factory() as mod:
            u = mod.usage()
        self.assertTrue(u["unreadable"])
        self.assertEqual((u["five_hour"], u["seven_day"], u["fresh"]), (None, None, False))
        self.assertIn("claude usage unreadable", self.ok("res").stdout)

    def test_bad_value_is_none_for_that_field_only(self):
        with self.env.load_factory() as mod:
            for five, seven, want in (("abc", 97, (None, 97.0)), (None, 97, (None, 97.0)), (91, "abc", (91.0, None)),
                                      ("nan", 12, (None, 12.0)), ("inf", 12, (None, 12.0))):
                self.write_pcts(five, seven)
                u = mod.usage()
                self.assertNotIn("unreadable", u, (five, seven))
                self.assertEqual((u["five_hour"], u["seven_day"]), want, (five, seven))
                self.assertTrue(u["fresh"])
            self.write_raw(json.dumps({"seven_day_pct": "96", "updated_at": time.time()}))   # the 5h key is missing
            u = mod.usage()
            self.assertEqual((u["five_hour"], u["seven_day"]), (None, 96.0))

    def test_usage_missing_file_stays_stale(self):
        with self.env.load_factory() as mod:
            self.assertEqual(mod.usage(), {"five_hour": None, "seven_day": None, "fresh": False, "age_min": -1,
                                           "five_hour_resets_at": None, "seven_day_resets_at": None,
                                           "updated_at": None})

    def test_usage_reset_epochs_coerced_from_strings(self):
        self.write_raw(json.dumps({"five_hour_pct": "9", "seven_day_pct": "40", "updated_at": time.time(),
                                   "five_hour_resets_at": "1792888800", "seven_day_resets_at": 1793000000.0}))
        with self.env.load_factory() as mod:
            u = mod.usage()
        self.assertEqual((u["five_hour_resets_at"], u["seven_day_resets_at"]), (1792888800, 1793000000))
        self.assertIsInstance(u["five_hour_resets_at"], int)

    def test_usage_reset_garbage_is_none(self):
        self.write_raw(json.dumps({"five_hour_pct": 1, "seven_day_pct": 2, "updated_at": time.time(),
                                   "five_hour_resets_at": "soon", "seven_day_resets_at": True}))
        with self.env.load_factory() as mod:
            u = mod.usage()
        self.assertEqual((u["five_hour_resets_at"], u["seven_day_resets_at"]), (None, None))
        self.assertEqual((u["five_hour"], u["seven_day"], u["fresh"]), (1.0, 2.0, True))

    def test_gate_unreadable_usage_same_as_stale(self):
        self.write_raw("not json")
        self.assertIn("spawn gate: OPEN", self.ok("res").stdout)
        p = self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", "zz-test-c1")
        self.assertIn("SPAWNED", p.stdout)
        self.env.write_usage(five_hour=95, seven_day=95, age_s=5 * 3600)  # stale: the same outcome
        self.assertIn("spawn gate: OPEN", self.ok("res").stdout)



# T-1568 (G-049 SC3): darwin branches in bin/factory_cli.py (sys.platform patched on Linux).
import argparse
import builtins
import contextlib
import io
import os
from pathlib import Path
from unittest import mock

from harness import (
    DARWIN_PS_SAMPLE,
    DARWIN_START_500,
    DARWIN_VM_STAT,
    DarwinSubprocessRunner,
    FactoryTestCase,
    _darwin_cp,
)

class DetachedRun(FactoryTestCase):
    def test_b7_detached_run_skips_systemd_run_on_darwin(self):
        with self.darwin_platform(INVOCATION_ID="x") as (mod, runner):
            popen = mock.Mock()
            with mock.patch.object(mod, "mech_mode", return_value="on"), mock.patch.object(mod, "mech_candidates", return_value=[1]), \
                    mock.patch.object(mod.subprocess, "Popen", popen):
                mod.tick_mech_merge(None)
        self.assertEqual(runner.named("systemd-run"), [])
        self.assertEqual(popen.call_count, 1)
        self.assertTrue(popen.call_args.kwargs["start_new_session"])

    def test_b7_linux_still_uses_systemd_run(self):
        with mock.patch.dict(os.environ, {"INVOCATION_ID": "x"}), self.env.load_factory() as mod:
            run = mock.Mock()
            with mock.patch.object(mod, "mech_mode", return_value="on"), mock.patch.object(mod, "mech_candidates", return_value=[1]), \
                    mock.patch.object(mod.subprocess, "run", run):
                mod.tick_mech_merge(None)
        self.assertEqual(run.call_args.args[0][0], "systemd-run")


class ServePid(FactoryTestCase):
    def test_serve_pid_file_written_read_and_removed(self):
        with self.darwin_platform() as (mod, _r):
            runner = DarwinSubprocessRunner(ps=f"{os.getpid()} 1 501 {DARWIN_START_500} python3 factory serve\n")
            with mock.patch.object(mod.subprocess, "run", runner):
                seen = {}

                def fake_run(**kw):
                    seen["text"] = mod._serve_pidfile().read_text()
                    seen["pid"] = mod._serve_mainpid()  # protection reads the file on darwin
                    seen["unit"] = mod._serve_unit_pids()

                import web.server as srv
                with mock.patch.object(srv, "run", fake_run):
                    mod.cmd_serve(argparse.Namespace(bind=None, port=None, ttl=None))
                self.assertEqual(seen["text"], f"{os.getpid()} {DARWIN_START_500}\n")  # pid + start time
                self.assertEqual(seen["pid"], os.getpid())
                self.assertEqual(seen["unit"], {os.getpid()})
                self.assertFalse(mod._serve_pidfile().exists())  # removed on clean exit
                self.assertEqual(runner.named("systemctl"), [])

    def test_serve_pid_file_removed_when_serve_raises(self):
        with self.darwin_platform() as (mod, _r):
            import web.server as srv
            with mock.patch.object(srv, "run", side_effect=RuntimeError("boom")):
                with self.assertRaises(RuntimeError):
                    mod.cmd_serve(argparse.Namespace(bind=None, port=None, ttl=None))
            self.assertFalse(mod._serve_pidfile().exists())

    def test_serve_pid_reused_pid_is_not_the_serve(self):
        with self.darwin_platform() as (mod, _r):
            f = mod._serve_pidfile()
            f.parent.mkdir(parents=True, exist_ok=True)
            f.write_text(f"500 Sat Nov 15 01:00:00 2026\n")  # start time differs from ps: the pid was reused
            self.assertIsNone(mod._serve_mainpid())
            self.assertEqual(mod._serve_unit_pids(), set())
            f.write_text(f"500 {DARWIN_START_500}\n")
            self.assertEqual(mod._serve_mainpid(), 500)
            f.write_text("garbage")
            self.assertIsNone(mod._serve_mainpid())

    def test_serve_pid_not_written_on_linux(self):
        with self.env.load_factory() as mod:
            import web.server as srv
            with mock.patch.object(srv, "run", lambda **kw: self.assertFalse(mod._serve_pidfile().exists())):
                mod.cmd_serve(argparse.Namespace(bind=None, port=None, ttl=None))


class Memory(FactoryTestCase):
    def test_r1_formula_free_inactive_speculative(self):
        with self.darwin_platform() as (mod, runner):
            self.assertEqual(mod.mem_available_mb(), (10000 + 20000 + 2000) * 16384 // 1048576)
        self.assertEqual(runner.named("sysctl")[0][1:], ["-n", "kern.memorystatus_vm_pressure_level"])

    def test_unreadable_vm_stat_closes_the_gate(self):
        for runner in (DarwinSubprocessRunner(vm=None), DarwinSubprocessRunner(level=None), DarwinSubprocessRunner(level="2"), DarwinSubprocessRunner(level="4"), DarwinSubprocessRunner(vm="garbage")):
            with self.darwin_platform(runner) as (mod, _r):
                self.assertEqual(mod.mem_available_mb(), 0)  # 0 < min_mem_available_mb: gate closed

    def test_unreadable_ps_keeps_the_pid(self):
        with self.darwin_platform(DarwinSubprocessRunner(ps=None)) as (mod, _r):
            self.assertIsNone(mod._proc_start(500))
            self.assertIsNone(mod._orphan_stat(500))
            self.assertIsNone(mod._serve_argv(500))
            self.assertIsNone(mod._orphan_ident(500))  # None = the reapers decide nothing for this pid
            self.assertIsNone(mod._pid_comm(Path("/x/500")))
            self.assertIsNone(mod._proc_holds(["/tmp/x"]))  # keep everything
            self.assertIsNone(mod._scan_claude())

    def test_unreadable_reapers_never_signal(self):
        with self.darwin_platform(DarwinSubprocessRunner(ps=None)) as (mod, _r):
            con = mod.db()
            kill = mock.Mock()
            with mock.patch.object(mod, "_serve_kill", kill):
                mod.reap_stray_serves(con)
                mod.reap_orphans(con)
            self.assertEqual(kill.call_count, 0)


class TmpGate(FactoryTestCase):
    def test_r5_fs_type_from_stat_f_T_private_tmp(self):
        with self.darwin_platform(DarwinSubprocessRunner(stat_t="apfs\n")) as (mod, runner):
            with mock.patch.object(mod, "tmp_dir", lambda: Path("/tmp")), \
                    mock.patch.dict(os.environ, {"FACTORY_TMP_STATVFS": f"{500 * 2**30},{100 * 2**30}"}):
                u = mod.tmp_usage()
        self.assertEqual(u["fstype"], "apfs")
        self.assertEqual(runner.named("stat")[0], ["/usr/bin/stat", "-f", "%T", "/private/tmp"])
        self.assertEqual((u["total_mb"], u["free_mb"]), (500 * 1024, 100 * 1024))

    def test_r5_unreadable_stat_is_unknown_fstype_not_a_crash(self):
        r = DarwinSubprocessRunner()
        r.stat_t = ""
        with self.darwin_platform(r, FACTORY_TMP_STATVFS=f"{2**30},{2**29}") as (mod, _r):
            self.assertIsNone(mod.tmp_usage()["fstype"])


class TmpFloor(FactoryTestCase):
    def gate_with(self, tmp, role=None):
        with self.darwin_platform() as (mod, _r):
            with mock.patch.object(mod, "tmp_usage", return_value=tmp), mock.patch.object(mod, "load1", return_value=0.0):
                return mod.gate(mod.db(), role=role)[:2]

    def tmp(self, free_mb, fstype):
        return {"used_mb": 1, "total_mb": 1 + free_mb, "pct": 1.0, "fstype": fstype, "free_mb": free_mb}

    def test_r5_disk_tmp_below_floor_closes_gate(self):
        ok, why = self.gate_with(self.tmp(10239, "apfs"))
        self.assertFalse(ok)
        self.assertIn("10240 MB floor", why)

    def test_r5_disk_tmp_at_floor_passes_the_tmp_check(self):
        ok, why = self.gate_with(self.tmp(10240, "apfs"))
        self.assertNotIn("floor", why)

    def test_r5_tmpfs_has_no_floor_and_unreadable_tmp_closes(self):
        self.assertNotIn("floor", self.gate_with(self.tmp(5, "tmpfs"))[1])
        ok, why = self.gate_with(None)
        self.assertFalse(ok)
        self.assertIn("unreadable", why)


class ProcIdentity(FactoryTestCase):
    def test_r9_identity_from_one_ps_snapshot(self):
        with self.darwin_platform() as (mod, runner):
            self.assertEqual(mod._proc_start(500), DARWIN_START_500)
            self.assertEqual(mod._orphan_stat(600)[0], 500)
            self.assertEqual(mod._serve_argv(500)[-1], "serve")
            self.assertEqual(mod._pid_comm(Path("/x/500")), "python3")
            with self.assertRaises(FileNotFoundError):
                mod._pid_comm(Path("/x/999"))  # exited: not in the snapshot
            self.assertEqual(sorted(mod._scan_claude()), [600, 700])
            self.assertEqual(mod._scan_claude()[600][1], 500)
        ps_calls = runner.named("ps")
        self.assertTrue(all(c == ["/bin/ps", "-axo", "pid=,ppid=,uid=,lstart=,command="] for c in ps_calls))

    def test_r9_ps_runs_with_lc_all_c(self):
        seen = {}

        def run(cmd, *a, **kw):
            seen["env"] = kw.get("env", {})
            return _darwin_cp(DARWIN_PS_SAMPLE)
        with self.darwin_platform() as (mod, _r), mock.patch.object(mod.subprocess, "run", run):
            mod._proc_start(500)
        self.assertEqual(seen["env"].get("LC_ALL"), "C")

    def test_r13_userns_link_is_none_on_darwin(self):
        with self.darwin_platform() as (mod, _r):
            self.assertIsNone(mod._userns_link(os.getpid()))
            self.assertIsNone(mod._userns_link(1))

    def test_ancestry_worker_walks_ps_parents(self):
        ps = f"""  {os.getpid()}   600   501 {DARWIN_START_500} python3 factory done
  600   1   501 {DARWIN_START_500} claude --settings /h/settings/coder.json
"""
        with self.darwin_platform(DarwinSubprocessRunner(ps=ps)) as (mod, _r):
            self.assertEqual(mod._ancestry_worker(), "coder")

    def test_ancestry_worker_refuses_spaced_settings_path(self):
        ps = f"""  {os.getpid()}   600   501 {DARWIN_START_500} python3 factory tick
  600   1   501 {DARWIN_START_500} claude --settings /Users/Jane Doe/settings/coder.json --resume abc
"""
        with self.darwin_platform(DarwinSubprocessRunner(ps=ps)) as (mod, _r):
            self.assertEqual(mod._ancestry_worker(), "coder")
            with self.assertRaises(SystemExit) as ctx:
                mod.refuse_worker("tick")
            self.assertEqual(ctx.exception.code, 3)

    def test_ps_unbalanced_quote_does_not_crash_snapshot(self):
        ps = f"""  {os.getpid()}   600   501 {DARWIN_START_500} python3 factory tick
  600   1   501 {DARWIN_START_500} claude --settings /h/settings/coder.json
  700   1   501 {DARWIN_START_500} claude -p "don't stop
"""
        with self.darwin_platform(DarwinSubprocessRunner(ps=ps)) as (mod, _r):
            snap = mod._darwin_ps()
            self.assertIn(700, snap)
            self.assertEqual(mod._ancestry_worker(), "coder")

    def test_ancestry_settings_match_is_anchored_on_the_settings_flag(self):
        ps = f"""  {os.getpid()}   600   501 {DARWIN_START_500} python3 factory tick
  600   1   501 {DARWIN_START_500} claude -p read /h/settings/coder.json --settings /h/settings/lead.json
"""
        with self.darwin_platform(DarwinSubprocessRunner(ps=ps)) as (mod, _r):
            self.assertIsNone(mod._ancestry_worker())  # a free-form arg naming coder.json is not the settings file
        ps = f"""  {os.getpid()}   600   501 {DARWIN_START_500} python3 factory tick
  600   1   501 {DARWIN_START_500} claude --settings=/h/settings/qa.json --resume x
"""
        with self.darwin_platform(DarwinSubprocessRunner(ps=ps)) as (mod, _r):
            self.assertEqual(mod._ancestry_worker(), "qa")

    def test_darwin_tools_run_by_absolute_path_with_fixed_path_env(self):
        r = DarwinSubprocessRunner(top="  2048 /usr/bin/big\n")
        with self.darwin_platform(r, PATH="/evil/bin:" + os.environ.get("PATH", ""),
                                  FACTORY_TMP_STATVFS=f"{2**30},{2**29}") as (mod, runner):
            seen = []

            def spy(cmd, *a, **kw):
                seen.append((cmd[0], (kw.get("env") or {}).get("PATH")))
                return runner(cmd, *a, **kw)
            with mock.patch.object(mod.subprocess, "run", spy):
                mod._darwin_ps()
                mod._darwin_mem_available_mb()
                mod.tmp_usage()
        tools = {c: p for c, p in seen if os.path.basename(c) in ("ps", "vm_stat", "sysctl", "stat")}
        self.assertEqual(sorted(tools), ["/bin/ps", "/usr/bin/stat", "/usr/bin/vm_stat", "/usr/sbin/sysctl"])
        for c, p in tools.items():
            self.assertEqual(p, "/usr/bin:/bin:/usr/sbin:/sbin", c)

    def test_fake_ps_first_on_path_is_ignored(self):
        """T-1568 S1: a worker puts a fake `ps` first on PATH to forge a non-worker ancestry; the real ps runs."""
        if not os.access("/bin/ps", os.X_OK):
            self.skipTest("/bin/ps not present on this host")
        d = Path(self.env.home) / "evilbin"
        d.mkdir()
        marker = d / "ran"
        (d / "ps").write_text(f"#!/bin/sh\ntouch {marker}\necho '  {os.getpid()}  1  0 Sat Nov 15 21:20:43 2026 init'\n")
        (d / "ps").chmod(0o755)
        with mock.patch.dict(os.environ, {"PATH": f"{d}:{os.environ.get('PATH', '')}"}), \
                self.env.load_factory() as mod, mock.patch.object(mod.sys, "platform", "darwin"):
            snap = mod._darwin_ps()  # the real subprocess.run: only the PATH lookup is under test
        self.assertFalse(marker.exists(), "the PATH-resolved fake ps ran")
        self.assertIsNotNone(snap)
        self.assertEqual(snap[os.getpid()]["ppid"], os.getppid())  # the real tree, not the forged ppid 1



class NoProc(FactoryTestCase):
    def test_no_proc_read_on_the_darwin_path(self):
        real_open, real_readlink, real_iterdir = builtins.open, os.readlink, Path.iterdir

        def is_proc(p):
            return str(p) == "/proc" or str(p).startswith("/proc/")

        def bad_open(f, *a, **kw):
            if is_proc(f):
                raise AssertionError(f"open of {f} on the darwin path")
            return real_open(f, *a, **kw)

        def bad_readlink(p, *a, **kw):
            if is_proc(p):
                raise AssertionError(f"readlink of {p} on the darwin path")
            return real_readlink(p, *a, **kw)

        def bad_iterdir(self_):
            if is_proc(self_):
                raise AssertionError(f"iterdir of {self_} on the darwin path")
            return real_iterdir(self_)

        with self.darwin_platform(FACTORY_TMP_STATVFS=f"{2**30},{2**29}") as (mod, _r):
            con = mod.db()
            with mock.patch.object(builtins, "open", bad_open), mock.patch.object(io, "open", bad_open), \
                    mock.patch.object(os, "readlink", bad_readlink), mock.patch.object(Path, "iterdir", bad_iterdir):
                mod._userns_link(1)
                mod._proc_start(500)
                mod._orphan_stat(600)
                mod._serve_argv(500)
                mod._scan_claude()
                mod._scan_host_agent_procs()
                mod.total_claude_procs()
                mod.mem_available_mb()
                mod.tmp_usage()
                mod._serve_mainpid()
                mod._serve_unit_pids()
                mod._proc_holds(["/tmp/x"])
                mod._ancestry_worker()
                mod.reap_stray_serves(con)
                mod.reap_orphans(con)

    def test_the_test_would_catch_a_proc_read(self):
        # the guard of the test above must fire: on linux the same call reads /proc
        real_open = builtins.open

        def bad_open(f, *a, **kw):
            if str(f).startswith("/proc/"):
                raise AssertionError("proc read")
            return real_open(f, *a, **kw)
        with self.env.load_factory() as mod, mock.patch.object(builtins, "open", bad_open), mock.patch.object(io, "open", bad_open):
            with self.assertRaises(AssertionError):
                mod.mem_available_mb()


class ResOutput(FactoryTestCase):
    def test_r8_bsd_ps_form_and_serve_drift_note(self):
        with self.darwin_platform(DarwinSubprocessRunner(top="  2048 /usr/bin/big\n 1024 /usr/bin/small\n"), FACTORY_TMP_STATVFS=f"{2**30},{2**29}") as (mod, runner):
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                mod.cmd_res(argparse.Namespace())
        self.assertIn(["/bin/ps", "-axo", "rss=,command=", "-m"], runner.named("ps"))
        top_env = runner.envs[runner.calls.index(["/bin/ps", "-axo", "rss=,command=", "-m"])]
        self.assertEqual((top_env or {}).get("PATH"), "/usr/bin:/bin:/usr/sbin:/sbin")  # not the caller's PATH
        self.assertNotIn("--sort=-rss", str(runner.calls))
        self.assertIn("serve unit drift check: skipped on macOS", out.getvalue())
        self.assertIn("2 MB  /usr/bin/big", out.getvalue())

    def test_serve_unit_drift_tick_hook_skips_on_darwin(self):
        with self.darwin_platform() as (mod, _r):
            with mock.patch.object(mod.Path, "read_bytes", side_effect=AssertionError("read")):
                self.assertIsNone(mod.tick_serve_unit_drift(None))


class NiceCheck(FactoryTestCase):
    def test_r18_usr_bin_nice_existence_check_is_unchanged(self):
        src = (Path(__file__).resolve().parent.parent / "bin" / "factory_cli.py").read_text()
        for line in (l for l in src.splitlines() if "/usr/bin/nice" in l):
            self.assertNotIn("darwin", line)  # no platform branch: /usr/bin/nice exists on macOS too


class TickDeadline(FactoryTestCase):
    def run_tick(self, deadline):
        with self.env.load_factory() as mod:
            clock = [100.0]
            ran = []

            def unit(name, cost):
                def f(con, st):
                    ran.append(name)
                    clock[0] += cost
                return f
            mod._tick_u_slow = unit("slow", 5)
            mod._tick_u_after = unit("after", 0)
            mod._tick_u_emit = lambda con, st: ran.append("emit")
            with mock.patch.object(mod, "TICK_WALK_UNITS", ["migrate_db", "slow", "after"]), \
                    mock.patch.object(mod.time, "monotonic", lambda: clock[0]):
                mod.cmd_tick(argparse.Namespace(phases=None, deadline=deadline))
            ev = [dict(r) for r in mod.db().execute("SELECT action,detail FROM events WHERE action='tick-deadline'")]
            return ran, ev

    def test_deadline_aborts_remaining_phases_with_event_and_returns(self):
        ran, ev = self.run_tick("1s")
        self.assertEqual(ran, ["slow", "emit"])  # 'after' was not run; the phase timers are still emitted
        self.assertEqual(len(ev), 1)
        self.assertIn("before phase after", ev[0]["detail"])

    def test_no_flag_no_event_all_phases_run(self):
        ran, ev = self.run_tick(None)
        self.assertEqual(ran, ["slow", "after", "emit"])
        self.assertEqual(ev, [])

    def test_deadline_not_reached_runs_everything(self):
        ran, ev = self.run_tick("1h")
        self.assertEqual(ran, ["slow", "after", "emit"])
        self.assertEqual(ev, [])

    def test_bad_deadline_is_refused_exit_2(self):
        p = self.env.run("tick", "--deadline", "soon")
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("not a positive duration", p.stderr + p.stdout)

    def test_deadline_with_phases_is_refused_exit_2(self):
        p = self.env.run("tick", "--phases", "emit", "--deadline", "240s")
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("--deadline is not supported with --phases", p.stderr + p.stdout)

    def test_deadline_flag_hidden_from_help_but_accepted(self):
        p = self.env.run("tick", "--help")
        self.assertNotIn("--deadline", p.stdout)


class PlacementTests(FactoryTestCase):
    """G-057 placement. worker_machines empty keeps today's gate; the ssh stub records every call."""

    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_task(status="todo", pr_url=None)

    def facts(self, workerhost, mem="8000", load="0.1", nproc="8"):
        return {
            "FACTORY_MEM_MB": str(mem),
            "FACTORY_LOADAVG": str(load),
            "FACTORY_NPROC": str(nproc),
            "FACTORY_SSH_FACTS": json.dumps({"workerhost": workerhost}),
        }

    def enable(self):
        self.ok("config", "worker_machines", "workerhost")
        self.ok("config", "machine_hq", "hqhost")

    def test_worker_machines_empty_makes_no_ssh_call(self):
        env = self.facts({"mem_mb": 11000, "load1": 0.5, "nproc": 16, "exit": 0})
        p = self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", "zz-off", extra_env=env)
        self.assertIn("SPAWNED", p.stdout)
        self.assertEqual(self.env.calls("ssh"), [])
        self.ok("res", extra_env=env)
        self.assertEqual(self.env.calls("ssh"), [])

    def test_res_matches_origin_main_when_worker_machines_empty(self):
        import subprocess
        import sys
        ps = self.env.stub_bin / "ps"
        ps.write_text("#!/bin/sh\nprintf '%s\\n' 'RSS COMMAND' ' 2048 zz-factory-test'\n")
        ps.chmod(0o755)
        meminfo = self.env.home / "meminfo"
        meminfo.write_text("MemAvailable: 8388608 kB\nSwapTotal: 4194304 kB\nSwapFree: 3145728 kB\n")
        pinned = {"FACTORY_MEMINFO": str(meminfo)}
        ours = self.ok("res", extra_env=pinned).stdout
        # T-1823: the one intended difference is the new "daily cap:" line (origin/main has no such line)
        ours = "".join(ln for ln in ours.splitlines(keepends=True) if not ln.startswith("daily cap:"))
        origin = Path(__file__).resolve().parent.parent / "bin" / "_factory_cli_origin_main.py"
        self.addCleanup(origin.unlink, missing_ok=True)
        body = subprocess.check_output(["git", "show", "origin/main:bin/factory_cli.py"],
                                       cwd=Path(__file__).resolve().parent.parent)
        # origin has no FACTORY_MEMINFO pin; point its /proc/meminfo reads at the same snapshot
        body = body.replace(b'Path("/proc/meminfo")', b'Path(os.environ["FACTORY_MEMINFO"])')
        origin.write_bytes(body + b"\nmain()\n")
        p = subprocess.run([sys.executable, str(origin), "res"], capture_output=True, text=True,
                           env=self.env.env(extra=pinned), cwd=self.env.home, timeout=60)
        self.assertEqual(p.returncode, 0, p.stderr)
        # the "claude usage" line is the one thing this change rewrites (a missing file is stale, not 0%)
        keep = lambda out: [l for l in out.splitlines() if "claude usage" not in l]
        self.assertEqual(keep(ours), keep(p.stdout))
        self.assertEqual(self.env.calls("ssh"), [])

    def test_incident_2026_11_15_replay(self):
        self.ok("config", "min_mem_available_mb", "1024")
        env = self.facts({"mem_mb": 11000, "load1": 0.5, "nproc": 16, "exit": 0}, mem="300", load="14", nproc="8")
        p = self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", "zz-inc-off", extra_env=env)
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("MemAvailable 300MB < floor 1024MB", p.stdout)
        self.assertNotIn("workerhost", p.stdout)
        self.assertEqual(self.env.calls("ssh"), [])
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-inc-off'")[0]["status"], "queued")
        self.enable()
        with self.env.load_factory() as mod:
            os.environ.update(env)
            os.environ["FACTORY_FACTS_NOW"] = "10000"
            ok, why, stats = mod.gate(mod.db(), role="coder", runtime="claude")
        self.assertTrue(ok, why)  # the extra work is placed on workerhost; hqhost's own floor does not apply there
        self.assertEqual(stats["machine"], "workerhost")
        # a spawn that cannot start remotely (TUI host here, not hc) stays on hqhost and is refused there: queued
        p = self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", "zz-inc-on", extra_env=env)
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("hqhost:", p.stdout)
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-inc-on'")[0]["status"], "queued")

    def test_placement_shares_follow_nproc(self):
        self.ok("config", "max_live_agents", "28")
        self.enable()
        for i, role in enumerate(("boss", "director", "lead", "judge")):
            self.env.seed_agent(f"zz-hq{i}", role, goal="G-001")
        doc = (Path(__file__).resolve().parent.parent / "docs" / "multi-machine.md").read_text()
        self.assertEqual(sum(1 for ln in doc.splitlines() if ln == "## Placement"), 1)
        self.assertIn("24 * 8 / 24 = 8", doc)
        self.assertIn("24 * 16 / 24 = 16", doc)
        with self.env.load_factory() as mod:
            os.environ.update(self.facts({"mem_mb": 11000, "load1": 0.5, "nproc": 16, "exit": 0}, nproc="8"))
            os.environ["FACTORY_FACTS_NOW"] = "10000"
            _, reason, stats = mod.place_machine(mod.db(), "coder", "claude", True)
        self.assertEqual(reason, "ok")
        dev, two = stats["machines"]["hqhost"]["share"], stats["machines"]["workerhost"]["share"]
        self.assertEqual(dev + two, 24)
        self.assertGreater(two, dev)

    def test_placement_owner_cap_blocks_a_free_machine(self):
        self.ok("config", "max_live_agents", "2")
        self.enable()
        names = ("zz-cap-a", "zz-cap-b")
        for n in names:
            self.env.seed_agent(n, "coder", goal="G-001")
            self.env.sql("UPDATE agents SET pane_id=? WHERE name=?", ("p-" + n, n))
        self.env.set_herdr(agents=[{"name": n, "agent": "claude", "pane_id": "p-" + n} for n in names])
        env = self.facts({"mem_mb": 11000, "load1": 0.1, "nproc": 16, "exit": 0})
        p = self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", "zz-cap", extra_env=env)
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("2/2 agent panes live", p.stdout)
        self.assertNotIn("workerhost", p.stdout)
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-cap'")[0]["status"], "queued")

    def test_placement_gate_passes_onto_workerhost(self):
        self.enable()
        with self.env.load_factory() as mod:
            os.environ.update(self.facts({"mem_mb": 11000, "load1": 0.2, "nproc": 16, "exit": 0}))
            os.environ["FACTORY_FACTS_NOW"] = "10000"
            ok, why, stats = mod.gate(mod.db(), role="coder", runtime="claude")
        self.assertTrue(ok, why)
        self.assertEqual(why, "ok")
        self.assertEqual(stats["machine"], "workerhost")

    def test_machine_floor_cursor_2300_blocked_2500_eligible(self):
        self.enable()
        with self.env.load_factory() as mod:
            os.environ["FACTORY_MEM_MB"] = "8000"
            os.environ["FACTORY_LOADAVG"] = "0.1"
            os.environ["FACTORY_NPROC"] = "8"
            os.environ["FACTORY_FACTS_NOW"] = "10000"
            os.environ["FACTORY_SSH_FACTS"] = json.dumps(
                {"workerhost": {"mem_mb": 2300, "load1": 0.1, "nproc": 16, "exit": 0}})
            con = mod.db()
            machine, reason, stats = mod.place_machine(con, "coder", "cursor", True)
            self.assertNotEqual(machine, "workerhost")
            self.assertTrue(any("floor" in f for f in stats["machines"]["workerhost"]["fails"]), stats)
            self.assertEqual(machine, "hqhost")
            os.environ["FACTORY_FACTS_NOW"] = "20000"
            os.environ["FACTORY_SSH_FACTS"] = json.dumps(
                {"workerhost": {"mem_mb": 2500, "load1": 0.1, "nproc": 16, "exit": 0}})
            machine, reason, stats = mod.place_machine(con, "coder", "cursor", True)
        self.assertEqual((machine, reason), ("workerhost", "ok"))
        self.assertEqual(stats["machines"]["workerhost"]["fails"], [])

    def test_placement_hq_roles_never_leave_machine_hq(self):
        self.enable()
        pinned = ("boss", "director", "lead", "judge", "pm", "staff-engineer")
        workers = ("coder", "qa", "reviewer", "cleaner")
        with self.env.load_factory() as mod:
            os.environ.update(self.facts({"mem_mb": 20000, "load1": 0.1, "nproc": 16, "exit": 0}, mem="2000"))
            os.environ["FACTORY_FACTS_NOW"] = "10000"
            con = mod.db()
            for role in pinned:
                machine, reason, _ = mod.place_machine(con, role, "claude", role in ("coder", "qa"))
                self.assertEqual((role, machine, reason), (role, "hqhost", "ok"))
            for role in workers:
                machine, reason, _ = mod.place_machine(con, role, "claude", role in ("coder", "qa"))
                self.assertEqual((role, machine, reason), (role, "workerhost", "ok"))
            os.environ["FACTORY_FACTS_NOW"] = "20000"
            os.environ["FACTORY_MEM_MB"] = "1000"  # below the floor: refused on hqhost, still not workerhost
            for role in pinned:
                machine, reason, _ = mod.place_machine(con, role, "claude", False)
                self.assertNotEqual(machine, "workerhost", role)
                self.assertNotEqual(reason, "ok", role)
                self.assertIn("hqhost:", reason)
                self.assertNotIn("workerhost", reason)

    def test_machine_name_guard_refuses_factory_and_accepts_workerhost(self):
        p = self.refused(2, "config", "set", "worker_machines", "factorybox2")
        self.assertIn('refused: machine name must not contain "factory"', p.stderr)
        p = self.refused(2, "config", "set", "worker_machines", "Bad_Name")
        self.assertIn("refused: bad machine name", p.stderr)
        self.ok("config", "set", "worker_machines", "workerhost")
        self.assertEqual(self.env.sql("SELECT value FROM config WHERE key='worker_machines'")[0]["value"], "workerhost")

    def test_machine_mem_pins_ignored_on_a_live_home(self):
        """FACTORY_MEM_MB / FACTORY_MEMINFO / FACTORY_NPROC pin facts on a test home only; a live home reads /proc."""
        info = self.env.home / "meminfo-pin"
        info.write_text("MemTotal: 99999999 kB\nMemAvailable: 99999999 kB\n")
        pins = {"FACTORY_MEM_MB": "123456", "FACTORY_MEMINFO": str(info), "FACTORY_NPROC": "999"}
        with self.env.load_factory() as mod, mock.patch.dict("os.environ", pins):
            self.assertEqual(mod.host_mem_available_mb(), 123456)
            self.assertEqual(mod.mem_available_mb(), 99999999 // 1024)
            self.assertEqual(mod._host_nproc(), 999)
            with mock.patch.object(mod, "LIVE_HOMES", {str(Path(mod.HOME).resolve())}):
                live_mb = mod.host_mem_available_mb()
                self.assertNotEqual(live_mb, 123456)
                self.assertNotEqual(live_mb, 99999999 // 1024)
                self.assertEqual(mod._host_nproc(), os.cpu_count() or 1)

    def test_machine_hq_name_guard_refuses_factory_and_accepts_hqhost(self):
        p = self.refused(2, "config", "set", "machine_hq", "factorybox")
        self.assertIn('refused: machine name must not contain "factory"', p.stderr)
        p = self.refused(2, "config", "set", "machine_hq", "Bad_Name")
        self.assertIn("refused: bad machine name", p.stderr)
        self.ok("config", "set", "machine_hq", "hqhost")
        self.assertEqual(self.env.sql("SELECT value FROM config WHERE key='machine_hq'")[0]["value"], "hqhost")

    def test_roleless_machine_gate_keeps_owner_mem_and_load(self):
        self.ok("config", "min_mem_available_mb", "1024")  # the harness pins this to 1
        self.enable()
        low = self.facts({"mem_mb": 11000, "load1": 0.5, "nproc": 16, "exit": 0}, mem="300", load="14", nproc="8")
        out = self.ok("res", extra_env=low).stdout
        self.assertIn("spawn gate: CLOSED — MemAvailable 300MB < floor 1024MB", out)
        self.assertNotIn("spawn gate: OPEN", out)
        hot = self.facts({"mem_mb": 11000, "load1": 0.1, "nproc": 16, "exit": 0}, mem="8000", load="1000000", nproc="8")
        out = self.ok("res", extra_env=hot).stdout
        self.assertIn("spawn gate: CLOSED", out)
        self.assertIn("load1", out)
        self.assertNotIn("spawn gate: OPEN", out)
        cool = self.facts({"mem_mb": 11000, "load1": 0.1, "nproc": 16, "exit": 0}, mem="8000", load="0.1", nproc="8")
        self.assertIn("spawn gate: OPEN", self.ok("res", extra_env=cool).stdout)

    def test_owner_mem_floor_blocks_a_machine_floor_pass(self):
        """HQ at 2100 MB clears the claude per-machine floor (1500+500) and fails an owner floor of 3000."""
        self.ok("config", "min_mem_available_mb", "3000")
        self.enable()
        env = self.facts({"mem_mb": 11000, "load1": 0.1, "nproc": 16, "exit": 0}, mem="2100", load="0.1", nproc="8")
        p = self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", "zz-floor", extra_env=env)
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("MemAvailable 2100MB < floor 3000MB", p.stdout)
        self.assertNotIn("SPAWNED", p.stdout)
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-floor'")[0]["status"], "queued")
        with self.env.load_factory() as mod:
            os.environ.update(env)
            os.environ["FACTORY_FACTS_NOW"] = "30000"
            ok, why, stats = mod.gate(mod.db(), role="boss", runtime="claude")
        self.assertFalse(ok)
        self.assertIn("MemAvailable 2100MB < floor 3000MB", why)
        self.assertNotEqual(stats.get("machine"), "workerhost")
        self.ok("config", "min_mem_available_mb", "1024")
        p = self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", "zz-floor-ok", extra_env=env)
        self.assertIn("SPAWNED", p.stdout)
        self.assertNotIn("MemAvailable", p.stdout)

    def test_placement_owner_floor_holds_a_queued_cursor_coder(self):
        """2500 MB clears the cursor per-machine floor (1500+900) and fails an owner floor of 3000. The tick must not start it."""
        self.ok("config", "min_mem_available_mb", "3000")
        self.enable()
        name = "cursor-coder-zz"
        args = json.dumps({"role": "coder", "goal": "G-001", "team": None, "task": "T-0001", "name": name,
                           "cwd": None, "prompt": None, "model": None, "resume": None, "org": "cursor"})
        self.env.sql("INSERT INTO agents(name,role,goal_id,task_id,status,spawn_args,queued_at,org) VALUES(?,?,?,?,?,?,?,?)",
                     (name, "coder", "G-001", "T-0001", "queued", args, "2026-11-05T11:00:00", "cursor"))
        env = self.facts({"mem_mb": 11000, "load1": 0.1, "nproc": 16, "exit": 0}, mem="2500", load="0.1", nproc="8")
        env["FACTORY_NOW"] = "2026-11-05T12:00:00+00:00"
        self.ok("tick", extra_env=env)
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name=?", (name,))[0]["status"], "queued")
        starts = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"] and name in c["argv"]]
        self.assertEqual(starts, [])

    def test_machine_facts_ttl_ssh_once(self):
        self.enable()
        with self.env.load_factory() as mod:
            os.environ.update(self.facts({"mem_mb": 11000, "load1": 0.2, "nproc": 16, "exit": 0}))
            os.environ["FACTORY_FACTS_NOW"] = "10000"
            self.env.calls_file.write_text("")
            con = mod.db()
            for _ in range(10):
                mod.place_machine(con, "coder", "claude", True)
        ssh = self.env.calls("ssh")
        self.assertEqual(len(ssh), 1)
        self.assertIn("workerhost", ssh[0]["argv"])

    def test_placement_unreachable_worker_machine_uses_hq(self):
        self.enable()
        with self.env.load_factory() as mod:
            os.environ.update(self.facts({"mem_mb": 11000, "load1": 0.1, "nproc": 16, "exit": 255}, mem="8000"))
            os.environ["FACTORY_FACTS_NOW"] = "10000"
            con = mod.db()
            machine, reason, stats = mod.place_machine(con, "reviewer", "claude", False)
        self.assertEqual((machine, reason), ("hqhost", "ok"))
        self.assertFalse(stats["machines"]["workerhost"]["reachable"])

    def test_placement_relaunch_does_not_spend_its_own_share(self):
        self.ok("config", "max_live_agents", "6")
        self.enable()
        for i, role in enumerate(("boss", "director", "lead")):
            self.env.seed_agent(f"zz-hq{i}", role, goal="G-001")
        self.env.seed_agent("zz-coder", "coder", goal="G-001", pane_id="p-coder")
        with self.env.load_factory() as mod:
            os.environ.update(self.facts({"mem_mb": 11000, "load1": 0.1, "nproc": 16, "exit": 0}, mem="8000", nproc="8"))
            os.environ["FACTORY_FACTS_NOW"] = "10000"
            con = mod.db()
            ok, why, stats = mod.gate(con, role="coder", runtime="claude")
            self.assertTrue(ok, why)  # hqhost's share is full, so the new coder goes to workerhost (T-1586 starts it there)
            self.assertEqual(stats["machine"], "workerhost")
            self.assertTrue(any(f.startswith("workers") for f in stats["placement"]["machines"]["hqhost"]["fails"]))
            os.environ["FACTORY_FACTS_NOW"] = "10001"
            ok, why, stats = mod.gate(con, role="coder", runtime="claude", placement_discount="hqhost")
        self.assertTrue(ok, why)
        self.assertEqual(stats["placement"]["machines"]["hqhost"]["fails"], [])
        self.assertEqual(stats["placement"]["machines"]["hqhost"]["workers"], 0)


class DailyCapTests(FactoryTestCase):
    """T-1823 (owner M41393): max_day_pct closes the spawn gate when weekly usage rose more than that since day_start_hour."""

    DAY1 = {"FACTORY_NOW": "2026-06-10T09:00:00"}
    LATER = {"FACTORY_NOW": "2026-06-10T15:00:00"}
    NEXT = {"FACTORY_NOW": "2026-06-11T08:30:00"}

    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_task(status="todo", pr_url=None)

    def res(self, now):
        return self.ok("res", extra_env=now).stdout

    def spawn(self, now, name):
        return self.env.run("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", name, extra_env=now)

    def test_key_empty_gate_unaffected(self):
        self.assertEqual(self.ok("config", "max_day_pct").stdout.strip(), "")
        self.env.write_usage(seven_day=30)
        self.res(self.DAY1)
        self.env.write_usage(seven_day=80)
        out = self.res(self.LATER)
        self.assertIn("spawn gate: OPEN", out)
        self.assertIn("daily cap: off", out)

    def test_open_at_39_closed_at_41_with_reason(self):
        self.ok("config", "max_day_pct", "10")
        self.env.write_usage(seven_day=30)
        self.assertIn("daily cap: used 0% of max_day_pct 10% since 2026-06-10 08:00 (gate open)", self.res(self.DAY1))
        self.env.write_usage(seven_day=39)
        out = self.res(self.LATER)
        self.assertIn("spawn gate: OPEN", out)
        self.assertIn("daily cap: used 9% of max_day_pct 10% since 2026-06-10 08:00 (gate open)", out)
        self.env.write_usage(seven_day=41)
        out = self.res(self.LATER)
        self.assertIn("spawn gate: CLOSED — daily usage cap", out)
        self.assertIn("max_day_pct 10%", out)
        self.assertIn("daily cap: used 11% of max_day_pct 10% since 2026-06-10 08:00 (gate closed)", out)
        p = self.spawn(self.LATER, "zz-day-c1")
        self.assertIn("QUEUED", p.stdout)
        self.assertIn("max_day_pct", p.stdout)

    def test_rollover_at_day_start_resets_baseline(self):
        self.ok("config", "max_day_pct", "10")
        self.env.write_usage(seven_day=30)
        self.res(self.DAY1)
        self.env.write_usage(seven_day=41)
        self.assertIn("CLOSED", self.res(self.LATER))
        out = self.res({"FACTORY_NOW": "2026-06-11T07:59:00"})  # before day start: still yesterday's baseline
        self.assertIn("since 2026-06-10 08:00 (gate closed)", out)
        out = self.res(self.NEXT)  # 08:30 next day: new baseline 41
        self.assertIn("spawn gate: OPEN", out)
        self.assertIn("daily cap: used 0% of max_day_pct 10% since 2026-06-11 08:00 (gate open)", out)

    def test_baseline_recorded_when_an_earlier_check_closes_the_gate(self):
        self.ok("config", "max_day_pct", "10")
        self.ok("config", "max_live_agents", "0")  # a full box closes the gate before the daily check at day start
        self.env.write_usage(seven_day=30)
        self.assertIn("QUEUED", self.spawn(self.DAY1, "zz-day-full").stdout)  # spawn stops at the agent cap, as the tick does
        self.ok("config", "max_live_agents", "9")
        self.env.write_usage(seven_day=41)
        out = self.res(self.LATER)  # the day's usage must count from the 30 seen at day start, not from 41
        self.assertIn("daily cap: used 11% of max_day_pct 10% since 2026-06-10 08:00 (gate closed)", out)

    def test_stale_reading_never_sets_a_baseline(self):
        self.ok("config", "max_day_pct", "10")
        self.env.write_usage(seven_day=30, age_s=99999)
        out = self.res(self.DAY1)
        self.assertIn("daily cap: used n/a of max_day_pct 10%", out)
        self.assertEqual(self.env.sql("SELECT ts FROM throttle WHERE key='day-usage-baseline'"), [])

    def test_weekly_reset_lowers_baseline(self):
        self.ok("config", "max_day_pct", "10")
        self.env.write_usage(seven_day=90)
        self.res(self.DAY1)
        self.env.write_usage(seven_day=3)  # the weekly window reset
        self.assertIn("used 0% of max_day_pct", self.res(self.LATER))

    def test_agent_cannot_set_key_owner_and_director_can(self):
        self.env.seed_agent("zz-day-coder", "coder", goal="G-001", task="T-0001")
        p = self.env.run("config", "max_day_pct", "50", agent="zz-day-coder")
        self.assertNotEqual(p.returncode, 0, p.stdout)
        self.assertEqual(self.ok("config", "max_day_pct").stdout.strip(), "")
        self.ok("config", "max_day_pct", "10", agent="owner")
        self.assertEqual(self.ok("config", "max_day_pct").stdout.strip(), "10")

    def test_bad_value_refused(self):
        for bad in ("abc", "0", "-5", "150", "nan"):
            self.refused(2, "config", "max_day_pct", bad)
        self.ok("config", "max_day_pct", "")


if __name__ == "__main__":
    unittest.main()
