"""T-1097: in `git config <value option> --get al*` the action flag is the VALUE of the option (git writes: key al..., value
the next match), so the T-1063 glob check must not treat it as a read action. Refused for coder, judge and cleaner (exit 0
on origin/main); reviewer, qa and pm already refuse (unchanged). Literal reads stay allowed. Hook probes run with cwd in a
LINKED worktree built here."""
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
TASK = "t-1097"
WRITERS = ("coder", "judge", "cleaner")
OTHERS = ("reviewer", "qa", "pm")
TEXT = "is built from an expansion, so it could be a write; write the arguments literally (T-1063)"

# origin/main exit 0 for the writers (measured, task note), exit 2 on this head
TIGHTENED = [
    "git config --comment --get al*", "git config --comment --list al*", "git config --comment --get-all al*",
    "git config --file --get al*", "git config -f --get al*", "git config --type --get al*",
    "git config -t --get al*", "git config --default --get al*", "git config --blob --get al*",
    "git config --value --get al*", "git config --fil --get al*",
    "git config --comment --get a[l]*", "git config --comment --get a?ias.x",
]
# refused for every role by the older parser check (unknown option or abbreviation), kept refused
OLDER = ["git config --com --get al*", "git config --unknown-opt --get al*"]
READS = [
    "git config --get alias.x", "git config --get al*", "git config --comment c --get alias.x",
    "git config --file f --get a.b", "git config --comment c --get al*", "git config --file=f --get al*",
    "git config --type bool --get core.bare",
]


class ConfigValueGlobTests(unittest.TestCase):
    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = os.path.realpath(td.name)
        self.home = os.path.join(self.tmp, "factory")
        for d in (os.path.join(self.home, "roles"), os.path.join(self.home, "db")):
            os.makedirs(d)
        self.main = os.path.join(self.home, "main-checkout")
        self.wt = os.path.join(self.home, "worktrees", "G-010-T-1097")
        os.makedirs(self.main)
        os.makedirs(os.path.dirname(self.wt))
        git("init", "-q", "-b", "main", self.main, cwd=self.tmp)
        git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i", cwd=self.main)
        git("worktree", "add", "-q", "-b", "factory/t-1097", self.wt, cwd=self.main)
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

    def check(self, cmds, code, roles, text=None):
        for role in roles:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    p = self.run_hook(c, role)
                    self.assertEqual(p.returncode, code, p.stderr)
                    if text:
                        self.assertIn(text, p.stderr)

    def test_linked_worktree_fixture(self):
        self.assertTrue(os.path.isfile(os.path.join(self.wt, ".git")), "cwd must be a linked worktree")

    def test_value_option_then_action_flag_then_glob_refused_for_writers(self):
        self.check(TIGHTENED, 2, WRITERS, text=TEXT)

    def test_abbreviation_and_unknown_option_stay_refused(self):
        self.check(OLDER, 2, WRITERS + OTHERS)

    def test_other_roles_exit_as_on_main(self):
        self.check(TIGHTENED, 2, OTHERS)

    def test_literal_reads_allowed_for_writers(self):
        self.check(READS, 0, WRITERS)

    def test_literal_reads_allowed_for_other_roles(self):
        self.check(READS, 0, OTHERS)


if __name__ == "__main__":
    unittest.main()
