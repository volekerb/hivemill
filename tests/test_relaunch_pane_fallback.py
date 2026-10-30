"""T-0757 (G-010): relaunch finds the host pane of an agent the agent list reports without agent_session
(workers, sandboxed or not): the row's own pane, when exactly one claude process runs the session."""
import contextlib
import io
import unittest
from unittest import mock

from harness import FactoryTestCase

NAME = "zz-test-coder"
SESS = "sess-zz-coder"
PANE = "pw1"
PROC = [(4242, ["claude", "--session-id", SESS])]


def entry(session=None, name=NAME, pane=PANE, status="idle"):
    e = {"name": name, "agent": "claude", "pane_id": pane, "agent_status": status}
    if session:
        e["agent_session"] = {"value": session}
    return e


class PaneFallbackTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(lead="zz-test-lead")
        self.env.seed_agent(NAME, "coder", goal="G-001", team="g-001-a", pane_id=PANE)
        self.env.sql("UPDATE agents SET session_id=?, cwd=? WHERE name=?", (SESS, str(self.env.home), NAME))

    def relaunch(self, agents, procs, dry_run=False, force=False):
        """Run relaunch_agent with herdr mocked; returns (exit code or None, stdout, herdr calls, spawn calls)."""
        out = io.StringIO()
        with self.env.load_factory() as mod:
            con = mod.db()
            r = con.execute("SELECT * FROM agents WHERE name=?", (NAME,)).fetchone()
            seq = iter([procs, []])  # the process is gone once the pane is closed
            herdr_calls, spawns, code = [], [], None
            with mock.patch.object(mod, "herdr_agents", return_value=agents), \
                    mock.patch.object(mod, "claude_procs", side_effect=lambda s: next(seq, [])), \
                    mock.patch.object(mod, "find_pane", return_value=None), \
                    mock.patch.object(mod, "herdr", side_effect=lambda *a: herdr_calls.append(list(a))), \
                    mock.patch.object(mod, "spawn", side_effect=lambda *a, **k: spawns.append(k)), \
                    contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
                try:
                    mod.relaunch_agent(con, r, dry_run=dry_run, force=force)
                except SystemExit as e:
                    code = e.code
        return code, out.getvalue(), herdr_calls, spawns

    def test_pane_and_name_match_without_agent_session_closes_that_pane_and_resumes(self):
        code, out, herdr, spawns = self.relaunch([entry()], PROC)
        self.assertIsNone(code)
        self.assertEqual(herdr, [["pane", "close", PANE]])
        self.assertEqual([s["resume"] for s in spawns], [SESS])

    def test_dry_run_names_the_pane_the_fallback_would_close(self):
        code, out, herdr, spawns = self.relaunch([entry()], PROC, dry_run=True)
        self.assertIsNone(code)
        self.assertIn(f"fallback: pane {PANE} ({NAME}) has no agent_session", out)
        self.assertIn(f"herdr pane close {PANE}", out)
        self.assertEqual((herdr, spawns), ([], []))

    def test_two_claude_processes_for_the_session_are_refused(self):
        code, out, herdr, spawns = self.relaunch([entry()], PROC + [(4243, ["claude", "--resume", SESS])])
        self.assertEqual(code, 3)
        self.assertEqual((herdr, spawns), ([], []))

    def test_pane_entry_with_a_different_agent_session_is_refused(self):
        code, out, herdr, spawns = self.relaunch([entry(session="sess-other")], PROC)
        self.assertEqual(code, 3)
        self.assertEqual((herdr, spawns), ([], []))

    def test_name_mismatch_on_the_pane_is_refused(self):
        code, out, herdr, spawns = self.relaunch([entry(name="zz-someone-else")], PROC)
        self.assertEqual(code, 3)
        self.assertEqual((herdr, spawns), ([], []))

    def test_no_entry_for_the_pane_is_refused(self):
        code, out, herdr, spawns = self.relaunch([entry(pane="pw9")], PROC)
        self.assertEqual(code, 3)
        self.assertEqual((herdr, spawns), ([], []))

    def test_session_match_path_is_unchanged(self):
        code, out, herdr, spawns = self.relaunch([entry(session=SESS, pane="pw-real")], PROC)
        self.assertIsNone(code)
        self.assertEqual(herdr, [["pane", "close", "pw-real"]])
        self.assertEqual([s["resume"] for s in spawns], [SESS])
        self.assertNotIn("fallback", out)

    def test_working_without_force_is_refused_with_2_on_the_fallback_path(self):
        code, out, herdr, spawns = self.relaunch([entry(status="working")], PROC)
        self.assertEqual(code, 2)
        self.assertEqual((herdr, spawns), ([], []))
        code, out, herdr, spawns = self.relaunch([entry(status="working")], PROC, force=True)
        self.assertIsNone(code)
        self.assertEqual(herdr, [["pane", "close", PANE]])

    def test_working_without_force_is_refused_with_2_on_the_session_path(self):
        code, *_ = self.relaunch([entry(session=SESS, status="working")], PROC)
        self.assertEqual(code, 2)


if __name__ == "__main__":
    unittest.main()
