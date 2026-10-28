"""T-0179 (G-014): `factory relaunch --fresh` (new session, no --resume, re-orient kickoff) and the tick's context
recycling of director/lead sessions over config recycle_context_tokens."""
import json
import unittest

from harness import FactoryTestCase  # noqa: F401 — the base case is reused below
from test_relaunch_idle import RelaunchIdleCase, LEAD, SESS, PANE

NOW1 = "2026-03-01T10:00:00+00:00"
NOW2 = "2026-03-01T14:00:00+00:00"  # +4 h: inside the 6 h window
NOW3 = "2026-03-01T17:00:01+00:00"  # +7 h: past it


class FreshBase(RelaunchIdleCase):
    def setUp(self):
        super().setUp()
        self.env.sql("UPDATE goals SET intent=?, success_criteria=? WHERE id='G-001'", ("zz intent", "zz criteria"))
        self.env.seed_task("T-0001", status="in_progress", pr_url="https://github.com/zz-test/repo/pull/7")
        self.env.seed_task("T-0002", status="done")
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,created,read_at) "
                     "VALUES('zz-director',?,'decision','zz-decision-subject','zz-decision-body','G-001','2026-01-01T00:00:00','x')",
                     (LEAD,))

    def transcript(self, session, tokens):
        d = self.env.user_home / ".claude" / "projects" / "-zz-proj"
        d.mkdir(parents=True, exist_ok=True)
        lines = [{"type": "assistant", "message": {"id": "m0", "usage": {"input_tokens": 5, "cache_read_input_tokens": 1}}},
                 {"type": "assistant", "message": {"id": "m1", "usage": {
                     "input_tokens": 10, "cache_read_input_tokens": tokens - 30, "cache_creation_input_tokens": 20}}}]
        (d / f"{session}.jsonl").write_text("\n".join(json.dumps(x) for x in lines) + "\n")

    def sid(self, name=LEAD):
        return self.env.sql("SELECT session_id FROM agents WHERE name=?", (name,))[0][0]

    def tick_at(self, now):
        return self.ok("tick", extra_env={"FACTORY_NOW": now})

    def recycled(self):
        return self.events("recycle-queued")


