"""G-029 T-0846: the researcher role through the real worker-guard hook (GUARD_IMPL=go|diff runs the Go port too).

A researcher is a no-push role that may clone a public https repo into its research dir
(<state>/factory/research/<name>) and write only there or in its scratchpad."""
import json
import os
import pwd
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402

HOME = pwd.getpwuid(os.getuid()).pw_dir
LIVE = os.path.join(HOME, "factory")
URL = "https://github.com/octocat/Hello-World.git"
AT = chr(64)


class ResearcherBase(unittest.TestCase):
    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = os.path.realpath(td.name)
        self.state = os.path.join(self.tmp, "state")
        self.rdir = os.path.join(self.state, "factory", "research", "T-9999")
        self.scratch = os.path.join(self.tmp, "scratch")
        os.makedirs(self.rdir)
        os.makedirs(self.scratch)
        self.env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_HOME")}
        self.env["WORKER_GUARD_STATE"] = self.state
        self.env["WORKER_GUARD_SCRATCH"] = self.scratch

    def run_hook(self, payload, role="researcher"):
        return hook_run(json.dumps(payload), ["--role", role], env=self.env)

    def bash(self, cmd, cwd=None, role="researcher"):
        return self.run_hook({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": cwd or self.rdir,
                              "session_id": "s1"}, role)

    def tool(self, name, path, cwd=None, key="file_path"):
        return self.run_hook({"tool_name": name, "tool_input": {key: path}, "cwd": cwd or self.rdir, "session_id": "s1"})

    def ok(self, p):
        self.assertEqual(p.returncode, 0, p.stderr)

    def deny(self, p, what=""):
        self.assertEqual(p.returncode, 2, f"should block {what}: {p.stdout}")


class CloneAllowed(ResearcherBase):
    def test_public_clone(self):
        for cmd in (f"git clone --depth 1 {URL} hello",
                    f"git clone --depth=1 --quiet {URL} hello",
                    f"git clone --branch v1 --single-branch --filter=blob:none --no-tags {URL} hello",
                    f"git clone -q -b main -- {URL} sub/hello",
                    f"git clone {URL} {self.rdir}/abs",
                    f"cd sub && git clone {URL} hello"):
            with self.subTest(cmd=cmd):
                self.ok(self.bash(cmd))

    def test_clone_into_scratchpad(self):
        self.ok(self.bash(f"git clone {URL} here", cwd=self.scratch))
        self.ok(self.bash(f"git clone {URL} {self.scratch}/x"))

    def test_read_only_git_in_clone(self):
        for cmd in ("git -C hello log -1", "git -C hello status", "git log -1", "git fetch origin"):
            with self.subTest(cmd=cmd):
                self.ok(self.bash(cmd))


