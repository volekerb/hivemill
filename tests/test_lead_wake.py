"""T-0361 (G-014): the tick wakes parked leads on unread mail, `factory team wake`, alert routing, up/relaunch skip."""
import json
import unittest

from harness import FactoryTestCase

LEAD = "lead-g-001-a"
TEAM = "g-001-a"
PARKED = "2026-01-01T00:00:00+00:00"


class WakeBase(FactoryTestCase):
    INPROC = True
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(TEAM, lead=LEAD)
        e.seed_agent(LEAD, "lead", goal="G-001", team=TEAM, status="exited")
        e.seed_agent("zz-director", "director")
        e.sql("UPDATE teams SET parked_at=?, resume_note='waiting on T-9 review' WHERE id=?", (PARKED, TEAM))
        self.ok("config", "lead_parking", "1")

    def team(self, tid=TEAM):
        return self.env.sql("SELECT status, parked_at FROM teams WHERE id=?", (tid,))[0]

    def lead(self, name=LEAD):
        return self.env.sql("SELECT status FROM agents WHERE name=?", (name,))[0]["status"]

    def wakes(self, tid=TEAM):
        return [json.loads(r["detail"]) for r in self.env.sql(
            "SELECT detail FROM events WHERE entity='team' AND entity_id=? AND action='lead-wake' ORDER BY id", (tid,))]

    def mail(self, to=LEAD, sender="zz-director"):
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) "
                     "VALUES(?,?,'request','q','b','2026-01-01T00:00:00')", (sender, to))
        return self.env.sql("SELECT max(id) i FROM messages")[0]["i"]

    def starts(self):
        return [c["argv"][2] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]

    def kickoffs(self):
        return [r["body"] for r in self.env.sql("SELECT body FROM messages WHERE recipient=? AND sender='factory'", (LEAD,))]


class WakeTickTests(WakeBase):
    def test_wake_on_message(self):
        mid = self.mail()
        self.ok("tick")
        self.assertEqual(self.lead(), "live")
        self.assertEqual(self.wakes(), [{"trigger": f"M{mid}", "queued": False}])
        self.assertIsNone(self.team()["parked_at"])
        self.assertEqual(self.starts(), [LEAD])

    def test_wake_on_worker_done(self):
        self.env.seed_task("T-0001", status="in_progress", pr_url=None, team=TEAM)
        self.env.seed_agent("coder-t-0001", "coder", goal="G-001", team=TEAM, task="T-0001")
        p = self.env.run("done", "x", agent="coder-t-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.lead(), "exited")
        self.ok("tick")
        self.assertEqual(len(self.wakes()), 1)
        self.assertEqual(self.lead(), "live")

    def test_no_lost_messages_and_kickoff_text(self):
        mid = self.mail()
        self.ok("tick")
        row = self.env.sql("SELECT read_at FROM messages WHERE id=?", (mid,))[0]
        self.assertIsNone(row["read_at"])
        kick = [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "prompt"]]
        text = " ".join(" ".join(a) for a in kick)
        self.assertIn("WAKE (M%d)" % mid, text)
        self.assertIn("resume note: waiting on T-9 review", text)
        self.assertIn("unread: 1", text)
        self.assertTrue(any("unread: 1" in b for b in self.kickoffs()))

    def test_queued_wake_survives_closed_gate(self):
        self.ok("config", "max_live_agents", "0")
        self.mail()
        self.ok("tick")
        self.assertEqual(self.lead(), "queued")
        self.assertIsNotNone(self.team()["parked_at"])
        self.ok("config", "max_live_agents", "5")
        self.ok("tick")
        self.assertEqual(self.lead(), "live")
        self.assertIsNone(self.team()["parked_at"])
        self.assertEqual(len(self.wakes()), 1)

    def test_no_double_wake(self):
        self.mail()
        self.ok("tick")
        self.mail()
        self.env.sql("UPDATE agents SET status='exited' WHERE name=?", (LEAD,))
        self.env.sql("UPDATE teams SET parked_at=? WHERE id=?", (PARKED, TEAM))
        self.ok("tick")  # parked again within 120 s of the first wake: throttled
        self.assertEqual(len(self.wakes()), 1)
        self.assertEqual(self.starts(), [LEAD])

    def test_already_queued_lead_adds_no_row(self):
        self.ok("config", "max_live_agents", "0")
        self.mail()
        self.ok("tick")
        self.env.sql("DELETE FROM throttle WHERE key LIKE 'wake:%'")
        self.ok("tick")
        self.assertEqual(self.lead(), "queued")
        self.assertEqual(len(self.wakes()), 1)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM agents WHERE name=?", (LEAD,))[0]["c"], 1)

    def test_no_mail_flag_on_no_wake(self):
        self.ok("tick")
        self.assertEqual(self.wakes(), [])
        self.assertEqual(self.lead(), "exited")

    def test_role_change_notice_does_not_wake(self):
        with self.env.load_factory() as f:
            con = f.db()
            f.gh_json = lambda *a, **k: {"files": [{"path": "roles/lead.md"}]}
            self.env.seed_task("T-0002", status="done", pr_url="https://example.invalid/pr/2")
            self.env.seed_agent("lead-live", "lead", goal="G-001")
            t = con.execute("SELECT * FROM tasks WHERE id='T-0002'").fetchone()
            f.after_merge(con, t, "https://example.invalid/pr/2")
            con.commit()
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE recipient=? AND subject LIKE 'role files%'",
                                      (LEAD,))[0]["c"], 0)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE recipient='lead-live' "
                                      "AND subject LIKE 'role files changed%'")[0]["c"], 1)
        self.assertIsNone(self.env.sql("SELECT relaunch_requested_at r FROM agents WHERE name=?", (LEAD,))[0]["r"])
        self.ok("tick")
        self.assertEqual(self.wakes(), [])

    def test_woken_lead_gets_current_role_file(self):
        self.mail()
        self.ok("tick")
        prompt = self.env.home_dir / "run" / f"{LEAD}.prompt.md"
        self.assertIn((self.env.home_dir / "roles" / "lead.md").read_text(), prompt.read_text())


