import json

from harness import FactoryTestCase


class ResumeTestCase(FactoryTestCase):
    INPROC = True
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        self.proc = e.home / "fakeproc"
        self.proc.mkdir()
        for name, role, sess, team in (("zz-test-director", "director", "sess-dir", None),
                                       ("zz-test-boss", "boss", "sess-boss", None),
                                       ("zz-test-lead", "lead", "sess-lead", "g-001-a")):
            e.seed_agent(name, role, goal="G-001" if team else None, team=team, status="lost")
            e.sql("UPDATE agents SET session_id=? WHERE name=?", (sess, name))

    # -- helpers ---------------------------------------------------------
    def settings(self, role):
        return str(self.env.home_dir / "run" / "settings" / f"{role}.json")

    def fake_proc(self, pid, sess, role=None, extra=()):
        """A fake /proc/<pid>/cmdline: claude resumed under `sess`, with role flags only if role is given."""
        argv = ["claude", "--resume", sess, *extra]
        if role:
            argv += ["--model", "m", "--permission-mode", "auto", "--settings", self.settings(role)]
        d = self.proc / str(pid)
        d.mkdir()
        (d / "cmdline").write_bytes(b"\0".join(x.encode() for x in argv) + b"\0")

    def frun(self, *args, **kw):
        kw.setdefault("extra_env", {})["FACTORY_PROC_DIR"] = str(self.proc)
        return self.env.run(*args, **kw)

    def okr(self, *args, **kw):
        p = self.frun(*args, **kw)
        self.assertEqual(p.returncode, 0, f"{args}: rc={p.returncode}\n{p.stdout}\n{p.stderr}")
        return p

    def set_agents(self, *pairs):
        self.env.set_herdr(agents=[{"name": n, "agent": "claude", "pane_id": f"px{i}", "agent_status": "idle",
                                    "agent_session": {"value": s}} for i, (n, s) in enumerate(pairs)])

    def starts(self):
        return {c["argv"][2]: c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]}

    def flagless_msgs(self):
        return self.env.sql("SELECT recipient, body FROM messages WHERE subject LIKE '%flagless%'")

    def status(self, name):
        return self.env.sql("SELECT status FROM agents WHERE name=?", (name,))[0][0]

    def flagless(self, name):
        r = self.env.sql("SELECT flagless, flagless_since FROM agents WHERE name=?", (name,))[0]
        return r["flagless"], r["flagless_since"]


