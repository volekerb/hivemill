import json
import os
import pwd
import subprocess
import sys
import tempfile
import unittest

LIVE_HOME = os.path.join(pwd.getpwuid(os.getuid()).pw_dir, "factory")  # T-0212
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")


class GitGuardBase(unittest.TestCase):
    """Fixture: <tmp>/factory/.git (dir) is the shared checkout, <tmp>/factory/worktrees/W/.git (file) a task worktree."""

    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = os.path.realpath(td.name)
        self.home = os.path.join(self.tmp, "factory")
        self.wt = os.path.join(self.home, "worktrees", "G-007-T-x")
        os.makedirs(os.path.join(self.home, ".git"))
        os.makedirs(os.path.join(self.home, "roles"))
        os.makedirs(os.path.join(self.home, ".git", "worktrees", "G-007-T-x"))
        os.makedirs(self.wt)
        with open(os.path.join(self.wt, ".git"), "w") as f:
            f.write(f"gitdir: {self.home}/.git/worktrees/G-007-T-x\n")

    def run_hook(self, cmd, cwd=None, role="coder", override=True):
        env = {k: v for k, v in os.environ.items() if k != "WORKER_GUARD_LIVE_HOME"}
        env["HOME"] = self.tmp
        if getattr(self, "scratch", None):
            env["WORKER_GUARD_SCRATCH"] = self.scratch  # the hook's own env: names the worker's scratchpad
        if override:
            env["WORKER_GUARD_LIVE_HOME"] = self.home
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": cwd or self.wt})
        return subprocess.run([sys.executable, HOOK, "--role", role], input=payload, capture_output=True,
                              text=True, timeout=30, env=env)

    def blocked(self, cmd, **kw):
        p = self.run_hook(cmd, **kw)
        self.assertEqual(p.returncode, 2, f"should block: {cmd!r}\n{p.stderr}")
        return p

    def allowed(self, cmd, **kw):
        p = self.run_hook(cmd, **kw)
        self.assertEqual(p.returncode, 0, f"should allow: {cmd!r}\n{p.stderr}")


class SharedCheckoutTests(GitGuardBase):
    def test_mutating_blocked_with_advice(self):
        for cmd in ("git commit -m x", "git stash", "git checkout -b foo", "git reset --hard", "git branch -D x",
                    "git worktree add ../y"):
            with self.subTest(cmd=cmd):
                err = self.blocked(cmd, cwd=self.home).stderr
                self.assertIn(f"`{cmd}`", err)
                self.assertIn("own worktree", err)
                self.assertIn("shared checkout ~/factory (branch main)", err)

    def test_more_mutating_blocked(self):
        for cmd in ("git push origin main", "git merge origin/main", "git rebase main", "git add .", "git pull",
                    "git switch x", "git restore f", "git clean -fd", "git tag v1", "git branch newbranch",
                    "git branch -m a b", "git config user.name x", "git update-ref -d refs/x", "git gc",
                    "git worktree prune", "git frobnicate"):
            with self.subTest(cmd=cmd):
                self.blocked(cmd, cwd=self.home)

    def test_read_only_allowed(self):
        for cmd in ("git log --oneline -5", "git status", "git diff", "git fetch origin", "git branch",
                    "git worktree list", "git branch --list", "git branch -a", "git branch -r -v",
                    "git remote -v", "git config --get user.name", "git config --list", "git show HEAD",
                    "git rev-parse HEAD", "git ls-files", "git blame f", "git grep x", "git --version"):
            with self.subTest(cmd=cmd):
                self.allowed(cmd, cwd=self.home)

    def test_subdirectory_of_shared_checkout_is_shared(self):
        self.blocked("git commit -m x", cwd=os.path.join(self.home, "roles"))
        self.blocked("git commit -m x", cwd=os.path.join(self.home, "worktrees"))

    def test_without_override_the_real_home_is_the_target(self):
        # no override: the fixture home is not the live home, so the fixture checkout is not guarded
        self.allowed("git commit -m x", cwd=self.home, override=False)


