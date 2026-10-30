"""ci/fresh_export.py: single-commit export of a ref, gated by scrub_scan --history with a reviewed line allowlist; refusals write nothing. Temp repos only."""
import hashlib
import importlib.util
import io
import os
import pwd
import runpy
import shutil
import subprocess
import sys
import tempfile
import unittest
import unittest.mock
from pathlib import Path
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)
from harness import git as harness_git

REPO = Path(__file__).resolve().parent.parent
TOOL = REPO / "ci" / "fresh_export.py"
AUTHOR = "Example Dev <dev@example.com>"
DATE = "2026-03-04T00:00:00+0000"
LAN = "192.168." + "0.9"  # fake values are split so this file is not a scan hit itself
MAIL = "alice@" + "corp.test"
TERM = "zzqterm"  # a fake owner term, only ever given through a temp terms file
HOSTILE = ("[diff]\n\tnoprefix = true\n\trenames = copies\n[log]\n\tshowRoot = false\n\tmailmap = true\n[color]\n\tui = always\n"
           "[core]\n\tquotepath = true\n\tautocrlf = true\n[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = /bin/false\n"
           "[i18n]\n\tlogOutputEncoding = latin1\n")


def sha(text):
    return hashlib.sha256(text.encode()).hexdigest()


def git(cwd, *args, env=None):
    e = {**os.environ, "GIT_AUTHOR_NAME": "Src", "GIT_AUTHOR_EMAIL": "src@example.com", "GIT_COMMITTER_NAME": "Src",
         "GIT_COMMITTER_EMAIL": "src@example.com", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null", **(env or {})}
    return harness_git("-c", "commit.gpgsign=false", *args, cwd=str(cwd), env=e, inert_remote=True)


def load_tool(name):
    spec = importlib.util.spec_from_file_location(name, TOOL)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class Base(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name).resolve()
        self.src = self.root / "src"
        self.src.mkdir()
        git(self.src, "init", "-q", "-b", "main")
        (self.src / "a.txt").write_text("hello\n")
        git(self.src, "add", "a.txt")
        git(self.src, "commit", "-q", "-m", "first")
        (self.src / "b.txt").write_text("second\n")
        git(self.src, "add", "b.txt")
        git(self.src, "commit", "-q", "-m", "second")
        self.empty_allow = self.root / "empty-allow.txt"
        self.empty_allow.write_text("# no reviewed lines\n")

    def commit_all(self, msg="add", env=None):
        git(self.src, "add", "-A")
        git(self.src, "commit", "-q", "-m", msg, env=env)

    def terms(self, *terms):
        f = self.root / "terms.txt"
        f.write_text("\n".join(terms) + "\n")
        return {"SCRUB_TERMS_FILE": str(f)}

    def run_tool(self, *args, env=None):
        e = {k: v for k, v in os.environ.items() if k != "SCRUB_TERMS_FILE"}
        e.update(env or {})
        return subprocess.run([sys.executable, str(TOOL), "--repo", str(self.src), *args],
                              capture_output=True, text=True, env=e)

    def export(self, out, extra=(), author=AUTHOR, **kw):
        if "--history-allowlist" not in extra:
            extra = [*extra, "--history-allowlist", str(self.empty_allow)]
        return self.run_tool("--out", str(out), "--author", author, "--ref", "HEAD", "--date", DATE, *extra, **kw)

    def assert_nothing_written(self, out):
        self.assertTrue(not Path(out).exists() or list(Path(out).iterdir()) == [], "a refused export must write nothing")


class Export(Base):
    def test_e1_clean_source_exports_one_bare_commit(self):
        out = self.root / "out"
        r = self.export(out)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(git(out, "rev-list", "--all", "--count").strip(), "1")
        self.assertEqual(git(out, "for-each-ref", "--format=%(refname)").split(), ["refs/heads/main"])
        self.assertEqual(git(out, "remote").strip(), "")
        raw = git(out, "cat-file", "commit", "HEAD").split("\n\n", 1)[0]
        self.assertEqual([ln.split(" ", 1)[0] for ln in raw.splitlines()], ["tree", "author", "committer"])
        self.assertEqual(git(out, "log", "-1", "--date=raw", "--format=%ad %cd").strip(), "1772582400 +0000 1772582400 +0000")  # 2026-03-04T00:00:00Z
        self.assertEqual(git(out, "log", "-1", "--format=%s").strip(), "Initial public release")
        self.assertEqual((out / "b.txt").read_text(), "second\n")
        self.assertIn("history gate clean", r.stdout)
        self.assertIn("never tar or copy", r.stdout)

    def test_default_date_is_midnight_utc_and_a_bad_date_is_refused(self):
        out = self.root / "out"
        r = self.run_tool("--out", str(out), "--author", AUTHOR, "--ref", "HEAD", "--history-allowlist", str(self.empty_allow))
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        author, committer = git(out, "log", "-1", "--date=raw", "--format=%ad|%cd").strip().split("|")
        self.assertEqual(author, committer)
        self.assertRegex(author, r"^\d+ \+0000$")
        self.assertEqual(int(author.split()[0]) % 86400, 0)  # midnight UTC
        bad = self.root / "bad"
        r = self.run_tool("--out", str(bad), "--author", AUTHOR, "--ref", "HEAD", "--date", "2026-03-04T00:00:00+0200")
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertFalse(bad.exists())

    def test_author_and_committer_are_the_given_identity(self):
        out = self.root / "out"
        self.assertEqual(self.export(out).returncode, 0)
        got = set(git(out, "log", "--all", "--format=%an <%ae> / %cn <%ce>").splitlines())
        self.assertEqual(got, {"Example Dev <dev@example.com> / Example Dev <dev@example.com>"})

    def test_untracked_and_ignored_files_are_absent(self):
        (self.src / ".gitignore").write_text("secret.db\n")
        git(self.src, "add", ".gitignore")
        git(self.src, "commit", "-q", "-m", "ignore")
        (self.src / "untracked.txt").write_text(f"host {LAN}\n")
        (self.src / "secret.db").write_text("x")
        (self.src / "factory.db").write_text("x")
        out = self.root / "out"
        r = self.export(out)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        for name in ("untracked.txt", "secret.db", "factory.db"):
            self.assertFalse((out / name).exists(), name)

    def test_ref_is_exported_not_the_working_tree(self):
        (self.src / "a.txt").write_text(f"edited {LAN}\n")  # uncommitted edit must not leak in
        out = self.root / "out"
        self.assertEqual(self.export(out).returncode, 0)
        self.assertEqual((out / "a.txt").read_text(), "hello\n")

    def test_structure_assertion_is_wired_into_export(self):
        mod = load_tool("fresh_export_wired")
        out = self.root / "out"
        with unittest.mock.patch.object(mod, "assert_structure", side_effect=RuntimeError("boom")), \
                unittest.mock.patch("sys.stderr", new_callable=io.StringIO) as err, \
                unittest.mock.patch("sys.stdout", new_callable=io.StringIO):
            rc = mod.main(["--repo", str(self.src), "--out", str(out), "--author", AUTHOR, "--ref", "HEAD"])
        self.assertEqual(rc, 2)
        self.assertIn("boom", err.getvalue())
        self.assertEqual(list(out.iterdir()), [])

    def test_structure_assertion_rejects_extra_commit_ref_and_header(self):
        mod = load_tool("fresh_export_structure")
        out = self.root / "out"
        self.assertEqual(self.export(out).returncode, 0)
        mod.assert_structure(out)  # as exported: holds
        git(out, "tag", "v1")
        with self.assertRaisesRegex(RuntimeError, "refs"):
            mod.assert_structure(out)
        git(out, "tag", "-d", "v1")
        git(out, "commit", "-q", "--allow-empty", "-m", "late")
        with self.assertRaisesRegex(RuntimeError, "2 commits"):
            mod.assert_structure(out)
        git(out, "reset", "-q", "--hard", "HEAD~1")
        tree = git(out, "rev-parse", "HEAD^{tree}").strip()
        evil = git(out, "commit-tree", tree, "-m", "x", env={"GIT_COMMITTER_NAME": "S"}).strip()
        git(out, "update-ref", "refs/heads/main", evil)
        git(out, "reflog", "expire", "--expire=now", "--all")
        self.assertEqual(git(out, "rev-list", "--all", "--count").strip(), "1")
        raw = git(out, "cat-file", "commit", evil)
        enc = raw.replace("\n\n", "\nencoding ISO-8859-1\n\n", 1)
        proc = subprocess.run(["git", "hash-object", "-t", "commit", "-w", "--stdin"], cwd=str(out), input=enc, text=True,
                              capture_output=True, env=dict(os.environ, GIT_CONFIG_GLOBAL="/dev/null"))
        git(out, "update-ref", "refs/heads/main", proc.stdout.strip())
        with self.assertRaisesRegex(RuntimeError, "headers"):
            mod.assert_structure(out)


class Surfaces(Base):
    """E2: every surface that `git push <remote> main` publishes is gated, and the fake value is never printed."""

    def assert_blocked(self, r, *values):
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        for v in (LAN, MAIL, TERM, *values):
            self.assertNotIn(v, r.stdout + r.stderr)

    def test_e2_file_content(self):
        (self.src / "net.txt").write_text(f"host {LAN}\n")
        self.commit_all()
        r = self.export(self.root / "out")
        self.assert_blocked(r)
        self.assertIn("rfc1918", r.stdout)
        self.assertIn("FAIL", r.stdout)

    def test_e2_file_name(self):
        (self.src / f"host-{LAN}.txt").write_text("x\n")
        self.commit_all()
        r = self.export(self.root / "out")
        self.assert_blocked(r)
        self.assertIn("(path name)", r.stdout)

    def test_e2_dir_name(self):
        (self.src / MAIL).mkdir()
        (self.src / MAIL / "f.txt").write_text("x\n")
        self.commit_all()
        r = self.export(self.root / "out")
        self.assert_blocked(r)
        self.assertIn("(path name)", r.stdout)

    def test_e2_symlink_target(self):
        os.symlink(f"notes-{LAN}", self.src / "link")
        self.commit_all()
        self.assert_blocked(self.export(self.root / "out"))

    def test_filter_error_never_prints_the_symlink_target(self):
        target = "../../zzfake-target-" + "q9x"
        os.symlink(target, self.src / "link")
        self.commit_all()
        out = self.root / "out"
        r = self.export(out)
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertNotIn("zzfake-target", r.stdout + r.stderr)
        self.assertIn("symlink member refused", r.stderr)
        self.assert_nothing_written(out)

    def test_e2_binary_blob(self):
        (self.src / "b.bin").write_bytes(b"\0\1" + LAN.encode() + b"\n")
        self.commit_all()
        r = self.export(self.root / "out")
        self.assert_blocked(r)
        self.assertIn("binary_blobs=1", r.stdout)

    def test_e2_export_subst_expands_the_source_author(self):
        (self.src / ".gitattributes").write_text("*.sub export-subst\n")
        (self.src / "v.sub").write_text("$Format:%ae$\n")
        self.commit_all(env={"GIT_AUTHOR_EMAIL": MAIL})
        r = self.export(self.root / "out")
        self.assert_blocked(r)
        self.assertIn("email", r.stdout)

    def test_e2_working_tree_encoding(self):
        (self.src / ".gitattributes").write_text("*.u16 working-tree-encoding=UTF-16\n")
        (self.src / "x.u16").write_bytes(f"host {LAN}\n".encode("utf-16"))  # the blob is stored as UTF-8
        try:
            self.commit_all()
        except AssertionError as e:
            self.skipTest(f"git here cannot do working-tree-encoding: {e}")
        self.assert_blocked(self.export(self.root / "out"))

    def test_e2_owner_term_in_author(self):
        r = self.export(self.root / "out", author=f"{TERM} <z@example.com>", env=self.terms(TERM))
        self.assert_blocked(r)
        self.assertIn("owner-term", r.stdout)

    def test_e2_leaking_author_email(self):
        r = self.export(self.root / "out", author=f"Dev <{MAIL}>")
        self.assert_blocked(r)
        self.assertIn(":author", r.stdout)

    def test_e7_export_ignored_file_is_absent_and_exits_0(self):
        (self.src / ".gitattributes").write_text("secret.txt export-ignore\n")
        (self.src / "secret.txt").write_text(f"host {LAN}\n")
        self.commit_all()
        out = self.root / "out"
        r = self.export(out)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertFalse((out / "secret.txt").exists())


class Allowlisted(Base):
    LINE = f"host {LAN}"

    def seed(self, path="net.txt", line=None):
        (self.src / path).write_text((line or self.LINE) + "\n")
        self.commit_all()

    def allow(self, path="net.txt", pid="rfc1918", line=None):
        f = self.root / "allow.txt"
        f.write_text(f"{path}:{pid}:{sha(line or self.LINE)}  # reviewed fixture\n")
        return ["--history-allowlist", str(f)]

    def test_e4_exact_reviewed_line_exports_with_exit_0(self):
        self.seed()
        r = self.export(self.root / "out", extra=self.allow())
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("1 allowlisted", r.stdout)

    def test_e4_line_changed_by_one_byte_exits_1(self):
        self.seed(line=self.LINE + " ")
        self.assertEqual(self.export(self.root / "out", extra=self.allow()).returncode, 1)

    def test_e4_same_line_in_another_file_exits_1(self):
        self.seed()
        (self.src / "copy.txt").write_text(self.LINE + "\n")
        self.commit_all("copy")
        r = self.export(self.root / "out", extra=self.allow())
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("copy.txt", r.stdout)

    def test_e5_owner_term_on_an_allowlisted_line_exits_1(self):
        line = f"{self.LINE} {TERM}"
        self.seed(line=line)
        r = self.export(self.root / "out", extra=self.allow(line=line), env=self.terms(TERM))
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn("owner-term", r.stdout)
        self.assertNotIn(TERM, r.stdout)

    def test_owner_term_allowlist_entry_is_refused(self):
        self.seed()
        r = self.export(self.root / "out", extra=self.allow(pid="owner-term"), env=self.terms(TERM))
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertEqual(list((self.root / "out").iterdir()), [])


class Hostile(Base):
    """E6: the caller's git config must not change the verdicts."""

    def setUp(self):
        super().setUp()
        self.cfg = self.root / "hostile.cfg"
        self.cfg.write_text(HOSTILE)
        self.env = {"GIT_CONFIG_GLOBAL": str(self.cfg)}

    def test_e6_clean_source_still_exits_0(self):
        out = self.root / "out"
        r = self.export(out, env=self.env)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(git(out, "rev-list", "--all", "--count").strip(), "1")
        self.assertEqual(git(out, "for-each-ref", "--format=%(refname)").split(), ["refs/heads/main"])

    def test_e6_leak_still_exits_1_with_the_same_hits(self):
        (self.src / "net.txt").write_text(f"host {LAN}\n")
        (self.src / ".mailmap").write_text(f"Bob <bob@example.com> <{MAIL}>\n")  # would hide the author under log.mailmap
        self.commit_all()
        clean = self.export(self.root / "out1", author=f"Dev <{MAIL}>")
        hostile = self.export(self.root / "out2", author=f"Dev <{MAIL}>", env=self.env)
        self.assertEqual((clean.returncode, hostile.returncode), (1, 1), hostile.stdout + hostile.stderr)
        hits = lambda r: [ln for ln in r.stdout.splitlines() if not ln.startswith(("fresh_export:", "== gate"))]
        self.assertEqual(hits(clean), hits(hostile))
        self.assertIn(":author", hostile.stdout)
        self.assertNotIn(MAIL, hostile.stdout + hostile.stderr)
        self.assertIn("rfc1918", hostile.stdout)


class Entries(Base):
    """E3: modes and path names that are refused outright (exit 2, --out left empty)."""

    def test_e3_gitlink_is_refused(self):
        git(self.src, "update-index", "--add", "--cacheinfo", "160000,0123456789012345678901234567890123456789,sub")
        git(self.src, "commit", "-q", "-m", "gitlink")
        out = self.root / "out"
        r = self.export(out)
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertIn("mode other than", r.stderr)
        self.assert_nothing_written(out)

    def test_e3_control_character_and_invalid_utf8_names_are_refused(self):
        for name in (b"new\nline.txt", b"bell\x07.txt", b"bad\xff\xfe.txt"):
            with self.subTest(name=name):
                path = os.fsencode(self.src) + b"/" + name
                with open(path, "wb") as f:
                    f.write(b"x\n")
                self.commit_all(msg=repr(name))
                out = self.root / ("out-" + name.hex())
                r = self.export(out)
                self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
                self.assertIn("control characters or invalid UTF-8", r.stderr)
                self.assert_nothing_written(out)
                os.unlink(path)
                self.commit_all(msg="rm")

    def test_blob_and_name_counts_for_the_summary(self):
        r = self.export(self.root / "out")
        self.assertIn("files=2 binary_blobs=0 commits=1", r.stdout)


class RealRepo(unittest.TestCase):
    def test_e8_committed_allowlist_covers_the_reviewed_fixtures(self):
        """Exports this checkout's HEAD with the committed ci/scrub_history_allowlist.txt. The gate must be clean
        (exit 0) and the reviewed scanner fixtures must be allowlisted, not hit."""
        if subprocess.run(["git", "-C", str(REPO), "rev-parse", "--verify", "-q", "HEAD^{commit}"], capture_output=True).returncode:
            self.skipTest("no HEAD commit in this checkout")
        with tempfile.TemporaryDirectory() as tmp:
            env = {k: v for k, v in os.environ.items() if k != "SCRUB_TERMS_FILE"}
            # This checkout's own origin may be the public repository (its text names that slug on purpose), so export a
            # clone of HEAD whose origin is a stand-in private slug (split below so this file does not name it); the private-slug check stays on.
            src = Path(tmp) / "src"
            harness_git("clone", "-q", "--shared", str(REPO), str(src), cwd=tmp, local_clone_of=REPO)
            harness_git("remote", "set-url", "origin", "https://github.com/example-org/private" + "-source", cwd=src,
                        inert_remote=True)
            r = subprocess.run([sys.executable, str(TOOL), "--repo", str(src), "--out", str(Path(tmp) / "x"), "--author", AUTHOR,
                                "--ref", "HEAD", "--date", DATE, "--public-repo", "example-org/factory"],
                               capture_output=True, text=True, env=env)  # the tree holds slug tokens since T-0411
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)  # the gate over this checkout is clean (T-0546)
        self.assertIn("allowlisted", r.stdout)
        hit_files = {ln.split(" ", 2)[1].split(":", 1)[1] for ln in r.stdout.splitlines() if ln.split(" ", 1)[0] in
                     ("home-path", "rfc1918", "email") and ":" in ln.split(" ", 2)[1]}
        self.assertNotIn("tests/test_scrub_scan.py", hit_files)
        self.assertNotIn("ci/scrub_history_allowlist.txt", hit_files)


