import json
import time
import unittest
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


if __name__ == "__main__":
    unittest.main()