class CloneBlocked(ResearcherBase):
    def test_clone_abuse(self):
        D = "$D"
        for cmd in ("git clone ext::sh -c x d", "git clone file:///home x", "git clone /srv/other/repo x",
                    "git clone ssh://github.com/a/b x", "git clone git" + AT + "github.com:a/b x",
                    "git clone https://u:p" + AT + "github.com/a/b x",
                    "git clone http://github.com/a/b x", "git clone https://127.0.0.1/a/b x",
                    "git clone https://localhost/a/b x",
                    "git clone -c core.hooksPath=x https://github.com/a/b d",
                    "git clone --config core.hooksPath=x https://github.com/a/b d",
                    "git clone --template=/t https://github.com/a/b d",
                    "git clone --upload-pack=x https://github.com/a/b d",
                    "git clone -u x https://github.com/a/b d",
                    "git clone --recurse-submodules https://github.com/a/b d",
                    "git clone --separate-git-dir=/x https://github.com/a/b d",
                    "git clone --reference /x https://github.com/a/b d",
                    "git clone --shared https://github.com/a/b d",
                    "git clone --dep 1 https://github.com/a/b d",
                    "git clone https://github.com/a/b ../../escape",
                    "git clone https://github.com/a/b sub/../../escape",
                    f"git clone https://github.com/a/b {LIVE}/x",
                    "git clone https://github.com/a/b /tmp/x",
                    "git clone https://github.com/a/b ~/x",
                    f"git clone https://github.com/a/b {D}",
                    "git clone https://github.com/a/b",
                    "git clone https://github.com/a/b d e", "git clone https://github.com/a/b d*",
                    "git clone --branch -x https://github.com/a/b d",
                    f"git -C {LIVE} clone https://github.com/a/b d",
                    "git -c alias.clone=push clone https://github.com/a/b d"):
            with self.subTest(cmd=cmd):
                self.deny(self.bash(cmd), cmd)

    def test_clone_wrappers_and_variants(self):
        c = f"git clone -c core.hooksPath=x {URL} d"
        for cmd in (c, f"true; {c}", f"true && {c}", f"echo $({c})", f"env {c}", f"nohup {c}", f"timeout 5 {c}",
                    f"/usr/bin/{c}", f"./{c}", f"bash -c '{c}'", f"zsh -c \"{c}\"", "'gi" + "t' clone ext::sh d",
                    f"git clone {URL} ../x", f"nohup git clone {URL} /tmp/x", "env git clone ext::sh d"):
            with self.subTest(cmd=cmd):
                self.deny(self.bash(cmd), cmd)

    def test_clone_outside_research_cwd(self):
        for cwd in (self.tmp, LIVE, HOME, os.path.join(self.state, "factory", "research"), "/tmp"):
            with self.subTest(cwd=cwd):
                self.deny(self.bash(f"git clone {URL} hello", cwd=cwd), cwd)

    def test_clone_symlink_escape(self):
        os.symlink(HOME, os.path.join(self.rdir, "link"))
        self.deny(self.bash(f"git clone {URL} link/x"))

    def test_clone_after_fs_change_on_same_line(self):
        # B1: a symlink/move/clone planted earlier on the line relocates the clone outside the research dir
        for cmd in (f"ln -s {LIVE}/worktrees d && git clone {URL} d/x",
                    f"ln -s {HOME}/.claude/skills d && git clone {URL} d/evil",
                    f"mv x d && git clone {URL} d/x",
                    f"git clone {URL} d && git clone {URL} d/x"):
            with self.subTest(cmd=cmd):
                self.deny(self.bash(cmd), cmd)
                self.deny(self.bash(cmd, role="reviewer"), cmd)

    def test_other_roles_cannot_clone_here(self):
        # coder and the other no-push roles keep their rules: clone stays refused for the read-only roles
        for role in ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator"):
            with self.subTest(role=role):
                self.deny(self.bash(f"git clone {URL} hello", role=role), role)


class WritesBlocked(ResearcherBase):
    def test_git_and_spawn_blocked(self):
        for cmd in ("git push origin HEAD", "git commit -m x", "git -C hello push", "git merge x", "gh pr merge 1",
                    "gh pr create", "factory spawn coder", "factory judge T-1", "claude -p hi", "herdr pane split",
                    "gh api -X POST repos/a/b", "gh api repos/a/b -f x=1", "gh pr view 1; gh pr merge 1", "gh issue create", "gh pr -R view merge 5",
                    "gh repo delete a/b", "factory task update T-1 --status done", "factory pr create", "factory goal add x",
                    "git add .", "git checkout -b x", "git config user.name x", "git push --force origin main",
                    "git remote add x https://github.com/a/b"):
            with self.subTest(cmd=cmd):
                self.deny(self.bash(cmd), cmd)

    def test_secret_bash(self):
        for cmd in ("cat ~/.config/factory/gh-worker-rw-token", "cat ~/.config/factory/worker-oauth-token",
                    "cat ~/.ssh/id_ed25519", "cat $HOME/.claude/.credentials.json", "grep x ~/.netrc",
                    "cat ~/.config/gh/hosts.yml", "cp ~/.aws/credentials .", "cat '~/.ssh'/id_rsa"):
            with self.subTest(cmd=cmd):
                self.deny(self.bash(cmd), cmd)

    def test_ordinary_bash_allowed(self):
        for cmd in ("ls", "echo hi > notes.md", "python3 -V", "git status", "curl -sI https://example.com",
                    "factory whoami", "factory inbox", "factory msg lead:g-1 s b", "factory done ok", "factory report h b",
                    "factory task show T-1", "factory task note T-1 x", "gh pr view 1",
                    "gh repo view a/b", "gh search repos x", "gh api repos/a/b"):
            with self.subTest(cmd=cmd):
                self.ok(self.bash(cmd))


