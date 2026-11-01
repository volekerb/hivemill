"""T-0334: workers cannot persist config into the live box (global/system git config, ~/.config/git, shell init files)."""
import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
import pwd
import re
import subprocess
import sys
import tempfile
import shutil
import unittest
from unittest import mock

from harness import rendered_settings
from guard_impl import hook_run, impl

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
PW = os.path.realpath(pwd.getpwuid(os.getuid()).pw_dir)  # the hook only reads the text of a command: nothing here writes it
ROLES = ("coder", "qa", "reviewer")
NO_PUSH = ("qa", "reviewer")  # T-0755: read-only git allow-list roles
NO_PUSH_CONFIG_READ = re.compile(r"--get|--list| -l(?: |$)")
WORKERS = ("pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "scout", "evaluator")
PROTECTED = [".gitconfig", ".config/git/config", ".config/git/attributes", ".config/git/ignore", ".bashrc", ".zshrc",
             ".zshenv", ".profile", ".bash_profile", ".zprofile"]


def settings_groups(role):
    with open(rendered_settings(role)) as f:
        return json.load(f)["hooks"]["PreToolUse"]


def hook_subprocess(payload, role="coder", hq=False):
    argv = (["--hq"] if hq else ["--role", role])
    return hook_run(json.dumps(payload), argv)


_HOOK_MOD = []


def hook(payload, role="coder", hq=False):
    """The hook's main() run in-process (the matrix below makes ~600 calls; a subprocess each costs ~40 ms of startup).
    Same argv, stdin, exit code and stderr as the script; assert_in_process_matches_real_hook (called from
    test_hq_mode_unchanged) keeps the real entry point covered."""
    if impl() != "python":  # GUARD_IMPL=go|diff: the real binaries decide, never the in-process Python module
        return hook_subprocess(payload, role, hq)
    if not _HOOK_MOD:
        loader = importlib.machinery.SourceFileLoader("worker_guard_under_test", HOOK)
        mod = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
        loader.exec_module(mod)
        _HOOK_MOD.append(mod)
    mod = _HOOK_MOD[0]
    argv = ["worker-guard.py"] + (["--hq"] if hq else ["--role", role])
    err = io.StringIO()
    mod.SESSION_ID = None
    with mock.patch.object(sys, "argv", argv), mock.patch.object(sys, "stdin", io.StringIO(json.dumps(payload))), \
            contextlib.redirect_stderr(err):
        rc = mod.main()
    return subprocess.CompletedProcess(argv, rc, "", err.getvalue())


def assert_in_process_matches_real_hook(test):
    for payload, role, hq in (
            ({"tool_name": "Bash", "tool_input": {"command": "echo x > ~/.bashrc"}}, "coder", False),
            ({"tool_name": "Bash", "tool_input": {"command": "git config --global user.name x"}}, "qa", False),
            ({"tool_name": "Bash", "tool_input": {"command": "ls -la"}}, "reviewer", False),
            ({"tool_name": "Bash", "tool_input": {"command": "echo x > ~/.bashrc"}}, "coder", True),
            ({"tool_name": "Write", "tool_input": {"file_path": f"{PW}/.bashrc"}}, "coder", False),
            ({"tool_name": "Write", "tool_input": {}}, "coder", False)):
        real, fast = hook_subprocess(payload, role, hq), hook(payload, role, hq)
        test.assertEqual((fast.returncode, fast.stderr), (real.returncode, real.stderr), payload)
    blocked = hook_subprocess({"tool_name": "Bash", "tool_input": {"command": "echo x > ~/.bashrc"}})
    test.assertEqual(blocked.returncode, 2)


def bash(cmd, role="coder", hq=False, cwd=None):
    return hook({"tool_name": "Bash", "tool_input": {"command": cmd}, **({"cwd": cwd} if cwd else {})}, role, hq)


_HOME_PATCH = None


def setUpModule():
    """Root cause T-0636: the hook reads public_text_repos from $FACTORY_HOME's DB. A pane exports the live home, whose
    config turns the public-text rule on, so plain `git -c alias.*` / quoted-backtick commands block; CI has no DB. Run
    against an empty temp home so the result does not depend on the caller's environment."""
    global _HOME_PATCH
    tmp = tempfile.mkdtemp(prefix="zz-test-factory-guardhome-")
    _HOME_PATCH = mock.patch.dict(os.environ, {"FACTORY_HOME": tmp})
    _HOME_PATCH.start()
    unittest.addModuleCleanup(shutil.rmtree, tmp, True)
    unittest.addModuleCleanup(_HOME_PATCH.stop)


class PersistGuardTests(unittest.TestCase):
    def blocked(self, cmd, role="coder", cwd=None):
        p = bash(cmd, role, cwd=cwd)
        self.assertEqual(p.returncode, 2, f"should block ({role}): {cmd!r}\n{p.stderr}")
        return p

    def allowed(self, cmd, role="coder", cwd=None):
        p = bash(cmd, role, cwd=cwd)
        self.assertEqual(p.returncode, 0, f"should allow ({role}): {cmd!r}\n{p.stderr}")

    def test_git_config_global_and_system_writes_blocked_for_every_worker_role(self):
        for role in ROLES:
            for cmd in ("git config --global user.name x", "git config --global --add core.fsmonitor /tmp/x",
                        "git config --global --unset user.name", "git config --global --unset-all a.b",
                        "git config --global --replace-all a.b c", "git config --global --edit",
                        "git config --global -e", "git config --global --rename-section a b",
                        "git config --global --remove-section a", "git config --system core.fsmonitor x",
                        "git config --global set a.b c", "git config --global unset a.b",
                        "git -c core.pager=cat config --global a.b c", "git -C /tmp config --global a.b c",
                        "git config --glo a.b c", "git config a.b c --global",
                        "env git config --global a.b c", "sh -c 'git config --global a.b c'"):
                self.blocked(cmd, role)

    def test_git_config_file_to_protected_path_blocked(self):
        for role in ROLES:
            for rel in PROTECTED:
                self.blocked(f"git config -f {PW}/{rel} core.fsmonitor x", role)
        for cmd in ("git config --file ~/.gitconfig a.b c", "git config --file=$HOME/.config/git/config a.b c",
                    "git config -f${HOME}/.gitconfig a.b c", "git config -f /etc/gitconfig a.b c",
                    "git config -f ~/x/../.gitconfig a.b c", "git config --file ~/.gitconfig --unset a.b"):
            self.blocked(cmd)

    def test_alias_routes_to_global_config_blocked(self):
        self.blocked("git -c alias.c=config c --global a.b c")
        self.blocked("git -c 'alias.c=!git config --global a.b c' c")
        self.blocked("git -c 'alias.c=!echo x >> ~/.bashrc' c")

    def test_bash_writes_to_every_protected_path_blocked(self):
        for role in ROLES:  # every spelling and tool for the coder; qa and reviewer share the same code path
            for rel in PROTECTED if role == "coder" else PROTECTED[:1]:
                for spell in (f"~/{rel}", f"$HOME/{rel}", f"{PW}/{rel}", f"{PW}/x/../{rel}"):
                    for tmpl in ("echo x > {p}", "echo x >> {p}", "echo x | tee {p}", "cp /tmp/a {p}",
                                 "mv /tmp/a {p}", "ln -sf /tmp/a {p}", "install -m644 /tmp/a {p}",
                                 "sed -i s/a/b/ {p}", "dd if=/tmp/a of={p}", "bash -c 'echo x > {p}'"):
                        self.blocked(tmpl.format(p=spell), role)
        for cmd in ("cp /tmp/a /etc/gitconfig", "echo x > /etc/gitconfig", "install /tmp/a /etc/gitconfig",
                    "cp /tmp/a ~/.config/git/", "cp -t ~/.config/git /tmp/a", "mv ~/.bashrc /tmp/gone",
                    "ln /tmp/a ~/.bashrc", "ln ~/.bashrc /tmp/hard", "echo x > ~/.config/git/sub/new"):
            self.blocked(cmd)

    def test_relative_and_cd_spellings_blocked(self):
        self.blocked("cd ~ && echo x > .bashrc")
        self.blocked("cd && echo x >> .zshrc")
        self.blocked("cd ~/.config/git && echo x > config")
        self.blocked("echo x > .bashrc", cwd=PW)
        self.blocked("echo x > ../.zshrc", cwd=os.path.join(PW, "factory"))
        self.blocked("cd $SOMEWHERE && echo x > .bashrc")  # unknown cwd: the name alone decides
        self.blocked("echo x > ~/.bash*")
        self.blocked("echo x > ~/.{bashrc,motd}")
        self.blocked("echo x > $X/.bashrc")

    def test_self_review_bypasses_blocked(self):
        for cmd in ("cp /tmp/.bashrc ~/", "cp /tmp/.bashrc ~", "cp -t ~ /tmp/.gitconfig", "mv /tmp/.bashrc ~",
                    "cp /tmp/config ~/.config/git/", "f=~/.bashrc; echo x > $f", "f=.bashrc; echo x > ~/$f",
                    "export F=.bashrc; echo x > ~/$F", "echo x > ~/.$(echo bashrc)", "echo x > $(echo ~)/.bashrc",
                    "echo x > ~/.bash$'r'c", "git -c alias.c='!git config --global' c a b",
                    "echo x > ~/.config/g*/config", "echo x > ~/.config/*/config", "ln -sfn /tmp/e ~/.config"):
            self.blocked(cmd)
        for cmd in ("cp /tmp/a.txt ~/", "f=/tmp/o; echo x > $f", "echo x > $(mktemp -d)/a", "echo x > $OUT/a",
                    "cp .bashrc.sample /tmp/"):
            self.allowed(cmd)

    def test_symlink_spelling_blocked(self):
        with tempfile.TemporaryDirectory(prefix="zz-test-persist-") as tmp:
            tmp = os.path.realpath(tmp)
            for rel in (".bashrc", ".gitconfig", ".config/git"):
                link = os.path.join(tmp, "l" + rel.replace("/", "_").replace(".", ""))
                os.symlink(os.path.join(PW, rel), link)
                self.blocked(f"echo x > {link}")
                self.blocked(f"echo x > {link}/config" if rel == ".config/git" else f"cp /tmp/a {link}")

    def test_interpreter_and_editor_naming_protected_file_blocked(self):
        self.blocked("python3 -c \"open('/x/.bashrc','a')\"")
        self.blocked("python3 -c \"open('~/.gi'+'tconfig','w')\"")
        self.blocked("vim ~/.zshrc")
        self.blocked("perl -pi -e 's/a/b/' ~/.profile")
        self.blocked("python3 - <<'EOF'\nopen('/h/.bashrc','a').write('x')\nEOF")
        self.blocked("awk 'BEGIN{print 1 > \"/h/.zshenv\"}'")

    def test_read_only_and_repo_local_forms_allowed(self):
        for role in ROLES:
            for cmd in ("git config --global --get user.name", "git config --global user.name",
                        "git config --global --list", "git config --global -l", "git config --global --get-all a.b",
                        "git config --global --get-regexp user", "git config --global get user.name",
                        "git config --global list", "git config --global --type bool --get a.b",
                        "git config --system --list", "git config -f ~/.gitconfig --get user.name",
                        "git config --file ~/.gitconfig --list", "git config --get user.name",
                        "git config user.name", "git config --local --get a.b",
                        "git -c alias.c=config c --get a.b",
                        "cat ~/.bashrc", "grep PATH ~/.zshrc", "cat ~/.gitconfig ~/.config/git/config",
                        "cp ~/.bashrc /tmp/bashrc.copy", "cp ~/.config/git/config /tmp/c", "ls ~/.config/git",
                        "echo x > .bashrc.sample", "echo x > notes.profile", "echo x > ~/.bashrc.d.txt",
                        "echo x > /tmp/gitconfig", "echo x > ~/.config/gitx", "echo x > ~/.config/gitkraken/c",
                        "sed -i s/a/b/ src/.profile.in", "echo x >> out.txt", "tee notes.txt"):
                if role in NO_PUSH and cmd.startswith("git") and (cmd.startswith("git -c") or not NO_PUSH_CONFIG_READ.search(cmd)):
                    continue  # T-0755: read-only roles run only the --get/--list forms of git config (allow-list)
                self.allowed(cmd, role)

    def test_repo_local_config_in_own_worktree_allowed(self):
        with tempfile.TemporaryDirectory(prefix="zz-test-persist-") as tmp:
            subprocess.run(["git", "init", "-q", tmp], check=True, capture_output=True)
            for cmd in ("echo x > .gitconfig", "echo x > .bashrc", "git config --get user.name",
                        "git config user.name", "git config --list"):
                self.allowed(cmd, cwd=tmp)

    def test_git_config_write_with_env_assignment_blocked(self):
        for role in ROLES:
            for cmd in ("GIT_CONFIG=~/.gitconfig git config core.fsmonitor x",
                        "export GIT_CONFIG=~/.gitconfig; git config core.fsmonitor x",
                        "GIT_CONFIG=$HOME/.config/git/config git config a.b c",
                        "env GIT_CONFIG_GLOBAL=~/.gitconfig git config a.b c",
                        "declare -x GIT_CONFIG=~/.gitconfig; git config a.b c",
                        "HOME=/tmp git config --file .gitconfig a.b c",
                        "export XDG_CONFIG_HOME=/tmp/x && git config -f x/git/config a.b c"):
                self.blocked(cmd, role)

    def test_repo_local_config_and_reads_stay_allowed(self):
        for cmd in ("git config --get a.b", "git -C . config --list", "git config --get user.name",
                    "awk 1 ~/.zshrc", "awk '{print $1}' ~/.gitconfig", "echo HOME=1; git status"):
            self.allowed(cmd)
        self.blocked("awk -i inplace 1 ~/.zshrc")

    def test_hq_mode_unchanged(self):
        for cmd in ("git config --global user.name x", "echo x > ~/.bashrc", "cp /tmp/a ~/.gitconfig"):
            self.assertEqual(bash(cmd, hq=True).returncode, 0, cmd)
        p = hook({"tool_name": "Write", "tool_input": {"file_path": f"{PW}/.bashrc"}}, hq=True)
        self.assertEqual(p.returncode, 0, p.stderr)
        assert_in_process_matches_real_hook(self)


class PersistEditToolTests(unittest.TestCase):
    def call(self, tool, key, path, role="coder", cwd="/tmp"):
        return hook({"tool_name": tool, "cwd": cwd, "tool_input": {key: path}}, role)

    def test_edit_write_payloads_to_protected_paths_blocked(self):
        for role in ROLES:
            for tool, key in (("Edit", "file_path"), ("Write", "file_path"), ("MultiEdit", "file_path"),
                              ("NotebookEdit", "notebook_path")):
                for rel in PROTECTED if tool == "Write" else PROTECTED[:3]:
                    for spell in (f"{PW}/{rel}", f"~/{rel}", f"$HOME/{rel}", f"{PW}/x/../{rel}"):
                        p = self.call(tool, key, spell, role)
                        self.assertEqual(p.returncode, 2, f"{role} {tool} {spell}\n{p.stderr}")
            self.assertEqual(self.call("Write", "file_path", "/etc/gitconfig", role).returncode, 2)
        self.assertEqual(self.call("Edit", "file_path", ".bashrc", cwd=PW).returncode, 2)
        self.assertEqual(self.call("Write", "file_path", "../.zshrc", cwd=os.path.join(PW, "factory")).returncode, 2)

    def test_symlink_payload_blocked(self):
        with tempfile.TemporaryDirectory(prefix="zz-test-persist-") as tmp:
            link = os.path.join(os.path.realpath(tmp), "rc")
            os.symlink(os.path.join(PW, ".zshrc"), link)
            self.assertEqual(self.call("Write", "file_path", link).returncode, 2)

    def test_other_paths_allowed_and_unreadable_payload_fails_closed(self):
        for path in ("/tmp/x.txt", f"{PW}/factory/README.md", f"{PW}/.config/gitx", f"{PW}/.bashrc.bak", "notes.md"):
            p = self.call("Write", "file_path", path)
            self.assertEqual(p.returncode, 0, f"{path}\n{p.stderr}")
        self.assertEqual(hook({"tool_name": "Write", "tool_input": {}}).returncode, 2)
        self.assertEqual(hook({"tool_name": "Edit", "tool_input": {"file_path": 7}}).returncode, 2)
        self.assertEqual(hook({"tool_name": "Read", "tool_input": {"file_path": f"{PW}/.bashrc"}}).returncode, 0)

    def test_worker_settings_route_edit_tools_to_the_hook_and_hq_settings_do_not(self):
        for role in WORKERS:
            groups = settings_groups(role)
            edit = [g for g in groups if set(g.get("matcher", "").split("|")) == PERSIST_TOOLS]
            self.assertEqual(len(edit), 1, role)
            self.assertTrue(edit[0]["hooks"][0]["command"].endswith(f"worker-guard.py --role {role}"), role)
        for role in ("boss", "director", "lead"):
            self.assertFalse([g for g in settings_groups(role) if "Edit" in g.get("matcher", "")], role)


PERSIST_TOOLS = {"Edit", "Write", "MultiEdit", "NotebookEdit"}

if __name__ == "__main__":
    unittest.main()
