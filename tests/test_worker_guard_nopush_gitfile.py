"""T-0783: NO_PUSH_ROLES may not reach git config through a `.git` gitfile or through archive extraction into a .git dir
(or an extraction whose target the hook cannot bound). Listing, extraction into a plain scratchpad dir and reads stay
allowed; coder, cleaner, judge and HQ are unchanged."""
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


class NoPushGitFileTests(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-nopushgitfile-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = os.path.join(self.tmp, "live")
        self.scratch = os.path.join(self.tmp, "scratch")
        self.repo = os.path.join(self.scratch, "r")
        self.out = os.path.join(self.scratch, "out")
        os.makedirs(self.home)
        os.makedirs(self.out)
        subprocess.run(["git", "init", "-q", self.repo], check=True, capture_output=True)

    def run_hook(self, payload, role, *flags):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        env.update(WORKER_GUARD_LIVE_HOME=self.home, WORKER_GUARD_SCRATCH=self.scratch)
        return subprocess.run([sys.executable, HOOK, "--role", role, *flags], input=json.dumps(payload),
                              capture_output=True, text=True, timeout=30, env=env)

    def bash(self, cmd, role, cwd=None, *flags):
        return self.run_hook({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": cwd or self.scratch}, role, *flags)

    def blocked(self, cmd, role, cwd=None):
        p = self.bash(cmd, role, cwd)
        self.assertEqual(p.returncode, 2, f"should block ({role}): {cmd!r}\n{p.stderr}")

    def allowed(self, cmd, role, cwd=None):
        p = self.bash(cmd, role, cwd)
        self.assertEqual(p.returncode, 0, f"should allow ({role}): {cmd!r}\n{p.stderr}")

    def test_gitfile_writes_refused(self):
        r, s = self.repo, self.scratch
        cmds = [
            f"printf 'gitdir: x\\n' > {s}/n/.git",
            f"echo 'gitdir: {s}/g' >> {r}/.git",
            f"echo 'gitdir: x' | tee {s}/n/.git",
            f"cp gf {s}/n/.git",
            f"ln -s {s}/g {s}/n/.git",
            f"touch {s}/n/.git",
            f"sed -i s/a/b/ {s}/n/.git",
        ]
        os.makedirs(os.path.join(s, "n"))
        open(os.path.join(s, "gf"), "w").close()
        open(os.path.join(s, "n", "x"), "w").close()
        cmds.append(f"cp gf {s}/out/.git")
        cmds.append(f"cp {s}/n/.git {s}/out/")  # lands as out/.git
        for role in NO_PUSH:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    self.blocked(c, role)

    def test_gitfile_edit_tools_refused(self):
        for role in NO_PUSH:
            for tool in ("Write", "Edit", "MultiEdit"):
                for p in (f"{self.scratch}/n/.git", f"{self.repo}/.git"):
                    ti = {"file_path": p, "content": "gitdir: x"} if tool == "Write" else {"file_path": p, "old_string": "a", "new_string": "b"}
                    if tool == "MultiEdit":
                        ti = {"file_path": p, "edits": [{"old_string": "a", "new_string": "b"}]}
                    with self.subTest(role=role, tool=tool, path=p):
                        res = self.run_hook({"tool_name": tool, "tool_input": ti, "cwd": self.scratch}, role)
                        self.assertEqual(res.returncode, 2, res.stderr)

    def test_git_dir_forms_refused(self):
        g = os.path.join(self.scratch, "g")
        for role in NO_PUSH:
            self.blocked(f"git --git-dir={g} status", role)
            self.blocked(f"git --git-dir {g} status", role)
            self.blocked(f"GIT_DIR={g} git status", role)
            self.blocked(f"export GIT_DIR={g}", role)

    def test_extraction_into_git_dir_refused(self):
        git = os.path.join(self.repo, ".git")
        cmds = [
            f"tar -xf a.tar -C {git}",
            f"tar xf a.tar -C {git}",
            f"tar -xf a.tar --directory={git}",
            f"tar -xf a.tar -C {git}/hooks",
            f"bsdtar -xf a.tar -C {git}",
            f"unzip a.zip -d {git}",
            f"unzip -o a.zip -d{git}",
            f"cpio -id -D {git} < a.cpio",
            f"rsync -a src/ {git}/",
            f"tar -xf a.tar -C {self.repo}",  # the repo dir already holds a .git
            f"unzip a.zip -d {self.repo}",
        ]
        for role in NO_PUSH:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    self.blocked(c, role)

    def test_unbounded_extraction_refused(self):
        for role in NO_PUSH:
            for c in ("tar -xf a.tar", "tar xzf a.tar.gz", "tar --extract -f a.tar", "bsdtar -xf a.tar", "unzip a.zip",
                      "cpio -id < a.cpio"):
                with self.subTest(role=role, cmd=c):
                    self.blocked(c, role, cwd=self.repo)
                    self.blocked(c, role)
            self.blocked("tar -xf a.tar -C $WHERE", role)

    def test_listing_and_plain_extraction_allowed(self):
        o = self.out
        cmds = [
            "tar -tf a.tar",
            "tar tvf a.tar",
            f"tar -xf a.tar -C {o}",
            f"tar xf a.tar --directory={o}",
            "unzip -l a.zip",
            "unzip -p a.zip f",
            f"unzip a.zip -d {o}",
            f"cpio -id -D {o} < a.cpio",
            "cpio -it < a.cpio",
            f"rsync -a src/ {o}/",
            f"cat {self.repo}/.git/HEAD",
            f"ls -a {self.repo}",
            f"git -C {self.repo} status",
        ]
        for role in NO_PUSH:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    self.allowed(c, role)

    def test_gitfile_read_and_other_writes_allowed(self):
        for role in NO_PUSH:
            self.allowed(f"cat {self.repo}/.git/HEAD", role)
            self.allowed(f"echo x > {self.scratch}/.gitignore", role)
            res = self.run_hook({"tool_name": "Write", "tool_input": {"file_path": f"{self.scratch}/.gitignore", "content": "x"},
                                 "cwd": self.scratch}, role)
            self.assertEqual(res.returncode, 0, res.stderr)

    def test_other_roles_unchanged(self):
        s = self.scratch
        for role in ("coder", "judge", "cleaner"):
            for c in (f"echo 'gitdir: x' > {s}/n/.git", f"tar -xf a.tar -C {self.repo}/.git", "tar -xf a.tar",
                      f"unzip a.zip -d {self.repo}/.git"):
                with self.subTest(role=role, cmd=c):
                    self.allowed(c, role)

    def test_hq_unchanged(self):
        s = self.scratch
        for role in ("boss", "director", "lead"):
            for c in (f"echo 'gitdir: x' > {s}/n/.git", f"tar -xf a.tar -C {self.repo}/.git", "tar -xf a.tar"):
                with self.subTest(role=role, cmd=c):
                    p = self.bash(c, role, None, "--hq")
                    self.assertEqual(p.returncode, 0, p.stderr)
        res = self.run_hook({"tool_name": "Write", "tool_input": {"file_path": f"{s}/n/.git", "content": "x"}, "cwd": s},
                            "qa", "--hq")
        self.assertEqual(res.returncode, 0, res.stderr)


if __name__ == "__main__":
    unittest.main()