class ArchiveInitTests(GitGuardBase):
    """T-0303 (M5056, M5058, M5368): git init of a scratch dir from the shared checkout; git archive stays refused as on main."""

    def setUp(self):
        super().setUp()
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.scratch = os.path.realpath(td.name)  # outside <tmp>/factory: the worker's scratchpad
        self.link = os.path.join(self.scratch, "link")
        os.symlink(self.home, self.link)

    def test_archive_refused(self):
        """No archive loosening: every form is refused from the shared checkout, as on origin/main."""
        h, s = self.home, self.scratch
        for cmd in ("git archive -o %s/x.tar origin/main" % h,
                    "git archive --output=%s/x.tar origin/main" % h,
                    "git archive -o x.tar origin/main",
                    "git archive -o %s/%s/x.tar origin/main" % (s, os.path.relpath(h, s)),
                    "git archive -o %s/x.tar origin/main" % self.link,
                    "git archive -o ~/nowhere/../factory/x.tar origin/main",
                    "git archive -o '~' HEAD", "git archive --output=~ HEAD", "git archive -o '~/x' HEAD",
                    "git -c tar.foo.command='tee roles/pwn' archive --format=foo HEAD",
                    "git --config-env=tar.foo.command=FOO archive --format=foo HEAD",
                    "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=tar.foo.command GIT_CONFIG_VALUE_0='tee x' git archive HEAD",
                    "GIT_EXEC_PATH=/tmp git archive HEAD",
                    "git archive --remote=origin origin/main",
                    "git archive --remote origin origin/main",
                    "git archive --exec=/bin/true origin/main",
                    "git archive --exec /bin/true origin/main",
                    "git archive -o $SCRATCH/x.tar origin/main",
                    "git archive -o `pwd`/x.tar origin/main",
                    "git archive --frobnicate origin/main",
                    "git archive -o %s/x.tar" % s,
                    "git archive -o %s HEAD" % h, "git archive --output=%s HEAD" % h,
                    "git archive -o",
                    "git archive origin/main | tar -x -C %s/d" % s, "git archive HEAD -- roles",
                    "git archive --format=tar --prefix=p/ -o %s/x.tar origin/main" % s,
                    "git archive --output %s/x.tar origin/main" % s,
                    "HOME=%s git archive --format=foo HEAD" % s,
                    "XDG_CONFIG_HOME=%s git archive --format=foo HEAD" % s,
                    "export HOME=%s; git archive --format=foo HEAD" % s):
            with self.subTest(cmd=cmd):
                self.blocked(cmd, cwd=self.home)

    def test_init_scratch_allowed_shared_refused(self):
        s, h = self.scratch, self.home
        for cmd in ("git init -q -b main %s/r" % s, "git init %s/r" % s, "git init --bare %s/r" % s):
            with self.subTest(cmd=cmd):
                self.allowed(cmd, cwd=h)
        for cmd in ("git init '~'", "git init '~/r'", "export GIT_DIR=%s/.git; git init %s/r" % (h, s),
                    "git init", "git init -q -b main", "git init %s/sub" % h, "git init r", "git init %s" % self.link,
                    "git init --separate-git-dir=%s/g %s/r" % (h, s), "git init --template=%s/t %s/r" % (s, s),
                    "git init %s/a %s/b" % (s, s), "git init $SCRATCH/r",
                    # M5368: only the own scratchpad; ancestors, homes, linked worktrees, / and ~ are refused
                    "git init %s" % self.tmp, "git init /", "git init ~", "git init %s/.." % h, "git init %s" % self.wt,
                    "git init %s/worktrees/W" % h, "git init /etc/x", "git init %s/../x" % s,
                    "git init %s/link/r" % s):
            with self.subTest(cmd=cmd):
                self.blocked(cmd, cwd=h)

    def test_init_scratchpad_from_session_id(self):
        """Without the test override the scratchpad is /tmp/claude-<uid>/<project>/<session_id>/scratchpad."""
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home)
        base = "/tmp/claude-%d/-proj" % os.getuid()

        def rc(cmd, sid):
            data = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.home}
            if sid:
                data["session_id"] = sid
            return subprocess.run([sys.executable, HOOK, "--role", "coder"], input=json.dumps(data),
                                  capture_output=True, text=True, timeout=30, env=env).returncode
        self.assertEqual(rc("git init %s/abc/scratchpad/r" % base, "abc"), 0)
        for cmd, sid in (("git init %s/abc/scratchpad/r" % base, "other"), ("git init %s/abc/scratchpad/r" % base, None),
                         ("git init %s/abc/r" % base, "abc"), ("git init %s/abc/scratchpad/../r" % base, "abc"),
                         ("git init /tmp/claude-%d/abc/scratchpad/r" % (os.getuid() + 1), "abc")):
            with self.subTest(cmd=cmd, sid=sid):
                self.assertEqual(rc(cmd, sid), 2)

    def test_git_env_config_anywhere_refuses_init(self):
        """any GIT_*= in the text (export, env, own statement, prefix) keeps git init refused."""
        s = self.scratch
        for env in ("export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=tar.foo.command GIT_CONFIG_VALUE_0='tee roles/pwn'",
                    "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=tar.foo.command GIT_CONFIG_VALUE_0='tee roles/pwn'",
                    "export GIT_CONFIG_PARAMETERS=\"'tar.foo.command'='tee roles/pwn'\"",
                    "export GIT_CONFIG_GLOBAL=%s/gitconfig" % s,
                    "env GIT_CONFIG_GLOBAL=%s/gitconfig true" % s,
                    "declare -x GIT_CONFIG_SYSTEM=%s/gitconfig" % s):
            for cmd in ("%s; git init %s/r" % (env, s), "%s && git init -q %s/r" % (env, s)):
                with self.subTest(cmd=cmd):
                    self.blocked(cmd, cwd=self.home)

    def test_git_dir_override_not_loosened(self):
        self.blocked("git --git-dir=%s/.git archive origin/main" % self.home, cwd=self.home)
        self.blocked("GIT_DIR=%s/.git git archive origin/main" % self.home, cwd=self.home)

    def test_hq_mode_unchanged(self):
        for cmd in ("git archive -o %s/x.tar origin/main" % self.home, "git archive --remote=origin main",
                    "git init %s/r" % self.scratch):
            with self.subTest(cmd=cmd):
                p = subprocess.run([sys.executable, HOOK, "--hq"], capture_output=True, text=True, timeout=30,
                                   input=json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.home}),
                                   env={**os.environ, "HOME": self.tmp, "WORKER_GUARD_LIVE_HOME": self.home})
                self.assertEqual(p.returncode, 0, p.stderr)