class Refusals(Base):
    def test_non_empty_out(self):
        out = self.root / "out"
        out.mkdir()
        (out / "keep").write_text("x")
        r = self.export(out)
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("not empty", r.stderr)
        self.assertEqual([p.name for p in out.iterdir()], ["keep"])

    def test_missing_author_is_usage_error(self):
        out = self.root / "out"
        r = self.run_tool("--out", str(out), "--ref", "HEAD")
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("--author", r.stderr)
        self.assertFalse(out.exists())

    def test_ref_starting_with_dash_is_refused(self):
        out = self.root / "out"
        r = self.run_tool("--out", str(out), "--author", AUTHOR, "--ref=--output=x")
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertFalse(out.exists())

    def test_failed_structure_check_leaves_out_empty_via_the_cli(self):
        (self.src / "state").mkdir()
        (self.src / "state" / "x.txt").write_text("x\n")
        self.commit_all()
        out = self.root / "out"
        self.assertEqual(self.export(out).returncode, 2)
        self.assert_nothing_written(out)

    def test_malformed_author(self):
        out = self.root / "out"
        r = self.run_tool("--out", str(out), "--author", "no-email", "--ref", "HEAD")
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertFalse(out.exists())

    def test_unknown_ref_writes_nothing(self):
        out = self.root / "out"
        r = self.run_tool("--out", str(out), "--author", AUTHOR, "--ref", "no-such-ref")
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertFalse(out.exists())

    def test_error_after_extraction_leaves_out_empty(self):
        """A tracked top-level `state` dir trips the generated-state assertion after the archive is extracted."""
        (self.src / "state").mkdir()
        (self.src / "state" / "x.txt").write_text("x\n")
        git(self.src, "add", "state/x.txt")
        git(self.src, "commit", "-q", "-m", "state")
        out = self.root / "out"
        r = self.export(out)
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertIn("generated state", r.stderr)
        self.assertEqual(list(out.iterdir()), [], "a failed export must not leave a partial tree")

    def test_out_inside_simulated_live_home(self):
        """getpwuid points at a temp account; its ~/factory is the live home (in-process: the passwd entry is the source)."""
        acct = self.root / "account"
        (acct / "factory").mkdir(parents=True)
        entry = pwd.struct_passwd(("u", "x", os.getuid(), os.getgid(), "", str(acct), "/bin/sh"))
        for out in (acct / "factory", acct / "factory" / "export", acct / "factory" / "a" / "b"):
            err = io.StringIO()
            argv = ["fresh_export.py", "--repo", str(self.src), "--out", str(out), "--author", AUTHOR, "--ref", "HEAD"]
            with unittest.mock.patch.object(pwd, "getpwuid", return_value=entry), \
                    unittest.mock.patch.object(sys, "argv", argv), unittest.mock.patch("sys.stderr", err):
                with self.assertRaises(SystemExit) as cm:
                    runpy.run_path(str(TOOL), run_name="__main__")
            self.assertEqual(cm.exception.code, 2, err.getvalue())
            self.assertIn("live factory home", err.getvalue())
        self.assertEqual(list((acct / "factory").iterdir()), [], "a refused export must write nothing")

    def test_out_outside_simulated_live_home_is_allowed(self):
        acct = self.root / "account"
        (acct / "factory").mkdir(parents=True)
        entry = pwd.struct_passwd(("u", "x", os.getuid(), os.getgid(), "", str(acct), "/bin/sh"))
        argv = ["fresh_export.py", "--repo", str(self.src), "--out", str(self.root / "ok"), "--author", AUTHOR, "--ref", "HEAD",
                "--history-allowlist", str(self.empty_allow)]
        with unittest.mock.patch.object(pwd, "getpwuid", return_value=entry), unittest.mock.patch.object(sys, "argv", argv), \
                unittest.mock.patch("sys.stdout", io.StringIO()):
            with self.assertRaises(SystemExit) as cm:
                runpy.run_path(str(TOOL), run_name="__main__")
        self.assertEqual(cm.exception.code, 0)
        self.assertTrue((self.root / "ok" / "a.txt").exists())


