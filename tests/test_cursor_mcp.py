"""T-0828: beforeMCPExecution hook + preToolUse StrReplace/EditNotebook guard parity."""
import importlib
import importlib.util
import json
import os
import pwd
import subprocess
import sys
import tempfile
import unittest
from io import StringIO
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from harness import REPO
from guard_impl import hook_run

HOOKS = REPO / "roles" / "hooks"
ADAPTER = HOOKS / "cursor-guard-adapter.py"

MCP_PAYLOAD = {
    "tool_name": "mcp_auth",
    "tool_input": "{}",
    "mcp_server_name": "plugin-higgsfield",
    "cwd": "/tmp",
}

MATCHER = "Shell|Write|Delete|Read|Grep|StrReplace|EditNotebook"


def load_harness():
    return importlib.import_module("scout.cursor_harness")


def load_adapter_module():
    spec = importlib.util.spec_from_file_location("cursor_guard_adapter_t0828", ADAPTER)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


def _passwd_home() -> str:
    return pwd.getpwuid(os.getuid()).pw_dir


def _last_json(stdout: str) -> dict:
    line = stdout.strip().splitlines()[-1]
    return json.loads(line)


class CursorMcpHookTests(unittest.TestCase):
    def setUp(self):
        self._tmpdir = tempfile.TemporaryDirectory(prefix="zz-cursor-mcp-")
        self.factory_home = self._tmpdir.name
        self.env = {**os.environ, "FACTORY_HOME": self.factory_home}

    def tearDown(self):
        self._tmpdir.cleanup()

    def run_adapter(self, event, payload, *, role="pm", hq=False, stdin_text=None):
        argv = [sys.executable, str(ADAPTER), "--event", event]
        if hq:
            argv.append("--hq")
        argv.extend(["--role", role])
        inp = stdin_text if stdin_text is not None else json.dumps(payload)
        return subprocess.run(
            argv,
            input=inp,
            capture_output=True,
            text=True,
            timeout=60,
            env=self.env,
        )

    def test_works_matrix_mcp_item_is_works(self):
        h = load_harness()
        row = next(r for r in h.works_needs_work() if r["item"] == "MCP strict empty (user MCP blind)")
        self.assertEqual(row["status"], "WORKS")
        self.assertIn("beforeMCPExecution", row["detail"])

    def test_cursor_hooks_json_has_before_mcp_fail_closed(self):
        h = load_harness()
        hooks = h.cursor_hooks_json(HOOKS, "coder")
        self.assertIn("beforeMCPExecution", hooks["hooks"])
        entries = hooks["hooks"]["beforeMCPExecution"]
        self.assertEqual(len(entries), 1)
        self.assertTrue(entries[0].get("failClosed"))
        self.assertIn("beforeMCPExecution", entries[0]["command"])
        self.assertIn("--event", entries[0]["command"])
        for _event, hook_entries in hooks["hooks"].items():
            for entry in hook_entries:
                self.assertTrue(entry.get("failClosed"), entry)

    def test_materialized_policy_before_mcp_command(self):
        h = load_harness()
        settings = {"permissions": {"deny": ["Bash(sudo:*)"], "allow": []}}
        with tempfile.TemporaryDirectory(prefix="zz-cursor-mcp-mat-") as tmp:
            h.materialize_cursor_policy(
                tmp,
                "coder",
                hooks_dir=HOOKS,
                claude_settings=settings,
                system_prompt="# coder\n",
            )
            hooks = json.loads((Path(tmp) / ".cursor" / "hooks.json").read_text())
        cmd = hooks["hooks"]["beforeMCPExecution"][0]["command"]
        self.assertIn("beforeMCPExecution", cmd)
        matcher = hooks["hooks"]["preToolUse"][0]["matcher"]
        self.assertEqual(matcher, MATCHER)

    def test_non_boss_roles_deny_mcp_exit_two(self):
        for role in ("pm", "coder", "lead"):
            with self.subTest(role=role):
                hq = role in load_harness().HQ_ROLES
                r = self.run_adapter("beforeMCPExecution", MCP_PAYLOAD, role=role, hq=hq)
                body = _last_json(r.stdout)
                self.assertEqual(body["permission"], "deny")
                self.assertIn("MCP disabled for factory role", body.get("agent_message", ""))
                self.assertIn(role, body.get("agent_message", ""))
                self.assertEqual(r.returncode, 2)

    def test_boss_mcp_matches_worker_guard_and_invokes_guard(self):
        adapter_mod = load_adapter_module()
        guard_payload = adapter_mod.mcp_to_guard_payload(MCP_PAYLOAD)
        guard_run = hook_run(json.dumps(guard_payload), ["--hq"], env=self.env, timeout=60)
        calls: list[tuple] = []

        def fake_run_guard(payload, role, hq):
            calls.append((payload, role, hq))
            return guard_run

        with mock.patch.object(adapter_mod, "run_guard", side_effect=fake_run_guard):
            with mock.patch("sys.stdin", StringIO(json.dumps(MCP_PAYLOAD))):
                rc = adapter_mod.main(["--hq", "--role", "boss", "--event", "beforeMCPExecution"])

        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0][1], "boss")
        self.assertTrue(calls[0][2])
        if guard_run.returncode == 0:
            self.assertEqual(rc, 0)
        else:
            self.assertEqual(rc, 2)

        r = self.run_adapter("beforeMCPExecution", MCP_PAYLOAD, role="boss", hq=True)
        if guard_run.returncode == 0:
            self.assertEqual(_last_json(r.stdout)["permission"], "allow")
            self.assertEqual(r.returncode, 0)
        else:
            self.assertEqual(_last_json(r.stdout)["permission"], "deny")
            self.assertEqual(r.returncode, 2)

    def test_before_mcp_malformed_stdin_exit_two_deny(self):
        r = self.run_adapter("beforeMCPExecution", {}, stdin_text="not json")
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 2)

    def test_pretooluse_strreplace_bashrc_denied(self):
        home = _passwd_home()
        r = self.run_adapter(
            "preToolUse",
            {
                "tool_name": "StrReplace",
                "tool_input": {"file_path": f"{home}/.bashrc", "old_string": "a", "new_string": "b"},
                "cwd": "/tmp",
            },
        )
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 2)

    def test_pretooluse_strreplace_temp_allowed(self):
        r = self.run_adapter(
            "preToolUse",
            {
                "tool_name": "StrReplace",
                "tool_input": {"file_path": "/tmp/cursor-ok.txt", "old_string": "a", "new_string": "b"},
                "cwd": "/tmp",
            },
        )
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "allow")
        self.assertEqual(r.returncode, 0)

    def test_pretooluse_strreplace_decoy_file_path_real_path_denied(self):
        home = _passwd_home()
        r = self.run_adapter(
            "preToolUse",
            {
                "tool_name": "StrReplace",
                "tool_input": {
                    "file_path": "/tmp/cursor-ok.txt",
                    "path": f"{home}/.bashrc",
                    "old_string": "a",
                    "new_string": "b",
                },
                "cwd": "/tmp",
            },
        )
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 2)

    def test_pretooluse_editnotebook_decoy_file_path_target_denied(self):
        home = _passwd_home()
        r = self.run_adapter(
            "preToolUse",
            {
                "tool_name": "EditNotebook",
                "tool_input": {
                    "file_path": "/tmp/cursor-ok.ipynb",
                    "target_notebook": f"{home}/.bashrc",
                },
                "cwd": "/tmp",
            },
        )
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 2)

    def test_before_mcp_hook_required_in_policy(self):
        """C4: drop beforeMCPExecution from cursor_hooks_json and this test fails."""
        h = load_harness()
        self.assertIn("beforeMCPExecution", h.cursor_hooks_json(HOOKS, "coder")["hooks"])


if __name__ == "__main__":
    unittest.main()
