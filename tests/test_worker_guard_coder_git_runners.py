import json
import os
import shlex
import sqlite3
import tempfile
import unittest

from harness import git
from guard_impl import hook_run

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TASK = "t-0908"
P = "git push origin main"
OWN = f"git push origin HEAD:factory/{TASK}"


def wrappers(cmd):
    return (cmd, f"cd . && {cmd}", f"bash -c {shlex.quote(cmd)}", f"zsh -c {shlex.quote(cmd)}")


class CoderGitRunnerTests(unittest.TestCase):
    """T-0908: a coder's git may not run a push through submodule foreach, rebase -x, bisect run or difftool -x."""

    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = os.path.realpath(td.name)
        self.home = os.path.join(self.tmp, "factory")
        self.wt = os.path.join(self.home, "worktrees", "G-018-T-0689")
        os.makedirs(os.path.join(self.home, ".git"))
        os.makedirs(os.path.join(self.home, "roles"))
        self.write_db(self.wt)
        # a real clone under worktrees/ (own .git dir, not the live home) with a local bare origin
        origin = os.path.join(self.tmp, "origin.git")
        ident = ("-c", "user.email=t@t", "-c", "user.name=t")
        git(*ident, "init", "-q", "--bare", origin, cwd=self.tmp)
        git(*ident, "init", "-q", "-b", "main", self.wt, cwd=self.tmp)
        git(*ident, "commit", "-q", "--allow-empty", "-m", "i", cwd=self.wt)
        git(*ident, "remote", "add", "origin", origin, cwd=self.wt)
        git(*ident, "push", "-q", "origin", "main", cwd=self.wt)
        git(*ident, "fetch", "-q", "origin", cwd=self.wt)
        git(*ident, "checkout", "-q", "-b", f"factory/{TASK}", cwd=self.wt)

    def write_db(self, worktree, task=TASK.upper()):
        """T-0743: the factory DB row the pin resolves the own worktree from (tasks.worktree)."""
        os.makedirs(os.path.join(self.home, "db"), exist_ok=True)
        db = os.path.join(self.home, "db", "factory.db")
        if os.path.exists(db):
            os.remove(db)
        con = sqlite3.connect(db)
        con.execute("CREATE TABLE tasks (id TEXT PRIMARY KEY, worktree TEXT)")
        con.execute("INSERT INTO tasks VALUES (?, ?)", (task, worktree))
        con.commit()
        con.close()

    def run_hook(self, cmd, role="coder", agent=f"coder-{TASK}", cwd=None):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home)
        if agent:
            env["FACTORY_AGENT"] = agent
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": cwd or self.wt})
        return hook_run(payload, ["--role", role], env=env)

    def code(self, cmd, role="coder", hq=False):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home,
                   FACTORY_AGENT=f"coder-{TASK}" if role == "coder" else f"{role}-x")
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.wt})
        args = ["--role", role] + (["--hq"] if hq else [])
        return hook_run(payload, args, env=env).returncode

    def assert_all(self, cmds, want, wrapped=True):
        for c in cmds:
            for w in (wrappers(c) if wrapped else (c,)):
                self.assertEqual(self.code(w), want, w)

    def test_runners_refused(self):
        self.assert_all((
            f"git submodule foreach '{P}'", f"git submodule --quiet foreach '{P}'",
            f"git submodule foreach --recursive '{P}'", f"git submodule foreach 'echo a && {P}'",
            f"git submodule foreach '{OWN}'",
            f"git rebase -x '{P}' HEAD~1", f"git rebase -x'{P}' HEAD~1", f"git rebase -ix '{P}' HEAD~1",
            f"git rebase -i -x '{P}' HEAD~1", f"git rebase --exec '{P}' HEAD~1", f"git rebase --exec='{P}' HEAD~1",
            f"git rebase --exe='{P}' HEAD~1", f"git rebase --exec 'echo a; {P}' HEAD~1",
            f"git rebase -x 'git status' -x '{P}' HEAD~1", f"git rebase -ix'{P}' HEAD~1",
            f"git bisect run {P}", f"git bisect run bash -c '{P}'",
            f"git difftool -x '{P}'", f"git difftool -x'{P}'", f"git difftool -yx '{P}'",
            f"git difftool --extcmd='{P}'", f"git difftool --extcmd '{P}'", f"git difftool --ext='{P}'",
            f"git -C . submodule foreach '{P}'", f"git -c core.quotepath=off rebase -x '{P}' HEAD~1",
            f"echo foreach '{P}' | xargs git submodule", f"echo '{P}' | xargs git submodule foreach",
            f"echo -x '{P}' | xargs git rebase", f"echo run {P} | xargs git bisect",
            f"echo -x '{P}' | xargs git difftool",
        ), 2)

    def test_dynamic_or_missing_inner_refused(self):
        self.assert_all((
            'git submodule foreach "$X"', 'git rebase -x "$X" HEAD~1', 'git bisect run "$X"', 'git difftool -x "$X"',
            "git submodule $S", "git bisect $S", "git submodule foreach", "git rebase -x", "git bisect run",
            "git difftool -x", "git rebase --exec",
        ), 2)

    def test_controls_allowed(self):
        self.assert_all((
            "git submodule foreach 'git status'", "git submodule foreach ./x.sh", "git rebase -x 'pytest tests/test_x.py' HEAD~1",
            "git rebase -x ./x.sh HEAD~1", "git rebase -Xours origin/main", "git rebase origin/main",
            "git rebase -sx origin/main", "git rebase -C4 origin/main",
            "git submodule update --init", "git bisect start", "git bisect run pytest tests/test_x.py", "git bisect run ./x.sh",
            "git difftool --stat", "git difftool --tool=vimdiff", "git difftool -x ./x.sh", "git difftool -tx",
            "echo f | xargs git submodule update", "echo f | xargs git bisect good", "echo x | xargs git status",
        ), 0)

    def test_own_push_plain_allowed(self):
        self.assertEqual(self.code(OWN), 0)

    def test_other_roles_unchanged(self):
        """Exit codes measured on origin/main 1df5ae252: runners judge 0, cleaner 0, qa 2, pm 2, hq 0."""
        runners = (f"git submodule foreach '{P}'", f"git rebase -x '{P}' HEAD~1", f"git bisect run {P}",
                   f"git difftool -x '{P}'")
        for c in runners:
            for role, want in (("judge", 0), ("cleaner", 0), ("qa", 2), ("pm", 2)):
                self.assertEqual(self.code(c, role=role), want, f"{role}: {c}")
            self.assertEqual(self.code(c, role="boss", hq=True), 0, f"hq: {c}")
        for role, want in (("judge", 0), ("cleaner", 0), ("qa", 2), ("pm", 2)):
            self.assertEqual(self.code("git rebase -Xours origin/main", role=role), want, role)


if __name__ == "__main__":
    unittest.main()
