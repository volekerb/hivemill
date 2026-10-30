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
        for cmd in ("git --no-pager log -1", "git -P log -1", "git --no-optional-locks status", "git --version"):
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd, role="reviewer", agent="reviewer-t-0689")
                self.assertEqual(p.returncode, 0, p.stderr)

    def test_non_coder_non_push_git_allowed(self):
        for cmd in ("git status", "git log -1", f"git -C {self.wt} diff", "git fetch origin"):
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd, role="qa", agent="qa-t-0689")
                self.assertEqual(p.returncode, 0, p.stderr)

    ROLES = ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator")
    # T-0755 attempt 3: the allow-list. Each of these exited 0 for a read-only role at f792b8f93 (the push deny-list).
    ALLOWLIST_REFUSED = (
        "p=push; git $p origin main", "p=push; git ${p} x", 'p=push; git "$p" origin main', "git `echo push` origin main",
        "p=push; git $p --dry-run origin main",
        "GIT_CONFIG_GLOBAL=f git p origin main", "GIT_CONFIG_SYSTEM=f git p origin main", "GIT_CONFIG=f git p origin main",
        "HOME=d git p", "env HOME=d git p", "export HOME=d; git p", "export GIT_CONFIG_GLOBAL=f; git p",
        "unset HOME; git p", "HOME=d; git p", "PATH=/x:$PATH git status",
        "git -c include.path=f p", "git -c includeIf.x.path=f p", "git -c core.pager=cat log", "git p", "git p origin main",
        "git fetch --upload-pack='x' .", "git fetch --upload-pack=x origin", "git fetch -u x origin",
        "git fetch 'ext::sh -c x'", "git fetch origin 'ext::sh -c x'", "git fetch https://example.com/x.git",
        "git fetch /tmp/other", "git ls-remote --upload-pack=x origin", "git ls-remote git@example.com:x/y.git",
        "git fetch --server-option=x origin", "git fetch $r", "git ls-remote $r",
        "o=--output=y; git diff $o", "o=--ext-diff; git log -1 $o", "git log -1 --format=x $(echo --output=y)",
        "git fetch origin main:refs/heads/x", "git fetch origin +main:main",
        "git grep -O x", "git grep -nO x", "git grep --open-files-in-pager=x y", "git grep --open-f=x y", "git grep $pat",
        "git log --ext-diff", "git diff --ext-diff", "git show --output=f", "git log --output f", "git diff --outp=f",
        "git -p log", "git --paginate log",
        "echo '[alias] p=push' >> .git/config", "tee -a .git/config < /dev/null", "cp /dev/null .git/config",
        "sed -i s/a/b/ .git/config", "echo x > .git/hooks/pre-push", "echo x > .git/info/attributes",
        "echo '[alias]' >> ~/.gitconfig", "echo x > ~/.config/git/config",
        "git config --file .git/config alias.p push",
        "printf push | xargs git", "echo push | xargs -n1 git", "find . -maxdepth 0 -exec git push \\;",
        "find . -maxdepth 0 -execdir git p \\;", "find . -ok git p \\;",
        "git-push origin main", "/usr/lib/git-core/git-push", "/usr/lib/git-core/git push origin main", "./git push",
        "$G push", "\\git p", "git rebase --exec 'git push' HEAD~1", "git rebase -x p HEAD~1", "git bisect run p",
        "git submodule foreach git push", "git filter-branch -f", "git difftool", "git archive --remote=x HEAD",
        "git push origin main", "git stash", "git commit -m x", "git checkout -b x", "git reset --hard", "git add .",
        "git branch -D x", "git branch new", "git remote add x y", "git remote set-url origin y", "git worktree add ../x",
        "git config alias.p push", "git config --unset alias.p", "git config --list --edit", "git config --global alias.p push",
        "git send-pack /x HEAD:main", "git subtree push --prefix=a origin main", "git daemon", "git var GIT_PAGER",
        "git remote update",
    )
    ALLOWLIST_ALLOWED = (
        "git log --oneline -5", "git -C {wt} diff origin/main...HEAD", "git show origin/x:f", "git fetch origin",
        "git fetch origin main", "git status", "git branch -a", "git config --get user.name", "git blame f",
        "git grep -n x", "git merge-tree --write-tree origin/main HEAD", "git rev-parse HEAD", "git ls-remote origin",
        "git fetch origin refs/heads/main:refs/remotes/origin/main", "git fetch --prune origin", "git ls-remote --heads origin",
        "git diff --stat HEAD~1", "git log --no-ext-diff -1", "git remote -v", "git remote get-url origin", "git remote", "git remote show origin",
        "git worktree list", "git config --list", "git config --get-regexp remote", "git branch --list 'f*'",
        "git cat-file -p HEAD", "git show-ref", "git describe --tags --always", "git --version",
    )

    REMOTE_WRITES = (
        "set-url origin https://example.com/evil.git", "add x https://example.com/evil.git", "rename origin y",
        "remove origin", "rm origin", "set-head origin -a", "set-branches origin main", "prune origin", "update",
        "set-url --push origin https://example.com/evil.git", "bogus", "show", "get-url", "show origin extra",
        "get-url origin extra",
    )
    REMOTE_READS = (
        "", "-v", "--verbose", "get-url origin", "get-url --push origin", "get-url --all origin", "show origin",
        "show -n origin", "-v show origin", "--verbose show -n origin",
    )

    def test_non_coder_git_remote_write_forms_refused(self):
        """T-0755 attempt 4: every word after `remote` is checked; -v in front must not hide a write form."""
        for role in self.ROLES:
            for form in self.REMOTE_WRITES:
                for pre in ("", "-v ", "--verbose "):
                    cmd = f"git remote {pre}{form}"
                    with self.subTest(role=role, cmd=cmd):
                        p = self.run_hook(cmd, role=role, agent=f"{role}-t-0689")
                        self.assertEqual(p.returncode, 2, p.stderr)

    def test_non_coder_git_remote_read_forms_allowed(self):
        for role in self.ROLES:
            for form in self.REMOTE_READS:
                cmd = f"git remote {form}".strip()
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role=role, agent=f"{role}-t-0689")
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_non_coder_git_allow_list_refuses(self):
        """T-0755 acceptance 1: fail-closed allow-list (dynamic subcommand, config-file aliases, fetch helpers, pager,
        config writes, xargs/find launchers, git-* helpers, unknown builtins)."""
        for role in self.ROLES:
            for cmd in self.ALLOWLIST_REFUSED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role=role, agent=f"{role}-t-0689")
                    self.assertEqual(p.returncode, 2, p.stderr)

    def test_non_coder_git_allow_list_allows(self):
        """T-0755 acceptance 2: the read-only forms stay allowed."""
        for role in self.ROLES:
            for cmd in self.ALLOWLIST_ALLOWED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd.format(wt=self.wt), role=role, agent=f"{role}-t-0689")
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_non_coder_script_files_are_inspected_or_refused(self):
        """T-0755 point 7: a script file run by a no-push role is read as sh (its body obeys the allow-list); one for another interpreter is refused."""
        def script(name, body, mode=0o755):
            path = os.path.join(self.tmp, name)
            with open(path, "w") as f:
                f.write(body)
            os.chmod(path, mode)
            return path
        push, py = script("push.sh", "git push origin main\n"), script("py.py", "#!/usr/bin/env python3\nprint(1)\n")
        ok = script("ok.sh", "git status\n")
        for role in self.ROLES:
            for cmd in (push, f"bash {push}", f"source {push}", py):
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role=role, agent=f"{role}-t-0689").returncode, 2)
            with self.subTest(role=role, cmd=ok):
                self.assertEqual(self.run_hook(ok, role=role, agent=f"{role}-t-0689").returncode, 0)

    def test_coder_cleaner_judge_not_under_allow_list(self):
        """The allow-list is for the no-push roles only: coder, cleaner and judge never get its message."""
        for role in ("coder", "cleaner", "judge"):
            p = self.run_hook("git p origin main", role=role, agent=f"{role}-t-0689")
            self.assertNotIn("read-only allow-list", p.stderr)

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
