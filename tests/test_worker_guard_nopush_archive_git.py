"""T-0805: a NO_PUSH_ROLES extraction (tar, bsdtar, unzip) is refused when the archive's member list holds a .git path
component or a directory with both config and HEAD (a bare-repo layout), and when the list cannot be read. Archives
without those, listing, and git in the role's own worktree stay allowed; coder, cleaner, judge and HQ are unchanged."""
import io
import json
import os
import shutil
import tarfile
import tempfile
import unittest
import zipfile
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)
from guard_impl import hook_run

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

NO_PUSH = ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator")
BAD = {
    "dotgit": ["x/.git/config", "x/f"],
    "dotgit_file": [".git", "f"],
    "bare": ["config", "HEAD", "hooks/post-checkout"],
    "bare_nested": ["sub/r/config", "sub/r/HEAD"],
    "dotslash_dotgit": ["./.git/config"],
}
OK = {"plain": ["a.txt", "dir/b.txt"], "config_only": ["config", "x"], "gitignore": [".gitignore", ".github/w.yml"]}


class ArchiveGitTests(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-test-nopusharchive-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = os.path.join(self.tmp, "live")
        self.scratch = os.path.join(self.tmp, "scratch")
        self.out = os.path.join(self.scratch, "out")
        os.makedirs(self.home)
        os.makedirs(self.out)

    def mk(self, kind, members, links=()):
        path = os.path.join(self.scratch, f"{kind}.{'zip' if kind.startswith('z') else 'tar'}")
        if kind.startswith("z"):
            with zipfile.ZipFile(path, "w") as z:
                for m in members:
                    z.writestr(m, "x")
        else:
            with tarfile.open(path, "w") as t:
                for m in members:
                    info = tarfile.TarInfo(m)
                    info.size = 1
                    t.addfile(info, io.BytesIO(b"x"))
                for name, target in links:
                    info = tarfile.TarInfo(name)
                    info.type = tarfile.SYMTYPE
                    info.linkname = target
                    t.addfile(info)
        return path

    def bash(self, cmd, role, cwd=None, *flags):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        env.update(WORKER_GUARD_LIVE_HOME=self.home, WORKER_GUARD_SCRATCH=self.scratch)
        payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": cwd or self.scratch}
        return hook_run(json.dumps(payload), ["--role", role, *flags], env=env)

    def blocked(self, cmd, role, cwd=None):
        p = self.bash(cmd, role, cwd)
        self.assertEqual(p.returncode, 2, f"should block ({role}): {cmd!r}\n{p.stderr}")

    def allowed(self, cmd, role, cwd=None, *flags):
        p = self.bash(cmd, role, cwd, *flags)
        self.assertEqual(p.returncode, 0, f"should allow ({role}): {cmd!r}\n{p.stderr}")

    def extract_cmds(self, tar, zp):
        o = self.out
        return [f"tar -xf {tar} -C {o}", f"tar xzf {tar} -C {o}", f"tar --extract --file={tar} --directory={o}",
                f"bsdtar -xf {tar} -C {o}", f"unzip {zp} -d {o}", f"unzip -o -q {zp} -d {o}"]

    def test_bad_archives_refused(self):
        for kind, members in BAD.items():
            tar, zp = self.mk(kind, members), self.mk("z" + kind, members)
            for role in NO_PUSH:
                for c in self.extract_cmds(tar, zp):
                    with self.subTest(role=role, kind=kind, cmd=c):
                        self.blocked(c, role)

    def test_link_into_dotgit_refused(self):
        tar = self.mk("link", ["a"], links=[("l", ".git"), ("m", "x/.git/y")])
        for role in NO_PUSH:
            self.blocked(f"tar -xf {tar} -C {self.out}", role)

    def test_after_cd_and_in_shell_wrapper_refused(self):
        tar, zp = self.mk("dotgit", BAD["dotgit"]), self.mk("zbare", BAD["bare"])
        cmds = [f"cd {self.scratch} && tar -xf dotgit.tar -C out", f"cd {self.scratch} && unzip zbare.zip -d out",
                f"zsh -c 'tar -xf {tar} -C {self.out}'", f"bash -c 'unzip {zp} -d {self.out}'",
                f"cd {self.scratch} && bash -c 'tar -xf dotgit.tar -C out'", f"zsh -c 'cd {self.scratch} && tar -xf dotgit.tar -C out'"]
        for role in NO_PUSH:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    self.blocked(c, role)

    def test_unlistable_archive_fails_closed(self):
        junk = os.path.join(self.scratch, "junk.tar")
        with open(junk, "wb") as f:
            f.write(b"not an archive at all" * 100)
        o = self.out
        cmds = [f"tar -xf {junk} -C {o}", f"unzip {junk} -d {o}", f"tar -xf - -C {o} < {junk}",
                f"cat {junk} | tar -xf - -C {o}", f"tar -xf $A -C {o}", f"tar -xf {junk}* -C {o}",
                f"echo hi > {self.scratch}/gone.tar; tar -xf {self.scratch}/gone.tar -C {o}",
                f"tar -cf {self.scratch}/new.tar x; tar -xf {self.scratch}/new.tar -C {o}"]
        for role in NO_PUSH:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    self.blocked(c, role)

    def test_missing_archive_alone_is_harmless(self):
        for role in NO_PUSH:
            self.allowed(f"tar -xf {self.scratch}/gone.tar -C {self.out}", role)

    def test_clean_archives_listing_and_worktree_git_allowed(self):
        for kind, members in OK.items():
            tar, zp = self.mk(kind, members), self.mk("z" + kind, members)
            for role in NO_PUSH:
                for c in self.extract_cmds(tar, zp):
                    with self.subTest(role=role, kind=kind, cmd=c):
                        self.allowed(c, role)
        tar, zp = self.mk("dotgit", BAD["dotgit"]), self.mk("zdotgit", BAD["dotgit"])
        wt = os.path.join(self.tmp, "wt")
        os.makedirs(wt)
        for role in NO_PUSH:
            for c in (f"tar -tf {tar}", f"tar tvf {tar}", f"unzip -l {zp}", f"cd {self.scratch} && tar -tf dotgit.tar",
                      "git status", "git log --oneline -5", f"git -C {wt} status"):
                with self.subTest(role=role, cmd=c):
                    self.allowed(c, role, cwd=wt)

    def test_coder_cleaner_judge_unchanged(self):
        tar = self.mk("dotgit", BAD["dotgit"])
        for role in ("coder", "cleaner", "judge"):
            with self.subTest(role=role):
                p = self.bash(f"tar -xf {tar} -C {self.out}", role)
                self.assertEqual(p.returncode, 0, p.stderr)

    def test_hq_unchanged(self):
        tar = self.mk("dotgit", BAD["dotgit"])
        for role in ("boss", "director", "lead"):
            with self.subTest(role=role):
                self.allowed(f"tar -xf {tar} -C {self.out}", role, None, "--hq")
        self.blocked(f"tar -xf {tar} -C {self.out}", "qa")

    def test_unzip_zip_suffix_candidates_listed(self):
        # unzip opens <name>, <name>.zip, <name>.ZIP: only evil.ZIP / evil.zip exists, the command names `evil`
        for suffix in (".ZIP", ".zip"):
            d = os.path.join(self.scratch, "u" + suffix[1:])
            os.makedirs(d)
            with zipfile.ZipFile(os.path.join(d, "evil" + suffix), "w") as z:
                z.writestr(".git/config", "x")
            for role in NO_PUSH:
                with self.subTest(role=role, suffix=suffix):
                    self.blocked(f"unzip {d}/evil -d {self.out}", role)

    def test_concatenated_tar_ignore_zeros_refused(self):
        clean, evil = self.mk("clean", ["a"]), self.mk("evil", [".git/config"])
        cat = os.path.join(self.scratch, "cat.tar")
        with open(cat, "wb") as f:
            for src in (clean, evil):
                with open(src, "rb") as g:
                    f.write(g.read())
        o = self.out
        cmds = [f"tar -xif {cat} -C {o}", f"tar -x -i -f {cat} -C {o}", f"tar xif {cat} -C {o}",
                f"tar --ignore-zeros -xf {cat} -C {o}", f"tar -xf {cat} --ignore-zeros -C {o}",
                f"tar -xf {cat} --ignore-z -C {o}", f"tar -xf {cat} --ig -C {o}", f"bsdtar -xif {cat} -C {o}"]
        for role in NO_PUSH:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    self.blocked(c, role)

    def test_name_rewrite_options_refused(self):
        tar = self.mk("plain", ["a/b"])
        o = self.out
        cmds = [f"tar -xf {tar} -C {o} --xfo=s,a,.git,", f"tar -xf {tar} -C {o} --xform=s,a,.git,",
                f"tar -xf {tar} -C {o} --transf=s,a,.git,", f"tar -xf {tar} -C {o} --tr=s,a,.git,",
                f"tar -xf {tar} -C {o} --strip-components=1", f"tar -xf {tar} -C {o} --strip=1",
                f"bsdtar -xf {tar} -C {o} -s /a/.git/", f"tar -xsf {tar} -C {o}", f"bsdtar -xf {tar} -C {o} -s/a/.git/",
                f"tar -xf {tar} -C {o} --one-top=x", f"tar -xf {tar} -C {o} --to-comm=cat", f"tar -xf {tar} -C {o} --absolute"]
        for role in NO_PUSH:
            for c in cmds:
                with self.subTest(role=role, cmd=c):
                    self.blocked(c, role)
        for role in NO_PUSH:  # listing with the same options and plain extraction stay allowed
            self.allowed(f"tar -tf {tar} --xform=s,a,b,", role)
            self.allowed(f"tar -xf {tar} -C {o} --no-same-owner", role)
    def test_frozen_probe_table(self):
        """Rule E probe table: every block row exits 2 and every allow row exits 0, for each no-push role."""
        s, o = self.scratch, self.out
        evil = self.mk("evil", [".git/config"])
        clean = self.mk("clean", ["a/b"])
        cat = os.path.join(s, "cat.tar")
        with open(cat, "wb") as f:
            for src in (clean, evil):
                with open(src, "rb") as g:
                    f.write(g.read())
        for suffix in (".ZIP", ".zip"):
            with zipfile.ZipFile(os.path.join(s, "zevil" + suffix), "w") as z:
                z.writestr(".git/config", "x")
        gitcfg = self.mk("a", ["x/.git/config"])
        zclean = self.mk("zclean", ["a/b"])
        block = [
            f"unzip {s}/zevil -d {o}", f"unzip {s}/zevil.ZIP -d {o}", f"unzip {s}/zevil.zip -d {o}",
            f"tar -xif {cat} -C {o}", f"tar --ignore-zeros -xf {cat} -C {o}",
            f"tar -xf {clean} --xfo='s/^x/.git/' -C {o}", f"tar --transform='s/x/.git/' -xf {clean} -C {o}",
            f"bsdtar -xf {clean} -s ',x,.git,' -C {o}",
            f"tar -xf host:/tmp/a.tar -C {o}", f"tar -xf - -C {o}", f"tar -xf {s}/*.tar -C {o}", f"tar -xf $A -C {o}",
            f"tar -xf {gitcfg} -C {o}", f"tar -C {o} -xf {gitcfg}", f"tar -x -f {gitcfg} -C {o}",
            f"bsdtar -xf {gitcfg} -C {o}", f"tar -xf {clean} -C {o} --strip-components=1",
            f"tar -xf {clean} -C {o} -P", f"tar -xf {clean} -C {o} -T list", f"tar --get -f {clean} -C {o}",
            f"tar -xf {clean} -C {o} --use-compress-program=sh", f"tar -xf {clean} -C {o} -I sh",
            f"unzip {zclean} -d {o} -x foo", f"unzip -o {zclean} -d{o}/.git", f"tar -xf {clean} -C{o}/.git", f"tar -xf {gitcfg} -C{o}", f"unzip -P pw {zclean} -d {o}",
        ]
        allow = [f"tar -xf {clean} -C {o}", f"tar -xzf {clean} -C {o}", f"unzip {zclean} -d {o}",
                 f"tar -tf {evil}", f"unzip -l {s}/zevil.ZIP", f"unzip -l {zclean}"]
        for role in NO_PUSH:
            for c in block:
                with self.subTest(role=role, cmd=c):
                    self.blocked(c, role)
            for c in allow:
                with self.subTest(role=role, cmd=c):
                    self.allowed(c, role)


if __name__ == "__main__":
    unittest.main()
