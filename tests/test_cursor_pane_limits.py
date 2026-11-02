"""T-0878: tick records Cursor pane usage-limit state; web shows tier, limits, and time spent."""
import json
import unittest

from harness import FactoryTestCase
from web import cursor_limits

LIMIT_TEXT = (
    "You've hit your usage limit for Opus\n\n"
    "Resets November 5, 2026\n\n"
    "otherModelsLimitSwitchTarget composer-2.5\n"
)


class ParseTests(unittest.TestCase):
    def test_parses_fixture_limit_error(self):
        p = cursor_limits.parse_limit_error(LIMIT_TEXT)
        self.assertEqual(p["model"], "Opus")
        self.assertEqual(p["reset"], "2026-11-05")
        self.assertEqual(p["switch_target"], "composer-2.5")

    def test_no_error_returns_none(self):
        self.assertIsNone(cursor_limits.parse_limit_error("All normal\n"))


class TickLimits(FactoryTestCase):
    INPROC = True

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal("G-027")
        e.sql("UPDATE goals SET org='cursor' WHERE id='G-027'")
        e.seed_team("g-027-a", goal="G-027", lead="cursor-lead-g-027-a")
        e.seed_task(goal="G-027", team="g-027-a", tid="T-0878", status="in_progress")
        e.seed_agent("cursor-director", "director", goal="G-027", pane_id="p-dir")
        e.seed_agent(
            "cursor-coder-t-0878",
            "coder",
            goal="G-027",
            team="g-027-a",
            task="T-0878",
            pane_id="p-c",
        )
        self.env.sql(
            "UPDATE agents SET org='cursor', model='grok-4.7-high', "
            "spawn_args=? WHERE name='cursor-coder-t-0878'",
            (json.dumps({"runtime": "cursor"}),),
        )
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "w"}],
            panes=[{"pane_id": "p-c", "tab_id": "t1", "workspace_id": "w1"}],
            agents=[
                {
                    "name": "cursor-coder-t-0878",
                    "agent": "cursor",
                    "pane_id": "p-c",
                    "agent_status": "idle",
                }
            ],
        )

    def tick(self):
        return self.ok("tick")

    def _state(self):
        row = self.env.sql("SELECT value FROM config WHERE key=?", (cursor_limits.CONFIG_KEY,))
        return json.loads(row[0]["value"])

    def test_limit_recorded_and_director_alerted(self):
        self.env.set_herdr(read_text={"cursor-coder-t-0878": LIMIT_TEXT})
        self.tick()
        state = self._state()
        self.assertEqual(state["grok-4.7-high"]["status"], "limited")
        self.assertEqual(state["grok-4.7-high"]["reset"], "2026-11-05")
        self.assertEqual(state["grok-4.7-high"]["switch_target"], "composer-2.5")
        rows = self.env.sql(
            "SELECT recipient, subject FROM messages WHERE kind='alert' AND subject LIKE 'Cursor limit:%'"
        )
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["recipient"], "cursor-director")
        about = [c for c in self.env.calls("herdr") if len(c["argv"]) >= 2 and c["argv"][:2] == ["agent", "about"]]
        self.assertEqual(about, [])

    def test_ok_until_limit_error(self):
        self.env.set_herdr(read_text={"cursor-coder-t-0878": "working normally\n"})
        self.tick()
        state = self._state()
        self.assertEqual(state["grok-4.7-high"]["status"], "ok")
        self.assertEqual(state["composer-2.5"]["status"], "ok")
