"""T-0675 (pilot test-first, T-0704): worker-guard routes bare test runners in coder/qa panes to `factory test`
when config test_sandbox=1. Hook JSON on stdin, role via --role, temp FACTORY_HOME with its own DB."""
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
RUNNERS = (
    "python3 -m unittest tests.test_y -v",
    "python -m unittest tests.test_x",
    "python3 -m pytest tests/test_x.py -q",
    "pytest -q tests",
    "go test ./...",
)
SHAPES = (
    "{r}",
    "FOO=1 {r}",
    "env X=1 {r}",
    "timeout 60 {r}",
    "bash -c '{r}'",
    "sh -c '{r}'",
    "cd x && {r}",
    "cd x ; {r}",
    "echo hi ; {r}",
)
OTHER_ROLES = ("reviewer", "judge", "lead", "pm", "cleaner")
NON_RUNNERS = ("grep -n unittest x.py", "git log --grep pytest", "echo go test", "grep -rn 'python3 -m pytest' docs",
               "cat tests/test_pytest_helper.py")


class TestSandboxRoutingTests(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-tsb-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.live = os.path.join(self.tmp, "live")
        self.scratch = os.path.join(self.tmp, "scratch")
        os.makedirs(self.live)
        os.makedirs(self.scratch)

    def make_home(self, value):
        """Temp FACTORY_HOME whose DB holds config test_sandbox=<value>; None leaves the key unset."""
        home = os.path.join(self.tmp, "home")
        os.makedirs(os.path.join(home, "db"), exist_ok=True)
        con = sqlite3.connect(os.path.join(home, "db", "factory.db"))
        con.execute("CREATE TABLE IF NOT EXISTS config (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
        con.execute("CREATE TABLE IF NOT EXISTS agents (name TEXT PRIMARY KEY, goal_id TEXT)")
        if value is not None:
            con.execute("INSERT OR REPLACE INTO config VALUES ('test_sandbox', ?)", (value,))
        con.commit()
        con.close()
        return home

    def run_hook(self, cmd, role, home):
        env = {k: v for k, v in os.environ.items()
               if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH", "FACTORY_HOME")}
        env.update(WORKER_GUARD_LIVE_HOME=self.live, WORKER_GUARD_SCRATCH=self.scratch, FACTORY_HOME=home,
                   FACTORY_AGENT="zz-agent")
        payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.scratch}
        return subprocess.run([sys.executable, HOOK, "--role", role], input=json.dumps(payload),
                              capture_output=True, text=True, timeout=30, env=env)

    def assert_denied(self, cmd, role, home):
        p = self.run_hook(cmd, role, home)
        self.assertEqual(p.returncode, 2, f"should deny ({role}): {cmd!r}\nstderr={p.stderr}")
        self.assertIn("factory test", p.stderr, f"deny message must name `factory test`: {cmd!r}\n{p.stderr}")

    def assert_allowed(self, cmd, role, home):
        p = self.run_hook(cmd, role, home)
        self.assertEqual(p.returncode, 0, f"should allow ({role}): {cmd!r}\nstderr={p.stderr}")

    # item 1
    def test_item1_bare_runners_denied_for_coder_and_qa_when_enabled(self):
        home = self.make_home("1")
        for role in ("coder", "qa"):
            for r in RUNNERS:
                for s in SHAPES:
                    self.assert_denied(s.format(r=r), role, home)

    # item 2
    def test_item2_factory_test_wrapper_allowed_when_enabled(self):
        home = self.make_home("1")
        for role in ("coder", "qa"):
            for r in RUNNERS:
                self.assert_allowed(f"factory test {r}", role, home)
                self.assert_allowed(f"cd x && factory test {r}", role, home)

    # item 3
    def test_item3_unchanged_when_flag_zero(self):
        home = self.make_home("0")
        for role in ("coder", "qa") + OTHER_ROLES:
            for r in RUNNERS:
                self.assert_allowed(r, role, home)

    def test_item3_unchanged_when_flag_unset(self):
        home = self.make_home(None)
        for role in ("coder", "qa") + OTHER_ROLES:
            for r in RUNNERS:
                self.assert_allowed(r, role, home)

    def test_item3_unchanged_when_config_unreadable(self):
        missing = os.path.join(self.tmp, "nohome")  # no DB at all
        garbage = os.path.join(self.tmp, "garbage")
        os.makedirs(os.path.join(garbage, "db"))
        with open(os.path.join(garbage, "db", "factory.db"), "w") as f:
            f.write("not a sqlite database")
        notable = os.path.join(self.tmp, "notable")
        os.makedirs(os.path.join(notable, "db"))
        sqlite3.connect(os.path.join(notable, "db", "factory.db")).close()  # valid DB, no config table
        for home in (missing, garbage, notable):
            for role in ("coder", "qa"):
                for r in RUNNERS:
                    self.assert_allowed(r, role, home)

    # item 4
    def test_item4_other_roles_not_newly_denied_when_enabled(self):
        home = self.make_home("1")
        for role in OTHER_ROLES:
            for r in RUNNERS:
                self.assert_allowed(r, role, home)

    # item 5
    def test_item5_commands_merely_mentioning_runner_words_not_denied(self):
        home = self.make_home("1")
        for role in ("coder", "qa"):
            for cmd in NON_RUNNERS:
                p = self.run_hook(cmd, role, home)
                self.assertNotIn("factory test", p.stderr, f"{role}: {cmd!r} wrongly routed\n{p.stderr}")
                self.assertEqual(p.returncode, 0, f"{role}: {cmd!r} should be allowed\n{p.stderr}")


if __name__ == "__main__":
    unittest.main()