class WakeDrainTests(WakeBase):
    def test_flag_off_drains_one_per_tick(self):
        self.env.seed_goal("G-002")
        self.env.seed_team("g-002-a", goal="G-002", lead="lead-g-002-a")
        self.env.seed_agent("lead-g-002-a", "lead", goal="G-002", team="g-002-a", status="exited")
        self.env.sql("UPDATE teams SET parked_at=? WHERE id='g-002-a'", (PARKED,))
        self.ok("config", "lead_parking", "0")
        self.ok("tick")
        first = self.wakes() + self.wakes("g-002-a")
        self.assertEqual(len(first), 1)
        self.assertEqual(first[0]["trigger"], "flag-off")
        self.env.sql("DELETE FROM throttle WHERE key LIKE 'wake:%'")
        self.ok("tick")
        self.assertEqual(len(self.wakes()), 1)
        self.assertEqual(len(self.wakes("g-002-a")), 1)

    def test_flag_on_no_mail_no_wake(self):
        self.ok("tick")
        self.ok("tick")
        self.assertEqual(self.wakes(), [])


class StaleParkTests(WakeBase):
    def alerts(self):
        return self.env.sql("SELECT subject FROM messages WHERE recipient='director' AND subject LIKE 'team %parked 12h%'")

    def test_stale_park_alert_once(self):
        extra = {"FACTORY_NOW": "2026-01-01T13:00:00+00:00"}
        self.assertEqual(self.env.run("tick", extra_env=extra).returncode, 0)
        self.assertEqual([r["subject"][:23] for r in self.alerts()], ["team g-001-a parked 12h"])
        self.assertEqual(self.env.run("tick", extra_env=extra).returncode, 0)
        self.assertEqual(len(self.alerts()), 1)
        self.assertEqual(self.wakes(), [])

    def test_young_park_no_alert(self):
        self.assertEqual(self.env.run("tick", extra_env={"FACTORY_NOW": "2026-01-01T11:00:00+00:00"}).returncode, 0)
        self.assertEqual(self.alerts(), [])

    def test_team_worker_suppresses_alert(self):
        self.env.seed_agent("coder-x", "coder", goal="G-001", team=TEAM)  # live: the tick's reap is not run here
        with self.env.load_factory() as f:
            con = f.db()
            f.os.environ["FACTORY_NOW"] = "2026-01-01T13:00:00+00:00"
            try:
                f.tick_wake_leads(con)
            finally:
                del f.os.environ["FACTORY_NOW"]
        self.assertEqual(self.alerts(), [])