class EditWrite(ResearcherBase):
    def test_allowed_paths(self):
        for name in ("Edit", "Write", "NotebookEdit", "MultiEdit"):
            key = "notebook_path" if name == "NotebookEdit" else "file_path"
            for path in (os.path.join(self.rdir, "notes.md"), "rel/notes.md", os.path.join(self.scratch, "a.txt")):
                with self.subTest(tool=name, path=path):
                    self.ok(self.tool(name, path, key=key))

    def test_blocked_paths(self):
        os.symlink(HOME, os.path.join(self.rdir, "link"))
        os.symlink(self.tmp, os.path.join(self.rdir, "up"))
        for name in ("Edit", "Write"):
            for path in (f"{LIVE}/roles/x.md", f"{LIVE}/db/factory.db", f"{HOME}/.claude/settings.json",
                         "~/.claude/settings.json", "~/.bashrc", f"{HOME}/.config/git/config",
                         os.path.join(self.rdir, "link", "x"), os.path.join(self.rdir, "up", "x"),
                         os.path.join(self.rdir, "..", "..", "x"), os.path.join(self.rdir, "..", "T-1", "x"),
                         os.path.join(self.state, "factory", "research", "x.md"),
                         os.path.join(self.state, "factory", "research", "T-1", "x.md"),
                         os.path.join(self.rdir, "$X"), os.path.join(self.tmp, "x"), "/etc/passwd"):
                with self.subTest(tool=name, path=path):
                    self.deny(self.tool(name, path), path)

    def test_blocked_without_path_or_outside_cwd(self):
        self.deny(self.run_hook({"tool_name": "Write", "tool_input": {}, "cwd": self.rdir}))
        self.deny(self.run_hook({"tool_name": "Write", "tool_input": {"file_path": "x"}, "cwd": self.tmp}))
        self.deny(self.run_hook({"tool_name": "Write", "tool_input": {"file_path": "x"}, "cwd": LIVE}))

    def test_git_config_in_clone_still_blocked(self):
        self.deny(self.tool("Write", os.path.join(self.rdir, "hello", ".git", "config")))
        self.deny(self.tool("Write", os.path.join(self.rdir, "hello", ".git", "hooks", "pre-commit")))

    def test_other_role_unchanged(self):
        p = self.run_hook({"tool_name": "Write", "tool_input": {"file_path": os.path.join(self.tmp, "x")},
                           "cwd": self.tmp, "session_id": "s1"}, role="coder")
        self.ok(p)


class SecretReads(ResearcherBase):
    def test_read_grep_secrets_blocked(self):
        for path in ("~/.config/factory/worker-oauth-token", f"{HOME}/.ssh/id_ed25519", "~/.claude/.credentials.json",
                     f"{HOME}/.config/factory/gh-worker-rw-token", f"{HOME}/.config/gh/hosts.yml", "~/.netrc", "~/.git-credentials", "~/.kube/config", "/proc/self/environ"):
            with self.subTest(read=path):
                self.deny(self.tool("Read", path), path)
        for path in (f"{HOME}/.ssh", HOME, f"{HOME}/.config", f"{HOME}/.config/factory"):
            with self.subTest(grep=path):
                self.deny(self.tool("Grep", path, key="path"), path)

    def test_read_grep_ordinary_allowed(self):
        self.ok(self.tool("Read", os.path.join(self.rdir, "notes.md")))
        self.ok(self.tool("Grep", self.rdir, key="path"))
        self.ok(self.tool("Read", f"{HOME}/.claude/settings.json"))


class NoPushRole(ResearcherBase):
    def test_push_pin_style_loopholes(self):
        for cmd in ("GIT_DIR=x git status", "export GIT_CONFIG_COUNT=1",
                    "unset PATH", "git -c alias.s=push s", "git --git-dir=/x status"):
            with self.subTest(cmd=cmd):
                self.deny(self.bash(cmd), cmd)


if __name__ == "__main__":
    unittest.main()
