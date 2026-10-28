"""G-006 A4: `factory relaunch --when-idle` (run by the tick), role permissions, mid-turn refusal, the
--system-prompt-snapshot off flag on every resume, and the prompt file of a live agent never being rewritten."""
import re
import shutil
import unittest

from harness import FactoryTestCase, FACTORY

LEAD = "zz-lead-x"
SESS = "sess-zz-lead-x"  # the herdr stub reports "sess-<name>" for a started agent, so a relaunch keeps the id
PANE = "px9"


class RelaunchIdleCase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead=LEAD)
        for name, role, sess in (("zz-director", "director", "sess-dir"), ("zz-boss", "boss", "sess-boss")):
            e.seed_agent(name, role, pane_id=f"p-{role}")
            e.sql("UPDATE agents SET session_id=? WHERE name=?", (sess, name))
        e.seed_agent(LEAD, "lead", goal="G-001", team="g-001-a", pane_id=PANE)
        e.sql("UPDATE agents SET session_id=? WHERE name=?", (SESS, LEAD))
        self.herdr_agents(("idle", LEAD, SESS, PANE))
        e.set_herdr(workspaces=[{"workspace_id": "w1", "label": "x"}],
                    tabs=[{"tab_id": "t1", "workspace_id": "w1", "label": "hq", "pane_count": 1}],
                    panes=[{"pane_id": PANE, "tab_id": "t1", "workspace_id": "w1"}], seq=10)
        # the harness symlinks roles/ to the repo: copy it so editing a role file never writes into the repo
        link = e.home_dir / "roles"
        src = link.resolve()
        link.unlink()
        shutil.copytree(src, link)

    def herdr_agents(self, *rows):
        self.env.set_herdr(agents=[{"name": n, "agent": "claude", "pane_id": p, "agent_status": st,
                                    "agent_session": {"value": s}} for st, n, s, p in rows])

    def set_status(self, st):
        self.herdr_agents((st, LEAD, SESS, PANE))

    def starts(self):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]

    def closes(self):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["pane", "close"]]

    def flag(self, name=LEAD):
        r = self.env.sql("SELECT relaunch_requested_at, relaunch_requested_by FROM agents WHERE name=?", (name,))[0]
        return r[0], r[1]

    def events(self, action):
        return self.env.sql("SELECT detail FROM events WHERE action=? AND entity_id=?", (action, LEAD))

    def queue(self, who="zz-director"):
        return self.ok("relaunch", LEAD, "--when-idle", agent=who)

    def edit_lead_role(self):
        with open(self.env.home_dir / "roles" / "lead.md", "a") as f:
            f.write("\nZZ-NEW-RULE\n")


