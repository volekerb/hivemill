"""T-1035: `git config --comment --get alias.x push` (and `set --value --get`) is a WRITE: the value-taking option eats
--get. Read-only roles and the coder must be refused; real reads stay allowed. Defense in depth; the sandbox is the boundary."""
import json
import os
import sqlite3
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402
from harness import git  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"
TASK = "t-1035"

ROLES = ("coder", "reviewer", "qa", "pm")
DENIED = [
    "git config --comment --get alias.x push",
    "git config --comm --get alias.x push",
    "git config --comment --get-all alias.x push",
    "git config --comment --list alias.x push",
    "git config set --value --get alias.y push",
    "git config --value --get alias.y push",
    "git config --default --get alias.x push",
    "git config --type --get alias.x push",
]
CODER_ONLY_DENIED = [
    "echo alias.x push | xargs git config --comment --get",
    "echo alias.y push | xargs git config set --value --get",
]
ALLOWED = ["git config --get alias.x", "git config --get user.name", "git config --list", "git config --type bool --get core.bare",
           "git config --get-all --default x user.name", "git config --file f --get a.b"]


class ConfigValueOptTests(unittest.TestCase):
    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = os.path.realpath(td.name)
        self.home = os.path.join(self.tmp, "factory")
        self.wt = os.path.join(self.home, "worktrees", "G-010-T-1035")
        for d in (os.path.join(self.home, ".git"), os.path.join(self.home, "roles"), os.path.join(self.home, "db"), self.wt):
            os.makedirs(d)
        git("init", "-q", "-b", "main", self.wt, cwd=self.tmp)
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE tasks (id TEXT PRIMARY KEY, worktree TEXT)")
        con.execute("INSERT INTO tasks VALUES (?, ?)", (TASK.upper(), self.wt))
        con.commit()
        con.close()

    def run_hook(self, cmd, role):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home,
                   FACTORY_AGENT=f"coder-{TASK}" if role == "coder" else f"{role}-x")
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.wt})
        return hook_run(payload, ["--role", role], env=env)

    def test_value_option_write_denied(self):
        for role in ROLES:
            for cmd in DENIED + (CODER_ONLY_DENIED if role == "coder" else []):
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 2, p.stderr)
                    self.assertTrue(p.stderr.strip(), "refusal needs a message")

    def test_real_reads_allowed(self):
        for role in ROLES:
            for cmd in ALLOWED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)


if __name__ == "__main__":
    unittest.main()
