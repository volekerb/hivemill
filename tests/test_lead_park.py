"""T-0360 (G-014): `factory park`, teams.parked_at/resume_note, `factory team reopen`, the new config keys."""
import sqlite3
import unittest

from harness import FactoryTestCase, FACTORY

LEAD = "lead-g-001-a"
TEAM = "g-001-a"


class ParkBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(TEAM, lead=LEAD)
        self.env.seed_agent(LEAD, "lead", goal="G-001", team=TEAM)
        self.env.seed_agent("zz-director", "director")
        self.ok("config", "lead_parking", "1")

    def team(self):
        return self.env.sql("SELECT status, parked_at, resume_note FROM teams WHERE id=?", (TEAM,))[0]

    def agent(self):
        return self.env.sql("SELECT status, summary, relaunch_requested_at FROM agents WHERE name=?", (LEAD,))[0]

    def mail(self):
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) "
                     "VALUES('zz-director',?,'request','s','b','2026-01-01T00:00:00')", (LEAD,))


class ParkTests(ParkBase):
    def test_flag_off_refuses(self):
        self.ok("config", "lead_parking", "0")
        p = self.refused(3, "park", "x", agent=LEAD)
        self.assertIn("lead parking is off", p.stderr)
        self.assertIsNone(self.team()["parked_at"])
        self.assertEqual(self.agent()["status"], "live")

    def test_default_ships_off(self):
        self.env.sql("DELETE FROM config WHERE key='lead_parking'")
        self.refused(3, "park", "x", agent=LEAD)

    def test_unread_mail_refuses_and_stays_unread(self):
        self.mail()
        p = self.refused(2, "park", "x", agent=LEAD)
        self.assertIn("read your inbox first: 1 unread", p.stderr)
        self.assertIsNone(self.env.sql("SELECT read_at FROM messages")[0][0])
        self.assertEqual(self.agent()["status"], "live")
        self.assertIsNone(self.team()["parked_at"])

    def test_happy_path(self):
        self.env.sql("UPDATE agents SET relaunch_requested_at='t' WHERE name=?", (LEAD,))
        p = self.ok("park", "waiting on T-9 review", agent=LEAD)
        self.assertIn(f"parked {TEAM}", p.stdout)
        t = self.team()
        self.assertIsNotNone(t["parked_at"])
        self.assertEqual(t["resume_note"], "waiting on T-9 review")
        self.assertEqual(t["status"], "active")
        a = self.agent()
        self.assertEqual(a["status"], "done")
        self.assertTrue(a["summary"].startswith("parked: "))
        self.assertIsNone(a["relaunch_requested_at"])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='park'")), 1)

    def test_note_from_stdin_and_bounds(self):
        self.refused(2, "park", "   ", agent=LEAD)
        self.refused(2, "park", "x" * 2001, agent=LEAD)
        self.assertEqual(self.agent()["status"], "live")
        self.ok("park", "-", agent=LEAD, stdin="from stdin\n")
        self.assertEqual(self.team()["resume_note"], "from stdin")

    def test_not_live_changes_nothing(self):
        self.env.sql("UPDATE agents SET status='exited' WHERE name=?", (LEAD,))
        self.refused(2, "park", "x", agent=LEAD)
        self.assertIsNone(self.team()["parked_at"])

    def test_closed_team_refuses(self):
        self.env.sql("UPDATE teams SET status='closed'")
        self.refused(3, "park", "x", agent=LEAD)

    def test_worker_and_director_refused(self):
        self.env.seed_agent("zz-coder", "coder", goal="G-001", team=TEAM)
        self.refused(3, "park", "x", agent="zz-coder")
        self.refused(3, "park", "x", agent="zz-director")
        self.assertIsNone(self.team()["parked_at"])
        self.assertEqual(self.agent()["status"], "live")


class ReopenCloseTests(ParkBase):
    def test_reopen_closed_team(self):
        self.env.sql("UPDATE teams SET status='closed'")
        p = self.ok("team", "reopen", TEAM, agent="zz-director")
        self.assertIn(f"{TEAM} reopened as parked", p.stdout)
        t = self.team()
        self.assertEqual(t["status"], "active")
        self.assertIsNotNone(t["parked_at"])
        self.assertTrue(t["resume_note"].startswith("reopened from closed"))
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='reopen'")), 1)

    def test_reopen_refused_for_done_goal_and_active_team(self):
        self.env.sql("UPDATE teams SET status='closed'")
        self.env.sql("UPDATE goals SET status='done'")
        self.refused(3, "team", "reopen", TEAM, agent="zz-director")
        self.assertEqual(self.team()["status"], "closed")
        self.env.sql("UPDATE goals SET status='active'")
        self.env.sql("UPDATE teams SET status='active'")
        self.refused(3, "team", "reopen", TEAM, agent="zz-director")

    def test_reopen_role_refusals(self):
        self.env.sql("UPDATE teams SET status='closed'")
        self.refused(3, "team", "reopen", TEAM, agent=LEAD)
        self.env.seed_agent("zz-coder", "coder", goal="G-001", team=TEAM)
        self.refused(3, "team", "reopen", TEAM, agent="zz-coder")
        self.assertEqual(self.team()["status"], "closed")

    def test_close_clears_park(self):
        self.ok("park", "w", agent=LEAD)
        self.assertIsNotNone(self.team()["parked_at"])
        self.ok("team", "close", TEAM, agent="zz-director")
        self.assertIsNone(self.team()["parked_at"])


class SchemaTests(FactoryTestCase):
    def test_migrate_idempotent_on_old_teams_table(self):
        con = sqlite3.connect(self.env.db_path)
        con.executescript("ALTER TABLE teams RENAME TO teams_new; CREATE TABLE teams (id TEXT PRIMARY KEY, "
                          "goal_id TEXT NOT NULL, scope TEXT, lead_agent TEXT, tab_id TEXT, "
                          "status TEXT NOT NULL DEFAULT 'active', created TEXT NOT NULL); DROP TABLE teams_new;")
        con.commit()
        con.close()
        self.ok("team", "list")
        self.ok("team", "list")
        cols = {r["name"] for r in self.env.db().execute("PRAGMA table_info(teams)")}
        self.assertTrue({"parked_at", "resume_note"} <= cols)

    def test_config_keys(self):
        src = FACTORY.read_text()
        for line in ('"lead_parking": "0"', '"max_live_leads": "4"', '"reserved_worker_slots": "10"'):
            self.assertEqual(src.count(line), 1, line)


if __name__ == "__main__":
    unittest.main()
