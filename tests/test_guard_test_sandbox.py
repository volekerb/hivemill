"""T-0675: extra worker-guard cases for test_sandbox routing (the pilot file covers acceptance 1-5)."""
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")


class TestSandboxRoutingExtra(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-tsb2-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.scratch = os.path.join(self.tmp, "scratch")
        os.makedirs(self.scratch)
        self.home = os.path.join(self.tmp, "home")
        os.makedirs(os.path.join(self.home, "db"))

    def seed(self, value):
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
        con.execute("INSERT OR REPLACE INTO config VALUES ('test_sandbox', ?)", (value,))
        con.commit()
        con.close()

    def run_hook(self, cmd, role="coder"):
        env = {k: v for k, v in os.environ.items() if k not in ("FACTORY_HOME", "WORKER_GUARD_SCRATCH")}
        env.update(WORKER_GUARD_LIVE_HOME=os.path.join(self.tmp, "live"), WORKER_GUARD_SCRATCH=self.scratch,
                   FACTORY_HOME=self.home, FACTORY_AGENT="zz-agent")
        payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.scratch}
        return subprocess.run([sys.executable, HOOK, "--role", role], input=json.dumps(payload),
                              capture_output=True, text=True, timeout=30, env=env)

    def test_deny_message_suggests_same_command(self):
        self.seed("1")
        p = self.run_hook("go test ./pkg/...")
        self.assertEqual(p.returncode, 2)
        self.assertIn("factory test go test ./pkg/...", p.stderr)

    def test_pytest_dash_m_attached_form_denied(self):
        self.seed("1")
        self.assertEqual(self.run_hook("python3 -mpytest tests").returncode, 2)

    def test_other_go_and_python_commands_allowed(self):
        self.seed("1")
        for cmd in ("go build ./...", "go vet ./...", "python3 -m json.tool x.json", "python3 script.py"):
            self.assertEqual(self.run_hook(cmd).returncode, 0, cmd)

    def test_flag_value_other_than_one_does_not_route(self):
        for v in ("2", "true", ""):
            self.seed(v)
            self.assertEqual(self.run_hook("pytest -q").returncode, 0, v)


if __name__ == "__main__":
    unittest.main()
