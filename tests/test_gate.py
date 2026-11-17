import json
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

    def test_usage_info_unreadable_shown(self):
        self.write_raw('{"five_hour_pct": 4')
        out = self.ok("res").stdout
        self.assertIn("claude usage unreadable", out)
        self.assertNotIn("0%", "\n".join(l for l in out.splitlines() if "claude usage" in l))  # not the tmp line's percentages
        with self.env.load_factory() as mod:
            self.assertEqual(mod.usage(), {"five_hour": None, "seven_day": None, "fresh": False, "age_min": -1,
                                           "unreadable": True, "five_hour_resets_at": None,
                                           "seven_day_resets_at": None})

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
            self.assertEqual(mod.usage(), {"five_hour": 0.0, "seven_day": 0.0, "fresh": False, "age_min": -1,
                                           "five_hour_resets_at": None, "seven_day_resets_at": None})

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


if __name__ == "__main__":
    unittest.main()
if __name__ == "__main__":
    unittest.main()
