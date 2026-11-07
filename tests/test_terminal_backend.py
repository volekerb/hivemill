"""T-1084: terminal_backend (herdr | hc | auto), doctor/res lines, missing agent_status -> unknown."""
import importlib.util
import json
import os
import tempfile
import unittest
from pathlib import Path

from harness import FactoryTestCase

ROOT = Path(__file__).resolve().parents[1]
_FACTORY_CLI = ROOT / "bin" / "factory_cli.py"


def _load_factory_cli():
    spec = importlib.util.spec_from_file_location("factory_cli", _FACTORY_CLI)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


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
        hc_calls = [c for c in self.env.calls("hc") if c["argv"][:1] == ["ls"]]
        herdr_calls = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "list"]]
        self.assertTrue(hc_calls, hc_calls)
        self.assertFalse(herdr_calls, herdr_calls)
        p = self.ok("tick", agent="owner")
        self.assertNotIn("herdr unreachable", p.stdout)

    def test_hc_ls_envelope_parses_to_agent_rows(self):
        f = _load_factory_cli()
        env = json.dumps({"hidden_finished": 0, "terminals": [
            {"name": "zz-coder", "agent": "claude", "state": "live"},
            {"name": "zz-qa", "agent": "claude", "state": "live", "status": "working"},
        ]})
        parsed = f._hc_unwrap_result(["ls"], json.loads(env))
        self.assertEqual(parsed["agents"][0], {"name": "zz-coder", "agent": "claude"})
        self.assertEqual(parsed["agents"][1]["agent_status"], "working")
        empty = f._hc_unwrap_result(["ls"], {"hidden_finished": 0, "terminals": []})
        self.assertEqual(empty, {"agents": []})

    def test_hc_agent_start_argv_drops_tui_only_flags(self):
        f = _load_factory_cli()
        argv = ("agent", "start", "zz-coder", "--kind", "claude", "--pane", "p1", "--timeout", "90000",
                "--", "--model", "opus", "-n", "zz-coder")
        f._HC_RUN_CWD = "/tmp/wt"
        try:
            self.assertEqual(
                f._hc_translate_args(argv),
                ["run", "--bg", "--name", "zz-coder", "--cwd", "/tmp/wt", "claude",
                 "--model", "opus", "-n", "zz-coder"],
            )
        finally:
            f._HC_RUN_CWD = None

    def test_hc_agent_start_cursor_uses_resolved_binary_not_shim(self):
        f = _load_factory_cli()
        argv = ("agent", "start", "zz-cur", "--kind", "cursor", "--pane", "p9", "--timeout", "90000",
                "--", "--workspace", "/tmp/wt", "--model", "opus", "--trust", "--force")
        f._HC_RUN_CWD = "/tmp/wt"
        f._HC_RUN_PROGRAM = "/usr/bin/agent"
        try:
            self.assertEqual(
                f._hc_translate_args(argv),
                ["run", "--bg", "--name", "zz-cur", "--cwd", "/tmp/wt",
                 "/usr/bin/agent", "--workspace", "/tmp/wt", "--model", "opus", "--trust", "--force"],
            )
        finally:
            f._HC_RUN_CWD = f._HC_RUN_PROGRAM = None

    def test_hc_agent_start_spawn_script_not_argv_env(self):
        f = _load_factory_cli()
        argv = ("agent", "start", "zz-coder", "--kind", "claude", "--pane", "p1", "--timeout", "90000",
                "--", "-n", "zz-coder")
        root = Path(tempfile.mkdtemp(prefix="hc-spawn-scripts-"))
        tok = root / "tok"
        f._HC_RUN_CWD = "/tmp/wt"
        f._HC_RUN_NAME = "zz-coder"
        dead = f.DEAD_SOCKET
        f._HC_RUN_ENV = {"FACTORY_AGENT": "zz-coder", "FACTORY_ROLE": "coder", "HERDR_SOCKET_PATH": dead,
                         "FACTORY_GH_TOKEN_FILE": str(tok)}
        old = os.environ.get("FACTORY_HC_SPAWN_SCRIPT_ROOT")
        os.environ["FACTORY_HC_SPAWN_SCRIPT_ROOT"] = str(root)
        try:
            run_argv = f._hc_translate_args(argv)
            self.assertEqual(run_argv[:6], ["run", "--bg", "--name", "zz-coder", "--cwd", "/tmp/wt"])
            self.assertEqual(run_argv[6:8], ["sh", str(root / "zz-coder.hc-spawn.sh")])
            flat = " ".join(run_argv)
            self.assertNotIn("FACTORY_GH", flat)
            self.assertNotIn("=", flat)
            body = (root / "zz-coder.hc-spawn.sh").read_text()
            self.assertRegex(body, r"FACTORY_ROLE=(?:'coder'|coder)\b")
            self.assertIn("FACTORY_GH_TOKEN_FILE", body)
            self.assertIn(str(tok), body)
            self.assertIn("exec claude -n zz-coder", body)
        finally:
            if old is None:
                os.environ.pop("FACTORY_HC_SPAWN_SCRIPT_ROOT", None)
            else:
                os.environ["FACTORY_HC_SPAWN_SCRIPT_ROOT"] = old
            f._HC_RUN_CWD = f._HC_RUN_ENV = f._HC_RUN_NAME = None

    def test_hc_piped_read_keeps_jsonish_screen_as_text(self):
        f = _load_factory_cli()
        for screen in ('line\n{"ok": true}', "hello\n[1, 2]", "done\ntrue"):
            with self.subTest(screen=screen):
                tail = f._hc_parse_host_stdout(["read", "zz-coder"], screen)
                self.assertEqual(tail, {"text": screen})

    def test_hc_agent_read_argv_lines_only(self):
        f = _load_factory_cli()
        self.assertEqual(
            f._hc_translate_args(("agent", "read", "zz-coder", "--source", "recent", "--lines", "60", "--timeout", "5")),
            ["read", "zz-coder", "--lines", "60"],
        )

    def test_herdr_config_routes_through_herdr_stub_not_hc(self):
        """Explicit herdr keeps the TUI path even when `hc` is also on PATH (harness stubs)."""
        self.ok("config", "terminal_backend", "herdr", agent="owner")
        self.env.set_herdr(agents=[])
        self.ok("tick", agent="owner")
        herdr_calls = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "list"]]
        hc_calls = [c for c in self.env.calls("hc") if c["argv"][:2] == ["agent", "list"]]
        self.assertTrue(herdr_calls, herdr_calls)
        self.assertFalse(hc_calls, hc_calls)

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
