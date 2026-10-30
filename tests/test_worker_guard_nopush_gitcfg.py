"""T-0770: NO_PUSH_ROLES never write any git config/hooks/info file, scratchpad repos included (fsmonitor, hooksPath,
sshCommand would run as git and could push). Reads and ordinary scratchpad writes stay allowed; other roles unchanged."""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
NO_PUSH = ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator")


class NoPushGitCfgTests(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-nopushcfg-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = os.path.join(self.tmp, "live")
        self.scratch = os.path.join(self.tmp, "scratch")
        self.repo = os.path.join(self.scratch, "r")
        os.makedirs(self.scratch)
        os.makedirs(self.home)
        subprocess.run(["git", "init", "-q", self.repo], check=True, capture_output=True)

    def bash(self, cmd, role):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        env.update(WORKER_GUARD_LIVE_HOME=self.home, WORKER_GUARD_SCRATCH=self.scratch)
        payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.scratch}
        return subprocess.run([sys.executable, HOOK, "--role", role], input=json.dumps(payload), capture_output=True,
                              text=True, timeout=30, env=env)

    def blocked(self, cmd, role):
        p = self.bash(cmd, role)
        self.assertEqual(p.returncode, 2, f"should block ({role}): {cmd!r}\n{p.stderr}")

    def allowed(self, cmd, role):
        p = self.bash(cmd, role)
        self.assertEqual(p.returncode, 0, f"should allow ({role}): {cmd!r}\n{p.stderr}")

    def test_writes_refused_for_every_no_push_role(self):
        r = self.repo
        cmds = [
            f"printf '[core]\\n\\tfsmonitor = touch m\\n' > {r}/.git/config",
            f"echo x >> {r}/.git/config",
            f"echo x | tee {r}/.git/config",
            f"cp f {r}/.git/config",
            f"sed -i s/a/b/ {r}/.git/config",
            f"git config --file {r}/.git/config core.hooksPath h",
            f"git config --file {r}/.git/config core.sshCommand 'ssh -i k'",
            f"git config --file {r}/.git/config core.fsmonitor 'touch m'",
            f"echo x > {r}/.git/hooks/pre-commit",
            f"echo x > {r}/.git/info/x",
            f"echo x > {r}/.git/config.worktree",
            "echo x > ~/.gitconfig",
            "echo x > ~/.config/git/config",
        ]
        for role in NO_PUSH:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    self.blocked(c, role)

    def test_reads_and_plain_scratch_writes_allowed(self):
        r = self.repo
        cmds = [
            f"cat {r}/.git/config",
            f"git config --file {r}/.git/config --get core.bare",
            f"echo x > {self.scratch}/notes.txt",
            f"echo x > {r}/file.txt",
        ]
        for role in NO_PUSH:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    self.allowed(c, role)

    def test_git_init_with_c_or_template_refused(self):
        for role in NO_PUSH:
            self.blocked(f"git -c core.fsmonitor=x init {self.scratch}/n", role)
            self.blocked(f"git init --template=/tmp/t {self.scratch}/n", role)

    def test_other_roles_unchanged(self):
        for role in ("coder", "judge", "cleaner"):
            self.allowed(f"git config --file {self.repo}/.git/config core.bare false", role)
            self.allowed(f"echo x > {self.repo}/.git/hooks/pre-commit", role)
            self.blocked("echo x > ~/.gitconfig", role)


if __name__ == "__main__":
    unittest.main()