class FreshRelaunchTests(FreshBase):
    def test_fresh_argv_has_no_resume_and_the_same_role_flags_as_plain(self):
        self.ok("relaunch", LEAD, agent="zz-director")
        plain = self.starts()[0]
        self.env.calls_file.write_text("")
        self.herdr_agents(("idle", LEAD, SESS, PANE))
        self.ok("relaunch", LEAD, "--fresh", agent="zz-director")
        fresh = self.starts()[0]
        self.assertIn("--resume", plain)
        self.assertNotIn("--resume", fresh)
        self.assertIn("--session-id", fresh)
        for flag in ("--model", "--permission-mode", "--settings", "--append-system-prompt-file", "--system-prompt-snapshot",
                     "--add-dir", "-n", "--effort", "--plugin-dir"):
            if flag in plain:
                self.assertIn(flag, fresh, flag)
                self.assertEqual(plain[plain.index(flag) + 1], fresh[fresh.index(flag) + 1], flag)

    def test_new_session_id_stored_and_event_written(self):
        self.ok("relaunch", LEAD, "--fresh", agent="zz-director")
        argv = self.starts()[0]
        new = argv[argv.index("--session-id") + 1]
        self.assertNotEqual(new, SESS)
        self.assertEqual(self.sid(), new)
        ev = [json.loads(d[0]) for d in self.events("relaunched-fresh")]
        self.assertEqual(ev, [{"old_sid": SESS, "new_sid": new}])

    def test_dry_run_changes_nothing(self):
        p = self.ok("relaunch", LEAD, "--fresh", "--dry-run", agent="zz-director")
        self.assertIn("--session-id", p.stdout)
        self.assertNotIn(f"--resume {SESS}", p.stdout)
        self.assertEqual((self.starts(), self.closes()), ([], []))
        self.assertEqual(self.sid(), SESS)
        self.assertEqual(self.events("relaunched-fresh"), [])
        p = self.ok("relaunch", LEAD, "--fresh", "--when-idle", "--dry-run", agent="zz-director")
        self.assertIn("fresh session", p.stdout)
        self.assertEqual(self.flag(), (None, None))

    def test_when_idle_reuses_the_queue_and_the_tick_runs_it_fresh(self):
        p = self.ok("relaunch", LEAD, "--fresh", "--when-idle", agent="zz-director")
        self.assertIn("queued", p.stdout)
        self.assertEqual(self.starts(), [])
        self.assertTrue(self.flag()[0])
        self.set_status("working")
        self.ok("tick")
        self.assertEqual(self.starts(), [])
        self.set_status("idle")
        self.ok("tick")
        argv = self.starts()[0]
        self.assertNotIn("--resume", argv)
        self.assertNotEqual(self.sid(), SESS)
        self.assertEqual(self.flag(), (None, None))
        self.assertEqual(self.env.sql("SELECT relaunch_fresh FROM agents WHERE name=?", (LEAD,))[0][0], 0)

    def test_permissions_match_plain_relaunch(self):
        self.refused(3, "relaunch", LEAD, "--fresh", agent="zz-boss")  # boss: the director only
        self.ok("relaunch", "zz-director", "--fresh", "--when-idle", agent="zz-boss")
        self.refused(3, "relaunch", "zz-director", "--fresh", agent="zz-director")  # own pane needs --when-idle
        self.ok("relaunch", "zz-director", "--fresh", "--when-idle", "--dry-run", agent="zz-director")
        self.env.seed_agent("zz-coder", "coder", goal="G-001", task="T-0001", pane_id="pc")
        self.refused(3, "relaunch", LEAD, "--fresh", agent="zz-coder")
        self.assertEqual(self.starts(), [])

    def test_lead_kickoff_one_pager(self):
        self.ok("relaunch", LEAD, "--fresh", agent="zz-director")
        kick = self.env.sql("SELECT body FROM messages WHERE recipient=? AND subject LIKE 'kickoff:%' ORDER BY id DESC",
                            (LEAD,))[0][0]
        for want in ("factory whoami", "factory inbox", "RE-ORIENT", "zz intent", "zz criteria", "T-0001",
                     "pull/7", "zz-decision-subject", "zz-decision-body"):
            self.assertIn(want, kick)
        self.assertNotIn("T-0002 [done]", kick)  # only open tasks are listed
        self.assertLess(len(kick.encode()), 20 * 1024)

    def test_director_kickoff_one_pager_lists_active_goals_and_owner_items(self):
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) "
                     "VALUES('owner','boss','escalation','zz-owner-item','b','2026-01-01T00:00:00')")
        self.env.set_herdr(agents=[{"name": "zz-director", "agent": "claude", "pane_id": "p-director",
                                    "agent_status": "idle", "agent_session": {"value": "sess-dir"}}],
                           panes=[{"pane_id": "p-director", "tab_id": "t1", "workspace_id": "w1"}])
        self.ok("relaunch", "zz-director", "--fresh", agent="zz-boss")
        kick = self.env.sql("SELECT body FROM messages WHERE recipient='zz-director' AND subject LIKE 'kickoff:%'")[0][0]
        self.assertIn("G-001", kick)
        self.assertIn("zz-owner-item", kick)

    def test_one_pager_stays_under_20kb_with_a_huge_backlog(self):
        for i in range(300):
            self.env.seed_task(f"T-1{i:03d}", status="todo", pr_url=None)
        self.env.sql("UPDATE tasks SET title=? WHERE id LIKE 'T-1%'", ("x" * 500,))
        self.env.sql("UPDATE goals SET intent=? WHERE id='G-001'", ("y" * 50000,))
        for i in range(30):
            self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,created,read_at) "
                         "VALUES('d','x','decision',?,?,'G-001','2026-01-01T00:00:00','x')", ("s" * 900, "b" * 9000))
        self.ok("relaunch", LEAD, "--fresh", agent="zz-director")
        kick = self.env.sql("SELECT body FROM messages WHERE recipient=? AND subject LIKE 'kickoff:%' ORDER BY id DESC", (LEAD,))[0][0]
        self.assertLess(len(kick.encode()), 20 * 1024)
        self.assertIn("RE-ORIENT", kick)


