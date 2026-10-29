"""ci/scrub_scan.py through its real CLI, against throwaway git repos under a temp dir.

Token-shaped fixtures are assembled at runtime so this file never holds a whole token."""
import hashlib
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)

ROOT = Path(__file__).resolve().parent.parent
SCAN = ROOT / "ci" / "scrub_scan.py"

GHP = "gh" + "p_" + "A1b2C3d4E5" * 3 + "A1b2C3"
PAT = "github" + "_pat_" + "A1b2C3d4E5F6g7H8i9J0k1"
ANT = "sk" + "-ant-" + "api03-" + "A1b2C3d4E5F6g7H8i9J0"
AWS = "AK" + "IA" + "ABCDEFGHIJKLMNOP"
PEM = "-----" + "BEGIN RSA " + "PRIVATE " + "KEY-----"
ALL = {
    "home-path": "cfg=/home/alice/work\n",
    "rfc1918": "host 192.168.0.7\n",
    "email": "mail alice@corp.test\n",
    "github-token": f"t={GHP}\n",
    "github-pat": f"t={PAT}\n",
    "anthropic-key": f"t={ANT}\n",
    "aws-access-key-id": f"k={AWS}\n",
    "private-key": f"{PEM}\n",
}
CLEAN = "\n".join(["/home/user/x", "host 8.8.8.8", "version 10.0.0", "user@example.com", "noreply@anthropic.com", "noreply@github.com"]) + "\n"

GIT_ENV = {
    "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1",
    "GIT_AUTHOR_NAME": "Dev", "GIT_AUTHOR_EMAIL": "dev@example.com",
    "GIT_COMMITTER_NAME": "Dev", "GIT_COMMITTER_EMAIL": "dev@example.com",
}


def git(cwd, *args, env=None):
    e = {**os.environ, **GIT_ENV, **(env or {})}
    return subprocess.run(["git", "-C", str(cwd), *args], check=True, capture_output=True, text=True, env=e).stdout.strip()


def scan(*args, terms_env=None, env_extra=None):
    env = {k: v for k, v in os.environ.items() if k != "SCRUB_TERMS_FILE"}
    env.update(env_extra or {})
    if terms_env:
        env["SCRUB_TERMS_FILE"] = str(terms_env)
    return subprocess.run([sys.executable, str(SCAN), *map(str, args)], capture_output=True, text=True, env=env)


