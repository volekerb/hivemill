"""T-0851: the scout schedule on a post-G-028 install (org_prefix_default=1: the scout's agents row is claude-scout).
Symptom (b): schedule.py looked the row up as 'scout', so the run failed 'scout agent row missing' or never saw the
live agent (no budget, no finish). Symptom (a): an unreadable session log was stored as tokens=0, not NULL."""
import json
import unittest

from harness import REPO  # noqa: F401 — puts the repo on sys.path like the other schedule tests
from test_scout_schedule import Base, at

SCOUT = "claude-scout"


class OrgBase(Base):
    def setUp(self):
        super().setUp()
        self.ok("config", "org_prefix_default", "1")

    def agent(self):
        return dict(self.env.sql("SELECT * FROM agents WHERE name=?", (SCOUT,))[0])

    def pin(self, started=None, queued=None):
        if started:
            self.env.sql("UPDATE agents SET started=? WHERE name=?", (started, SCOUT))
        if queued:
            self.env.sql("UPDATE agents SET queued_at=? WHERE name=?", (queued, SCOUT))

    def finish(self):
        self.ok("inbox", agent=SCOUT)
        self.ok("done", "scanned", agent=SCOUT)

    def spawned(self):
        self.tick(at("09:00"))
        self.pin(started=at("09:00"))
        return self.agent()


class OrgNameTests(OrgBase):
    def test_run_follows_the_org_prefixed_scout_row_and_is_not_failed_as_missing(self):
        self.tick(at("09:01"))
        self.assertEqual([a[2] for a in self.starts()], [SCOUT])
        self.pin(started=at("09:01"))
        self.tick(at("09:05"))
        (run,) = self.runs()
        self.assertEqual((run["status"], run["agent"], run["stop_reason"]), ("running", SCOUT, None))

    def test_wall_budget_stops_the_org_prefixed_scout(self):
        self.spawned()
        self.tick(at("09:21"))
        self.assertEqual(self.agent()["status"], "stopped")
        (run,) = self.runs()
        self.assertEqual((run["status"], run["stop_reason"]), ("stopped", "wall"))

    def test_token_budget_stops_the_org_prefixed_scout_and_a_real_count_is_recorded(self):
        sid = self.spawned()["session_id"]
        self.usage_log(sid, input_=200000, output=100000, cache_write=1)
        self.tick(at("09:06"))
        (run,) = self.runs()
        self.assertEqual((run["status"], run["stop_reason"], run["tokens"]), ("stopped", "tokens", 300001))

    def test_finished_scout_records_its_real_token_count(self):
        sid = self.spawned()["session_id"]
        self.usage_log(sid, input_=10, output=20, cache_write=30)
        self.finish()
        self.tick(at("09:10"))
        (run,) = self.runs()
        self.assertEqual((run["status"], run["tokens"]), ("done", 60))
        self.assertGreater(run["tokens"], 0)


class LegacyCursorRowTests(OrgBase):
    def test_a_legacy_cursor_scout_row_does_not_pull_the_scheduled_scout_onto_the_cursor_org(self):
        # live 2026-11-01: agents row 'scout' org=cursor runtime=cursor, status stopped (the run before)
        self.env.sql("INSERT INTO agents(name,role,status,spawn_args,org) VALUES('scout','scout','stopped',?,'cursor')",
                     (json.dumps({"role": "scout", "name": "scout", "org": "cursor", "runtime": "cursor"}),))
        self.tick(at("09:01"))
        self.assertEqual([a[2] for a in self.starts()], [SCOUT])
        row = self.agent()
        self.assertEqual((row["org"], row["status"]), ("claude", "live"))
        self.assertEqual(json.loads(row["spawn_args"]).get("runtime", "claude"), "claude")
        spawn_ev = self.events("spawn")[-1]["detail"]
        self.assertNotIn("cursor", spawn_ev)  # live: spawn event runtime=cursor kind=cursor, no Claude session id
        self.pin(started=at("09:01"))
        sid = row["session_id"]
        self.usage_log(sid, input_=5, output=6, cache_write=7)
        self.finish()
        self.tick(at("09:10"))
        (run,) = self.runs()
        self.assertEqual((run["agent"], run["status"], run["tokens"]), (SCOUT, "done", 18))


class UnknownTokensTests(OrgBase):
    def test_unreadable_session_log_stores_null_not_zero(self):
        self.spawned()  # no usage log written for its session
        self.finish()
        self.tick(at("09:10"))
        (run,) = self.runs()
        self.assertEqual(run["status"], "done")
        self.assertIsNone(run["tokens"])

    def test_a_log_with_no_usage_line_is_unknown_too(self):
        sid = self.spawned()["session_id"]
        d = self.env.user_home / ".claude" / "projects" / "-zz-proj"
        d.mkdir(parents=True, exist_ok=True)
        (d / f"{sid}.jsonl").write_text(json.dumps({"type": "user", "message": {"content": "hi"}}) + "\n")
        self.finish()
        self.tick(at("09:10"))
        self.assertIsNone(self.runs()[0]["tokens"])

    def test_a_log_that_shows_zero_usage_is_a_real_zero(self):
        sid = self.spawned()["session_id"]
        self.usage_log(sid)
        self.finish()
        self.tick(at("09:10"))
        self.assertEqual(self.runs()[0]["tokens"], 0)

    def test_unknown_tokens_do_not_trip_the_token_budget_and_the_wall_still_does(self):
        self.spawned()
        self.tick(at("09:06"))
        self.assertEqual(self.runs()[0]["status"], "running")  # unreadable log: not over budget
        self.tick(at("09:21"))
        (run,) = self.runs()
        self.assertEqual((run["status"], run["stop_reason"], run["tokens"]), ("stopped", "wall", None))

    def test_scout_runs_prints_a_dash_and_the_skill_scout_report_renders(self):
        self.spawned()
        self.finish()
        self.tick(at("09:10"))
        out = self.ok("scout", "runs", agent="owner").stdout
        self.assertIn("tokens=- ", out)
        self.assertNotIn("None", out)
        self.assertEqual(self.ok("scout", "report", agent="owner").returncode, 0)


if __name__ == "__main__":
    unittest.main()
