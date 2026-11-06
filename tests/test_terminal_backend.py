"""T-1084: terminal_backend (herdr | hc | auto), doctor/res lines, missing agent_status -> unknown."""
import json
import os
import unittest

from harness import FactoryTestCase


class TerminalBackendTests(FactoryTestCase):
    def test_auto_prefers_herdr_when_both_stubs_on_path(self):
        with self.env.load_factory() as f:
            con = f.db()
            try:
                raw, eff, exe = f.terminal_backend_summary(con)
            finally:
                con.close()
            self.assertEqual(raw, "auto")
            self.assertEqual(exe, "herdr")
            self.assertEqual(eff, "auto→herdr")

    def test_hc_config_routes_herdr_calls_through_hc_stub(self):
        self.ok("config", "terminal_backend", "hc", agent="owner")
        self.env.set_herdr(agents=[])
        self.ok("tick", agent="owner")
        hc_calls = [c for c in self.env.calls("hc") if c["argv"][:2] == ["agent", "list"]]
        herdr_calls = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "list"]]
        self.assertTrue(hc_calls, hc_calls)
        self.assertFalse(herdr_calls, herdr_calls)

    def test_res_names_active_backend(self):
        out = self.ok("res", agent="owner").stdout
        self.assertRegex(out, r"terminal backend: herdr \(config=auto; effective=auto→herdr\)")

    def test_doctor_names_active_backend_and_socket_pin(self):
        out = self.ok("doctor", agent="owner").stdout
        self.assertIn("terminal backend: herdr (config=auto; effective=auto→herdr)", out)
        self.assertIn("herdr_socket (HERDR_SOCKET_PATH pin):", out)

    def test_missing_agent_status_is_unknown_not_idle_for_nudge(self):
        """Missing host status must not count as idle (nudge step requires idle or done)."""
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_agent("zz-pm", "pm", goal="G-001", team="g-001-a", pane_id="p1")
        st = json.loads(self.env.herdr_state.read_text())
        st["agents"] = [{"name": "zz-pm", "agent": "claude", "pane_id": "p1"}]  # no agent_status
        self.env.herdr_state.write_text(json.dumps(st))
        p = self.env.run("tick", agent="owner", extra_env={"FACTORY_NOW": "2020-01-15T12:00:00+00:00"})
        self.assertEqual(p.returncode, 0, p.stderr)
        prompts = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "prompt"]]
        self.assertEqual(prompts, [])


if __name__ == "__main__":
    unittest.main()