class FlaglessTests(ResumeTestCase):
    def test_up_adopts_flagless_resumed_agent_and_flags_it(self):
        self.set_agents(("restored-1", "sess-dir"), ("restored-2", "sess-boss"), ("restored-3", "sess-lead"))
        self.fake_proc(101, "sess-dir")  # bare `claude --resume`
        self.fake_proc(102, "sess-boss", "boss")
        self.fake_proc(103, "sess-lead", "lead")
        p = self.okr("up")
        self.assertIn("zz-test-director", p.stdout)
        self.assertEqual(self.status("zz-test-director"), "live")  # adopted, still addressable
        self.assertEqual(self.flagless("zz-test-director")[0], 1)
        self.assertTrue(self.flagless("zz-test-director")[1])
        self.assertEqual(self.flagless("zz-test-boss")[0], 0)
        self.assertEqual(self.env.sql("SELECT count(*) FROM agents WHERE status='flagless'")[0][0], 0)
        self.assertEqual(self.starts(), {})  # nothing relaunched, nothing started
        calls = [c["argv"][:2] for c in self.env.calls("herdr")]
        self.assertNotIn(["pane", "close"], calls)  # nothing closed
        ev = self.env.sql("SELECT detail FROM events WHERE action='flagless'")
        self.assertEqual(len(ev), 1)
        self.assertEqual(json.loads(ev[0][0]), {"name": "zz-test-director", "pane": "px0", "pid": 101})
        msgs = self.flagless_msgs()
        self.assertEqual(len(msgs), 1)
        self.assertIn("flagless agents after restore: zz-test-director", msgs[0]["body"])
        self.assertIn("Relaunch needs owner approval", msgs[0]["body"])

    def test_up_does_not_realert_already_flagless(self):
        self.set_agents(("restored-1", "sess-dir"), ("restored-2", "sess-boss"), ("restored-3", "sess-lead"))
        self.fake_proc(101, "sess-dir")
        self.fake_proc(102, "sess-boss", "boss")
        self.fake_proc(103, "sess-lead", "lead")
        self.okr("up")
        self.assertEqual(len(self.flagless_msgs()), 1)
        self.okr("up")  # director already flagged: still adopted, not alerted again
        self.assertEqual(len(self.flagless_msgs()), 1)
        self.assertEqual(self.flagless("zz-test-director")[0], 1)
        # a newly flagged agent is still reported
        self.fake_proc(104, "sess-lead")
        self.env.sql("UPDATE agents SET flagless=0, flagless_since=NULL WHERE name='zz-test-lead'")
        import shutil
        shutil.rmtree(self.proc / "103")
        self.okr("up")
        msgs = self.flagless_msgs()
        self.assertEqual(len(msgs), 2)
        self.assertIn("zz-test-lead", msgs[1]["body"])
        self.assertNotIn("zz-test-director", msgs[1]["body"])

    def test_up_adopts_resumed_agent_with_flags(self):
        self.set_agents(("restored-1", "sess-dir"), ("restored-2", "sess-boss"), ("restored-3", "sess-lead"))
        self.fake_proc(101, "sess-dir", "director")
        self.fake_proc(102, "sess-boss", "boss")
        self.fake_proc(103, "sess-lead", "lead")
        self.okr("up")
        self.assertEqual(self.starts(), {})
        for n in ("zz-test-director", "zz-test-boss", "zz-test-lead"):
            self.assertEqual(self.status(n), "live")
        self.assertEqual(self.flagless_msgs(), [])
        self.assertEqual(self.env.sql("SELECT count(*) FROM events WHERE action='flagless'")[0][0], 0)

    def test_flagless_detected_by_tick_throttled(self):
        for n, s in (("zz-test-director", "sess-dir"),):
            self.env.sql("UPDATE agents SET status='live', pane_id='px0' WHERE name=?", (n,))
        self.set_agents(("zz-test-director", "sess-dir"))
        self.fake_proc(101, "sess-dir")
        self.okr("tick")
        self.assertEqual(self.status("zz-test-director"), "live")
        self.assertEqual(self.flagless("zz-test-director")[0], 1)
        self.assertEqual(len(self.flagless_msgs()), 1)
        # within the hour the agent is not checked again, even if its flag is reset
        self.env.sql("UPDATE agents SET flagless=0, flagless_since=NULL WHERE name='zz-test-director'")
        self.okr("tick")
        self.assertEqual(self.flagless("zz-test-director")[0], 0)
        self.assertEqual(len(self.flagless_msgs()), 1)
        # after the throttle window it is detected again
        self.env.sql("DELETE FROM throttle WHERE key='flagless_check:zz-test-director'")
        self.okr("tick")
        self.assertEqual(self.flagless("zz-test-director")[0], 1)
        self.assertEqual(self.env.sql("SELECT count(*) FROM events WHERE action='flagless'")[0][0], 2)
        # a relaunched, flagged process clears the mark
        (self.proc / "101" / "cmdline").unlink()
        self.fake_proc(102, "sess-dir", "director")
        self.env.sql("DELETE FROM throttle WHERE key='flagless_check:zz-test-director'")
        self.okr("tick")
        self.assertEqual(self.flagless("zz-test-director"), (0, None))
        self.assertEqual(self.status("zz-test-director"), "live")

    def test_throttle_keys_stay_out_of_config_and_state_dump(self):
        self.env.sql("UPDATE agents SET status='live', pane_id='px0' WHERE name='zz-test-director'")
        self.set_agents(("zz-test-director", "sess-dir"))
        self.fake_proc(101, "sess-dir")
        self.okr("tick")
        self.assertEqual(self.env.sql("SELECT count(*) FROM throttle WHERE key LIKE 'flagless_check%'")[0][0], 1)
        self.assertNotIn("flagless_check", self.okr("config").stdout)
        self.assertEqual(self.env.sql("SELECT count(*) FROM config WHERE key LIKE 'flagless_check%'")[0][0], 0)
        self.okr("export", "--no-push")
        sql = (self.env.home_dir / "state" / "factory.sql").read_text()
        self.assertNotIn("flagless_check", sql)