class WhenIdleTickTests(RelaunchIdleCase):
    def test_request_flags_row_and_tick_relaunches_with_resume_and_new_prompt(self):
        self.edit_lead_role()
        p = self.queue()
        self.assertIn(f"relaunch of {LEAD} queued: the tick runs it when {LEAD} is idle", p.stdout)
        at, by = self.flag()
        self.assertTrue(at)
        self.assertEqual(by, "zz-director")
        self.assertEqual(len(self.events("relaunch-requested")), 1)
        self.assertEqual(self.starts(), [])  # nothing happens before the tick
        self.ok("tick")
        self.assertEqual(self.closes(), [["pane", "close", PANE]])
        starts = self.starts()
        self.assertEqual(len(starts), 1)
        argv = starts[0]
        self.assertEqual(argv[2], LEAD)
        self.assertEqual(argv[argv.index("--resume") + 1], SESS)
        self.assertIn("--append-system-prompt-file", argv)
        calls = [c["argv"][:2] for c in self.env.calls("herdr") if c["argv"][:2] in (["pane", "close"], ["agent", "start"])]
        self.assertEqual(calls, [["pane", "close"], ["agent", "start"]])  # close first, then start
        prompt = (self.env.home_dir / "run" / f"{LEAD}.prompt.md").read_text()
        self.assertIn("ZZ-NEW-RULE", prompt)
        self.assertEqual(self.env.sql("SELECT session_id FROM agents WHERE name=?", (LEAD,))[0][0], SESS)
        self.assertEqual(self.flag(), (None, None))
        self.assertEqual(len(self.events("relaunch-run")), 1)
        self.ok("tick")  # the flag is gone: nothing starts again
        self.assertEqual(len(self.starts()), 1)
        self.assertEqual(len(self.closes()), 1)

    def test_working_agent_waits_then_relaunches_when_idle(self):
        self.queue()
        self.set_status("working")
        self.ok("tick")
        self.assertEqual((self.starts(), self.closes()), ([], []))
        self.assertTrue(self.flag()[0])
        self.set_status("blocked")
        self.ok("tick")
        self.assertEqual(self.closes(), [])
        self.set_status("idle")
        self.ok("tick")
        self.assertEqual(len(self.starts()), 1)
        self.assertEqual(self.flag(), (None, None))

    def test_memory_floor_defers_without_closing_the_pane(self):
        self.queue()
        self.ok("config", "min_mem_available_mb", "100000000")
        self.ok("tick")
        self.assertEqual(self.closes(), [])
        self.assertEqual(self.starts(), [])
        self.assertTrue(self.flag()[0])
        self.assertEqual(len(self.events("relaunch-deferred")), 1)
        self.ok("tick")  # throttled: at most one event an hour
        self.assertEqual(len(self.events("relaunch-deferred")), 1)
        self.ok("config", "min_mem_available_mb", "1")
        self.ok("tick")
        self.assertEqual(len(self.closes()), 1)
        self.assertEqual(len(self.starts()), 1)
        self.assertEqual(self.flag(), (None, None))

    def test_usage_limit_defers_too(self):
        self.queue()
        self.env.write_usage(five_hour=99, seven_day=0)
        self.ok("tick")
        self.assertEqual(self.closes(), [])
        self.assertEqual(len(self.events("relaunch-deferred")), 1)

    def test_agent_holding_the_last_slot_still_relaunches(self):
        self.ok("config", "max_live_agents", "1")  # only the lead is listed: it holds the slot itself
        self.queue()
        self.ok("tick")
        self.assertEqual(len(self.starts()), 1)

    def test_over_cap_without_the_agent_defers_without_closing_the_pane(self):
        # lead + director listed, cap 1: without the lead's own slot the factory is still at the cap, so
        # spawn()'s gate would queue the restart after the close; the tick must defer before closing
        self.herdr_agents(("idle", LEAD, SESS, PANE), ("working", "zz-director", "sess-dir", "p-director"))
        self.ok("config", "max_live_agents", "1")
        self.queue()
        self.ok("tick")
        self.assertEqual(self.closes(), [])
        self.assertEqual(self.starts(), [])
        self.assertTrue(self.flag()[0])
        self.assertEqual(len(self.events("relaunch-deferred")), 1)
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name=?", (LEAD,))[0][0], "live")
        self.ok("config", "max_live_agents", "2")
        self.ok("tick")
        self.assertEqual(len(self.closes()), 1)
        self.assertEqual(len(self.starts()), 1)

    def test_failure_in_tick_records_event_one_alert_and_exit_0(self):
        self.queue()
        self.env.set_herdr(pane_close_fail=True)
        p = self.ok("tick")
        self.assertEqual(len(self.events("relaunch-failed")), 1)
        alerts = self.env.sql("SELECT recipient, sender, kind FROM messages WHERE subject LIKE 'relaunch of %failed'")
        self.assertEqual([tuple(r) for r in alerts], [("zz-director", "tick", "alert")])
        self.assertEqual(self.flag(), (None, None))  # cleared first: no retry loop
        self.ok("tick")
        self.assertEqual(len(self.env.sql("SELECT 1 FROM messages WHERE subject LIKE 'relaunch of %failed'")), 1)
        self.assertEqual(p.returncode, 0)

    def test_repeat_request_refreshes_timestamp(self):
        self.queue()
        self.env.sql("UPDATE agents SET relaunch_requested_at='2020-01-01T00:00:00+00:00' WHERE name=?", (LEAD,))
        self.queue()
        self.assertNotEqual(self.flag()[0], "2020-01-01T00:00:00+00:00")

    def test_dry_run_when_idle_changes_nothing(self):
        p = self.ok("relaunch", LEAD, "--when-idle", "--dry-run", agent="zz-director")
        self.assertIn("dry run", p.stdout)
        self.assertEqual(self.flag(), (None, None))
        self.assertEqual(self.events("relaunch-requested"), [])

    def test_agents_shows_relaunch_pending(self):
        self.assertNotIn("RELAUNCH-PENDING", self.ok("agents").stdout)
        self.queue()
        row = [l for l in self.ok("agents").stdout.splitlines() if l.startswith(LEAD)]
        self.assertIn("RELAUNCH-PENDING", row[0])


