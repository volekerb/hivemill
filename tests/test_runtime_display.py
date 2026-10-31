"""Runtime display helpers: Claude vs Cursor chips (docs/runtimes.md)."""
import datetime as dt
import json
import sqlite3
import unittest

from web.components import runtime_badge
from web.queries import agent_runtime_of, agents

KICKOFF = "KICKOFF-SECRET-do-not-leak: you are coder-x, read the spec"
NOW = dt.datetime(2026, 10, 24, 12, 0, tzinfo=dt.timezone.utc)


class AgentRuntimeOfTests(unittest.TestCase):
    def test_none_and_empty_string_default_to_claude(self):
        self.assertEqual(agent_runtime_of(None), "claude")
        self.assertEqual(agent_runtime_of(""), "claude")

    def test_missing_runtime_key_defaults_to_claude(self):
        self.assertEqual(agent_runtime_of("{}"), "claude")
        self.assertEqual(agent_runtime_of('{"kickoff":"hello","model":"m"}'), "claude")
        self.assertEqual(agent_runtime_of('{"runtime":null}'), "claude")

    def test_explicit_empty_runtime_defaults_to_claude(self):
        self.assertEqual(agent_runtime_of('{"runtime":""}'), "claude")

    def test_cursor(self):
        self.assertEqual(agent_runtime_of('{"runtime":"cursor"}'), "cursor")
        self.assertEqual(agent_runtime_of('{"runtime":"claude"}'), "claude")

    def test_junk_json_defaults_to_claude(self):
        self.assertEqual(agent_runtime_of("{bad"), "claude")
        self.assertEqual(agent_runtime_of("not json at all"), "claude")

    def test_unknown_runtime_defaults_to_claude(self):
        self.assertEqual(agent_runtime_of('{"runtime":"codex"}'), "claude")


class AgentsQueryTests(unittest.TestCase):
    """web.queries.agents(con) on a temp sqlite DB: runtime derived, spawn_args (kickoff text) never returned."""

    def setUp(self):
        self.con = sqlite3.connect(":memory:")
        self.con.row_factory = sqlite3.Row
        self.addCleanup(self.con.close)
        self.con.execute("CREATE TABLE agents(name TEXT PRIMARY KEY, role TEXT, goal_id TEXT, team_id TEXT, task_id TEXT,"
                         " status TEXT, model TEXT, pane_id TEXT, started TEXT, ended TEXT, queued_at TEXT,"
                         " session_id TEXT, stop_requested_at TEXT, spawn_args TEXT)")
        rows = [
            ("pm-a", "pm", json.dumps({"runtime": "cursor", "kickoff": KICKOFF})),
            ("coder-a", "coder", json.dumps({"kickoff": KICKOFF})),
            ("qa-a", "qa", json.dumps({"runtime": "", "kickoff": KICKOFF})),
            ("lead-a", "lead", KICKOFF),   # kickoff text that is not JSON
            ("judge-a", "judge", None),
        ]
        for name, role, sa in rows:
            self.con.execute("INSERT INTO agents(name, role, status, started, spawn_args) VALUES(?,?,?,?,?)",
                             (name, role, "live", "2026-10-24T11:00:00+00:00", sa))

    def test_rows_carry_runtime(self):
        got = {r["name"]: r["runtime"] for r in agents(self.con, now=NOW)}
        self.assertEqual(got, {"pm-a": "cursor", "coder-a": "claude", "qa-a": "claude",
                               "lead-a": "claude", "judge-a": "claude"})

    def test_kickoff_text_never_leaks(self):
        rows = agents(self.con, now=NOW)
        self.assertEqual(len(rows), 5)
        for r in rows:
            self.assertIn("runtime", r)
            self.assertNotIn("spawn_args", r)
            self.assertNotIn("_spawn_args", r)
        self.assertNotIn(KICKOFF, str(rows))
        self.assertNotIn("KICKOFF-SECRET", json.dumps(rows, default=str))

    def test_db_without_spawn_args_column_is_all_claude(self):
        con = sqlite3.connect(":memory:")
        con.row_factory = sqlite3.Row
        self.addCleanup(con.close)
        con.execute("CREATE TABLE agents(name TEXT, role TEXT, status TEXT, started TEXT)")
        con.execute("INSERT INTO agents VALUES('old','coder','live','2026-10-24T11:00:00+00:00')")
        rows = agents(con, now=NOW)
        self.assertEqual([(r["name"], r["runtime"]) for r in rows], [("old", "claude")])
        self.assertNotIn("_spawn_args", rows[0])


class RuntimeBadgeTests(unittest.TestCase):
    def test_claude_chip(self):
        h = str(runtime_badge(None))
        self.assertIn("rt-claude", h)
        self.assertIn("claude", h)

    def test_cursor_chip(self):
        h = str(runtime_badge("cursor"))
        self.assertIn("rt rt-cursor", h)
        self.assertNotIn("rt-claude", h)

    def test_unknown_falls_back(self):
        self.assertIn("rt-claude", str(runtime_badge("nope")))
        self.assertIn("rt-claude", str(runtime_badge("bogus")))
        self.assertNotIn("rt-cursor", str(runtime_badge("bogus")))


if __name__ == "__main__":
    unittest.main()
