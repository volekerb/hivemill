"""T-0799: the coder push pin also refuses `xargs git` (argv from stdin) and `git -c alias.X=...` (alias hides a push).
Defense in depth only; the G-010 sandbox is the boundary."""
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
TASK = "t-0799"

DENIED = [
    "echo push origin main | xargs git",
    "xargs git <<< 'push origin main'",
    "xargs -0 git",
    "xargs -I{} git {}",
    "xargs -n 1 git",
    "git -c alias.x=push x origin main",
    "git -c alias.x='!git push' x",
    "echo push origin main | xargs env git",
    "echo push origin main | xargs nice git",
    "echo push origin main | xargs command git",
    "echo push origin main | xargs timeout 9 git",
    "echo push origin main | xargs stdbuf -o0 git",
    "echo push origin main | xargs env -i git",
    "echo push origin main | xargs env A=1 git",
    "echo alias.x=push | xargs -I{} git -c {} x origin main",
    "echo alias.x=push | xargs -I% git -c % x origin main",
    "echo alias.x=push | xargs --replace git -c {} x origin main",
    "echo alias.x=push | xargs -i git -c {} x origin main",
]
ALLOWED = [
    f"git push origin HEAD:factory/{TASK}",
    "git status", "git log", "git commit -m x", "xargs grep foo", "git -c core.pager=cat log", "git -c alias.x=status x",
]


class CoderXargsAliasTests(unittest.TestCase):
    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = os.path.realpath(td.name)
        self.home = os.path.join(self.tmp, "factory")
        self.wt = os.path.join(self.home, "worktrees", "G-010-T-0799")
        os.makedirs(os.path.join(self.home, ".git"))
        os.makedirs(os.path.join(self.home, "roles"))
        os.makedirs(os.path.join(self.home, "db"))
        os.makedirs(self.wt)
        git("init", "-q", "-b", "main", self.wt, cwd=self.tmp)
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE tasks (id TEXT PRIMARY KEY, worktree TEXT)")
        con.execute("INSERT INTO tasks VALUES (?, ?)", (TASK.upper(), self.wt))
        con.commit()
        con.close()

    def run_hook(self, cmd, role="coder"):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home,
                   FACTORY_AGENT=f"coder-{TASK}" if role == "coder" else f"{role}-x")
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.wt})
        return hook_run(payload, ["--role", role], env=env)

    def test_denied_for_coder(self):
        for base in DENIED:
            for cmd in (base, f"cd /x && {base}", "bash -c " + json.dumps(base), "zsh -c " + json.dumps(base)):
                with self.subTest(cmd=cmd):
                    p = self.run_hook(cmd)
                    self.assertEqual(p.returncode, 2, p.stderr)
                    self.assertTrue(p.stderr.strip(), "refusal needs a message")

    def test_allowed_for_coder(self):
        for cmd in ALLOWED:
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd)
                self.assertEqual(p.returncode, 0, p.stderr)

    OTHER_RC = {"qa": 2, "reviewer": 2, "pm": 2, "cleaner": 0, "judge": 0}  # unchanged by this task
    MINE = ("xargs git:", "can hide a push")

    def test_other_roles_unchanged(self):
        # the coder-only refusals never fire for another role (qa/reviewer/pm refuse these via their own allow-list)
        for role in ("qa", "reviewer", "pm", "cleaner", "judge"):
            for cmd in ("echo push origin main | xargs git", "git -c alias.x=push x origin main"):
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    for m in self.MINE:
                        self.assertNotIn(m, p.stderr)
                    self.assertEqual(p.returncode, self.OTHER_RC[role])
            with self.subTest(role=role, cmd="git status"):
                self.assertEqual(self.run_hook("git status", role).returncode, 0)

    def test_hq_unchanged(self):
        for cmd in ("echo push origin main | xargs git", "git -c alias.x=push x origin main"):
            with self.subTest(cmd=cmd):
                env = dict(os.environ, HOME=self.tmp, FACTORY_AGENT="claude-director")
                payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.wt})
                p = hook_run(payload, ["--role", "director"], env=env)
                for m in self.MINE:
                    self.assertNotIn(m, p.stderr)


if __name__ == "__main__":
    unittest.main()