class PermissionTests(RelaunchIdleCase):
    def test_director_self_relaunch_needs_when_idle(self):
        p = self.refused(3, "relaunch", "zz-director", agent="zz-director")
        self.assertIn("use --when-idle", p.stderr)
        self.assertEqual(self.closes(), [])
        self.ok("relaunch", "zz-director", "--when-idle", agent="zz-director")
        self.assertTrue(self.flag("zz-director")[0])

    def test_boss_may_relaunch_only_the_director(self):
        self.ok("relaunch", "zz-director", "--when-idle", agent="zz-boss")
        self.assertEqual(self.flag("zz-director")[1], "zz-boss")
        self.refused(3, "relaunch", LEAD, "--when-idle", agent="zz-boss")
        self.refused(3, "relaunch", "zz-boss", "--when-idle", agent="zz-boss")
        self.assertEqual(self.flag(), (None, None))

    def test_workers_and_leads_refused(self):
        for role in ("pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "lead"):
            n = f"zz-{role}"
            self.env.seed_agent(n, role, goal="G-001", team="g-001-a")
            self.refused(3, "relaunch", LEAD, "--when-idle", agent=n)
            self.refused(3, "relaunch", LEAD, "--dry-run", agent=n)
        self.assertEqual(self.flag(), (None, None))

    def test_owner_may_relaunch_any_agent_when_idle(self):
        self.ok("relaunch", LEAD, "--when-idle")
        self.assertEqual(self.flag()[1], "owner")


class ImmediateRelaunchTests(RelaunchIdleCase):
    def test_working_agent_refused_with_exit_2_unless_force(self):
        self.set_status("working")
        p = self.refused(2, "relaunch", LEAD, agent="zz-director")
        self.assertIn("mid-turn", p.stderr)
        self.assertEqual((self.closes(), self.starts()), ([], []))
        self.ok("relaunch", LEAD, "--force", agent="zz-director")
        self.assertEqual(len(self.closes()), 1)
        self.assertEqual(len(self.starts()), 1)


class SnapshotFlagTests(RelaunchIdleCase):
    def assert_resume_argv(self, argv):
        i = argv.index("--system-prompt-snapshot")
        self.assertEqual(argv[i + 1], "off")
        self.assertEqual(argv[argv.index("--resume") + 1], SESS)
        self.assertIn("--settings", argv)
        self.assertIn("--append-system-prompt-file", argv)

    def test_tick_path_passes_snapshot_off(self):
        self.queue()
        self.ok("tick")
        self.assert_resume_argv(self.starts()[0])

    def test_immediate_relaunch_passes_snapshot_off(self):
        self.ok("relaunch", LEAD, agent="zz-director")
        self.assert_resume_argv(self.starts()[0])

    def test_dry_run_prints_the_flag(self):
        p = self.ok("relaunch", LEAD, "--dry-run", agent="zz-director")
        self.assertIn("--system-prompt-snapshot off", p.stdout)
        self.assertEqual(self.starts(), [])

    def test_fresh_spawn_has_no_snapshot_flag(self):
        self.ok("spawn", "lead", "--goal", "G-001", "--team", "g-001-a", "--name", "zz-fresh", agent="zz-director")
        argv = next(a for a in self.starts() if a[2] == "zz-fresh")
        self.assertNotIn("--system-prompt-snapshot", argv)
        self.assertNotIn("--resume", argv)
        self.assertIn("--session-id", argv)

    def test_spawn_with_resume_passes_it_whatever_the_caller(self):
        with self.env.load_factory() as f:
            con = f.db()
            f.spawn(con, "lead", "G-001", "g-001-a", name="zz-other", resume="sess-other")
            f.spawn(con, "lead", "G-001", "g-001-a", name="zz-other2", resume="sess-other2", from_queue=True)
        for n, s in (("zz-other", "sess-other"), ("zz-other2", "sess-other2")):
            argv = next(a for a in self.starts() if a[2] == n)
            self.assertEqual(argv[argv.index("--system-prompt-snapshot") + 1], "off")
            self.assertEqual(argv[argv.index("--resume") + 1], s)

    def test_up_restart_passes_it(self):
        # `up` restarts a director whose row is stale: it goes through spawn(resume=...)
        self.env.set_herdr(agents=[])
        self.env.sql("UPDATE agents SET status='lost', pane_id=NULL WHERE name='zz-director'")
        self.ok("up", "--no-boss")
        argv = next(a for a in self.starts() if a[2] == "zz-director")
        self.assertEqual(argv[argv.index("--system-prompt-snapshot") + 1], "off")

    def test_snapshot_flag_lives_in_one_constant(self):
        src = FACTORY.read_text()
        self.assertEqual(len(re.findall(r'"--system-prompt-snapshot"', src)), 1)


class PromptFileTests(RelaunchIdleCase):
    def prompt(self):
        return self.env.home_dir / "run" / f"{LEAD}.prompt.md"

    def test_live_agents_prompt_file_is_not_rewritten_by_other_spawns(self):
        self.prompt().write_text("SENTINEL live prompt\n")
        before = self.prompt().read_bytes()
        p = self.env.run("spawn", "lead", "--goal", "G-001", "--team", "g-001-a", "--name", LEAD, agent="zz-director")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("already live", p.stderr)
        self.assertEqual(self.prompt().read_bytes(), before)
        with self.env.load_factory() as f:
            con = f.db()
            for kw in ({}, {"from_queue": True}):
                with self.assertRaises(SystemExit):
                    f.spawn(con, "lead", "G-001", "g-001-a", name=LEAD, resume=SESS, **kw)
        self.assertEqual(self.prompt().read_bytes(), before)
        self.assertEqual(self.starts(), [])

    def test_own_relaunch_rewrites_it_after_the_pane_is_closed(self):
        self.prompt().write_text("SENTINEL live prompt\n")
        self.ok("relaunch", LEAD, agent="zz-director")
        self.assertNotIn("SENTINEL", self.prompt().read_text())
        self.assertEqual(len(self.closes()), 1)

    def test_a_process_still_running_the_session_blocks_the_rewrite(self):
        self.prompt().write_text("SENTINEL live prompt\n")
        proc = self.env.home / "fakeproc-relaunch"
        proc.mkdir()
        # herdr lists the agent and closes its pane, but a claude process on the session survives
        self.env.fake_claude(4242, argv=("claude", "--resume", SESS), proc_dir=proc)
        p = self.env.run("relaunch", LEAD, agent="zz-director", extra_env={"FACTORY_PROC_DIR": str(proc)})
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("still runs session", p.stderr)
        self.assertEqual(self.prompt().read_text(), "SENTINEL live prompt\n")
        self.assertEqual(self.starts(), [])

    def test_nothing_deletes_prompt_files(self):
        for line in FACTORY.read_text().splitlines():
            if "prompt.md" in line:
                self.assertNotRegex(line, r"unlink|rmtree|os\.remove|rm -")


class MigrationTests(RelaunchIdleCase):
    def cols(self):
        return {r[1] for r in self.env.sql("PRAGMA table_info(agents)")}

    def test_columns_added_on_first_command(self):
        for c in ("relaunch_requested_at", "relaunch_requested_by"):
            self.env.sql(f"ALTER TABLE agents DROP COLUMN {c}")
        self.assertFalse({"relaunch_requested_at", "relaunch_requested_by"} & self.cols())
        self.ok("agents")
        self.assertLessEqual({"relaunch_requested_at", "relaunch_requested_by"}, self.cols())
        self.ok("agents")  # idempotent
        self.assertEqual(len(self.env.sql("SELECT 1 FROM agents WHERE name=?", (LEAD,))), 1)


if __name__ == "__main__":
    unittest.main()
