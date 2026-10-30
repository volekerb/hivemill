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

    def test_dash_m_in_short_flag_cluster_denied(self):
        self.seed("1")
        for cmd in ("python3 -Im pytest x", "python3 -Bm unittest tests.test_x", "python3 -IBm pytest x",
                    "python3 -Impytest x"):
            self.assertEqual(self.run_hook(cmd).returncode, 2, cmd)

    BLOCK = ("-m pytest", "-mpytest", "-Im pytest", "-IBmpytest", "-OO -m pytest", "-bb -m unittest tests.t",
             "-m pytest.__main__", "-m unittest discover -s tests -p test_x.py", "-W error -m pytest", "-Werror -m pytest",
             "-IW error -m pytest", "-X dev -m pytest", "-Xdev -m unittest tests.t", "-W a -X b -m pytest",
             "-u -W ignore -m unittest tests.t", "--check-hash-based-pycs always -m pytest",
             "--check-hash-based-pycs=always -m pytest", "-Z -m pytest", "--bogus -m pytest")
    ALLOW = ("script.py -m pytest", "- -m pytest", "-- script.py -m pytest", '-c "pass" -m pytest',
             "-W -m pytest", "-Wm pytest", "-m pip", "-m pytestish", "-I -m mypkg")

    def test_python_argv_walk_blocked(self):
        self.seed("1")
        for args in self.BLOCK:
            self.assertEqual(self.run_hook("python3 " + args).returncode, 2, args)
        self.assertEqual(self.run_hook('bash -c "python3 -X dev -m pytest"').returncode, 2)

    def test_python_argv_walk_allowed(self):
        self.seed("1")
        for args in self.ALLOW:
            self.assertEqual(self.run_hook("python3 " + args).returncode, 0, args)

    def test_python_argv_walk_off_or_other_role_allowed(self):
        self.seed("0")
        for args in self.BLOCK:
            self.assertEqual(self.run_hook("python3 " + args).returncode, 0, args)
        self.seed("1")
        for args in self.BLOCK:
            self.assertEqual(self.run_hook("python3 " + args, role="reviewer").returncode, 0, args)

    def test_dash_m_after_script_is_script_arg(self):
        self.seed("1")
        self.assertEqual(self.run_hook("python3 script.py -m pytest").returncode, 0)

    def test_other_go_and_python_commands_allowed(self):
        self.seed("1")
        for cmd in ("go build ./...", "go vet ./...", "python3 -m json.tool x.json", "python3 script.py"):
            self.assertEqual(self.run_hook(cmd).returncode, 0, cmd)

    def test_flag_value_other_than_one_does_not_route(self):
        for v in ("2", "true", ""):
            self.seed(v)
            self.assertEqual(self.run_hook("pytest -q").returncode, 0, v)

    # T-0735
    DENY = ("python3 -W error -m unittest", "python3 -X dev -m unittest", "python3 -Im unittest",
            "python3 -Werror -m unittest", "python3 --check-hash-based-pycs always -m unittest",
            "go -C sub test ./...", "go -C sub -v test ./...",
            "coverage run -m pytest", "coverage run -m unittest tests.test_x", "coverage run --branch -m pytest x",
            "uv run pytest", "uv run --with foo pytest tests", "uv run python -m pytest", "uv run python3 -X dev -m pytest",
            "tox", "tox -e py3")
    ALLOW_ON = ("go -C sub build ./...", "coverage report", "coverage run script.py", "uv run script.py",
                "uv run python script.py", "uv sync", "python3 -W error -m pip list")

    def test_t0735_shapes_denied_with_message(self):
        self.seed("1")
        for cmd in self.DENY:
            for role in ("coder", "qa"):
                p = self.run_hook(cmd, role=role)
                self.assertEqual(p.returncode, 2, (role, cmd))
                self.assertIn("factory test", p.stderr, cmd)

    def test_t0735_shapes_allowed_when_flag_off(self):
        self.seed("0")
        for cmd in self.DENY:
            if "unittest" in cmd and cmd.startswith("python3"):
                continue  # unittest_full (T-0642) is independent of the flag
            self.assertEqual(self.run_hook(cmd).returncode, 0, cmd)

    def test_t0735_unrelated_allowed_when_flag_on(self):
        self.seed("1")
        for cmd in self.ALLOW_ON:
            self.assertEqual(self.run_hook(cmd).returncode, 0, cmd)

    def test_unittest_full_option_values(self):
        self.seed("0")
        for cmd in ("python3 -W error -m unittest", "python3 -X dev -m unittest", "python3 -Im unittest"):
            p = self.run_hook(cmd)
            self.assertEqual(p.returncode, 2, cmd)
            self.assertIn("full suite", p.stderr, cmd)
        for cmd in ("python3 -W error -m unittest tests.test_x", "python3 -X dev -m unittest discover -s tests -p test_x.py"):
            self.assertEqual(self.run_hook(cmd).returncode, 0, cmd)

    # T-0745: one test per form, same classification as T-0735
    def _denied(self, *cmds):
        self.seed("1")
        for cmd in cmds:
            p = self.run_hook(cmd)
            self.assertEqual(p.returncode, 2, cmd)
            self.assertIn("factory test", p.stderr, cmd)

    def test_t0745_coverage_run_long_module_option(self):
        self._denied("coverage run --module pytest", "coverage run --branch --module unittest tests.test_x",
                     "coverage run --module=pytest x")
        self.seed("1")
        self.assertEqual(self.run_hook("coverage run --module http.server").returncode, 0)

    def test_t0745_python_m_coverage_run_m(self):
        self._denied("python3 -m coverage run -m pytest", "python3 -m coverage run --module unittest tests.test_x",
                     "python3 -W error -m coverage run -m pytest")
        self.seed("1")
        for cmd in ("python3 -m coverage report", "python3 -m coverage run script.py"):
            self.assertEqual(self.run_hook(cmd).returncode, 0, cmd)

    def test_t0745_uvx_and_uv_tool_run_pytest(self):
        self._denied("uvx pytest", "uvx pytest -q tests", "uv tool run pytest", "uv tool run --with foo pytest tests")
        self.seed("1")
        for cmd in ("uvx ruff check .", "uv tool run ruff check .", "uv tool list"):
            self.assertEqual(self.run_hook(cmd).returncode, 0, cmd)

    def test_t0745_go_double_dash_C(self):
        self._denied("go --C sub test ./...", "go --C sub -v test ./...")
        self.seed("1")
        self.assertEqual(self.run_hook("go --C sub build ./...").returncode, 0)


if __name__ == "__main__":
    unittest.main()