class FlaglessStillLiveTests(ResumeTestCase):
    """A flagless agent (bare `claude --resume`, no FACTORY_AGENT, pane-id identity) must stay fully functional."""
    PANE = "px0"

    def setUp(self):
        super().setUp()
        self.env.sql("UPDATE agents SET status='live', pane_id=?, started='2026-01-01T00:00:00+00:00' "
                     "WHERE name='zz-test-director'", (self.PANE,))
        self.set_agents(("zz-test-director", "sess-dir"))
        self.fake_proc(101, "sess-dir")  # bare `claude --resume sess-dir`

    def pane_env(self):
        return {"HERDR_PANE_ID": self.PANE}

    def detect(self):
        self.okr("tick")
        self.assertEqual(self.flagless("zz-test-director")[0], 1)
        self.assertEqual(self.status("zz-test-director"), "live")

    def prompts(self):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "prompt"]]

    def test_flagless_director_still_nudged(self):
        self.okr("msg", "director", "work", "do it")  # owner -> director
        self.detect()
        nudges = [a for a in self.prompts() if a[2] == "zz-test-director" and "📬" in " ".join(a)]
        self.assertEqual(len(nudges), 1, self.prompts())

    def test_flagless_identity_recovered_from_pane(self):
        self.okr("msg", "director", "work", "do it")
        self.detect()
        p = self.okr("whoami", extra_env=self.pane_env())
        self.assertIn("zz-test-director", p.stdout)
        self.assertIn("role=director", p.stdout)
        p = self.okr("inbox", extra_env=self.pane_env())
        self.assertIn("work", p.stdout)

    def test_flagless_counted_by_gate(self):
        self.okr("config", "max_live_agents", "1")
        self.detect()
        self.assertIn("CLOSED — 1/1", self.okr("res").stdout)

    def test_flagless_duplicate_spawn_refused(self):
        self.detect()
        p = self.frun("spawn", "director", "--name", "zz-test-director")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("already live", p.stderr)
        self.assertEqual(self.starts(), {})

    def flagless_pm(self):
        self.env.seed_agent("zz-test-pm", "pm", goal="G-001", team="g-001-a", status="lost")
        self.env.sql("UPDATE agents SET flagless=1, flagless_since='2026-10-23T10:00:00+00:00' WHERE name='zz-test-pm'")

    def spawn_pm(self):
        return self.frun("spawn", "pm", "--name", "zz-test-pm", "--goal", "G-001", "--team", "g-001-a",
                         agent="zz-test-director")

    def test_fresh_respawn_clears_old_flagless(self):
        self.flagless_pm()
        p = self.spawn_pm()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("--session-id", self.starts()["zz-test-pm"])  # a fresh spawn, not a resume
        self.assertEqual(self.flagless("zz-test-pm"), (0, None))
        ev = self.env.sql("SELECT detail FROM events WHERE action='flagless-cleared' AND entity_id='zz-test-pm'")
        self.assertEqual([json.loads(e["detail"]) for e in ev], [{"via": "spawn"}])

    def test_flagless_kept_when_start_fails(self):
        self.flagless_pm()
        self.env.set_herdr(start_fail=True)
        p = self.spawn_pm()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.flagless("zz-test-pm"), (1, "2026-10-23T10:00:00+00:00"))
        self.assertEqual(self.env.sql("SELECT count(*) FROM events WHERE action='flagless-cleared'")[0][0], 0)
        self.assertEqual(self.env.sql("SELECT count(*) FROM events WHERE action='start-uncertain'")[0][0], 1)

    def test_flagless_listed_in_agents_with_marker(self):
        self.detect()
        out = self.okr("agents").stdout
        line = next(l for l in out.splitlines() if "zz-test-director" in l)
        self.assertIn("FLAGLESS", line)
        self.assertIn("live", line)
        self.assertEqual(self.env.sql("SELECT count(*) FROM agents WHERE status='flagless'")[0][0], 0)
        # the digest lists it under Blocked
        d = self.okr("digest").stdout
        self.assertIn("agent zz-test-director running without role flags since", d)

    def test_flagless_cleared_when_flags_present(self):
        self.detect()
        (self.proc / "101" / "cmdline").unlink()
        self.fake_proc(102, "sess-dir", "director")
        self.env.sql("DELETE FROM throttle")
        self.okr("tick")
        self.assertEqual(self.flagless("zz-test-director"), (0, None))
        line = next(l for l in self.okr("agents").stdout.splitlines() if "zz-test-director" in l)
        self.assertNotIn("FLAGLESS", line)

    def test_done_leaves_flagless_column(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", task="T-0001")
        self.env.sql("UPDATE agents SET flagless=1, flagless_since='t', session_id='s-c' WHERE name='zz-test-coder'")
        self.okr("done", "ok", agent="zz-test-coder")
        r = self.env.sql("SELECT status, flagless FROM agents WHERE name='zz-test-coder'")[0]
        self.assertEqual((r["status"], r["flagless"]), ("done", 1))

    def test_migration_adds_flagless_columns_idempotent(self):
        con = self.env.db()
        con.execute("ALTER TABLE agents DROP COLUMN flagless")
        con.execute("ALTER TABLE agents DROP COLUMN flagless_since")
        con.execute("DROP TABLE throttle")
        con.commit()
        con.close()
        cols = lambda: {r["name"] for r in self.env.sql("PRAGMA table_info(agents)")}  # noqa: E731
        self.assertNotIn("flagless", cols())
        before = [tuple(r) for r in self.env.sql("SELECT name, status FROM agents ORDER BY name")]
        for _ in range(2):  # first run upgrades, second changes nothing
            self.okr("agents")
            self.assertLessEqual({"flagless", "flagless_since"}, cols())
        self.assertEqual(before, [tuple(r) for r in self.env.sql("SELECT name, status FROM agents ORDER BY name")])
        self.assertEqual(self.env.sql("SELECT flagless FROM agents WHERE name='zz-test-director'")[0][0], 0)
        self.assertEqual(self.env.sql("SELECT count(*) FROM throttle")[0][0], 0)  # table recreated


class RestoredPaneTests(ResumeTestCase):
    def test_up_relaunches_in_restored_shell_pane_with_full_flags(self):
        # resume disabled: herdr restored the layout with plain shells and lists no agents
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "x"}],
            tabs=[{"tab_id": "t1", "workspace_id": "w1", "label": "hq", "pane_count": 3}],
            panes=[{"pane_id": f"p{i}", "tab_id": "t1", "workspace_id": "w1"} for i in (1, 2, 3)], seq=10)
        for n, pane in (("zz-test-director", "p1"), ("zz-test-boss", "p2"), ("zz-test-lead", "p3")):
            self.env.sql("UPDATE agents SET pane_id=? WHERE name=?", (pane, n))
        self.okr("up")
        starts = self.starts()
        self.assertEqual(set(starts), {"zz-test-director", "zz-test-boss", "zz-test-lead"})
        for name, pane, sess, role in (("zz-test-director", "p1", "sess-dir", "director"),
                                       ("zz-test-boss", "p2", "sess-boss", "boss"),
                                       ("zz-test-lead", "p3", "sess-lead", "lead")):
            argv = starts[name]
            self.assertEqual(argv[argv.index("--pane") + 1], pane)  # its own restored pane, no new one
            self.assertEqual(argv[argv.index("--resume") + 1], sess)
            self.assertEqual(argv[argv.index("--settings") + 1], self.settings(role))
            self.assertIn("--model", argv)
            self.assertIn("--append-system-prompt-file", argv)
            self.assertEqual(argv[argv.index("--permission-mode") + 1], "auto")
        made = [c["argv"][:2] for c in self.env.calls("herdr") if c["argv"][:2] in (["tab", "create"], ["pane", "split"])]
        self.assertEqual(made, [])

    def test_pane_with_parent_shell_but_claude_foreground_is_not_a_shell(self):
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "x"}],
            tabs=[{"tab_id": "t1", "workspace_id": "w1", "label": "director", "pane_count": 1}],
            panes=[{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1", "fg": "claude", "parent": "zsh"}], seq=10)
        self.env.sql("UPDATE agents SET pane_id='p1' WHERE name='zz-test-director'")
        self.okr("up")
        argv = self.starts()["zz-test-director"]
        self.assertNotEqual(argv[argv.index("--pane") + 1], "p1")  # never typed into a running TUI
        self.assertNotIn(["pane", "run", "p1"], [c["argv"][:3] for c in self.env.calls("herdr")])

    def test_up_places_new_pane_when_restored_pane_is_not_a_shell(self):
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "x"}],
            tabs=[{"tab_id": "t1", "workspace_id": "w1", "label": "director", "pane_count": 1}],
            panes=[{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1", "fg": "vim"}], seq=10)
        self.env.sql("UPDATE agents SET pane_id='p1' WHERE name='zz-test-director'")
        self.okr("up")
        argv = self.starts()["zz-test-director"]
        self.assertNotEqual(argv[argv.index("--pane") + 1], "p1")


class RelaunchTests(ResumeTestCase):
    def setUp(self):
        super().setUp()
        self.env.sql("UPDATE agents SET status='live', pane_id='px2' WHERE name='zz-test-lead'")
        self.set_agents(("zz-test-director", "sess-dir"), ("zz-test-boss", "sess-boss"), ("zz-test-lead", "sess-lead"))
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "x"}],
            tabs=[{"tab_id": "t1", "workspace_id": "w1", "label": "hq", "pane_count": 1}],
            panes=[{"pane_id": "px2", "tab_id": "t1", "workspace_id": "w1"}])

    def test_relaunch_dry_run_prints_plan_and_changes_nothing(self):
        before = self.env.sql("SELECT * FROM agents ORDER BY name")
        before = [tuple(r) for r in before]
        n_events = self.env.sql("SELECT count(*) FROM events")[0][0]
        p = self.okr("relaunch", "zz-test-lead", "--dry-run", agent="zz-test-director")
        self.assertIn("herdr pane close px2", p.stdout)
        self.assertIn("--resume sess-lead", p.stdout)
        self.assertIn("--settings", p.stdout)
        self.assertIn("dry run", p.stdout)
        self.assertEqual([tuple(r) for r in self.env.sql("SELECT * FROM agents ORDER BY name")], before)
        self.assertEqual(self.env.sql("SELECT count(*) FROM events")[0][0], n_events)
        mutating = [c["argv"][:2] for c in self.env.calls("herdr")
                    if c["argv"][:2] in (["pane", "close"], ["agent", "start"], ["pane", "run"])]
        self.assertEqual(mutating, [])

    def test_relaunch_refuses_non_factory_pane(self):
        p = self.refused(3, "relaunch", "stranger", "--dry-run", agent="zz-test-director")
        self.assertIn("not a factory agent", p.stderr)
        # the factory row's pane now hosts a different (non-factory) session
        self.env.set_herdr(agents=[{"name": "owner-claude", "agent": "claude", "pane_id": "px2",
                                    "agent_status": "idle", "agent_session": {"value": "owner-sess"}}])
        p = self.refused(3, "relaunch", "zz-test-lead", agent="zz-test-director")
        self.assertIn("refusing to touch", p.stderr)
        mutating = [c["argv"][:2] for c in self.env.calls("herdr") if c["argv"][:2] in (["pane", "close"], ["agent", "start"])]
        self.assertEqual(mutating, [])

    def test_relaunch_refuses_when_session_pane_unknown(self):
        # a live process runs the session, but herdr lists no agent for it
        self.fake_proc(101, "sess-lead")
        self.env.set_herdr(agents=[])
        p = self.frun("relaunch", "zz-test-lead", agent="zz-test-director")
        self.assertEqual(p.returncode, 3, p.stdout + p.stderr)
        self.assertIn("second copy", p.stderr)
        mutating = [c["argv"][:2] for c in self.env.calls("herdr") if c["argv"][:2] in (["pane", "close"], ["agent", "start"])]
        self.assertEqual(mutating, [])

    def test_relaunch_finds_host_by_session_when_row_pane_is_stale(self):
        self.env.sql("UPDATE agents SET pane_id='pstale' WHERE name='zz-test-lead'")
        self.okr("relaunch", "zz-test-lead", agent="zz-test-director")
        calls = [c["argv"] for c in self.env.calls("herdr")]
        self.assertIn(["pane", "close", "px2"], calls)  # the pane that really hosts the session
        self.assertNotIn(["pane", "close", "pstale"], calls)

    def test_relaunch_refused_for_workers(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a")
        self.refused(3, "relaunch", "zz-test-lead", "--dry-run", agent="zz-test-coder")

    def test_relaunch_closes_pane_and_resumes_with_full_flags(self):
        self.okr("relaunch", "zz-test-lead", agent="zz-test-director")
        calls = [c["argv"] for c in self.env.calls("herdr")]
        self.assertIn(["pane", "close", "px2"], calls)
        argv = self.starts()["zz-test-lead"]
        self.assertEqual(argv[argv.index("--resume") + 1], "sess-lead")
        self.assertEqual(argv[argv.index("--settings") + 1], self.settings("lead"))
        self.assertNotEqual(argv[argv.index("--pane") + 1], "px2")
