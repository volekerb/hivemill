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

    def test_name_rewriting_and_other_extractors_refused(self):
        o = self.out
        for role in NO_PUSH:
            for c in (f"tar -xPf a.tar -C {o}", f"tar -xf a.tar -C {o} --absolute-names",
                      f"tar -xf a.tar -C {o} --transform='s,^,../r/.git/,'", f"7z x a.7z -o{self.repo}/.git",
                      "7z x a.7z", "pax -rf a.tar", "jar xf a.jar"):
                with self.subTest(role=role, cmd=c):
                    self.blocked(c, role)
            self.allowed("7z l a.7z", role)
            self.allowed("jar tf a.jar", role)

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

    def test_nested_and_outside_scratchpad_targets_refused(self):
        """Fail closed unless the target is in the scratchpad and has no .git at any depth (nested repo, home, worktrees)."""
        wt = os.path.join(self.tmp, "factory", "worktrees")
        os.makedirs(wt)
        cmds = [
            f"tar -xf a.tar -C {self.scratch}",  # scratch/r/.git exists below
            f"unzip a.zip -d {self.scratch}",
            f"unzip a.zip -d {wt}",
            f"tar -xf a.tar -C {wt}",
            f"unzip a.zip -d {self.home}",
            "unzip a.zip -d ~",
            f"cpio -id -D {self.tmp} < a.cpio",
        ]
        for role in NO_PUSH:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    self.blocked(c, role)

    def test_walk_bound_and_gitfile_below_refused(self):
        deep = os.path.join(self.scratch, "deep", "a", "b")
        os.makedirs(deep)
        open(os.path.join(deep, ".git"), "w").close()  # a gitfile, not a dir
        big = os.path.join(self.scratch, "big")
        os.makedirs(big)
        for role in NO_PUSH:
            self.blocked(f"tar -xf a.tar -C {self.scratch}/deep", role)
            self.allowed(f"tar -xf a.tar -C {big}", role)

    def test_walk_bound_unit(self):
        """A tree larger than ARCHIVE_WALK_MAX is refused (unknown), a small clean tree is not."""
        import importlib.util
        spec = importlib.util.spec_from_file_location("wg", HOOK)
        wg = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(wg)
        big = os.path.join(self.scratch, "many")
        os.makedirs(big)
        for i in range(5):
            open(os.path.join(big, f"f{i}"), "w").close()
        self.assertFalse(wg.Analyzer.has_git_below(big))
        wg.ARCHIVE_WALK_MAX = 3
        self.assertTrue(wg.Analyzer.has_git_below(big))

    def test_python_and_ar_extractors_refused(self):
        git, o = os.path.join(self.repo, ".git"), self.out
        bad = [
            f"python3 -m zipfile -e a.zip {git}",
            f"python -m tarfile -e a.tar {git}",
            f"python3 -m tarfile --extract a.tar {git}",
            f"python3 -m zipfile -e a.zip {self.repo}",
            f"python3 -m zipfile -e a.zip {self.home}",
            "python3 -m zipfile -e a.zip",
            f"ar x --output={git} a.a",
            f"ar x --output {git} a.a",
            f"ar xv --output={self.repo} a.a",
            "ar x a.a",  # cwd target: scratch holds r/.git
            f"ar --extract --output={self.home} a.a",
        ]
        for role in NO_PUSH:
            for c in bad:
                with self.subTest(role=role, cmd=c):
                    self.blocked(c, role)
            self.blocked("ar x a.a", role, cwd=self.repo)
            for c in (f"python3 -m zipfile -e a.zip {o}", f"python3 -m tarfile -e a.tar {o}", f"ar x --output={o} a.a",
                      "python3 -m zipfile -l a.zip", "python3 -m tarfile -l a.tar", "ar t a.a", "ar p a.a m"):
                with self.subTest(role=role, cmd=c):
                    self.allowed(c, role)
            self.allowed("ar x a.a", role, cwd=self.out)

    def test_symlink_walk_error_and_earlier_mutation_refused(self):
        """Review B1: a symlink under the target, an unreadable dir, or an earlier ln/mv/cp/install/chmod/redirect on the
        same line fails closed; a clean target and a lone extraction stay allowed."""
        s, o = self.scratch, self.out
        link = os.path.join(s, "linked")
        os.makedirs(link)
        os.symlink(self.repo, os.path.join(link, "l"))
        locked = os.path.join(s, "locked")
        os.makedirs(os.path.join(locked, "sub"))
        os.chmod(locked, 0)
        self.addCleanup(os.chmod, locked, 0o755)
        chained = [
            f"ln -s {self.home} {o}/l && tar -xf a.tar -C {o}",
            f"mv {s}/x {o}/l; tar -xf a.tar -C {o}",
            f"cp -r {s}/x {o}/l && unzip a.zip -d {o}",
            f"install -d {o}/l && tar -xf a.tar -C {o}",
            f"chmod 755 {o} && tar -xf a.tar -C {o}",
            f"echo x > {o}/f && tar -xf a.tar -C {o}",
            f"tee {o}/f < a && tar -xf a.tar -C {o}",
            f"tar -xf a.tar -C {o}; tar -xf b.tar -C {o}",
        ]
        for role in NO_PUSH:
            self.blocked(f"tar -xf a.tar -C {link}", role)
            if os.geteuid() != 0:
                self.blocked(f"tar -xf a.tar -C {locked}", role)
            for c in chained:
                with self.subTest(role=role, cmd=c):
                    self.blocked(c, role)
            self.allowed(f"tar -xf a.tar -C {o}", role)
            self.allowed(f"tar -tf a.tar && tar -xf a.tar -C {o}", role)

    def test_symlink_walk_error_unit(self):
        import importlib.util
        spec = importlib.util.spec_from_file_location("wg", HOOK)
        wg = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(wg)
        top = os.path.join(self.scratch, "u")
        os.makedirs(os.path.join(top, "d"))
        self.assertFalse(wg.Analyzer.has_git_below(top))
        os.symlink("/", os.path.join(top, "d", "l"))
        self.assertTrue(wg.Analyzer.has_git_below(top))

    def test_python_flag_clusters_and_ar_options_refused(self):
        """Review B2/B3: interpreter flags before -m and ar options that take a value."""
        git, o = os.path.join(self.repo, ".git"), self.out
        bad = [
            f"python3 -Im zipfile -e a.zip {git}", f"python3 -Bm tarfile -e a.tar {git}",
            f"python3 -I -m zipfile -e a.zip {git}", f"python3 -IBm zipfile -e a.zip {git}",
            f"python3 -B -I -m tarfile --extract a.tar {git}", f"python3 -Imzipfile -e a.zip {git}",
            f"python3 -W ignore -m zipfile -e a.zip {git}", "python3 -Im zipfile -e a.zip",
            f"ar --output={git} --plugin x.so x a.a", f"ar --output {git} --plugin x.so x a.a",
            f"ar --plugin x.so --output={git} x a.a", f"ar --target elf64 --output={git} x a.a",
            f"ar -D --output={git} xv a.a", f"ar --plugin x.so x a.a",
        ]
        for role in NO_PUSH:
            for c in bad:
                with self.subTest(role=role, cmd=c):
                    self.blocked(c, role)
            for c in (f"python3 -Im zipfile -e a.zip {o}", f"python3 -I -m tarfile -e a.tar {o}", "python3 -Im zipfile -l a.zip",
                      f"ar --plugin x.so --output={o} x a.a", "ar --plugin x.so t a.a", "ar --target elf64 p a.a m"):
                with self.subTest(role=role, cmd=c):
                    self.allowed(c, role)
        self.allowed(f"python3 -Im zipfile -e a.zip {git}", "coder")
        self.allowed(f"ar --output={git} --plugin x.so x a.a", "coder")

    def test_other_roles_unchanged(self):
        s = self.scratch
        for role in ("coder", "judge", "cleaner"):
            for c in (f"echo 'gitdir: x' > {s}/n/.git", f"tar -xf a.tar -C {self.repo}/.git", "tar -xf a.tar",
                      f"unzip a.zip -d {self.repo}/.git", f"unzip a.zip -d {self.home}", f"python3 -m zipfile -e a.zip {self.home}",
                      f"ar x --output={self.home} a.a"):
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
