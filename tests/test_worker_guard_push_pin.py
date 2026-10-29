import json
import os
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
TASK = "t-0689"
OWN = f"HEAD:factory/{TASK}"


class PushPinTests(unittest.TestCase):
    """T-0689: a coder may only `git push origin HEAD:factory/<own task id>`; everything else is refused."""

    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = os.path.realpath(td.name)
        self.home = os.path.join(self.tmp, "factory")
        self.wt = os.path.join(self.home, "worktrees", "G-018-T-0689")
        os.makedirs(os.path.join(self.home, ".git"))
        os.makedirs(os.path.join(self.home, "roles"))
        # a real clone under worktrees/ (own .git dir, not the live home) with a local bare origin
        origin = os.path.join(self.tmp, "origin.git")
        git = ["git", "-c", "user.email=t@t", "-c", "user.name=t"]
        subprocess.run(git + ["init", "-q", "--bare", origin], check=True)
        subprocess.run(git + ["init", "-q", "-b", "main", self.wt], check=True)
        subprocess.run(git + ["commit", "-q", "--allow-empty", "-m", "i"], cwd=self.wt, check=True)
        subprocess.run(git + ["remote", "add", "origin", origin], cwd=self.wt, check=True)
        subprocess.run(git + ["push", "-q", "origin", "main"], cwd=self.wt, check=True)
        subprocess.run(git + ["fetch", "-q", "origin"], cwd=self.wt, check=True)
        subprocess.run(git + ["checkout", "-q", "-b", f"factory/{TASK}"], cwd=self.wt, check=True)

    def run_hook(self, cmd, role="coder", agent=f"coder-{TASK}"):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home)
        if agent:
            env["FACTORY_AGENT"] = agent
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.wt})
        return subprocess.run([sys.executable, HOOK, "--role", role], input=payload, capture_output=True,
                              text=True, timeout=30, env=env)

    def pinned(self, cmd, **kw):
        """True when the pin itself refused (its message names the exact allowed push)."""
        p = self.run_hook(cmd, **kw)
        return p.returncode == 2 and ("coders push exactly" in p.stderr or "git push:" in p.stderr
                                      or "before push" in p.stderr or "environment overrides" in p.stderr
                                      or "by another name" in p.stderr)

    def test_refused_shapes(self):
        for cmd in (
            "git push", "git push origin", "git push origin HEAD", "git push -u origin HEAD",
            f"git push origin {OWN} HEAD:main", f"git push origin '{OWN} HEAD:main'",
            f"git push origin '{OWN}\tHEAD:main'", f"git push origin '{OWN}\nHEAD:main'",
            f"git push origin {OWN} HEAD:factory/t-0001",
            "git push origin HEAD:factory/t-0001", "git push origin HEAD:main", "git push origin HEAD:refs/heads/main",
            f"git push --force origin {OWN}", f"git push -f origin {OWN}", f"git push --force-with-lease origin {OWN}",
            f"git push origin +{OWN}", f"git push origin {OWN} --force", f"git push origin {OWN} -f",
            "git push --all origin", "git push --mirror origin", "git push --tags origin", f"git push --tags origin {OWN}",
            f"git push origin --delete factory/{TASK}", f"git push --delete origin factory/{TASK}",
            f"git push origin :factory/{TASK}",
            f"git push other {OWN}", f"git push https://example.com/x.git {OWN}", f"git push git@example.com:x/y.git {OWN}",
            f"git push --repo=https://example.com/x.git {OWN}",
            f"git -c push.default=matching push origin {OWN}", f"git -c remote.origin.pushurl=/x push origin {OWN}",
            f"git --git-dir=/x push origin {OWN}", f"GIT_CONFIG_COUNT=1 git push origin {OWN}", f"GIT_DIR=/x git push origin {OWN}",
            f"GIT_SSH_COMMAND=true git push origin {OWN}", f"GIT_EXEC_PATH=/x git push origin {OWN}",
            f"export GIT_CONFIG_PARAMETERS=\"'remote.origin.url=/x'\"; git push origin {OWN}",
            f"export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=remote.origin.url GIT_CONFIG_VALUE_0=/x; git push origin {OWN}",
            f"export GIT_DIR=/x; git push origin {OWN}", f"export GIT_WORK_TREE=/x; git push origin {OWN}",
            f"GIT_DIR=/x; git push origin {OWN}",
            "git subtree push --prefix=x origin main",
            "git send-pack /x HEAD:main", "git http-push http://x/ HEAD:main",
            f"FACTORY_AGENT=coder-t-0001 git push origin HEAD:factory/t-0001",
            f"git push -o x origin {OWN}",
        ):
            with self.subTest(cmd=cmd):
                self.assertTrue(self.pinned(cmd), self.run_hook(cmd).stderr)

    def test_unknown_task_id_refuses(self):
        for agent in (None, "coder", "qa-t-0689", "coder-t-0689x"):
            with self.subTest(agent=agent):
                self.assertTrue(self.pinned(f"git push origin {OWN}", agent=agent))

    def test_other_roles_not_pinned(self):
        p = self.run_hook("git push origin HEAD:factory/t-0001", role="qa")
        self.assertNotIn("coders push exactly", p.stderr)

    def test_allowed_form_is_allowed(self):
        """The exact push exits 0 (real clone + local bare origin) and the pin names no refusal."""
        for cmd in (f"git push origin {OWN}", f"git -C {self.wt} push origin {OWN}"):
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd)
                self.assertEqual(p.returncode, 0, p.stderr)


if __name__ == "__main__":
    unittest.main()