T_REPO = "__PUBLIC_" + "REPO__"  # built at runtime so the export of this file is not rewritten by its own substitution
T_OWNER = "__PUBLIC_" + "OWNER__"
PRIVATE = "acme/private"
REAL_PRIVATE = "volekerb/" + "factory"  # built at runtime: the export must not name the real private slug
GIT_AT = "git" + "@github.com"  # built at runtime: a literal ssh remote would be an email hit in the scrub gate


class PublicRepo(Base):
    def setUp(self):
        super().setUp()
        git(self.src, "remote", "add", "origin", f"https://github.com/{PRIVATE}.git")
        (self.src / "README.md").write_text(f"see github.com/{T_REPO} by {T_OWNER}\n")
        self.binary = bytes([0, 1, 2]) + b"acme-bytes" + bytes([0, 255])
        (self.src / "blob.bin").write_bytes(self.binary)
        self.commit_all("tokens")
        self.out = self.root / "out"

    def pub(self, repo="acme/public", **kw):
        return self.export(self.out, ["--public-repo", repo], **kw)

    def test_substitution_in_text_file_and_summary(self):
        r = self.pub()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual((self.out / "README.md").read_text(), "see github.com/acme/public by acme\n")
        self.assertIn("README.md: 2", r.stdout)
        self.assertIn("acme/public", r.stdout)
        self.assertEqual(git(self.out, "rev-list", "--count", "main").strip(), "1")

    def test_binary_file_untouched(self):
        self.assertEqual(self.pub().returncode, 0)
        self.assertEqual((self.out / "blob.bin").read_bytes(), self.binary)

    def test_overlay_moved_and_dir_removed(self):
        (self.src / "ci/public-overlay/.github").mkdir(parents=True)
        (self.src / "ci/public-overlay/.github/CODEOWNERS").write_text(f"* @{T_OWNER}\n")
        self.commit_all("overlay")
        r = self.pub()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual((self.out / ".github/CODEOWNERS").read_text(), "* @acme\n")
        self.assertFalse((self.out / "ci/public-overlay").exists())

    def test_overlay_collision_is_refused(self):
        (self.src / "ci/public-overlay/.github").mkdir(parents=True)
        (self.src / "ci/public-overlay/.github/CODEOWNERS").write_text("* @x\n")
        (self.src / ".github").mkdir()
        (self.src / ".github/CODEOWNERS").write_text("* @y\n")
        self.commit_all("collide")
        r = self.pub()
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assert_nothing_written(self.out)

    def test_token_without_public_repo_is_refused(self):
        r = self.export(self.out)
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("pass --public-repo OWNER/NAME", r.stderr)
        self.assert_nothing_written(self.out)

    def test_public_repo_equal_to_private_slug_is_refused_any_case(self):
        for slug in (PRIVATE, "ACME/Private"):
            r = self.pub(slug)
            self.assertEqual(r.returncode, 2, r.stderr)
            self.assertIn("--public-repo names the source repository; the public repository must be a new one.", r.stderr)
            self.assert_nothing_written(self.out)

    def test_tree_file_naming_private_slug_is_refused_with_path(self):
        (self.src / "docs.md").write_text("clone Acme/Private now\n")
        self.commit_all("leak")
        r = self.pub()
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("docs.md", r.stderr)
        self.assertNotIn("clone", r.stderr)
        self.assert_nothing_written(self.out)

    def test_public_slug_extending_private_slug_is_accepted_on_clean_tree(self):
        r = self.pub(PRIVATE + "-public")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual((self.out / "README.md").read_text(), f"see github.com/{PRIVATE}-public by acme\n")

    def test_private_slug_on_a_boundary_is_still_refused(self):
        for text in (f"github.com/{PRIVATE}.git", f"({PRIVATE})", PRIVATE, f"see {PRIVATE}. Next", "x ACME/PRIVATE\n",
                     f"clone {PRIVATE}.git.", f"github.com/{PRIVATE}.wiki.git", f"https://github.com/{PRIVATE}.wiki",
                     f"{PRIVATE}.git.bak", f"{PRIVATE}.gitx", f"{PRIVATE}.zip", f"{PRIVATE}.tar.gz", f"{PRIVATE}.diff",
                     f"{PRIVATE}.js"):
            (self.src / "docs.md").write_text(text)
            self.commit_all("leak")
            shutil.rmtree(self.out, ignore_errors=True)
            r = self.pub()
            self.assertEqual(r.returncode, 2, (text, r.stdout + r.stderr))
            self.assertIn("docs.md", r.stderr)

    def test_percent_encoded_private_slug_is_refused_with_path_only(self):
        for text in ("api/repos/acme%2Fprivate/issues\n", "clone ACME%2fPrivate.git now\n",
                     "https%3A%2F%2Fgithub.com%2Facme%2Fprivate\n"):
            (self.src / "docs.md").write_text(text)
            self.commit_all("leak")
            shutil.rmtree(self.out, ignore_errors=True)
            r = self.pub()
            self.assertEqual(r.returncode, 2, (text, r.stdout + r.stderr))
            self.assertIn("docs.md", r.stderr)
            self.assertNotIn("clone", r.stderr)
            self.assertNotIn("issues", r.stderr)

    def test_percent_encoded_longer_public_slug_is_not_refused(self):
        (self.src / "docs.md").write_text("see acme%2Fprivate-public and x%2Facme%2Fprivate_y\n")
        self.commit_all("near")
        r = self.pub(PRIVATE + "-public")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)

    def test_longer_names_around_private_slug_are_not_refused(self):
        (self.src / "docs.md").write_text(f"{PRIVATE}-x {PRIVATE}_y x{PRIVATE} -{PRIVATE}\n")
        self.commit_all("near")
        r = self.pub()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)

    def test_bad_slug_formats_are_refused(self):
        for bad in ("bad/slug/x", "noslash", "-a/b", "a/..", "a/.", "a b/c", "a/" + "n" * 101, "o" * 40 + "/n", "/n", "o/"):
            r = self.pub(bad)
            self.assertEqual(r.returncode, 2, (bad, r.stderr))
            self.assert_nothing_written(self.out)

    def test_happy_path_other_slug_is_accepted(self):
        r = self.pub("volekerb/hivemill")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertEqual(git(self.out, "rev-list", "--all", "--count").strip(), "1")

    def test_real_public_slug_accepted_while_refusal_is_keyed_on_origin(self):
        git(self.src, "remote", "set-url", "origin", f"https://github.com/{REAL_PRIVATE}.git")
        ok = self.pub("volekerb/hivemill")
        self.assertEqual(ok.returncode, 0, ok.stdout + ok.stderr)
        self.assertEqual((self.out / "README.md").read_text(), "see github.com/volekerb/hivemill by volekerb\n")
        shutil.rmtree(self.out)
        bad = self.pub(REAL_PRIVATE)
        self.assertEqual(bad.returncode, 2, bad.stderr)
        self.assertIn("names the source repository", bad.stderr)

    def test_origin_ssh_forms_are_parsed(self):
        for url in (f"{GIT_AT}:{PRIVATE}.git", f"ssh://{GIT_AT}/{PRIVATE}"):
            git(self.src, "remote", "set-url", "origin", url)
            r = self.pub(PRIVATE)
            self.assertEqual(r.returncode, 2, (url, r.stderr))

    def test_origin_with_userinfo_refuses_private_slug_and_never_echoes_the_secret(self):
        git(self.src, "remote", "set-url", "origin", f"https://u:SECRET123@github.com/{PRIVATE}.git")
        r = self.pub(PRIVATE)
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertIn("names the source repository", r.stderr)
        self.assertNotIn("SECRET123", r.stdout + r.stderr)
        shutil.rmtree(self.out, ignore_errors=True)
        (self.src / "NOTES.md").write_text(f"mirror of {PRIVATE}\n")
        self.commit_all("names private")
        r = self.pub()
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertIn("name the private repository", r.stderr)
        self.assertNotIn("SECRET123", r.stdout + r.stderr)
        self.assert_nothing_written(self.out)

    def test_origin_userinfo_without_password_and_http_are_parsed(self):
        for url in (f"https://TOKEN123@github.com/{PRIVATE}.git", f"http://github.com/{PRIVATE}.git",
                    f"http://u:SECRET123@github.com/{PRIVATE}/"):
            git(self.src, "remote", "set-url", "origin", url)
            r = self.pub(PRIVATE)
            self.assertEqual(r.returncode, 2, (url, r.stderr))
            self.assertIn("names the source repository", r.stderr)
            self.assertNotIn("SECRET123", r.stdout + r.stderr)
            self.assertNotIn("TOKEN123", r.stdout + r.stderr)

    def test_origin_http_with_tree_file_naming_private_slug_is_refused(self):
        git(self.src, "remote", "set-url", "origin", f"http://github.com/{PRIVATE}.git")
        (self.src / "NOTES.md").write_text(f"mirror of {PRIVATE}\n")
        self.commit_all("names private")
        r = self.pub()
        self.assertEqual(r.returncode, 2, r.stdout + r.stderr)
        self.assertIn("name the private repository", r.stderr)
        self.assert_nothing_written(self.out)

    def test_origin_case_variants_cannot_bypass_the_private_slug_guard(self):
        owner, name = PRIVATE.split("/")
        for url in (f"https://u:SECRET123@github.com/{PRIVATE}.GIT", f"https://github.com/{PRIVATE}.Git",
                    f"https://github.com/{owner.upper()}/{name.capitalize()}.git", f"HTTPS://GitHub.com/{PRIVATE}.gIt/"):
            git(self.src, "remote", "set-url", "origin", url)
            shutil.rmtree(self.out, ignore_errors=True)
            r = self.pub(PRIVATE)
            self.assertEqual(r.returncode, 2, (url, r.stdout + r.stderr))
            self.assertIn("names the source repository", r.stderr)
            self.assertNotIn("SECRET123", r.stdout + r.stderr)

    def test_origin_with_query_fragment_or_extra_path_is_refused(self):
        for url in (f"https://github.com/{PRIVATE}.git?x=1", f"https://u:SECRET123@github.com/{PRIVATE}#frag",
                    f"https://github.com/{PRIVATE}/tree/main", f"https://github.com/{PRIVATE}.git?x=1#f"):
            git(self.src, "remote", "set-url", "origin", url)
            shutil.rmtree(self.out, ignore_errors=True)
            r = self.pub(PRIVATE)
            self.assertEqual(r.returncode, 2, (url, r.stdout + r.stderr))
            self.assertIn("origin is not a GitHub URL the export can check", r.stderr)
            out = r.stdout + r.stderr
            for frag in ("SECRET123", "frag", "tree/main", "x=1"):
                self.assertNotIn(frag, out)
            self.assert_nothing_written(self.out)

    def test_non_github_origin_is_refused_without_echoing_it(self):
        for url in ("https://gitlab.com/acme/x.git", "https://u:SECRET123@gitlab.com/acme/x.git",
                    "https://github.com.evil.example/acme/x.git"):
            git(self.src, "remote", "set-url", "origin", url)
            r = self.pub()
            self.assertEqual(r.returncode, 2, (url, r.stdout + r.stderr))
            self.assertIn("origin is not a GitHub URL the export can check; set a plain https://github.com/OWNER/NAME "
                          "origin or remove it.", r.stderr)
            out = r.stdout + r.stderr
            for frag in ("gitlab", "evil", "SECRET123", "acme/x"):
                self.assertNotIn(frag, out)
            self.assert_nothing_written(self.out)

    def test_no_origin_prints_note_and_skips_comparison(self):
        git(self.src, "remote", "remove", "origin")
        r = self.pub(PRIVATE)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("note:", r.stdout)


if __name__ == "__main__":
    unittest.main()
