"""T-1064 v3: the git_persist strip loop is order-aware. After a bare key git's legacy parser takes every token as the
VALUE, so `--global`/`--file` there is not a scope option (it must not be stripped into a "read"); a value option's value
(`--comment --global k v`) is not a scope token either; a leading subcommand word (`set --global k v`) is not a bare key.
Hook probes run with cwd in a LINKED worktree (the shared-repo rule fires there; /tmp skips it)."""
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
TASK = "t-1064"
WRITERS = ("coder", "judge", "cleaner")

# acceptance 10: tightened, all exit 2
TIGHTENED = [
    "alias.y --global", "alias.y --system", "alias.y --glob", "core.hooksPath --global", "core.fsmonitor --system",
    "alias.y --file=x", "alias.y -f x", "alias.y -fx", "core.hooksPath --file=x", "--global core.hooksPath --global",
    "--global core.hooksPath --file=x", "--global core.fsmonitor -f x", "--global alias.y --system",
    "--comment --global core.hooksPath /tmp/x", "--comment --glob core.hooksPath /tmp/x", "--comment -fx core.hooksPath /tmp/x",
    "--type --global core.hooksPath /tmp/x", "--default --global core.hooksPath /tmp/x",
]
# acceptance 11: still refused
STILL_REFUSED = [
    "set --global core.hooksPath /tmp/x", "--add --global core.hooksPath /tmp/x", "--global core.hooksPath /tmp/x",
    "--file=~/.config/git/config core.hooksPath /tmp/x", "--replace-all core.hooksPath /tmp/x --global",
    "a.b c --global",  # moved from test_worker_guard_persist: still refused, text is now the repo-config one (Go mirror: G-016)
    "unset --global core.hooksPath", "set --system core.hooksPath /tmp/x", "set -f x core.hooksPath /tmp/x",
]
# acceptance 12: reads stay reads (exit 0 for the coder)
READS = ["-fx --get k", "--glob --get k", "--get k --glob", "get --glob k", "list -fx", "--get alias.x --show-origin",
         "list --show-origin", "get --all alias.x", "--get alias.x --local", "--glob k", "-fx k"]
# acceptance 15: after key and value, a scope/file token is part of git's legacy value pattern, never a scope option
SCRATCH_PREFIXES = ["", "--replace-all ", "--rep ", "--fixed-value ", "--no-type ", "-z "]
SCRATCH_TOKENS = ["--file=<S>/y", "-f<S>/y"]


class ConfigOrderTests(unittest.TestCase):
    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = os.path.realpath(td.name)
        self.home = os.path.join(self.tmp, "factory")
        for d in (os.path.join(self.home, "roles"), os.path.join(self.home, "db")):
            os.makedirs(d)
        self.main = os.path.join(self.home, "main-checkout")
        self.wt = os.path.join(self.home, "worktrees", "G-010-T-1064")
        os.makedirs(self.main)
        os.makedirs(os.path.dirname(self.wt))
        git("init", "-q", "-b", "main", self.main, cwd=self.tmp)
        git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i", cwd=self.main)
        git("worktree", "add", "-q", "-b", "factory/t-1064", self.wt, cwd=self.main)
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE tasks (id TEXT PRIMARY KEY, worktree TEXT)")
        con.execute("INSERT INTO tasks VALUES (?, ?)", (TASK.upper(), self.wt))
        con.commit()
        con.close()

    def run_hook(self, cmd, role):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home,
                   FACTORY_AGENT=f"coder-{TASK}" if role == "coder" else f"{role}-x")
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": "git config " + cmd}, "cwd": self.wt})
        return hook_run(payload, ["--role", role], env=env)

    def test_linked_worktree_fixture(self):
        self.assertTrue(os.path.isfile(os.path.join(self.wt, ".git")), "cwd must be a linked worktree")
        p = self.run_hook("core.hooksPath /tmp/x", "coder")
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("repo config", p.stderr)  # the shared-repo rule fires here

    def check(self, forms, code, roles=WRITERS):
        for role in roles:
            for f in forms:
                with self.subTest(role=role, form=f):
                    p = self.run_hook(f, role)
                    self.assertEqual(p.returncode, code, p.stderr)

    def test_tightened_forms_refused(self):
        self.check(TIGHTENED, 2)

    def test_scope_and_file_writes_still_refused(self):
        self.check(STILL_REFUSED, 2)

    def test_abbreviated_value_option_in_scratch_clone(self):
        # git takes `--typ bool` as --type; its value is not a bare key, so a later --global is a real scope option.
        # In a scratchpad clone (own .git) only the scope refusal stops the write, so probe there.
        scratch = os.path.join(self.tmp, "scratch")
        clone = os.path.join(scratch, "clone")
        os.makedirs(clone)
        git("init", "-q", "-b", "main", clone, cwd=self.tmp)
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home,
                   WORKER_GUARD_SCRATCH=scratch, FACTORY_AGENT=f"coder-{TASK}")
        for f in ("--typ bool --global core.hooksPath /tmp/x", "--def x --global core.hooksPath /tmp/x",
                  "--comm x --global core.hooksPath /tmp/x", "--blo x --glob core.hooksPath /tmp/x",
                  "--type bool --global core.hooksPath /tmp/x", "--add --global core.hooksPath /tmp/x"):
            with self.subTest(form=f):
                payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": "git config " + f}, "cwd": clone})
                p = hook_run(payload, ["--role", "coder"], env=env)
                self.assertEqual(p.returncode, 2, p.stderr)
    def test_scratch_path_forms_refused(self):
        # WORKER_GUARD_SCRATCH set, <S> inside it: the path being in scratch must not make the form a "read"
        scratch = os.path.join(self.tmp, "scratch")
        os.makedirs(scratch)
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
        forms = [f"{pre}core.hooksPath /tmp/x {tok}".replace("<S>", scratch)
                 for pre in SCRATCH_PREFIXES for tok in SCRATCH_TOKENS]
        self.assertEqual(len(set(forms)), 12)
        for role in WRITERS:
            env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home,
                       WORKER_GUARD_SCRATCH=scratch, FACTORY_AGENT=f"coder-{TASK}" if role == "coder" else f"{role}-x")
            for f in forms:
                with self.subTest(role=role, form=f):
                    payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": "git config " + f}, "cwd": self.wt})
                    p = hook_run(payload, ["--role", role], env=env)
                    self.assertEqual(p.returncode, 2, p.stderr)

    def test_reads_stay_reads(self):
        self.check(READS, 0, ("coder",))


if __name__ == "__main__":
    unittest.main()