class RecycleTests(FreshBase):
    def setUp(self):
        super().setUp()
        self.ok("config", "recycle_context_tokens", "100000")
        self.transcript(SESS, 150000)

    def test_over_limit_idle_empty_inbox_queues_fresh_and_tick_runs_it(self):
        self.tick_at(NOW1)
        self.assertEqual(len(self.recycled()), 1)
        ev = json.loads(self.recycled()[0][0])
        self.assertEqual((ev["context_tokens"], ev["limit"]), (150000, 100000))
        argv = self.starts()[0]  # queued and run in the same tick: the agent is idle
        self.assertNotIn("--resume", argv)
        self.assertNotEqual(self.sid(), SESS)
        self.assertEqual(len(self.events("relaunched-fresh")), 1)

    def test_failed_recycle_alerts_the_director_not_nobody(self):
        self.env.set_herdr(pane_close_fail=True)
        self.tick_at(NOW1)
        self.assertEqual(len(self.events("relaunch-failed")), 1)
        alerts = self.env.sql("SELECT recipient, sender, kind FROM messages WHERE subject = ?", (f"relaunch of {LEAD} failed",))
        self.assertEqual([tuple(r) for r in alerts], [("director", "tick", "alert")])

    def test_at_or_under_the_limit_does_nothing(self):
        self.transcript(SESS, 100000)
        self.tick_at(NOW1)
        self.assertEqual((self.recycled(), self.starts()), ([], []))

    def test_no_transcript_does_nothing(self):
        (self.env.user_home / ".claude" / "projects" / "-zz-proj" / f"{SESS}.jsonl").unlink()
        self.tick_at(NOW1)
        self.assertEqual(self.recycled(), [])

    def test_working_agent_is_never_recycled(self):
        self.set_status("working")
        self.tick_at(NOW1)
        self.assertEqual((self.recycled(), self.starts(), self.flag()), ([], [], (None, None)))

    def test_unread_inbox_blocks_recycle(self):
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x',?,'msg','s','b','t')", (LEAD,))
        self.tick_at(NOW1)
        self.assertEqual((self.recycled(), self.starts()), ([], []))

    def test_boss_and_workers_are_never_recycled(self):
        self.env.sql("UPDATE agents SET pane_id='p-boss' WHERE name='zz-boss'")
        self.env.seed_agent("zz-coder", "coder", goal="G-001", task="T-0001", pane_id="pc")
        for n, s in (("zz-boss", "sess-boss"), ("zz-coder", "sess-coder")):
            self.env.sql("UPDATE agents SET session_id=? WHERE name=?", (s, n))
            self.transcript(s, 900000)
        self.env.set_herdr(agents=[{"name": n, "agent": "claude", "pane_id": p, "agent_status": "idle",
                                    "agent_session": {"value": s}} for n, p, s in
                                   (("zz-boss", "p-boss", "sess-boss"), ("zz-coder", "pc", "sess-coder"), (LEAD, PANE, SESS))])
        self.ok("config", "recycle_context_tokens", "1000000")  # only the boss and the coder are over; the lead is not
        self.tick_at(NOW1)
        self.assertEqual(self.env.sql("SELECT entity_id FROM events WHERE action='recycle-queued'"), [])

    def test_pending_relaunch_is_not_replaced(self):
        self.ok("relaunch", LEAD, "--when-idle", agent="zz-director")
        self.set_status("working")
        self.tick_at(NOW1)
        self.assertEqual(self.recycled(), [])

    def test_rate_limit_once_per_6h(self):
        self.tick_at(NOW1)
        self.assertEqual(len(self.recycled()), 1)
        self.transcript(self.sid(), 150000)
        self.env.sql("UPDATE messages SET read_at='x' WHERE recipient=?", (LEAD,))  # the kickoff of the fresh session
        self.herdr_agents(("idle", LEAD, self.sid(), PANE))
        self.tick_at(NOW2)
        self.assertEqual(len(self.recycled()), 1)
        self.tick_at(NOW3)
        self.assertEqual(len(self.recycled()), 2)

    def test_zero_turns_it_off(self):
        self.ok("config", "recycle_context_tokens", "0")
        self.tick_at(NOW1)
        self.assertEqual((self.recycled(), self.starts()), ([], []))

    def test_default_is_250000(self):
        self.env.sql("DELETE FROM config WHERE key='recycle_context_tokens'")
        self.transcript(SESS, 240000)
        self.tick_at(NOW1)
        self.assertEqual(self.recycled(), [])
        self.transcript(SESS, 260000)
        self.tick_at(NOW1)
        self.assertEqual(len(self.recycled()), 1)


if __name__ == "__main__":
    unittest.main()
