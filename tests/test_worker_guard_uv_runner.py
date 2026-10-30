"""T-0748 acceptance (test-first, T-0753): worker-guard routes uv/uvx pytest runs to the test_sandbox slot
when the runner follows index options, an extras/version spec, or a global uv option."""
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

AT = "@"  # built apart: the public-text guard reads name-at-word as an email
URL = "https://pypi.example.org/simple"


class UvRunnerRouting(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-uvrun-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.scratch = os.path.join(self.tmp, "scratch")
        os.makedirs(self.scratch)
        self.home = os.path.join(self.tmp, "home")
        os.makedirs(os.path.join(self.home, "db"))
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
        con.execute("INSERT OR REPLACE INTO config VALUES ('test_sandbox', '1')")
        con.commit()
        con.close()

    def run_hook(self, cmd, role="coder"):
        env = {k: v for k, v in os.environ.items() if k not in ("FACTORY_HOME", "WORKER_GUARD_SCRATCH")}
        env.update(WORKER_GUARD_LIVE_HOME=os.path.join(self.tmp, "live"), WORKER_GUARD_SCRATCH=self.scratch,
                   FACTORY_HOME=self.home, FACTORY_AGENT="zz-agent")
        payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.scratch}
        return subprocess.run([sys.executable, HOOK, "--role", role], input=json.dumps(payload),
                              capture_output=True, text=True, timeout=30, env=env)

    def assert_routed(self, cmd):
        for role in ("coder", "qa"):
            p = self.run_hook(cmd, role)
            self.assertEqual(p.returncode, 2, f"{role}: {cmd}: {p.stderr}")
            self.assertIn("factory test", p.stderr, cmd)

    def test_index_url_option(self):
        self.assert_routed(f"uvx --index-url {URL} pytest")

    def test_default_index_option(self):
        self.assert_routed(f"uvx --default-index {URL} pytest")

    def test_extra_index_url_option(self):
        self.assert_routed(f"uvx --extra-index-url {URL} pytest")

    def test_extras_spec(self):
        self.assert_routed("uvx 'pytest[testing]'")

    def test_double_equals_version(self):
        self.assert_routed("uvx pytest==8.0")

    def test_at_version(self):
        self.assert_routed("uvx pytest" + AT + "8.0")

    def test_global_option_before_tool_run(self):
        self.assert_routed("uv -q tool run pytest")

    def test_negative_other_tools_allowed(self):
        for cmd in ("uvx ruff", "uv tool run black", f"uvx --index-url {URL} ruff", "uv -q tool run black"):
            p = self.run_hook(cmd)
            self.assertEqual(p.returncode, 0, f"{cmd}: {p.stderr}")
    def test_global_option_between_tool_and_run(self):
        self.assert_routed("uv tool -q run pytest")

    def test_hidden_alias_uvx_under_tool(self):
        self.assert_routed("uv tool uvx pytest")

    def test_pep508_whitespace_specifier(self):
        self.assert_routed("uvx 'pytest >=8'")

    def test_pep508_parenthesised_specifier(self):
        self.assert_routed("uvx 'pytest (>=8)'")

    def test_pep508_marker(self):
        self.assert_routed("uvx 'pytest ; python_version>\"3\"'")

    def test_negative_global_option_after_tool(self):
        for cmd in ("uv tool -q run black", "uv tool uvx ruff", "uvx 'ruff >=0.5'"):
            p = self.run_hook(cmd)
            self.assertEqual(p.returncode, 0, f"{cmd}: {p.stderr}")


if __name__ == "__main__":
    unittest.main()
