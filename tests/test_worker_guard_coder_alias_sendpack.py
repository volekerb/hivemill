"""T-0907 (follow-up of T-0799): the coder push pin also covers `git -c alias.x=send-pack`, `git --config-env alias.x=VAR`
and `xargs git config` (key/value from stdin). Defense in depth only; the G-010 sandbox is the boundary."""
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
TASK = "t-0907"

DENIED = [
    "git -c alias.x=send-pack x origin HEAD",
    "git send-pack origin HEAD",
    "X=push git --config-env=alias.x=X x origin main",
    "X=push git --config-env alias.x=X x origin main",
    "echo alias.x push | xargs git config",
    "printf 'alias.x\\npush\\n' | xargs git config",
]
EXTRA_DENIED = [
    "git -calias.x=send-pack x origin HEAD",
    "git -calias.x=push x origin main",
    "echo user.name | xargs git config --get --add",
    "echo alias.y push | xargs git -c alias.x=config x",
    "echo alias.y push | xargs git -calias.x=config x",
    "git --config-env core.sshCommand=X fetch origin",
    "git --config-env core.fsmonitor=X status",
    "git --config-env core.pager=X log",
    "git --config-env remote.origin.url=X fetch",
    "git --config-env user.name=X status",
    "git --attr-source HEAD send-pack origin HEAD",
    "git --attr-source HEAD -c alias.x=send-pack x origin HEAD",
    "X=push git --attr-source HEAD --config-env=alias.x=X x origin main",
    "echo alias.x push | xargs git --attr-source HEAD config",
    "git --shallow-file x send-pack origin HEAD",
    "echo alias.x push | xargs git --shallow-file x config",
    "git --config-env alias.x=X x origin main",
    "git --attr-source HEAD status",
]
# corpus entry is now tagged role reviewer in ci/guard_corpus.json (python/go stderr parity); coder stays refused here
ATTR_CLONE = ["git --attr-source HEAD clone URL ~/.local/" + "go/x"]
ALLOWED = [
    "git -c alias.st=status st",
    "git config --get user.name",
    "echo user.name | xargs git config --get",
    f"git push origin HEAD:factory/{TASK}",
]
EXTRA_ALLOWED = [
    "echo x | xargs git config --list",
    "echo x | xargs git config --get-all",
    "git --config-env=user.name=X status",
    "git -cuser.name=x status",
    "git --no-pager log -1",
    "git -C . status",
    "git --attr-source=HEAD status",
]


class CoderAliasSendPackTests(unittest.TestCase):
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
        for base in DENIED + EXTRA_DENIED + ATTR_CLONE:
            for cmd in (base, f"cd /x && {base}", "bash -c " + json.dumps(base), "zsh -c " + json.dumps(base)):
                with self.subTest(cmd=cmd):
                    p = self.run_hook(cmd)
                    self.assertEqual(p.returncode, 2, p.stderr)
                    self.assertTrue(p.stderr.strip(), "refusal needs a message")

    def test_attr_source_space_clone_refused_by_allowlist_message(self):
        p = self.run_hook(ATTR_CLONE[0])
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("coders may use only known git options", p.stderr)

    def test_allowed_for_coder(self):
        for cmd in ALLOWED + EXTRA_ALLOWED:
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd)
                self.assertEqual(p.returncode, 0, p.stderr)

    # exit codes of origin/main for ALL_CMDS, in order (recorded before this change); other roles and HQ must match
    # T-1097: the two `xargs git config` forms (indices 4 and 5) have no literal key (keys come from stdin), so cleaner,
    # judge and director now refuse them too (exit 2, was 0 on main): the accepted bare `git config` tightening.
    ALL_CMDS = DENIED + ALLOWED
    MAIN_RC = {
        "qa": [2, 2, 2, 2, 2, 2, 2, 0, 2, 2],
        "reviewer": [2, 2, 2, 2, 2, 2, 2, 0, 2, 2],
        "pm": [2, 2, 2, 2, 2, 2, 2, 0, 2, 2],
        "cleaner": [0, 0, 0, 0, 2, 2, 0, 0, 0, 2],
        "judge": [0, 0, 0, 0, 2, 2, 0, 0, 0, 0],
        "director": [0, 0, 0, 0, 2, 2, 0, 0, 0, 0],
    }

    def test_other_roles_and_hq_unchanged(self):
        for role, want in self.MAIN_RC.items():
            for cmd, rc in zip(self.ALL_CMDS, want):
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, rc)

    # T-0907 attempt 3: exit codes of origin/main for the unknown-git-option probes (EXTRA_DENIED[-8:]); only coder changes
    ATTR_MAIN_RC = {"qa": 2, "reviewer": 2, "pm": 2, "cleaner": 0, "judge": 0, "director": 0}

    def test_unknown_git_option_other_roles_unchanged(self):
        for role, rc in self.ATTR_MAIN_RC.items():
            for cmd in EXTRA_DENIED[-8:]:
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, rc)


if __name__ == "__main__":
    unittest.main()