class WorktreeTests(GitGuardBase):
    def test_linked_worktree_allowed(self):
        for cmd in ("git commit -m x", "git push -u origin HEAD"):
            with self.subTest(cmd=cmd):
                self.allowed(cmd, cwd=self.wt)

    def test_subdir_of_worktree_allowed(self):
        os.makedirs(os.path.join(self.wt, "sub"))
        self.allowed("git commit -m x", cwd=os.path.join(self.wt, "sub"))


class RedirectionTests(GitGuardBase):
    def test_redirections_blocked_from_worktree(self):
        h = self.home
        for cmd in (f"cd {h} && git commit -m x", f"cd {h}; git commit -m x", f"git -C {h} reset --hard",
                    "git -C ../.. commit", f"git -C ../../ -C . commit -m x",
                    "cd $HOME/factory && git add .", "git -C $X commit", f"bash -c 'cd {h} && git commit -m x'",
                    "cd ~/factory && git commit -m x", f"env GIT_DIR={h}/.git git commit",
                    f"git --git-dir={h}/.git commit", f"git --work-tree={h} checkout x",
                    f"GIT_WORK_TREE={h} git checkout x", f"timeout 5 git -C {h} commit -m x"):
            with self.subTest(cmd=cmd):
                self.blocked(cmd)

    def test_redirections_that_stay_in_worktree(self):
        for cmd in (f"cd {self.wt} && git commit -m x", "git -C . commit -m x", f"git -C {self.wt} push",
                    f"bash -c 'cd {self.home} && git status'", f"(cd {self.home}); git add .",
                    "env GIT_DIR=x git status"):
            with self.subTest(cmd=cmd):
                if cmd.startswith("(cd"):
                    continue  # a subshell cd leaking out is a documented false positive
                self.allowed(cmd)

    def test_cd_inside_sh_c_does_not_move_caller(self):
        self.allowed(f"bash -c 'cd {self.home}' && git commit -m x")

    def test_cd_back_to_worktree(self):
        self.allowed(f"cd {self.home} && cd {self.wt} && git commit -m x")