class AlertRoutingTests(WakeBase):
    def test_blocked_alert_target(self):
        self.env.seed_agent("coder-x", "coder", goal="G-001", team=TEAM)
        with self.env.load_factory() as f:
            con = f.db()
            w = con.execute("SELECT * FROM agents WHERE name='coder-x'").fetchone()
            self.assertEqual(f.blocked_alert_target(con, w)[0], LEAD)
            con.execute("UPDATE teams SET parked_at=NULL WHERE id=?", (TEAM,))
            self.assertEqual(f.blocked_alert_target(con, w)[0], "director")


class TeamWakeCliTests(WakeBase):
    def test_director_wakes_parked_team(self):
        p = self.ok("team", "wake", TEAM, agent="zz-director")
        self.assertRegex(p.stdout, "SPAWNED|QUEUED")
        self.assertEqual(self.wakes()[0]["trigger"], "manual")
        self.assertEqual(self.lead(), "live")

    def test_worker_refused(self):
        self.env.seed_agent("coder-x", "coder", goal="G-001", team=TEAM)
        self.refused(3, "team", "wake", TEAM, agent="coder-x")
        self.assertEqual(self.wakes(), [])

    def test_not_parked_refused(self):
        self.env.sql("UPDATE teams SET parked_at=NULL WHERE id=?", (TEAM,))
        p = self.refused(3, "team", "wake", TEAM, agent="zz-director")
        self.assertIn("not parked", p.stderr)

    def test_already_waking_refused(self):
        self.env.sql("UPDATE agents SET status='queued' WHERE name=?", (LEAD,))
        p = self.refused(2, "team", "wake", TEAM, agent="zz-director")
        self.assertIn("already waking", p.stderr)


class TeamWakeLockTests(WakeBase):
    def test_wake_refused_while_tick_lock_held(self):
        import fcntl
        with open(self.env.home_dir / "run" / "tick.lock", "w") as f:
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
            p = self.refused(2, "team", "wake", TEAM, agent="zz-director")
        self.assertIn("tick running", p.stderr)
        self.assertEqual(self.starts(), [])
        self.assertEqual(self.wakes(), [])
        self.assertIsNotNone(self.team()["parked_at"])
        self.ok("team", "wake", TEAM, agent="zz-director")  # lock released: the retry starts exactly one lead
        self.assertEqual(self.starts().count(LEAD), 1)


class ParkedRoutingTests(WakeBase):
    def test_drift_target_prefers_live_lead(self):
        self.env.seed_team("g-001-b", lead="lead-g-001-b")
        self.env.seed_agent("lead-g-001-b", "lead", goal="G-001", team="g-001-b", status="live")
        url = "https://github.com/zz-test/repo/pull/7"
        self.env.seed_task("T-0007", status="in_review", pr_url=url, team="g-001-b")
        self.env.set_gh(prs={url: {"state": "MERGED"}})  # D2: in_review but the PR is merged
        with self.env.load_factory() as f:
            f.tick_drift(f.db())
        rows = self.env.sql("SELECT recipient, subject FROM messages WHERE subject LIKE 'drift %'")
        # g-001-a sorts first but is parked: the alert goes to the live lead's team
        self.assertEqual([(r["recipient"], r["subject"]) for r in rows], [("lead-g-001-b", "drift G-001: 1 items")])

    def test_parked_team_without_lead_row_alerts(self):
        self.env.sql("DELETE FROM agents WHERE name=?", (LEAD,))
        self.assertEqual(self.env.run("tick", extra_env={"FACTORY_NOW": "2026-01-01T13:00:00+00:00"}).returncode, 0)
        n = self.env.sql("SELECT count(*) c FROM messages WHERE recipient='director' AND subject LIKE 'team %parked 12h%'")[0]["c"]
        self.assertEqual(n, 1)
        self.assertEqual(self.wakes(), [])


