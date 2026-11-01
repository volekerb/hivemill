"""Subprocess tests for roles/hooks/cursor-guard-adapter.py (acceptance cases not in test_cursor_pilot.py)."""
import json
import os
import sys
import pwd
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from harness import REPO

HOOKS = REPO / "roles" / "hooks"
ADAPTER = HOOKS / "cursor-guard-adapter.py"


def _passwd_home() -> str:
    return pwd.getpwuid(os.getuid()).pw_dir


def _last_json(stdout: str) -> dict:
    line = stdout.strip().splitlines()[-1]
    return json.loads(line)


class AdapterSubprocessTests(unittest.TestCase):
    def setUp(self):
        self._tmpdir = tempfile.TemporaryDirectory(prefix="zz-cursor-adapter-")
        self.factory_home = self._tmpdir.name
        self.env = {**os.environ, "FACTORY_HOME": self.factory_home}

    def tearDown(self):
        self._tmpdir.cleanup()

    def run_adapter(self, event, payload, *, role="pm", adapter=None, stdin_text=None):
        argv = [sys.executable, str(adapter or ADAPTER), "--role", role, "--event", event]
        inp = stdin_text if stdin_text is not None else json.dumps(payload)
        return subprocess.run(
            argv,
            input=inp,
            capture_output=True,
            text=True,
            timeout=60,
            env=self.env,
        )

    def test_before_shell_blocks_sudo_id_exit_zero_deny(self):
        r = self.run_adapter("beforeShellExecution", {"command": "sudo id", "cwd": "/tmp"})
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 0)

    def test_pretooluse_shell_factory_tick_exit_two_deny(self):
        r = self.run_adapter(
            "preToolUse",
            {"tool_name": "Shell", "tool_input": {"command": "factory tick"}, "cwd": "/tmp"},
        )
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 2)

    def test_pretooluse_write_bashrc_file_path_denied(self):
        home = _passwd_home()
        r = self.run_adapter(
            "preToolUse",
            {"tool_name": "Write", "tool_input": {"file_path": f"{home}/.bashrc"}, "cwd": "/tmp"},
        )
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 2)

    def test_pretooluse_write_bashrc_path_key_denied(self):
        home = _passwd_home()
        r = self.run_adapter(
            "preToolUse",
            {"tool_name": "Write", "tool_input": {"path": f"{home}/.bashrc"}, "cwd": "/tmp"},
        )
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 2)

    def test_pretooluse_write_temp_file_path_allowed(self):
        r = self.run_adapter(
            "preToolUse",
            {
                "tool_name": "Write",
                "tool_input": {"file_path": "/tmp/cursor-ok.txt"},
                "cwd": "/tmp",
            },
        )
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "allow")
        self.assertEqual(r.returncode, 0)

    def test_pretooluse_write_temp_path_key_allowed(self):
        r = self.run_adapter(
            "preToolUse",
            {"tool_name": "Write", "tool_input": {"path": "/tmp/cursor-ok.txt"}, "cwd": "/tmp"},
        )
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "allow")
        self.assertEqual(r.returncode, 0)

    def test_unparseable_stdin_before_shell_exit_two_deny(self):
        r = self.run_adapter("beforeShellExecution", {}, stdin_text="not json")
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 2)

    def test_unparseable_stdin_pretooluse_exit_two_deny(self):
        r = self.run_adapter("preToolUse", {}, stdin_text="not json")
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 2)

    def test_unknown_event_exit_two_deny(self):
        r = self.run_adapter("afterShellExecution", {"command": "echo ok", "cwd": "/tmp"})
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 2)

    def test_missing_worker_guard_exit_two_deny(self):
        with tempfile.TemporaryDirectory(prefix="zz-adapter-alone-") as alone:
            lone = Path(alone) / "cursor-guard-adapter.py"
            shutil.copy2(ADAPTER, lone)
            r = self.run_adapter(
                "preToolUse",
                {"tool_name": "Shell", "tool_input": {"command": "echo ok"}, "cwd": "/tmp"},
                adapter=lone,
            )
        body = _last_json(r.stdout)
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 2)


if __name__ == "__main__":
    unittest.main()
