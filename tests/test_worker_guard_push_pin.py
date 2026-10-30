import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import unittest

from harness import git

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
        return subprocess.run([sys.executable, HOOK, "--role", role], input=payload, capture_output=True,
                              text=True, timeout=30, env=env)

    def pinned(self, cmd, **kw):
        """True when the pin itself refused (its message names the exact allowed push)."""
        p = self.run_hook(cmd, **kw)
        return p.returncode == 2 and ("coders push exactly" in p.stderr or "git push:" in p.stderr
                                      or "before push" in p.stderr or "environment overrides" in p.stderr
                                      or "by another name" in p.stderr or "whole command" in p.stderr)

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
            f"printf -v GIT_DIR /x; export GIT_DIR; git push origin {OWN}",
            f"read GIT_DIR </dev/null; export GIT_DIR; git push origin {OWN}",
            f"for GIT_DIR in /x; do export GIT_DIR; git push origin {OWN}; done",
            f"mapfile -t GIT_DIR < /dev/null; export GIT_DIR; git push origin {OWN}",
            f"GIT_DIR+=/x; export GIT_DIR; git push origin {OWN}",
            f"x=1; export x; git push origin {OWN}", f"declare -x y=1; git push origin {OWN}",
            f"git push origin {OWN} # GIT_DIR",
            "git subtree push --prefix=x origin main",
            "git send-pack /x HEAD:main", "git http-push http://x/ HEAD:main",
            f"FACTORY_AGENT=coder-t-0001 git push origin HEAD:factory/t-0001",
            f"git push -o x origin {OWN}",
            f"set -a; n=GI; printf -v \"${{n}}T_CONFIG_PARAMETERS\" '%s' \"'remote.origin.pushurl=/x'\"; git push origin {OWN}",
            f"f(){{ local -x ${{1}}IT_CONFIG_PARAMETERS=x; git push origin {OWN}; }}; f G",
            f"BASH_ENV=/tmp/benv bash -c 'git push origin {OWN}'",
            f"HOME=/tmp/h git push origin {OWN}",
            f"set -a; a=GI; b=T_DIR; printf -v \"$a$b\" /x; git push origin {OWN}",
            f"true && git push origin {OWN}", f"git push origin {OWN}; true", f"git push origin '{OWN}'",
            f"git -C $HOME push origin {OWN}",
        ):
            with self.subTest(cmd=cmd):
                self.assertTrue(self.pinned(cmd), cmd)

    def test_unknown_task_id_refuses(self):
        for agent in (None, "coder", "qa-t-0689", "coder-t-0689x"):
            with self.subTest(agent=agent):
                self.assertTrue(self.pinned(f"git push origin {OWN}", agent=agent))

    def test_other_roles_not_pinned(self):
        p = self.run_hook("git push origin HEAD:factory/t-0001", role="qa")
        self.assertNotIn("coders push exactly", p.stderr)

    def test_allowed_form_is_allowed(self):
        """The exact push exits 0 (real clone + local bare origin) and the pin names no refusal."""
        for cmd in (f"git push origin {OWN}", f"git -C {self.wt} push origin {OWN}", f"git -C . push origin {OWN}"):
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd)
                self.assertEqual(p.returncode, 0, p.stderr)

    def test_dash_c_must_be_own_worktree(self):
        """T-0719: -C into a scratch clone (another origin) is refused, also via a symlink to it; a symlink to the own one passes."""
        scratch = os.path.join(self.tmp, "scratch")
        git("init", "-q", "-b", "main", scratch, cwd=self.tmp)
        link = os.path.join(self.tmp, "link")
        os.symlink(scratch, link)
        for tgt in (scratch, link, "..", os.path.join(self.home, "worktrees", "G-018-T-0001")):
            cmd = f"git -C {tgt} push origin {OWN}"
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd)
                self.assertEqual(p.returncode, 2, p.stderr)
                self.assertIn("own worktree", p.stderr)
        # cwd in a scratch clone: even `-C .` is refused; cwd in a worktree subdir: -C <worktree root> passes
        for tgt in (".", scratch):
            self.assertEqual(self.run_hook(f"git -C {tgt} push origin {OWN}", cwd=scratch).returncode, 2)
        sub = os.path.join(self.wt, "sub")
        os.makedirs(sub)
        self.assertEqual(self.run_hook(f"git -C {self.wt} push origin {OWN}", cwd=sub).returncode, 0)
        link_own = os.path.join(self.tmp, "ownlink")
        os.symlink(self.wt, link_own)
        self.assertEqual(self.run_hook(f"git -C {link_own} push origin {OWN}").returncode, 0)

    def test_pane_in_another_worktree_refused(self):
        """T-0743: a pane cd'd into another worktree cannot push, with or without -C, even naming its own worktree."""
        other = os.path.join(self.home, "worktrees", "G-018-T-0001")
        git("init", "-q", "-b", "main", other, cwd=self.tmp)
        for cmd in (f"git push origin {OWN}", f"git -C . push origin {OWN}", f"git -C {other} push origin {OWN}",
                    f"git -C {self.wt} push origin {OWN}"):
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd, cwd=other)
                self.assertEqual(p.returncode, 2, p.stderr)
                self.assertIn("own worktree", p.stderr)

    def test_unresolvable_worktree_refuses(self):
        """T-0743: no DB, no task row or an empty worktree column refuses (fail closed)."""
        os.remove(os.path.join(self.home, "db", "factory.db"))
        p = self.run_hook(f"git push origin {OWN}")
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("cannot resolve the worktree", p.stderr)
        self.write_db("")
        self.assertEqual(self.run_hook(f"git push origin {OWN}").returncode, 2)
        self.write_db(self.wt, task="T-0001")  # a DB without this task's row
        self.assertEqual(self.run_hook(f"git push origin {OWN}").returncode, 2)

    def test_db_worktree_not_cwd_decides(self):
        """T-0743: the DB path is the pin; a worktree-looking cwd that is not the recorded one is refused, the recorded one passes."""
        other = os.path.join(self.home, "worktrees", "G-018-T-0002")
        git("init", "-q", "-b", "main", other, cwd=self.tmp)
        self.write_db(other)
        self.assertEqual(self.run_hook(f"git push origin {OWN}").returncode, 2)
        self.assertEqual(self.run_hook(f"git push origin {OWN}", cwd=other).returncode, 0)

    def test_non_coder_roles_never_push(self):
        """T-0755: qa/reviewer/pm/staff-engineer/scout/evaluator: every push shape is refused, even --dry-run."""
        shapes = (
            "git push", "git push origin", f"git push origin {OWN}", "git push origin HEAD:main", "git push origin +HEAD:main",
            "git push --dry-run origin HEAD:main", "git push -n origin main", "git push --all origin", "git push --mirror origin",
            "git push --tags origin", "git push --force origin main", f"git -C {self.wt} push origin {OWN}",
            "git -c remote.origin.url=https://example.com/x.git push origin main", "git -c push.default=current push",
            "git push https://example.com/x.git main", "git send-pack origin main", "git subtree push --prefix=a origin main",
            "git -c alias.p=push p origin main",
        )
        for role in ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator"):
            for cmd in shapes:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role=role, agent=f"{role}-t-0689")
                    self.assertEqual(p.returncode, 2, p.stderr)
                    self.assertTrue("may not run git push" in p.stderr or "alias" in p.stderr, p.stderr)

    def test_non_coder_git_option_and_env_bypasses_refused(self):
        """T-0755 retry: value-taking git global options, unknown options and env/--config-env aliases hide no push."""
        shapes = (
            "git --attr-source HEAD push origin main", "git --attr-source HEAD push --dry-run origin main",
            "git --attr-source=HEAD push origin main", "git --namespace ns push origin main",
            "git --git-dir /x/.git push origin main", "git --work-tree /x push origin main",
            "git --super-prefix p/ push origin main", "git --no-pager push origin main", "git -p push origin main",
            "git --future-option push origin main", "git --future-option value push origin main",
            "git --config-env=alias.p=PUSHVAR p origin main", "git --config-env alias.p=PUSHVAR p origin main",
            "git -c alias.s=send-pack s origin main", "git -c alias.s=subtree s push origin",
            "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.p GIT_CONFIG_VALUE_0=push git p origin main",
            "GIT_CONFIG_PARAMETERS=\"'alias.p=push'\" git p origin main",
            "env GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.p GIT_CONFIG_VALUE_0=push git p origin main",
            "export GIT_CONFIG_COUNT=1; git p origin main", "export GIT_CONFIG_KEY_0=alias.p",
            "GIT_CONFIG_VALUE_0=push git status", "GIT_CONFIG_PARAMETERS=x",
        )
        for role in ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator"):
            for cmd in shapes:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role=role, agent=f"{role}-t-0689")
                    self.assertEqual(p.returncode, 2, p.stderr)

    def test_non_coder_git_option_forms_still_allowed(self):
        for cmd in ("git --no-pager log -1", "git --attr-source HEAD status", f"git --git-dir {self.wt}/.git status",
                    "git -c core.pager=cat log -1", "git --namespace ns status"):
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd, role="reviewer", agent="reviewer-t-0689")
                self.assertEqual(p.returncode, 0, p.stderr)

    def test_non_coder_non_push_git_allowed(self):
        for cmd in ("git status", "git log -1", f"git -C {self.wt} diff", "git fetch origin"):
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd, role="qa", agent="qa-t-0689")
                self.assertEqual(p.returncode, 0, p.stderr)

    def test_judge_hq_and_cleaner_unchanged(self):
        """Judge and HQ are not in the no-push set (no refusal from this rule); the cleaner still gets its own messages."""
        p = self.run_hook("git push origin HEAD:main", role="judge", agent="judge-1")
        self.assertNotIn("may not run git push", p.stderr)
        env = dict(os.environ, HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home, FACTORY_AGENT="lead-g-1")
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": "git push origin HEAD:main"}, "cwd": self.wt})
        h = subprocess.run([sys.executable, HOOK, "--hq"], input=payload, capture_output=True, text=True, timeout=30, env=env)
        self.assertNotIn("may not run git push", h.stderr)
        c = self.run_hook("git push origin HEAD:main", role="cleaner", agent="cleaner-1")
        self.assertEqual(c.returncode, 2)
        self.assertIn("cleaner may only delete", c.stderr)
        c = self.run_hook("git push origin --delete factory/t-0001", role="cleaner", agent="cleaner-1")
        self.assertNotIn("may not run git push", c.stderr)


if __name__ == "__main__":
    unittest.main()