class Base(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmp = Path(self._tmp.name)

    def repo(self, name="r", files=None):
        d = self.tmp / name
        d.mkdir()
        git(d, "init", "-q", "-b", "main")
        for rel, text in (files or {}).items():
            self.write(d, rel, text)
        return d

    def write(self, d, rel, text):
        (d / rel).parent.mkdir(parents=True, exist_ok=True)
        (d / rel).write_text(text)

    def commit(self, d, msg="c", env=None):
        git(d, "add", "-A")
        git(d, "-c", "commit.gpgsign=false", "commit", "-q", "-m", msg, env=env)
        return git(d, "rev-parse", "HEAD")

    def terms(self, *terms):
        f = self.tmp / "terms.txt"
        f.write_text("\n".join(terms) + "\n")
        return f


class TreeTests(Base):
    def test_every_pattern_reported_once_and_masked(self):
        d = self.repo(files={f"{pid}.txt": text for pid, text in ALL.items()})
        self.commit(d)
        r = scan("--tree", d)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        for pid in ALL:
            self.assertEqual(sum(1 for ln in r.stdout.splitlines() if ln.startswith(pid + " ")), 1, (pid, r.stdout))
            self.assertIn(f"  {pid}: 1", r.stdout)
        self.assertIn("8 hits", r.stdout)
        for secret in (GHP, PAT, ANT, AWS, PEM, "/home/alice/", "192.168.0.7", "alice@corp.test"):
            self.assertNotIn(secret, r.stdout)
        self.assertIn("home-path home-path.txt:1 /hom***", r.stdout)

    def test_clean_text_passes(self):
        d = self.repo(files={"a.txt": CLEAN})
        self.commit(d)
        r = scan("--tree", d)
        self.assertEqual(r.returncode, 0, r.stdout)
        self.assertIn("0 hits", r.stdout)

    def test_b2_leading_zero_octets_are_hits(self):
        parts = (("192", "168", "001", "010"), ("10", "000", "000", "001"), ("172", "16", "000", "001"),
                 ("172", "016", "000", "001"), ("100", "064", "000", "001"))
        for ip in (".".join(p) for p in parts):  # built at runtime so this file is not a scan hit
            d = self.repo(name="r" + ip, files={"a.txt": f"host {ip}\n"})
            self.commit(d)
            r = scan("--tree", d)
            self.assertEqual(r.returncode, 1, ip + r.stdout)
            self.assertRegex(r.stdout, "rfc1918|cgnat")

    def test_b3_long_line_of_tokens_scans_fast(self):
        line = " ".join(["Ab1Cd2Ef3Gh4Ij5Kl6Mn7" + "Op8Qr9St0Uvw"] * 6000)  # ~200 KB, 33-char tokens
        self.assertGreater(len(line), 200000)
        d = self.repo(files={"a.txt": line + "\n"})
        self.commit(d)
        t0 = time.monotonic()
        r = scan("--tree", d)
        self.assertLess(time.monotonic() - t0, 2.0)
        self.assertEqual(r.returncode, 0, r.stdout[:200])

    def test_uppercase_env_file_is_a_hit(self):
        d = self.repo(files={"cfg/.ENV": "A=1\n"})
        self.commit(d)
        r = scan("--tree", d)
        self.assertEqual(r.returncode, 1, r.stdout)
        self.assertIn("env-file", r.stdout)

    def test_short_token_shapes_are_not_hits(self):
        short = "gh" + "p_" + "abc123 " + "sk" + "-ant-" + "api03-short " + "AK" + "IA" + "TOOSHORT"
        d = self.repo(files={"a.txt": short + "\n"})
        self.commit(d)
        self.assertEqual(scan("--tree", d).returncode, 0)

    def test_untracked_files_are_not_scanned(self):
        d = self.repo(files={"a.txt": CLEAN})
        self.commit(d)
        self.write(d, "untracked.txt", ALL["rfc1918"])
        self.assertEqual(scan("--tree", d).returncode, 0)

    def test_default_dir_is_cwd(self):
        d = self.repo(files={"a.txt": ALL["rfc1918"]})
        self.commit(d)
        env = {k: v for k, v in os.environ.items() if k != "SCRUB_TERMS_FILE"}
        r = subprocess.run([sys.executable, str(SCAN), "--tree"], cwd=d, capture_output=True, text=True, env=env)
        self.assertEqual(r.returncode, 1)

    def test_not_a_repo_is_exit_2(self):
        r = scan("--tree", self.tmp)
        self.assertEqual(r.returncode, 2)
        self.assertIn("git repository", r.stderr)


class OwnerTermTests(Base):
    def test_term_case_insensitive_and_masked(self):
        d = self.repo(files={"a.txt": "hello ZorBlax here\n"})
        self.commit(d)
        r = scan("--tree", d, terms_env=self.terms("zorblax"))
        self.assertEqual(r.returncode, 1)
        self.assertIn("owner-term a.txt:1", r.stdout)
        self.assertNotIn("zorblax", (r.stdout + r.stderr).lower())
        self.assertIn("1 owner term(s) loaded", r.stderr)

    def test_b1_re_ignorecase_fold_beyond_lower_is_a_hit_not_a_crash(self):
        d = self.repo(files={"a.txt": "x q\u017fynthmarkers\n".replace("markers", "o\u017f") + " y\n"})
        self.commit(d)
        r = scan("--tree", d, terms_env=self.terms("qsynthos"))
        out = r.stdout + r.stderr
        self.assertEqual(r.returncode, 1, out)
        self.assertIn("owner-term a.txt:1", r.stdout)
        self.assertNotIn("Traceback", out)
        self.assertNotIn("qsynthos", out.lower())
        self.assertNotIn("\u017f", out)

    def test_without_terms_passes_and_warns(self):
        d = self.repo(files={"a.txt": "hello ZorBlax here\n"})
        self.commit(d)
        r = scan("--tree", d)
        self.assertEqual(r.returncode, 0)
        self.assertIn("no owner terms loaded", r.stderr)

    def test_terms_file_flag(self):
        d = self.repo(files={"a.txt": "zorblax\n"})
        self.commit(d)
        r = scan("--tree", d, "--terms-file", self.terms("zorblax"))
        self.assertEqual(r.returncode, 1)
        self.assertNotIn("no owner terms loaded", r.stderr)

    def test_term_in_path_is_masked(self):
        d = self.repo(files={"zorblax-notes.txt": "x\n", "b.txt": "zorblax\n"})
        self.commit(d)
        r = scan("--tree", d, terms_env=self.terms("zorblax"))
        self.assertNotIn("zorblax", r.stdout.lower())

    def test_missing_terms_file_is_exit_2(self):
        d = self.repo(files={"a.txt": "x\n"})
        self.commit(d)
        r = scan("--tree", d, "--terms-file", self.tmp / "nope.txt")
        self.assertEqual(r.returncode, 2)


class HistoryTests(Base):
    def test_deleted_secret_is_history_only(self):
        d = self.repo(files={"s.txt": ALL["github-token"]})
        c1 = self.commit(d, "add")
        (d / "s.txt").unlink()
        self.commit(d, "remove")
        self.assertEqual(scan("--tree", d).returncode, 0)
        r = scan("--history", d)
        self.assertEqual(r.returncode, 1)
        self.assertIn(f"github-token {c1}:s.txt", r.stdout)
        self.assertNotIn(GHP, r.stdout)

    def test_author_email_reported(self):
        d = self.repo(files={"a.txt": "x\n"})
        c1 = self.commit(d, env={"GIT_AUTHOR_EMAIL": "alice@corp.test"})
        r = scan("--history", d)
        self.assertEqual(r.returncode, 1)
        self.assertIn(f"email {c1}:author", r.stdout)
        self.assertNotIn("alice@corp.test", r.stdout)

    def test_owner_term_in_author_name(self):
        d = self.repo(files={"a.txt": "x\n"})
        self.commit(d, env={"GIT_AUTHOR_NAME": "Zorblax Quux"})
        r = scan("--history", d, terms_env=self.terms("zorblax"))
        self.assertEqual(r.returncode, 1)
        self.assertNotIn("zorblax", r.stdout.lower())

    def test_exclude_ref(self):
        d = self.repo(files={"a.txt": "x\n"})
        self.commit(d, "base")
        git(d, "checkout", "-q", "-b", "side")
        self.write(d, "s.txt", ALL["rfc1918"])
        self.commit(d, "side secret")
        git(d, "checkout", "-q", "main")
        self.assertEqual(scan("--history", d).returncode, 1)
        r = scan("--history", d, "--exclude-ref", "refs/heads/side")
        self.assertEqual(r.returncode, 0, r.stdout)
        self.assertIn("0 hits", r.stdout)

    def test_exclude_ref_requires_history(self):
        d = self.repo(files={"a.txt": "x\n"})
        self.commit(d)
        self.assertEqual(scan("--tree", d, "--exclude-ref", "refs/heads/side").returncode, 2)

    def test_clean_history_passes(self):
        d = self.repo(files={"a.txt": CLEAN})
        self.commit(d)
        self.assertEqual(scan("--history", d).returncode, 0)

    def test_commit_message_scanned(self):
        d = self.repo(files={"a.txt": "x\n"})
        c1 = self.commit(d, "see 192.168.9.9 for details")
        r = scan("--history", d)
        self.assertIn(f"rfc1918 {c1}:message", r.stdout)

    def test_removed_dashes_line_is_scanned(self):
        d = self.repo(files={"a.sql": "-- host 192.168.4.4\n"})
        c1 = self.commit(d)
        self.write(d, "a.sql", "select 1;\n")
        self.commit(d, "drop")
        r = scan("--history", d)
        self.assertIn(f"rfc1918 {c1}:a.sql", r.stdout)
        self.assertEqual(r.stdout.count("rfc1918 "), 1 + 1)  # added in c1, removed in c2

    def test_extra_commit_headers_scanned(self):
        ips = ["192.168." + n for n in ("7.7", "8.8", "5.5")]  # split so this file is not a scan hit itself
        d = self.repo(files={"a.txt": "x\n"})
        c1 = self.commit(d)
        tree = git(d, "rev-parse", "HEAD^{tree}")
        body = (f"tree {tree}\nparent {c1}\nauthor A <a@example.com> 1 +0000\ncommitter A <a@example.com> 1 +0000\n"
                f"encoding {ips[0]}\ngpgsig -----BEGIN-----\n {ips[1]}\n -----END-----\n"
                f"mergetag object {ips[2]}\n\nmsg\n")
        c2 = subprocess.run(["git", "-C", str(d), "hash-object", "-t", "commit", "-w", "--stdin"], input=body, check=True,
                            capture_output=True, text=True, env={**os.environ, **GIT_ENV}).stdout.strip()
        git(d, "update-ref", "refs/heads/main", c2)
        r = scan("--history", d)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        for name in ("encoding", "gpgsig", "mergetag"):
            self.assertIn(f"{c2}:header {name}", r.stdout)
        for ip in ips:
            self.assertNotIn(ip, r.stdout)
        self.assertNotIn(f"{c1}:header", r.stdout)

    def test_extra_header_name_is_masked_in_location(self):
        ip = "192.168." + "6.6"  # split so this file is not a scan hit itself
        d = self.repo(files={"a.txt": "x\n"})
        c1 = self.commit(d)
        tree = git(d, "rev-parse", "HEAD^{tree}")
        body = (f"tree {tree}\nparent {c1}\nauthor A <a@example.com> 1 +0000\ncommitter A <a@example.com> 1 +0000\n"
                f"{ip} value\n\nmsg\n")
        c2 = subprocess.run(["git", "-C", str(d), "hash-object", "-t", "commit", "-w", "--literally", "--stdin"], input=body,
                            check=True, capture_output=True, text=True, env={**os.environ, **GIT_ENV}).stdout.strip()
        git(d, "update-ref", "refs/heads/main", c2)
        r = scan("--history", d)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
        self.assertIn(f"{c2}:header ", r.stdout)
        self.assertNotIn(ip, r.stdout + r.stderr)

    def test_not_a_repo_is_exit_2(self):
        self.assertEqual(scan("--history", self.tmp).returncode, 2)


class AllowlistTests(Base):
    def allowlist(self, *lines):
        f = self.tmp / "allow.txt"
        f.write_text("\n".join(lines) + "\n")
        return f

    def test_listed_path_suppressed_other_path_reported(self):
        d = self.repo(files={"ok.txt": ALL["rfc1918"], "other.txt": ALL["rfc1918"]})
        self.commit(d)
        r = scan("--tree", d, "--allowlist", self.allowlist(r"ok.txt:192\.168\.0\.  # reviewed fixture"))
        self.assertEqual(r.returncode, 1)
        self.assertIn("rfc1918 other.txt:1", r.stdout)
        self.assertNotIn("ok.txt", r.stdout)
        self.assertIn("1 hits (1 allowlisted)", r.stdout)

    def test_regex_must_match_the_line(self):
        d = self.repo(files={"ok.txt": ALL["rfc1918"]})
        self.commit(d)
        r = scan("--tree", d, "--allowlist", self.allowlist("ok.txt:nomatch  # reviewed"))
        self.assertEqual(r.returncode, 1)

    def test_history_ignores_allowlist(self):
        d = self.repo(files={"ok.txt": ALL["rfc1918"]})
        self.commit(d)
        r = scan("--history", d, "--allowlist", self.allowlist(r"ok.txt:192\.168  # reviewed"))
        self.assertEqual(r.returncode, 1)

    def test_path_mode_uses_allowlist(self):
        f = self.tmp / "dump.html"
        f.write_text(ALL["rfc1918"])
        allow = self.allowlist(f"{os.path.normpath(f)}:192  # reviewed")
        self.assertEqual(scan("--path", f, "--allowlist", allow).returncode, 0)

    def test_entry_without_reason_is_exit_2(self):
        d = self.repo(files={"a.txt": "x\n"})
        self.commit(d)
        r = scan("--tree", d, "--allowlist", self.allowlist("a.txt:x"))
        self.assertEqual(r.returncode, 2)
        self.assertIn("reason", r.stderr)

    def test_shipped_allowlist_parses(self):
        d = self.repo(files={"a.txt": "x\n"})
        self.commit(d)
        self.assertEqual(scan("--tree", d).returncode, 0)

    def test_legacy_entry_warns_on_stderr_and_still_suppresses(self):
        d = self.repo(files={"ok.txt": ALL["rfc1918"]})
        self.commit(d)
        r = scan("--tree", d, "--allowlist", self.allowlist(r"ok.txt:192\.168\.0\.  # reviewed"))
        self.assertEqual(r.returncode, 0, r.stdout)
        self.assertIn("deprecated", r.stderr)
        self.assertIn("1 allowlisted", r.stdout)

    def test_new_form_does_not_warn(self):
        d = self.repo(files={"ok.txt": ALL["rfc1918"]})
        self.commit(d)
        r = scan("--tree", d, "--allowlist", self.allowlist(r"ok.txt:rfc1918:192\.168\.0\.  # reviewed"))
        self.assertEqual(r.returncode, 0, r.stdout)
        self.assertNotIn("deprecated", r.stderr)

    def test_entry_suppresses_only_its_pattern_on_the_line(self):
        line = ALL["email"].strip() + " " + ALL["home-path"]
        d = self.repo(files={"ok.txt": line})
        self.commit(d)
        r = scan("--tree", d, "--allowlist", self.allowlist("ok.txt:email:alice  # reviewed"))
        self.assertEqual(r.returncode, 1, r.stdout)
        self.assertIn("home-path ok.txt:1", r.stdout)
        self.assertNotIn("email ok.txt", r.stdout)
        self.assertIn("1 hits (1 allowlisted)", r.stdout)

    def test_wrong_pattern_id_suppresses_nothing(self):
        d = self.repo(files={"ok.txt": ALL["email"]})
        self.commit(d)
        r = scan("--tree", d, "--allowlist", self.allowlist("ok.txt:rfc1918:alice  # reviewed"))
        self.assertEqual(r.returncode, 1)

    def test_token_hit_beside_allowlisted_email_is_reported(self):
        d = self.repo(files={"ok.txt": ALL["email"].strip() + f" {GHP}\n"})
        self.commit(d)
        for entry in ("ok.txt:alice  # legacy", "ok.txt:email:alice  # new"):
            r = scan("--tree", d, "--allowlist", self.allowlist(entry))
            self.assertEqual(r.returncode, 1, (entry, r.stdout))
            self.assertIn("github-token ok.txt:1", r.stdout)

    def test_token_hit_suppressed_only_by_its_exact_pattern_id(self):
        d = self.repo(files={"ok.txt": f"t={GHP}\n"})
        self.commit(d)
        r = scan("--tree", d, "--allowlist", self.allowlist("ok.txt:github-token:t=  # dummy fixture"))
        self.assertEqual(r.returncode, 0, r.stdout)
        r = scan("--tree", d, "--allowlist", self.allowlist("ok.txt:github-pat:t=  # wrong id"))
        self.assertEqual(r.returncode, 1)

    def test_owner_term_never_suppressed(self):
        d = self.repo(files={"ok.txt": ALL["email"].strip() + " ZorBlax\n"})
        self.commit(d)
        r = scan("--tree", d, "--allowlist", self.allowlist("ok.txt:alice  # legacy", "ok.txt:email:alice  # new"),
                 terms_env=self.terms("zorblax"))
        self.assertEqual(r.returncode, 1, r.stdout)
        self.assertIn("owner-term ok.txt:1", r.stdout)

    def test_owner_term_entry_is_exit_2(self):
        d = self.repo(files={"ok.txt": "x\n"})
        self.commit(d)
        r = scan("--tree", d, "--allowlist", self.allowlist("ok.txt:owner-term:x  # nope"))
        self.assertEqual(r.returncode, 2)
        self.assertIn("never be allowlisted", r.stderr)

    def test_shipped_allowlist_uses_new_form_only(self):
        r = scan("--path", SCAN)  # loads the shipped file; a legacy entry would warn
        self.assertNotIn("deprecated", r.stderr)


def sha(data):
    return hashlib.sha256(data if isinstance(data, bytes) else data.encode()).hexdigest()


FAKE_IP = "192.168." + "9.9"  # split so this file adds no scan hit of its own
FAKE_MAIL = "alice@" + "corp.test"
FAKE_MAIL2 = "bob@" + "corp.test"
LEAK = "host " + FAKE_IP


class HistoryAllowlistTests(Base):
    """--history-allowlist, --show-line-hash and the history surface (path names, odd paths, binary, symlinks, refs, tags)."""

    def alfile(self, *lines):
        f = self.tmp / "hallow.txt"
        f.write_bytes(b"\n".join(l if isinstance(l, bytes) else l.encode() for l in lines) + b"\n")
        return f

    def leak_repo(self, path="net.txt", text=LEAK + "\n"):
        d = self.repo(files={path: text})
        self.commit(d)
        return d

    def hscan(self, d, *lines, extra=(), **kw):
        return scan("--history", d, "--history-allowlist", self.alfile(*lines), *extra, **kw)

    def test_h1_entry_suppresses_exactly_that_hit(self):
        d = self.leak_repo()
        r = self.hscan(d, f"net.txt:rfc1918:{sha(LEAK)}  # reviewed")
        self.assertEqual((r.returncode, "1 allowlisted" in r.stdout), (0, True), r.stdout)
        self.write(d, "net.txt", LEAK + " \n")  # one byte changed
        self.commit(d, "edit")
        self.assertEqual(self.hscan(d, f"net.txt:rfc1918:{sha(LEAK)}  # reviewed").returncode, 1)

    def test_h1b_generic_secret_entry_lines_of_the_committed_allowlist_are_not_hits(self):
        entry = "tests/test_x.py:generic-" + "secret:" + "ab" * 32 + "  # fake sentinel"
        d = self.leak_repo(path="ci/scrub_history_allowlist.txt", text=entry + "\n")
        self.assertEqual(scan("--history", d).returncode, 0)
        d2 = self.repo(name="r2", files={"notes.txt": entry + "\n"})  # same line elsewhere stays a hit
        self.commit(d2)
        self.assertIn("generic-secret", scan("--history", d2).stdout)

    def test_h1c_allowlist_file_still_reports_a_secret_in_reason_or_loose_line(self):
        val = "Zx9" + "Qw8Er7Ty6Ui5Op4"
        bad_reason = "tests/test_x.py:generic-" + "secret:" + "ab" * 32 + "  # API_" + "KEY=" + val
        loose = "API_" + "KEY=" + val
        tail = ":generic-" + "secret:" + "ab" * 32 + "  # fine"
        for name, text in (("a", bad_reason), ("b", loose), ("c", "pass" + "word:" + val + tail),
                           ("d", "API_" + "KEY=" + val + tail), ("e", "tok" + "en=" + val + tail)):
            d = self.repo(name=name, files={"ci/scrub_history_allowlist.txt": text + "\n"})
            self.commit(d)
            r = scan("--history", d)
            self.assertEqual(r.returncode, 1, (name, r.stdout))
            self.assertNotIn(val, r.stdout)

    def test_h1d_normal_entry_line_stays_exempt(self):
        entry = "tests/test_x-1.py:generic-" + "secret:" + "ab" * 32 + "  # fake sentinel"
        d = self.repo(files={"ci/scrub_history_allowlist.txt": entry + "\n"})
        self.commit(d)
        self.assertEqual(scan("--history", d).returncode, 0)

    def test_h2_same_line_under_another_path_is_a_hit(self):
        d = self.repo(files={"net.txt": LEAK + "\n", "copy.txt": LEAK + "\n"})
        self.commit(d)
        r = self.hscan(d, f"net.txt:rfc1918:{sha(LEAK)}  # reviewed")
        self.assertEqual(r.returncode, 1)
        self.assertIn(":copy.txt ", r.stdout)
        self.assertNotIn(":net.txt ", r.stdout)

    def test_h3_email_entry_does_not_suppress_rfc1918_on_the_same_line(self):
        line = f"{FAKE_MAIL} {FAKE_IP}"
        d = self.leak_repo(text=line + "\n")
        r = self.hscan(d, f"net.txt:email:{sha(line)}  # reviewed")
        self.assertEqual(r.returncode, 1)
        self.assertIn("rfc1918 ", r.stdout)
        self.assertNotIn("email ", r.stdout)

    def test_h4_owner_term_entry_is_usage_error_and_hit_survives_other_entry(self):
        line = LEAK + " zorblax"
        d = self.leak_repo(text=line + "\n")
        terms = self.terms("zorblax")
        r = self.hscan(d, f"net.txt:owner-term:{sha(line)}  # no", terms_env=terms)
        self.assertEqual(r.returncode, 2, r.stdout)
        r = self.hscan(d, f"net.txt:rfc1918:{sha(line)}  # reviewed", terms_env=terms)
        self.assertEqual(r.returncode, 1)
        self.assertIn("owner-term ", r.stdout)
        self.assertNotIn("rfc1918 ", r.stdout)
        self.assertNotIn("zorblax", r.stdout.lower())

    def test_h5_entry_equal_to_message_author_or_path_name_suppresses_nothing(self):
        d = self.repo(files={LEAK: LEAK + "\n"})  # the file NAME is the leaking line, and so is its content
        self.commit(d, LEAK, env={"GIT_AUTHOR_NAME": LEAK})
        r = self.hscan(d, f"{LEAK}:rfc1918:{sha(LEAK)}  # reviewed")
        self.assertEqual(r.returncode, 1)
        for where in (":message", ":author", "(path name)"):
            self.assertIn(where, r.stdout)
        self.assertEqual(r.stdout.count("rfc1918 "), 3, r.stdout)  # the content hit alone is suppressed

    def test_h6_malformed_entries_are_usage_errors(self):
        d = self.leak_repo()
        good = sha(LEAK)
        for bad in (f"net.txt:rfc1918:{good}", f"net.txt:rfc1918:{good[:63]}  # r", f"net.txt:rfc1918:{good.upper()}  # r",
                    f"net.txt:nope:{good}  # r", f"net.txt:rfc1918:{good}  #"):
            r = self.hscan(d, bad)
            self.assertEqual(r.returncode, 2, bad)
        r = self.hscan(d, f"net.txt:rfc1918:{good}  # r", f"net.txt:rfc1918:{good}  # again")
        self.assertEqual(r.returncode, 2)
        self.assertIn("duplicate", r.stderr)

    def test_h7_without_the_option_output_is_unchanged(self):
        d = self.repo(files={"s.txt": ALL["github-token"]})
        c1 = self.commit(d, "add")
        r = scan("--history", d)
        self.assertEqual(r.stdout, f"github-token {c1}:s.txt ghp_***\n  github-token: 1\n1 hits\n")

    def test_h8_leaks_in_dir_and_file_names_are_hits_and_masked(self):
        d = self.repo(files={f"{FAKE_MAIL}/f.txt": "x\n", f"g-{FAKE_MAIL}.txt": "x\n",
                             "zorblax-dir/ok.txt": "x\n", "zorblax.txt": "x\n"})
        self.commit(d)
        r = scan("--history", d, terms_env=self.terms("zorblax"))
        self.assertEqual(r.returncode, 1)
        self.assertEqual(r.stdout.count("(path name)"), 4, r.stdout)
        self.assertNotIn(FAKE_MAIL, r.stdout)
        self.assertNotIn("zorblax", r.stdout.lower())

    def test_h9_odd_path_names_are_attributed_to_the_right_path(self):
        odd = {"x b/y.txt": b"x b/y.txt", "tab\tname.txt": b"tab\tname.txt", 'q"uote.txt': b'q"uote.txt',
               "back\\slash.txt": b"back\\slash.txt"}
        d = self.repo()
        for name in odd:
            self.write(d, name, LEAK + "\n")
        raw = os.fsencode(d) + b"/" + b"\xff\xfe.txt"
        Path(os.fsdecode(raw)).write_text(LEAK + "\n")
        self.write(d, "nl\nname.txt", LEAK + "\n")
        self.commit(d)
        r = scan("--history", d)
        self.assertEqual(r.returncode, 1)
        self.assertIn(":x b/y.txt ", r.stdout)
        self.assertIn(":nl\\x0aname.txt ", r.stdout)  # escaped: a name cannot forge report lines
        self.assertEqual(r.stdout.count("\n"), 6 + 1 + 1, r.stdout)  # 6 hits, one pattern line, total line
        entries = [f"{name}:rfc1918:{sha(LEAK)}  # reviewed" for name in odd]
        entries.append(b"\xff\xfe.txt" + f":rfc1918:{sha(LEAK)}  # reviewed".encode())
        r = self.hscan(d, *entries)
        self.assertEqual(r.stdout.count("rfc1918 "), 1, r.stdout)  # only the newline name cannot be listed
        self.assertIn("nl\\x0aname.txt", r.stdout)

    def test_h10_binary_blob_is_scanned(self):
        d = self.repo()
        (d / "b.bin").write_bytes(b"\0\1t=" + GHP.encode() + b"\n")
        self.commit(d)
        r = scan("--history", d)
        self.assertEqual(r.returncode, 1)
        self.assertIn("github-token", r.stdout)
        self.assertNotIn(GHP, r.stdout)

    def test_h11_symlink_target_is_scanned(self):
        d = self.repo()
        os.symlink("/ho" + "me/alice/work", d / "link")
        self.commit(d)
        r = scan("--history", d)
        self.assertEqual(r.returncode, 1)
        self.assertIn("home-path", r.stdout)
        self.assertNotIn("alice", r.stdout)

    def test_h12_key_is_over_raw_lf_split_bytes(self):
        lines = [LEAK.encode() + b" \xff\xfe", LEAK.encode() + b"\r", b"a\x0b" + LEAK.encode() + b"\x0cb"]
        d = self.repo()
        (d / "raw.txt").write_bytes(b"\n".join(lines) + b"\n")
        self.commit(d)
        r = scan("--history", d, "--show-line-hash")
        self.assertEqual(r.stdout.count("sha256="), 3, r.stdout)
        for ln in lines:
            self.assertIn("sha256=" + sha(ln), r.stdout)
        r = self.hscan(d, *[f"raw.txt:rfc1918:{sha(ln)}  # reviewed" for ln in lines])
        self.assertEqual((r.returncode, "3 allowlisted" in r.stdout), (0, True), r.stdout)
        # without the CR the CRLF line does not match
        r = self.hscan(d, *[f"raw.txt:rfc1918:{sha(ln)}  # reviewed" for ln in (lines[0], lines[1][:-1], lines[2])])
        self.assertEqual(r.returncode, 1)

    HOSTILE = "[diff]\n\tnoprefix = true\n\trenames = copies\n[log]\n\tshowRoot = false\n\tmailmap = true\n[color]\n\tui = always\n" \
              "[core]\n\tquotepath = true\n[i18n]\n\tlogOutputEncoding = latin1\n"

    def test_h13_hostile_git_config_changes_nothing(self):
        d = self.repo(files={"net.txt": LEAK + "\n", "caf\u00e9.txt": "x\n", ".mailmap": f"Bob <bob@example.com> <{FAKE_MAIL}>\n"})
        self.commit(d, "m", env={"GIT_AUTHOR_EMAIL": FAKE_MAIL})
        clean = scan("--history", d)
        glob = self.tmp / "hostile.cfg"
        glob.write_text(self.HOSTILE)
        hostile = scan("--history", d, env_extra={"GIT_CONFIG_GLOBAL": str(glob)})
        self.assertEqual(clean.returncode, 1)
        self.assertIn("email ", clean.stdout)
        self.assertEqual(hostile.stdout, clean.stdout)
        for key, val in (("diff.noprefix", "true"), ("diff.renames", "copies"), ("log.showRoot", "false"), ("log.mailmap", "true"),
                         ("color.ui", "always"), ("core.quotepath", "true"), ("i18n.logOutputEncoding", "latin1")):
            git(d, "config", key, val)  # the same settings in the scanned repo's own config
        self.assertEqual(scan("--history", d).stdout, clean.stdout)

    def test_h13_replace_refs_do_not_hide_a_commit(self):
        d = self.repo(files={"a.txt": "x\n"})
        base = self.commit(d, "base")
        self.write(d, "net.txt", LEAK + "\n")
        leaky = self.commit(d, "leak")
        git(d, "replace", leaky, base)
        r = scan("--history", d)
        self.assertEqual(r.returncode, 1, r.stdout)
        self.assertIn(":net.txt", r.stdout)

    def test_h14_gitattributes_cannot_hide_a_diff(self):
        d = self.repo(files={".gitattributes": "* -diff\n*.dat diff=x\n", "net.txt": LEAK + "\n", "f.dat": LEAK + "\n"})
        git(d, "config", "diff.x.textconv", "echo clean")
        self.commit(d)
        r = scan("--history", d)
        self.assertEqual(r.stdout.count("rfc1918 "), 2, r.stdout)

    def test_h15_removed_line_is_suppressed_by_the_same_entry(self):
        d = self.leak_repo()
        self.write(d, "net.txt", "clean\n")
        self.commit(d, "drop")
        self.assertEqual(scan("--history", d).stdout.count("rfc1918 "), 2)
        r = self.hscan(d, f"net.txt:rfc1918:{sha(LEAK)}  # reviewed")
        self.assertEqual((r.returncode, "2 allowlisted" in r.stdout), (0, True), r.stdout)

    def test_h16_unused_entry_warns_without_changing_the_exit_code(self):
        d = self.repo(files={"a.txt": "x\n"})
        self.commit(d)
        r = self.hscan(d, f"gone.txt:rfc1918:{sha(LEAK)}  # reviewed")
        self.assertEqual(r.returncode, 0)
        self.assertIn("unused history allowlist entry gone.txt:rfc1918", r.stderr)

    def test_h17_line_hash_only_with_the_flag(self):
        d = self.leak_repo()
        self.assertNotIn("sha256=", scan("--history", d).stdout)
        r = scan("--history", d, "--show-line-hash")
        self.assertIn("sha256=" + sha(LEAK), r.stdout)
        self.assertNotIn(FAKE_IP, r.stdout)
        self.assertEqual(scan("--history", d, "--history-allowlist", self.alfile(f"net.txt:rfc1918:{sha(LEAK)}  # r")).stdout.count("sha256="), 0)
        self.assertEqual(scan("--tree", d, "--show-line-hash").returncode, 2)

    def test_h18_ref_names_and_annotated_tags_are_scanned(self):
        d = self.repo(files={"a.txt": "x\n"})
        self.commit(d)
        git(d, "branch", FAKE_MAIL)
        git(d, "tag", "-a", "v1", "-m", f"see {FAKE_IP}", env={"GIT_COMMITTER_EMAIL": FAKE_MAIL2})
        r = scan("--history", d)
        self.assertEqual(r.returncode, 1)
        self.assertIn("email ref:", r.stdout)
        self.assertIn("email tag:refs/tags/v1", r.stdout)
        self.assertIn("rfc1918 tag:refs/tags/v1", r.stdout)
        for value in (FAKE_MAIL, FAKE_MAIL2, FAKE_IP):
            self.assertNotIn(value, r.stdout)

    def test_h19_control_bytes_in_messages_and_tags_cannot_forge_the_report(self):
        d = self.repo(files={"a.txt": "x\n"})
        msg = self.tmp / "msg.txt"
        msg.write_text("subject\n\n\x01H" + "0" * 40 + "\n\x01E\x01A\x01M\n" + LEAK + "\n")
        git(d, "add", "-A")
        git(d, "commit", "-q", "-F", str(msg))
        tagmsg = self.tmp / "tagmsg.txt"
        tagmsg.write_text("a\x01b\x02c\x01K\x01C " + LEAK + "\n")
        git(d, "tag", "-a", "v1", "-F", str(tagmsg))
        r = scan("--history", d)
        self.assertEqual(r.returncode, 1)
        self.assertIn(":message ", r.stdout)
        self.assertIn("tag:refs/tags/v1", r.stdout)
        self.assertNotIn(FAKE_IP, r.stdout)

    def test_h20_evil_merge_content_is_scanned(self):
        d = self.repo(files={"a.txt": "x\n"})
        self.commit(d, "base")
        git(d, "checkout", "-q", "-b", "side")
        self.write(d, "s.txt", "side\n")
        self.commit(d, "side")
        git(d, "checkout", "-q", "main")
        self.write(d, "m.txt", "main\n")
        self.commit(d, "main")
        git(d, "merge", "--no-commit", "--no-ff", "side")
        self.write(d, "net.txt", LEAK + "\n")  # introduced only by the merge resolution
        git(d, "add", "-A")
        git(d, "commit", "-q", "-m", "merge")
        r = scan("--history", d)
        self.assertEqual(r.returncode, 1, r.stdout)
        self.assertIn(":net.txt ", r.stdout)


class EmailTests(Base):
    def emails(self, *addrs):
        d = self.repo(name="e" + str(len(list(self.tmp.iterdir()))), files={"a.txt": "\n".join(f"mail {a}" for a in addrs) + "\n"})
        self.commit(d)
        return scan("--tree", d)

    def test_github_noreply_addresses_are_exempt(self):
        r = self.emails("12345+someone@users.noreply.github.com", "someone@users.noreply.github.com")
        self.assertEqual(r.returncode, 0, r.stdout)

    def test_lookalike_domains_still_hit(self):
        for addr in ("someone@github.com.evil.test", "x@noreply.github.com.attacker.test",
                     "someone@users.noreply.github.com.evil.test"):
            r = self.emails(addr)
            self.assertEqual(r.returncode, 1, (addr, r.stdout))
            self.assertIn("email a.txt:1", r.stdout)

    def test_noreply_author_scans_clean_in_history(self):
        d = self.repo(files={"a.txt": "x\n"})
        self.commit(d, env={"GIT_AUTHOR_EMAIL": "12345+someone@users.noreply.github.com",
                            "GIT_COMMITTER_EMAIL": "someone@users.noreply.github.com"})
        self.assertEqual(scan("--history", d).returncode, 0)

    def test_long_run_without_at_sign_is_fast(self):
        sys.path.insert(0, str(ROOT / "ci"))
        try:
            import scrub_scan
        finally:
            sys.path.pop(0)
        run = "A1b2._%+-" * 8889  # 80,001 characters of the local-part class, no "@"
        t = time.monotonic()
        hits = list(scrub_scan.Scanner([]).scan_line(run))
        self.assertLess(time.monotonic() - t, 0.5)
        self.assertEqual(hits, [])


class HistoryPipeTests(Base):
    def test_fake_git_flooding_stderr_does_not_deadlock(self):
        bindir = self.tmp / "bin"
        bindir.mkdir()
        fake = bindir / "git"
        fake.write_text("#!/bin/sh\nhead -c 300000 /dev/zero | tr '\\0' 'e' >&2\nexit 3\n")
        fake.chmod(0o755)
        env = {**os.environ, "PATH": f"{bindir}{os.pathsep}{os.environ['PATH']}"}
        env.pop("SCRUB_TERMS_FILE", None)
        r = subprocess.run([sys.executable, str(SCAN), "--history", str(self.tmp)], capture_output=True, text=True,
                           env=env, timeout=30)
        self.assertEqual(r.returncode, 2, r.stderr[:300])
        self.assertIn("git log failed", r.stderr)


class PathTests(Base):
    def test_html_hit_and_clean(self):
        bad, good = self.tmp / "bad.html", self.tmp / "good.html"
        bad.write_text("<p>" + ALL["email"] + "</p>")
        good.write_text("<p>" + CLEAN + "</p>")
        r = scan("--path", bad)
        self.assertEqual(r.returncode, 1)
        self.assertIn(f"email {bad}:1", r.stdout)
        self.assertEqual(scan("--path", good).returncode, 0)
        self.assertEqual(scan("--path", good, bad).returncode, 1)

    def test_missing_file_is_exit_2(self):
        self.assertEqual(scan("--path", self.tmp / "nope.sql").returncode, 2)

    def test_binary_skipped_with_note(self):
        b = self.tmp / "x.bin"
        b.write_bytes(b"\0\1\2" + ALL["rfc1918"].encode())
        r = scan("--path", b)
        self.assertEqual(r.returncode, 0)
        self.assertIn("skipped 1 binary", r.stderr)


def pids(r):
    return [ln.split()[0] for ln in r.stdout.splitlines() if ln and not ln.startswith(" ") and " hits" not in ln]


class NewPatternTests(Base):
    """T-0526: private networks, OAuth/Railway/generic secrets, .env files, owner-term index output."""

    def hits(self, text, name="a.txt", **kw):
        self.n = getattr(self, "n", 0) + 1
        d = self.repo(f"r{self.n}", files={name: text})
        self.commit(d)
        return scan("--tree", d, **kw)

    def test_private_nets_rfc1918_10_and_172(self):  # spec 1
        for ip in ("10." + "1.2.3", "172." + "16.0.1", "172." + "31.255.9", "192." + "168.1.1"):
            r = self.hits(f"host {ip}\n")
            self.assertEqual(pids(r), ["rfc1918"], (ip, r.stdout))
            self.assertNotIn(ip, r.stdout)

    def test_private_nets_negative(self):  # spec 1: version-like, out-of-range and out-of-block
        for text in ("v" + "10.0.0", "10." + "0.0.256", "172." + "32.0.1", "172." + "15.0.1", "110." + "1.2.3", "1.10." + "1.2.3"):
            self.assertEqual(self.hits(text + "\n").returncode, 0, text)

    def test_cgnat_positive_and_negative(self):  # spec 1
        for ip in ("100." + "64.0.1", "100." + "100.5.5", "100." + "127.255.255"):
            r = self.hits(f"ts {ip}\n")
            self.assertEqual(pids(r), ["cgnat"], (ip, r.stdout))
        for ip in ("100." + "63.0.1", "100." + "128.0.1", "100." + "64.0", "100." + "64.0.300"):
            self.assertEqual(self.hits(f"ts {ip}\n").returncode, 0, ip)

    def test_gho_token_is_github_token(self):  # spec 2
        r = self.hits("t=" + "gh" + "o_" + "A1b2C3d4E5" * 3 + "A1b2C3\n")
        self.assertEqual(pids(r), ["github-token"])

    def test_railway_token_assignment(self):  # spec 2
        val = "rw" + "Tok" + "A1b2C3d4E5F6g7"
        r = self.hits("RAILWAY_" + "TOKEN=" + val + "\n")
        self.assertIn("railway-token", pids(r))
        self.assertNotIn(val, r.stdout)
        self.assertIn("railway-token", pids(self.hits("export RAILWAY_API_" + "TOKEN: '" + val + "'\n")))

    def test_railway_token_negative(self):  # spec 2
        self.assertEqual(self.hits("RAILWAY_" + "TOKEN=short\n").returncode, 0)
        self.assertEqual(self.hits("RAILWAY_" + "TOKEN=<your-railway-token-here>\n").returncode, 0)
        self.assertEqual(self.hits("RAILWAY_" + "TOKEN=${RAILWAY_TOKEN_FROM_CI}\n").returncode, 0)

    def test_railway_uuid_only_with_railway_on_the_line(self):  # spec 2
        uuid = "12345678-" + "abcd-4abc-8abc-" + "0123456789ab"
        self.assertEqual(pids(self.hits("Railway id " + uuid + "\n")), ["railway-token"])
        self.assertEqual(pids(self.hits("RAILWAY id " + uuid + "\n")), ["railway-token"])
        self.assertEqual(self.hits("session id " + uuid + "\n").returncode, 0)

    def test_oauth_secret_positive_and_negative(self):  # spec 2
        val = "Ab1" + "Cd2Ef3Gh4Ij5Kl6Mn7Op"
        for key in ("client_" + "secret", "refresh_" + "token", "access_" + "token"):
            r = self.hits(f'{key} = "{val}"\n')
            self.assertIn("oauth-secret", pids(r), (key, r.stdout))
            self.assertNotIn(val, r.stdout)
        self.assertNotIn("oauth-secret", pids(self.hits("access_" + "token = abc123\n")))  # < 20 chars
        self.assertNotIn("oauth-secret", pids(self.hits("access_" + "token_url = " + val + "\n")))  # not the key

    def test_generic_secret_positive_and_negative(self):  # spec 2
        val = "Zx9" + "Qw8Er7Ty6Ui5Op4"
        for key in ("db_" + "password", "API_" + "KEY", "my-api-" + "key", "private_" + "key", "SESSION_" + "SECRET"):
            r = self.hits(f"{key}={val}\n")
            self.assertIn("generic-secret", pids(r), (key, r.stdout))
            self.assertNotIn(val, r.stdout)
        for bad in ("pass" + "word=<a-long-placeholder-value>", "to" + "ken=${SOME_LONG_VARIABLE_NAME}", "to" + "ken=$SOME_LONG_VARIABLE_NAME",
                    "api_" + "key=xxxxxxxxxxxxxxxxxxxx", "sec" + "ret=changeme-changeme-x", "to" + "ken=example-example-ex",
                    "pass" + "word=dummy-dummy-dummy", "sec" + "ret=test-value-long-enough",
                    "pass" + "word=short", "name=" + val, "to" + "kens = api.session_tokens_for_everyone"):
            self.assertEqual(self.hits(bad + "\n").returncode, 0, bad)

    def test_env_file_paths(self):  # spec 3
        for name in (".env", "prod.env", ".env.production", "sub/dir/.env", "x/a.env"):
            r = self.hits("harmless\n", name=name)
            self.assertEqual(pids(r), ["env-file"], (name, r.stdout))
            self.assertIn(f"env-file {name}:0", r.stdout)
        for name in (".env.example", ".env.sample", "sub/.env.example", "env.txt", "environment.md", "a.environ"):
            self.assertEqual(self.hits("harmless\n", name=name).returncode, 0, name)

    def test_env_file_content_never_printed(self):  # spec 3
        val = "Zx9" + "Qw8Er7Ty6Ui5Op4"
        r = self.hits("API_" + "KEY=" + val + "\n", name=".env")
        self.assertEqual(sorted(pids(r)), ["env-file", "generic-secret"])
        self.assertNotIn(val, r.stdout)

    def test_env_file_in_history(self):  # spec 3
        d = self.repo(files={".env": "x\n"})
        self.commit(d)
        (d / ".env").unlink()
        self.commit(d, "rm")
        self.assertEqual(scan("--tree", d).returncode, 0)
        r = scan("--history", d)
        self.assertEqual(pids(r), ["env-file", "env-file"], r.stdout)  # added in one commit, removed in the next

    def test_env_file_allowlist_exact_id(self):  # spec 3
        d = self.repo(files={"prod.env": "x\n"})
        self.commit(d)
        self.assertEqual(scan("--tree", d, "--allowlist", self.allowlist("prod.env:env-file:.  # reviewed empty template")).returncode, 0)

    def test_help_lists_new_ids(self):  # acceptance 2
        out = subprocess.run([sys.executable, str(SCAN), "--help"], capture_output=True, text=True, env={**os.environ, "COLUMNS": "400"}).stdout
        for pid in ("rfc1918", "cgnat", "railway-token", "oauth-secret", "generic-secret", "env-file"):
            self.assertIn(pid, out)

    def test_masked_output_never_has_full_value(self):  # spec 7
        vals = ["rw" + "Tok" + "A1b2C3d4E5F6g7", "Ab1" + "Cd2Ef3Gh4Ij5Kl6Mn7Op", "kJ8" + "dL2mQ9xR4vB7nC1zT6wY3pF5hG0sA8eU", "10." + "20.30.40"]
        text = "".join("RAILWAY_" + f"TOKEN={vals[0]}\nclient_secret={vals[1]}\nauth_token={vals[2]}\nhost {vals[3]}\n")
        r = self.hits(text)
        self.assertEqual(r.returncode, 1)
        for v in vals:
            self.assertNotIn(v, r.stdout + r.stderr)
            self.assertNotIn(v[:8], r.stdout + r.stderr)

    def allowlist(self, *lines):
        f = self.tmp / "allow.txt"
        f.write_text("\n".join(lines) + "\n")
        return f

    # owner-term index output (M8896)
    def test_owner_term_hit_prints_index_not_text(self):
        d = self.repo(files={"a.txt": "see ZorBlax and quuxel here\n"})
        self.commit(d)
        r = scan("--tree", d, terms_env=self.terms("zorblax", "Quuxel"))
        self.assertEqual(r.returncode, 1)
        self.assertIn("owner-term a.txt:1 owner-term#2", r.stdout)  # sorted: quuxel is #1, zorblax #2
        self.assertIn("owner-term a.txt:1 owner-term#1", r.stdout)
        for frag in ("zor", "quu", "ZorB", "Quux"):
            self.assertNotIn(frag.lower(), (r.stdout + r.stderr).lower())

    def test_owner_term_in_path_prints_index(self):
        d = self.repo(files={"zorblax-notes.txt": "zorblax\n", "x/quuxel.env": "q\n"})
        self.commit(d)
        r = scan("--tree", d, terms_env=self.terms("zorblax", "quuxel"))
        out = r.stdout.lower()
        self.assertNotIn("zor", out)
        self.assertNotIn("quu", out)
        self.assertIn("owner-term#2-notes.txt:1", r.stdout)
        self.assertIn("x/owner-term#1.env:0", r.stdout)  # env-file hit: path and masked basename both hide the term

    def test_owner_term_overlapping_other_hit_shows_no_prefix(self):
        d = self.repo(files={"a.txt": "mail zorblax" + "@corp.test\n"})
        self.commit(d)
        r = scan("--tree", d, terms_env=self.terms("zorblax"))
        self.assertNotIn("zor", r.stdout.lower())
        self.assertIn("owner-term#1", r.stdout)

    def test_owner_term_in_history_path_and_content(self):
        d = self.repo(files={"zorblax/a.txt": "zorblax\n"})
        self.commit(d)
        r = scan("--history", d, terms_env=self.terms("zorblax"))
        self.assertNotIn("zor", r.stdout.lower())
        self.assertIn("owner-term#1", r.stdout)

    # ADD 2 (M8939): terms <= 4 chars match whole words only; longer terms keep substring matching
    def test_short_term_inside_longer_word_is_not_a_hit(self):
        d = self.repo(files={"a.txt": "the zorbing and unzorb stuff\n"})
        self.commit(d)
        r = scan("--tree", d, terms_env=self.terms("zorb"))
        self.assertEqual(r.returncode, 0, r.stdout)

    def test_short_term_as_whole_word_is_a_hit(self):
        d = self.repo(files={"a.txt": "say (Zorb) now\n"})
        self.commit(d)
        r = scan("--tree", d, terms_env=self.terms("zorb"))
        self.assertEqual(r.returncode, 1)
        self.assertIn("owner-term a.txt:1 owner-term#1", r.stdout)

    def test_long_term_inside_longer_token_is_a_hit(self):
        d = self.repo(files={"a.txt": "path/to/xzorblaxy/file\n"})
        self.commit(d)
        r = scan("--tree", d, terms_env=self.terms("zorblax"))
        self.assertEqual(r.returncode, 1)
        self.assertIn("owner-term#1", r.stdout)


    @staticmethod
    def mod():
        sys.path.insert(0, str(ROOT / "ci"))
        try:
            import scrub_scan
        finally:
            sys.path.pop(0)
        return scrub_scan

    def test_long_word_run_scans_in_linear_time(self):
        sc = self.mod().Scanner([])
        start = time.monotonic()
        list(sc.scan_line("a" * 20000))
        self.assertLess(time.monotonic() - start, 2.0)

    def test_generic_secret_with_long_key_prefix_still_hit(self):
        hits = list(self.mod().Scanner([]).scan_line("MY_APP_SERVICE_API" + "_TOKEN = " + "q7Lm2Xz9" * 3))
        self.assertIn("generic-secret", [h[0] for h in hits])

    def test_192_168_needs_valid_octets(self):
        sc = self.mod().Scanner([])
        self.assertEqual([h[0] for h in sc.scan_line("host 192.168." + "999.1")], [])
        self.assertEqual([h[0] for h in sc.scan_line("host 192.168." + "1.20")], ["rfc1918"])

    def test_hide_all_term_equal_to_other_hit_span_does_not_crash(self):
        addr = "ab" + "@x.io"
        out = self.mod().Scanner([addr]).hide_all(addr)
        self.assertNotIn("ab@", out)  # no crash, no term text

class UsageTests(Base):
    def test_no_mode(self):
        self.assertEqual(scan().returncode, 2)

    def test_bad_argument(self):
        self.assertEqual(scan("--bogus").returncode, 2)

    def test_two_modes(self):
        self.assertEqual(scan("--tree", ".", "--history", ".").returncode, 2)

    def test_path_needs_a_file(self):
        self.assertEqual(scan("--path").returncode, 2)


class RepoTests(unittest.TestCase):
    def test_stdlib_only_and_no_third_party_scanners(self):
        src = SCAN.read_text()
        mods = {ln.split()[1].split(".")[0] for ln in src.splitlines() if ln.startswith(("import ", "from "))}
        self.assertLessEqual(mods, set(sys.stdlib_module_names))
        for word in ("gitlea" + "ks", "truffle" + "hog"):
            self.assertNotIn(word, src.lower())

    def test_scanner_source_is_clean_under_itself(self):
        r = scan("--path", SCAN)
        self.assertEqual(r.returncode, 0, r.stdout)


if __name__ == "__main__":
    unittest.main()