class RoleTests(GitGuardBase):
    def test_roles_blocked(self):
        for role in ("coder", "qa", "pm", "staff-engineer", "reviewer", "judge", "cleaner"):
            with self.subTest(role=role):
                self.blocked("git commit -m x", cwd=self.home, role=role)

    def test_cleaner_exemptions(self):
        for cmd in ("git worktree remove /tmp/x", "git worktree prune", "git branch -d factory/t-x"):
            with self.subTest(cmd=cmd):
                self.allowed(cmd, cwd=self.home, role="cleaner")
                self.blocked(cmd, cwd=self.home, role="coder")

    def test_cleaner_other_forms_blocked(self):
        for cmd in ("git worktree remove --force x", "git worktree remove -f x", "git branch -D x",
                    "git commit -m x", "git worktree add ../y", "git checkout main"):
            with self.subTest(cmd=cmd):
                self.blocked(cmd, cwd=self.home, role="cleaner")


class NoFalsePositiveTests(GitGuardBase):
    CMDS = ("git status", "git diff", "git log --oneline -5", "git add -A", "git commit -F /tmp/m", "git push -u origin HEAD",
            "git fetch origin", "git checkout --detach origin/x", "git rebase origin/main", "git merge origin/main",
            "git stash list", "git worktree list", "git branch -a", "gh pr create --fill", "gh pr view",
            "gh pr checks --watch", "git remote -v", "git config --get user.name",
            'grep "git commit" README.md', "echo git reset --hard", 'gh pr create --body "git stash ..."',
            "echo cd /x && echo git commit")

    def test_table_from_worktree(self):
        self.assertGreaterEqual(len(self.CMDS), 12)
        for cmd in self.CMDS:
            with self.subTest(cmd=cmd):
                self.allowed(cmd)

    def test_text_only_mentions_from_shared_checkout(self):
        for cmd in ('grep "git commit" README.md', "echo git reset --hard", 'gh pr create --body "git stash ..."'):
            with self.subTest(cmd=cmd):
                self.allowed(cmd, cwd=self.home)


class RealHookTests(unittest.TestCase):
    def test_real_live_home_status_allowed(self):
        env = {k: v for k, v in os.environ.items() if k != "WORKER_GUARD_LIVE_HOME"}
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": "git status"},
                              "cwd": LIVE_HOME})
        p = subprocess.run([sys.executable, HOOK, "--role", "coder"], input=payload, capture_output=True,
                           text=True, timeout=30, env=env)
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_real_live_home_commit_blocked(self):
        if not os.path.isdir(LIVE_HOME + "/.git"):
            self.skipTest("no live home checkout on this machine (CI)")
        env = {k: v for k, v in os.environ.items() if k != "WORKER_GUARD_LIVE_HOME"}
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": "git commit -m x"},
                              "cwd": LIVE_HOME})
        p = subprocess.run([sys.executable, HOOK, "--role", "coder"], input=payload, capture_output=True,
                           text=True, timeout=30, env=env)
        self.assertEqual(p.returncode, 2, p.stderr)  # the hook only inspects the text; nothing is run


if __name__ == "__main__":
    unittest.main()
