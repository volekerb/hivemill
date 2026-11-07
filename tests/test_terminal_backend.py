"""T-1084/T-1188: terminal_backend (herdr | hc | auto), hc spawn script, hc tick reconcile."""
import importlib.util
import json
import os
import subprocess
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

    def test_hc_spawn_script_env_i_drops_messaging_credential(self):
        f = _load_factory_cli()
        argv = ("agent", "start", "zz-coder", "--kind", "claude", "--pane", "p1", "--timeout", "90000",
                "--", "-n", "zz-coder")
        root = Path(tempfile.mkdtemp(prefix="hc-spawn-scripts-"))
        tok = root / "tok"
        f._HC_RUN_CWD = "/tmp/wt"
        f._HC_RUN_NAME = "zz-coder"
        dead = f.DEAD_SOCKET
        msg_cred_key = "CLAUDE_CODE_MESSAGING_" + "TOKEN"
        f._HC_RUN_ENV = {
            "FACTORY_AGENT": "zz-coder", "FACTORY_ROLE": "coder", "HERDR_SOCKET_PATH": dead,
            "FACTORY_GH_TOKEN_FILE": str(tok),
            msg_cred_key: "fixture-not-for-child",
            "HC_SOCKET": "/tmp/hc.sock",
        }
        old = os.environ.get("FACTORY_HC_SPAWN_SCRIPT_ROOT")
        os.environ["FACTORY_HC_SPAWN_SCRIPT_ROOT"] = str(root)
        try:
            run_argv = f._hc_translate_args(argv)
            self.assertEqual(run_argv[:6], ["run", "--bg", "--name", "zz-coder", "--cwd", "/tmp/wt"])
            self.assertEqual(run_argv[6:8], ["sh", str(root / "zz-coder.hc-spawn.sh")])
            flat = " ".join(run_argv)
            self.assertNotIn("FACTORY_GH", flat)
            self.assertNotIn("fixture-not", flat)
            body = (root / "zz-coder.hc-spawn.sh").read_text()
            self.assertIn("env -i", body)
            self.assertRegex(body, r"FACTORY_ROLE=(?:'coder'|coder)\b")
            self.assertIn("FACTORY_GH_TOKEN_FILE", body)
            self.assertNotIn("CLAUDE_CODE_MESSAGING_TOKEN", body)
            self.assertNotIn("HC_SOCKET", body)
            live_sock = os.path.expanduser(f.DEFAULT_CONFIG["herdr_socket"])
            f._HC_RUN_ENV["HERDR_SOCKET_PATH"] = live_sock
            live_body = (root / "zz-coder-live-sock.hc-spawn.sh")
            f._hc_write_spawn_script("zz-coder-live-sock", f._HC_RUN_ENV, "/bin/true", [])
            self.assertNotIn(live_sock, live_body.read_text())
            self.assertIn(f"HERDR_SOCKET_PATH={dead}", body)
            probe_script = f._hc_write_spawn_script(
                "zz-probe", f._HC_RUN_ENV, "/bin/sh",
                ["-c", "echo ROLE=$FACTORY_ROLE MSG=${CLAUDE_CODE_MESSAGING_TOKEN:-}"],
            )
            probe = subprocess.run(
                ["/bin/sh", str(probe_script)],
                capture_output=True, text=True,
                env={**os.environ, "CLAUDE_CODE_MESSAGING_TOKEN": "fixture-parent", "HC_SOCKET": "/tmp/fixture"},
            )
            self.assertEqual(probe.returncode, 0, probe.stderr)
            self.assertIn("ROLE=coder", probe.stdout)
            self.assertNotIn("fixture-parent", probe.stdout)
            self.assertNotIn("MSG=fixture", probe.stdout)
        finally:
            if old is None:
                os.environ.pop("FACTORY_HC_SPAWN_SCRIPT_ROOT", None)
            else:
                os.environ["FACTORY_HC_SPAWN_SCRIPT_ROOT"] = old
            f._HC_RUN_CWD = f._HC_RUN_ENV = f._HC_RUN_NAME = None

    def test_hc_spawn_refused_keeps_done_row_pane(self):
        """hc backend: reusing a done name must not reap the row before spawn queues."""
        high_load = str(10 * (os.cpu_count() or 1))
        self.ok("config", "terminal_backend", "hc", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_task("T-0002", goal="G-001", team="g-001-a", role="coder")
        self.env.seed_agent("zz-done", "coder", goal="G-001", team="g-001-a", pane_id="p-tui", status="done")
        self.ok("config", "max_live_agents", "99", agent="owner")
        self.env.set_herdr(agents=[])
        p = self.ok(
            "spawn",
            "coder",
            "--goal",
            "G-001",
            "--team",
            "g-001-a",
            "--task",
            "T-0002",
            "--name",
            "zz-done",
            agent="owner",
            extra_env={"FACTORY_LOADAVG": high_load},
        )
        self.assertIn("QUEUED", p.stdout)
        row = self.env.sql("SELECT status, pane_id FROM agents WHERE name='zz-done'")[0]
        self.assertEqual(row["status"], "queued")
        reaped = self.env.sql("SELECT action FROM events WHERE entity_id='zz-done' AND action='reaped'")
        self.assertEqual(reaped, [])
        close_calls = [c for c in self.env.calls("hc") if c["argv"][:2] == ["pane", "close"]]
        herdr_close = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["pane", "close"]]
        self.assertEqual(close_calls + herdr_close, [])

    def test_hc_spawn_refused_keeps_done_row_pane_when_load_gate_closed(self):
        """hc backend: a closed load gate queues without clearing a done row's pane."""
        high_load = str(10 * (os.cpu_count() or 1))
        self.ok("config", "terminal_backend", "hc", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_task("T-0003", goal="G-001", team="g-001-a", role="coder")
        self.env.seed_agent("zz-done-gate", "coder", goal="G-001", team="g-001-a", pane_id="p-tui", status="done")
        self.ok("config", "max_live_agents", "99", agent="owner")
        self.env.set_herdr(agents=[])
        p = self.ok(
            "spawn",
            "coder",
            "--goal",
            "G-001",
            "--team",
            "g-001-a",
            "--task",
            "T-0003",
            "--name",
            "zz-done-gate",
            agent="owner",
            extra_env={"FACTORY_LOADAVG": high_load},
        )
        self.assertIn("QUEUED", p.stdout)
        row = self.env.sql("SELECT status, pane_id FROM agents WHERE name='zz-done-gate'")[0]
        self.assertEqual(row["status"], "queued")
        queued = self.env.sql("SELECT action FROM events WHERE entity_id='zz-done-gate' AND action='queued'")
        self.assertEqual(len(queued), 1)
        reaped = self.env.sql("SELECT action FROM events WHERE entity_id='zz-done-gate' AND action='reaped'")
        self.assertEqual(reaped, [])
        close_calls = [c for c in self.env.calls("hc") if c["argv"][:2] == ["pane", "close"]]
        herdr_close = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["pane", "close"]]
        self.assertEqual(close_calls + herdr_close, [])

    def test_hc_relaunch_live_refused_keeps_pane_when_load_gate_closed(self):
        """hc backend: closed-gate relaunch of a live row queues without clearing pane_id."""
        high_load = str(10 * (os.cpu_count() or 1))
        self.ok("config", "terminal_backend", "hc", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_agent("zz-test-director", "director", goal="G-001", status="live")
        self.env.seed_agent(
            "zz-live-gate",
            "pm",
            goal="G-001",
            team="g-001-a",
            pane_id="p-tui",
            status="live",
        )
        self.env.sql("UPDATE agents SET session_id='sess-pm' WHERE name='zz-live-gate'")
        self.ok("config", "max_live_agents", "99", agent="owner")
        self.env.set_herdr(agents=[])
        p = self.ok(
            "relaunch",
            "zz-live-gate",
            "--force",
            agent="zz-test-director",
            extra_env={"FACTORY_LOADAVG": high_load},
        )
        self.assertIn("QUEUED", p.stdout)
        row = self.env.sql("SELECT status, pane_id FROM agents WHERE name='zz-live-gate'")[0]
        self.assertEqual(row["status"], "queued")
        queued = self.env.sql("SELECT action FROM events WHERE entity_id='zz-live-gate' AND action='queued'")
        self.assertEqual(len(queued), 1)
        close_calls = [c for c in self.env.calls("hc") if c["argv"][:2] == ["pane", "close"]]
        herdr_close = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["pane", "close"]]
        self.assertEqual(close_calls + herdr_close, [])

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

    def test_hc_tick_survives_ls_row_without_pane_id(self):
        """hc ls terminals may omit pane_id; the tick must not KeyError and must exit 0."""
        self.ok("config", "terminal_backend", "hc", agent="owner")
        self.env.set_herdr(agents=[{"name": "zz-hc-term", "agent": "claude", "agent_status": "idle"}])
        p = self.ok("tick", agent="owner")
        self.assertNotIn("herdr unreachable", p.stdout)

    def test_hc_tick_keeps_live_row_absent_from_hc_ls(self):
        """terminal_backend=hc: tick must not change agent rows when hc ls omits TUI panes."""
        self.ok("config", "terminal_backend", "hc", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_agent("zz-pm", "pm", goal="G-001", team="g-001-a", pane_id="p1", status="live")
        self.env.set_herdr(agents=[])
        p = self.ok("tick", agent="owner")
        self.assertEqual(p.returncode, 0, p.stderr)
        row = self.env.sql("SELECT status FROM agents WHERE name='zz-pm'")[0]
        self.assertEqual(row["status"], "live")
        lost = [e for e in self.env.sql("SELECT action FROM events WHERE entity_id='zz-pm' AND action='lost'")]
        self.assertEqual(lost, [])

    def test_hc_tick_keeps_done_tui_row_absent_from_hc_ls(self):
        """terminal_backend=hc: done rows stay done; no reap, pane close, or exited."""
        self.ok("config", "terminal_backend", "hc", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_agent("zz-done", "coder", goal="G-001", team="g-001-a", pane_id="p-tui", status="done")
        self.env.seed_agent("zz-live", "coder", goal="G-001", team="g-001-a", pane_id="p2", status="live")
        self.env.set_herdr(agents=[{"name": "zz-hc-term", "agent": "claude", "agent_status": "idle"}])
        p = self.ok("tick", agent="owner")
        self.assertEqual(p.returncode, 0, p.stderr)
        rows = {r["name"]: r["status"] for r in self.env.sql("SELECT name, status FROM agents")}
        self.assertEqual(rows["zz-done"], "done")
        self.assertEqual(rows["zz-live"], "live")
        exited_ev = [
            e for e in self.env.sql(
                "SELECT action FROM events WHERE entity_id='zz-done' AND action IN ('reaped','pane-close-skipped')"
            )
        ]
        self.assertEqual(exited_ev, [])
        close_calls = [c for c in self.env.calls("hc") if c["argv"][:2] == ["pane", "close"]]
        herdr_close = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["pane", "close"]]
        self.assertEqual(close_calls + herdr_close, [])

    def test_hc_spawn_refused_while_done_hc_name_still_on_ls(self):
        """hc-hosted done row still on hc ls: spawn must not reuse the name (release_stale_pane fails closed)."""
        self.ok("config", "terminal_backend", "hc", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_task("T-hc-name", goal="G-001", team="g-001-a", role="coder")
        self.env.seed_agent("zz-hc", "coder", goal="G-001", team="g-001-a", pane_id="zz-hc", status="done")
        self.env.sql(
            "UPDATE agents SET spawn_args=? WHERE name='zz-hc'",
            (json.dumps({"host_backend": "hc"}),),
        )
        self.env.set_herdr(agents=[{"name": "zz-hc", "agent": "claude", "agent_status": "idle"}])
        self.ok("config", "max_live_agents", "99", agent="owner")
        p = self.refused(
            1,
            "spawn",
            "coder",
            "--goal",
            "G-001",
            "--team",
            "g-001-a",
            "--task",
            "T-hc-name",
            "--name",
            "zz-hc",
            agent="owner",
        )
        self.assertIn("could not be confirmed closed", p.stderr + p.stdout)
        row = self.env.sql("SELECT status, pane_id FROM agents WHERE name='zz-hc'")[0]
        self.assertEqual(row["status"], "done")
        self.assertEqual(row["pane_id"], "zz-hc")
        hc_run = [c for c in self.env.calls("hc") if c["argv"][:2] == ["run", "--bg"]]
        self.assertEqual(hc_run, [])

    def test_hc_tick_kills_done_hc_row_still_on_ls(self):
        """hc-hosted done row listed on hc ls: tick hc-kills then reaps to exited."""
        self.ok("config", "terminal_backend", "hc", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_agent("zz-hc-done", "coder", goal="G-001", team="g-001-a", pane_id="zz-hc-done", status="done")
        self.env.sql(
            "UPDATE agents SET spawn_args=? WHERE name='zz-hc-done'",
            (json.dumps({"host_backend": "hc"}),),
        )
        self.env.set_herdr(agents=[{"name": "zz-hc-done", "agent": "claude", "agent_status": "idle"}])
        p = self.ok("tick", agent="owner")
        self.assertEqual(p.returncode, 0, p.stderr)
        row = self.env.sql("SELECT status, pane_id FROM agents WHERE name='zz-hc-done'")[0]
        self.assertEqual(row["status"], "exited")
        self.assertIsNone(row["pane_id"])
        kills = [c["argv"] for c in self.env.calls("hc") if c["argv"][:1] == ["kill"]]
        self.assertEqual(kills, [["kill", "zz-hc-done"]])
        reaped = self.env.sql("SELECT action FROM events WHERE entity_id='zz-hc-done' AND action='reaped'")
        self.assertEqual(len(reaped), 1)

    def test_hc_tick_reaps_done_hc_row_absent_from_ls(self):
        """hc-hosted done row absent from hc ls: tick marks exited without leaving done."""
        self.ok("config", "terminal_backend", "hc", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_agent("zz-hc-gone", "coder", goal="G-001", team="g-001-a", pane_id="zz-hc-gone", status="done")
        self.env.sql(
            "UPDATE agents SET spawn_args=? WHERE name='zz-hc-gone'",
            (json.dumps({"host_backend": "hc"}),),
        )
        self.env.set_herdr(agents=[])
        p = self.ok("tick", agent="owner")
        self.assertEqual(p.returncode, 0, p.stderr)
        row = self.env.sql("SELECT status, pane_id FROM agents WHERE name='zz-hc-gone'")[0]
        self.assertEqual(row["status"], "exited")
        self.assertIsNone(row["pane_id"])
        kills = [c for c in self.env.calls("hc") if c["argv"][:1] == ["kill"]]
        self.assertEqual(kills, [])
        reaped = self.env.sql("SELECT action FROM events WHERE entity_id='zz-hc-gone' AND action='reaped'")
        self.assertEqual(len(reaped), 1)

    def test_herdr_tick_reaps_done_row_when_host_lists_pane(self):
        """herdr backend: done row with pane on the host is closed and marked exited."""
        self.ok("config", "terminal_backend", "herdr", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_agent("zz-done", "coder", goal="G-001", team="g-001-a", pane_id="p-tui", status="done")
        self.env.set_herdr(agents=[{"name": "zz-done", "agent": "claude", "pane_id": "p-tui", "agent_status": "idle"}])
        p = self.ok("tick", agent="owner")
        self.assertEqual(p.returncode, 0, p.stderr)
        row = self.env.sql("SELECT status FROM agents WHERE name='zz-done'")[0]
        self.assertEqual(row["status"], "exited")
        reaped = [e for e in self.env.sql("SELECT action FROM events WHERE entity_id='zz-done' AND action='reaped'")]
        self.assertEqual(len(reaped), 1)
        closes = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["pane", "close"]]
        self.assertEqual(len(closes), 1)

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
