"""T-0226: workers cannot write repo git config (every worktree shares the live checkout's .git/config). Accident
prevention, not a security boundary; further spellings are tracked in T-0337."""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
ROLES = ("coder", "qa", "reviewer", "judge", "cleaner", "pm", "scout", "evaluator")
EXEC_KEYS = ("filter.x.smudge cmd", "filter.x.clean cmd", "diff.x.textconv cmd", "core.hooksPath x",
             "core.fsmonitor cmd", "core.sshCommand cmd", "remote.origin.uploadpack cmd")


class RepoCfgTests(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-repocfg-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = os.path.join(self.tmp, "live")  # stand-in for ~/factory
        self.wt = os.path.join(self.tmp, "live", "worktrees", "T-1")
        self.scratch = os.path.join(self.tmp, "scratch")
        self.clone = os.path.join(self.scratch, "clone")
        os.makedirs(self.scratch)
        git = ["git", "-c", "init.defaultBranch=main"]
        subprocess.run([*git, "init", "-q", self.home], check=True, capture_output=True)
        env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@x", "GIT_COMMITTER_NAME": "t",
               "GIT_COMMITTER_EMAIL": "t@x"}
        subprocess.run([*git, "-C", self.home, "commit", "-q", "--allow-empty", "-m", "i"], check=True,
                       capture_output=True, env=env)
        subprocess.run([*git, "-C", self.home, "worktree", "add", "-q", "-b", "factory/t-1", self.wt], check=True,
                       capture_output=True)
        subprocess.run([*git, "init", "-q", self.clone], check=True, capture_output=True)
        subprocess.run([*git, "init", "-q", "--bare", os.path.join(self.scratch, "bare.git")], check=True,
                       capture_output=True)

    def run_hook(self, payload, role):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        env.update(WORKER_GUARD_LIVE_HOME=self.home, WORKER_GUARD_SCRATCH=self.scratch)
        return subprocess.run([sys.executable, HOOK, "--role", role], input=json.dumps(payload), capture_output=True,
                              text=True, timeout=30, env=env)

    def bash(self, cmd, role="coder", cwd=None):
        return self.run_hook({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": cwd or self.wt}, role)

    def blocked(self, cmd, role="coder", cwd=None):
        p = self.bash(cmd, role, cwd)
        self.assertEqual(p.returncode, 2, f"should block ({role}): {cmd!r}\n{p.stderr}")
        return p

    def allowed(self, cmd, role="coder", cwd=None):
        p = self.bash(cmd, role, cwd)
        self.assertEqual(p.returncode, 0, f"should allow ({role}): {cmd!r}\n{p.stderr}")

    def test_exec_capable_keys_blocked_from_a_worktree_for_every_role(self):
        for role in ROLES:
            for kv in EXEC_KEYS:
                self.blocked(f"git config {kv}", role)

    def test_every_write_form_blocked(self):
        for cmd in ("git config --add a.b c", "git config --unset a.b", "git config --unset-all a.b",
                    "git config --replace-all a.b c", "git config --rename-section a b", "git config --remove-section a",
                    "git config --edit", "git config -e", "git config --local a.b c", "git config --worktree a.b c",
                    "git config set a.b c", "git config unset a.b", "git config edit", "git config a.b c",
                    "git config --type bool a.b true"):
            self.blocked(cmd, "qa")

    def test_other_directories_and_shapes_blocked(self):
        for role in ("coder", "qa"):
            self.blocked(f"git -C {self.wt} config core.hooksPath x", role, cwd=self.tmp)
            self.blocked(f"git -C {self.home} config core.hooksPath x", role, cwd=self.tmp)
            self.blocked(f"git --git-dir={self.home}/.git config core.hooksPath x", role)
            self.blocked(f"git --git-dir {self.home}/.git config core.hooksPath x", role)
            self.blocked(f"git --work-tree {self.wt} config core.hooksPath x", role)
            self.blocked(f"cd {self.tmp} ; git config core.hooksPath x", role)
            self.blocked(f"cd {self.home} && git config core.hooksPath x", role)
            self.blocked("cd /nonexistent ; git config core.hooksPath x", role)
            self.blocked("cd $SOMEWHERE && git config core.hooksPath x", role)
            self.blocked("GIT_DIR=/x/.git git config core.hooksPath x", role)
            self.blocked("env git config core.hooksPath x", role)
            self.blocked("sh -c 'git config core.hooksPath x'", role)
            self.blocked("git -c core.pager=cat config core.hooksPath x", role)
            self.blocked("git -c alias.c='config core.hooksPath x' c", role)
            self.blocked(f"git config --file {self.home}/.git/config core.hooksPath x", role)
            self.blocked("git config --file /tmp/elsewhere core.hooksPath x", role)

    def test_git_remote_writes_blocked_reads_allowed(self):
        for role in ("coder", "qa"):
            for sub in ("add up ~/factory", "set-url origin x", "rename origin o", "remove origin", "rm origin",
                        "set-head origin main", "set-branches origin main"):
                self.blocked(f"git remote {sub}", role)
            self.blocked(f"cd {self.tmp} ; git remote add up {self.home}", role)
            self.blocked(f"git -C {self.home} remote add up x", role)
            for cmd in ("git remote", "git remote -v", "git remote show origin", "git remote get-url origin"):
                self.allowed(cmd, role)

    def test_reads_allowed(self):
        for cmd in ("git config --get user.name", "git config --get-all a.b", "git config --get-regexp user",
                    "git config --list", "git config -l", "git config get user.name", "git config list",
                    "git config --local --get a.b", "git config --show-origin --list", "git config user.name",
                    f"git -C {self.home} config --get remote.origin.url", f"git --git-dir={self.home}/.git config --list"):
            for role in ("coder", "qa"):
                self.allowed(cmd, role)

    def test_file_inside_scratchpad_allowed(self):
        for role in ("coder", "qa"):
            self.allowed(f"git config --file {self.scratch}/x.cfg k v", role)
            self.allowed(f"git config -f {self.scratch}/x.cfg --add k v", role)
            self.allowed(f"cd {self.scratch} && git config --file x.cfg k v", role)

    def test_clone_inside_scratchpad_allowed_linked_worktree_not(self):
        for role in ("coder", "qa"):
            self.allowed(f"git -C {self.clone} config user.name x", role)
            self.allowed(f"git -C {self.clone} remote add o ../elsewhere", role)
            self.allowed(f"cd {self.clone} && git config core.autocrlf false", role)
            self.allowed("git config core.autocrlf false", role, cwd=self.clone)
            self.allowed(f"git --git-dir {self.clone}/.git config a.b c", role)
            self.allowed(f"git -C {self.scratch}/bare.git config a.b c", role)
        subprocess.run(["git", "-C", self.home, "worktree", "add", "-q", "-b", "factory/t-2", f"{self.scratch}/lw"],
                       check=True, capture_output=True)
        self.blocked(f"git -C {self.scratch}/lw config core.hooksPath x")
        self.blocked("git config core.hooksPath x", cwd=f"{self.scratch}/lw")

    def test_edit_write_and_redirects_to_repo_config_blocked(self):
        gitdir = os.path.join(self.home, ".git")
        targets = [f"{gitdir}/config", f"{self.wt}/.git/config", f"{self.wt}/.git/config.worktree",
                   f"{gitdir}/config.worktree", f"{gitdir}/worktrees/T-1/config.worktree", f"{gitdir}/info/attributes",
                   f"{self.scratch}/bare.git/../../live/.git/config"]
        for role in ("coder", "qa"):
            for t in targets:
                for tool in ("Edit", "Write", "MultiEdit"):
                    p = self.run_hook({"tool_name": tool, "tool_input": {"file_path": t}}, role)
                    self.assertEqual(p.returncode, 2, f"{tool} {t}\n{p.stderr}")
                    self.assertIn("accident prevention", p.stderr)
                for cmd in (f"echo x >> {t}", f"echo x > {t}", f"tee -a {t}", f"cp /tmp/f {t}", f"mv /tmp/f {t}",
                            f"ln -sf /tmp/f {t}", f"install /tmp/f {t}", f"sed -i s/a/b/ {t}", f"dd if=/tmp/f of={t}"):
                    self.blocked(cmd, role)
            self.blocked("echo x >> .git/config", role, cwd=self.wt)
            self.blocked("echo x >> ../../.git/config", role, cwd=self.wt)
            self.blocked("echo x >> .git/info/attributes", role, cwd=self.home)

    def test_bare_repo_config_blocked_outside_scratchpad_only(self):
        bare = os.path.join(self.tmp, "bare.git")
        subprocess.run(["git", "init", "-q", "--bare", bare], check=True, capture_output=True)
        self.blocked(f"echo x >> {bare}/config")
        self.blocked(f"echo x >> {bare}/info/attributes")
        self.allowed(f"echo x >> {self.scratch}/bare.git/config")
        self.allowed(f"echo x >> {self.clone}/.git/config")
        p = self.run_hook({"tool_name": "Write", "tool_input": {"file_path": f"{self.clone}/.git/config"}}, "coder")
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_reading_and_unrelated_config_files_allowed(self):
        gitdir = os.path.join(self.home, ".git")
        for cmd in (f"cat {gitdir}/config", f"grep url {gitdir}/config", f"cp {gitdir}/config /tmp/c",
                    "echo x > config", "echo x > config.json", f"echo x > {self.wt}/config", "echo x > app/.git/hooks-x",
                    f"echo x > {self.wt}/docs/config.md", "echo x > ./info/attributes"):
            self.allowed(cmd)

    def test_message_explains_the_alternative(self):
        err = self.blocked("git config core.hooksPath x").stderr
        self.assertIn("--file", err)
        self.assertIn("scratchpad", err)
        self.assertIn("not a security boundary", err)


if __name__ == "__main__":
    unittest.main()