class WakePriorityTests(WakeBase):
    """T-0481: a lead wake never starts a lower-priority lead ahead of a queued lead of a higher-priority goal."""

    def setUp(self):
        super().setUp()
        e = self.env
        self.ok("config", "max_live_agents", "18")
        self.ok("config", "max_live_leads", "2")
        e.seed_goal("G-002")
        e.seed_team("g-002-a", goal="G-002", lead="lead-g-002-a")
        e.sql("UPDATE goals SET priority=3 WHERE id='G-001'")
        e.seed_agent("lead-live", "lead", goal="G-001")  # cap 2, one slot taken: room for exactly one more lead
        e.sql("UPDATE agents SET pane_id='p-live' WHERE name='lead-live'")
        e.set_herdr(agents=[{"name": "lead-live", "agent": "claude", "pane_id": "p-live"}])
        self.mail()

    def queue_other(self, prio):
        e = self.env
        e.sql("UPDATE goals SET priority=? WHERE id='G-002'", (prio,))
        args = {"role": "lead", "goal": "G-002", "team": "g-002-a", "task": None, "name": "lead-g-002-a", "cwd": None,
                "prompt": "WAKE (M1)", "model": None, "resume": None, "fresh": True}
        e.sql("INSERT INTO agents(name,role,goal_id,team_id,status,spawn_args,queued_at) VALUES(?,?,?,?,?,?,?)",
              ("lead-g-002-a", "lead", "G-002", "g-002-a", "queued", json.dumps(args), "2026-01-01T00:00:00+00:00"))

    def test_p3_wake_queues_behind_queued_p1(self):
        self.queue_other(1)
        self.ok("tick")
        self.assertEqual(self.lead("lead-g-002-a"), "live")
        self.assertEqual(self.lead(), "queued")
        self.assertTrue(self.wakes()[0]["queued"])
        self.assertIsNotNone(self.team()["parked_at"])

    def test_p3_wake_starts_without_queued_p1(self):
        self.ok("tick")
        self.assertEqual(self.lead(), "live")
        self.assertIsNone(self.team()["parked_at"])

    def test_equal_priority_keeps_current_behaviour(self):
        self.queue_other(3)
        self.ok("tick")
        self.assertEqual(self.lead(), "live")
        self.assertEqual(self.lead("lead-g-002-a"), "queued")


class UpRelaunchTests(WakeBase):
    def test_up_skips_parked(self):
        self.env.sql("UPDATE agents SET status='lost', session_id='sess-lead' WHERE name=?", (LEAD,))
        self.env.seed_agent("zz-test-boss", "boss", status="lost")
        self.ok("up")
        self.assertNotIn(LEAD, self.starts())
        self.assertIsNotNone(self.team()["parked_at"])

    def test_up_resumes_unparked_lead(self):
        self.env.sql("UPDATE agents SET status='lost', session_id='sess-lead' WHERE name=?", (LEAD,))
        self.env.sql("UPDATE teams SET parked_at=NULL WHERE id=?", (TEAM,))
        self.ok("up")
        self.assertIn(LEAD, self.starts())

    def test_relaunch_refused_for_parked(self):
        self.env.sql("UPDATE agents SET session_id='sess-lead' WHERE name=?", (LEAD,))
        p = self.refused(3, "relaunch", LEAD, agent="zz-director")
        self.assertIn("is parked; use factory team wake", p.stderr)
        self.assertEqual(self.starts(), [])


if __name__ == "__main__":
    unittest.main()
