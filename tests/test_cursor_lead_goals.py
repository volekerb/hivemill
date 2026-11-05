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

    def run_hook(self, cmd, argv, agent=LEAD, cwd=None):
        env = {k: v for k, v in os.environ.items() if k not in ("FACTORY_AGENT", "FACTORY_HOME")}
        env["FACTORY_HOME"] = self.home
        env["FACTORY_AGENT"] = agent
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}})
        return hook_run(payload, argv, env=env, cwd=cwd)

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

    def test_lead_refused_unowned_goal_via_relative_script(self):
        with tempfile.TemporaryDirectory() as wd:
            script = os.path.join(wd, "show_g001.sh")
            with open(script, "w", encoding="utf-8") as f:
                f.write("#!/bin/sh\nfactory goal show G-001\n")
            os.chmod(script, 0o755)
            p = self.run_hook("./show_g001.sh", self._lead_shell_argv(), cwd=wd)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("broad factory reads", p.stderr)

    def test_lead_refused_unowned_goal_prefixed_or_wrapped(self):
        blocked = (
            "A=1 factory goal show G-001",
            "command factory goal show G-001",
            "env factory goal show G-001",
            "nice factory goal show G-001",
            "time factory goal show G-001",
            "bash -c 'factory goal show G-001'",
            "eval 'factory goal show G-001'",
        )
        for cmd in blocked:
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd, self._lead_shell_argv())
                self.assertEqual(p.returncode, 2, p.stderr)
                self.assertIn("broad factory reads", p.stderr)

    def test_lead_refused_unowned_goal_show_with_valued_flags(self):
        blocked = (
            "factory goal --org cursor show G-001",
            "factory goal --note x show G-001",
            "factory goal --title t show G-001",
            "factory goal --priority 1 show G-001",
            "factory goal --project p show G-001",
            "factory goal --reason because show G-001",
        )
        for cmd in blocked:
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd, self._lead_shell_argv())
                self.assertEqual(p.returncode, 2, p.stderr)
                self.assertIn("broad factory reads", p.stderr)

    def test_lead_owned_goal_show_with_org_flag_allowed(self):
        p = self.run_hook("factory goal --org cursor show G-028", self._lead_shell_argv())
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_lead_refused_unowned_goal_modifiers_and_find(self):
        blocked = (
            "noglob factory goal show G-001",
            "zsh -c 'noglob factory goal show G-001'",
            "find . -exec factory goal show G-001 \\;",
        )
        for cmd in blocked:
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd, self._lead_shell_argv())
                self.assertEqual(p.returncode, 2, p.stderr)
                self.assertIn("broad factory reads", p.stderr)

    def test_lead_noglob_show_owned_goal_allowed(self):
        p = self.run_hook("noglob factory goal show G-028", self._lead_shell_argv())
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_lead_prefixed_show_owned_goal_allowed(self):
        p = self.run_hook("A=1 factory goal show G-028", self._lead_shell_argv())
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_lead_goal_list_not_denied(self):
        p = self.run_hook("factory goal list", self._lead_shell_argv())
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_coder_refused_other_team_goal(self):
        p = self.run_hook("factory goal show G-028", ["--role", "coder"], agent=CODER)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("broad factory reads", p.stderr)


if __name__ == "__main__":
    unittest.main()
