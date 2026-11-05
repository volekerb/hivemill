"""T-0822: a lead may `factory goal show` every goal its teams own; workers stay own-goal only."""
import json
import os
import sqlite3
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "Lead team goals query is Python-only until the Go mirror gains teams.lead_agent (T-0836 chain)"

LEAD = "lead-g-027-a"
CODER = "cursor-coder-g-027"


class LeadTeamGoalShowTests(unittest.TestCase):
    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.home = td.name
        db = os.path.join(self.home, "db", "factory.db")
        os.makedirs(os.path.dirname(db), exist_ok=True)
        con = sqlite3.connect(db)
        try:
            con.executescript(
                """
                CREATE TABLE goals(id PRIMARY KEY, project_id, title, status, created, updated);
                CREATE TABLE teams(id PRIMARY KEY, goal_id, lead_agent, created);
                CREATE TABLE agents(name PRIMARY KEY, role, goal_id, team_id, task_id, status, started, pane_id);
                INSERT INTO goals(id, project_id, title, status, created, updated) VALUES
                  ('G-027', NULL, 'g27', 'active', 't', 't'),
                  ('G-028', NULL, 'g28', 'active', 't', 't'),
                  ('G-001', NULL, 'other', 'active', 't', 't');
                """
            )
            con.execute(
                "INSERT INTO teams(id, goal_id, lead_agent, created) VALUES (?,?,?,'t'), (?,?,?,'t')",
                ("g-027-a", "G-027", LEAD, "g-028-a", "G-028", LEAD),
            )
            con.execute(
                "INSERT INTO agents(name, role, goal_id, team_id, task_id, status, started, pane_id) VALUES "
                "(?, 'lead', 'G-027', 'g-027-a', NULL, 'live', 't', NULL), "
                "(?, 'coder', 'G-027', 'g-027-a', NULL, 'live', 't', NULL)",
                (LEAD, CODER),
            )
            con.commit()
        finally:
            con.close()

    def run_hook(self, cmd, argv, agent=LEAD):
        env = {k: v for k, v in os.environ.items() if k not in ("FACTORY_AGENT", "FACTORY_HOME")}
        env["FACTORY_HOME"] = self.home
        env["FACTORY_AGENT"] = agent
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}})
        return hook_run(payload, argv, env=env)

    def _lead_shell_argv(self):
        """Same flags as lead beforeShellExecution in materialized hooks.json (--hq --role lead)."""
        return ["--hq", "--role", "lead"]

    def test_lead_shows_own_and_sibling_team_goals(self):
        for gid in ("G-027", "G-028"):
            with self.subTest(gid=gid):
                p = self.run_hook(f"factory goal show {gid}", self._lead_shell_argv())
                self.assertEqual(p.returncode, 0, p.stderr)

    def test_lead_refused_unowned_goal(self):
        p = self.run_hook("factory goal show G-001", self._lead_shell_argv())
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("broad factory reads", p.stderr)

    def test_coder_refused_other_team_goal(self):
        p = self.run_hook("factory goal show G-028", ["--role", "coder"], agent=CODER)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("broad factory reads", p.stderr)


if __name__ == "__main__":
    unittest.main()
