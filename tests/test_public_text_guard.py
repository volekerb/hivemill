"""ci/public_text_guard.py through its real CLI and its importable API, against synthetic owner terms and throwaway git repos.

Private-looking values are templates in the corpus, expanded here, so no file in the repo holds a whole hit."""
import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
GUARD = ROOT / "ci" / "public_text_guard.py"
CORPUS = ROOT / "tests" / "fixtures" / "public_text_corpus.json"

D = "."
TERM, TERMHOST = "zebra" + "corp", "boxy-host-" + "7"
SUBST = {
    "{DOT}": ".", "{HOME}": "/ho" + "me/", "{LAN}": "192." + "168.1.5", "{AT}": "@",
    "{GHP}": "gh" + "p_" + "A" * 36, "{PAT}": "github" + "_pat_" + "A1b2C3d4E5F6g7H8i9J0k1",
    "{ANT}": "sk" + "-ant-" + "api03-" + "A1b2C3d4E5F6g7H8i9J0", "{AWS}": "AK" + "IA" + "ABCDEFGHIJKLMNOP",
    "{PEM}": "-----" + "BEGIN RSA " + "PRIVATE " + "KEY-----",
    "{RWT}": "RAILWAY_" + "TOKEN=" + "A1b2C3d4E5F6g7H8i9J0", "{OAUTH}": "client_" + "secret=" + "A1b2C3d4E5F6g7H8i9J0k1",
    "{GSEC}": "api_" + "token=" + "A1b2C3d4E5F6g7H8i9J0k1",
    "{TERM}": TERM, "{TERM_UP}": TERM.upper(), "{TERMHOST}": TERMHOST,
}
GIT_ENV = {
    "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1",
    "GIT_AUTHOR_NAME": "Dev", "GIT_AUTHOR_EMAIL": "dev@example.com",
    "GIT_COMMITTER_NAME": "Dev", "GIT_COMMITTER_EMAIL": "dev@example.com",
}


def expand(text):
    for k, v in SUBST.items():
        text = text.replace(k, v)
    return text


def load_guard():
    spec = importlib.util.spec_from_file_location("public_text_guard_mod", GUARD)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def guard(*args, stdin=None, terms=None, env_terms=None):
    env = {k: v for k, v in os.environ.items() if k != "SCRUB_TERMS_FILE"}
    if env_terms:
        env["SCRUB_TERMS_FILE"] = str(env_terms)
    cmd = [sys.executable, str(GUARD), *map(str, args)]
    return subprocess.run(cmd, input=stdin, capture_output=True, text=True, env=env)


def git(cwd, *args, env=None):
    e = {**os.environ, **GIT_ENV, **(env or {})}
    return subprocess.run(["git", "-C", str(cwd), *args], check=True, capture_output=True, text=True, env=e).stdout.strip()


class Base(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.tmp = Path(self._tmp.name)
        self.terms_file = self.tmp / "terms.txt"
        self.terms_file.write_text(f"{TERM}\n{TERMHOST}\n")
        self.terms = [TERM, TERMHOST]


class CorpusTests(Base):
    def setUp(self):
        super().setUp()
        self.corpus = json.loads(CORPUS.read_text())
        self.mod = load_guard()

    def test_corpus_shape(self):
        self.assertGreaterEqual(len(self.corpus), 40)
        blocked = {c["pattern"] for c in self.corpus if c["expect"] == "block"}
        self.assertEqual(blocked, set(self.mod.PATTERN_IDS))
        self.assertGreaterEqual(sum(c["expect"] == "allow" for c in self.corpus), 10)
        for pid in self.mod.PATTERN_IDS:
            self.assertTrue(any(c["pattern"] == pid and c["expect"] == "allow" for c in self.corpus), pid)

    def test_text_cli_corpus(self):
        for c in self.corpus:
            with self.subTest(c["id"]):
                r = guard("text", "-", stdin=expand(c["text"]) + "\n", env_terms=self.terms_file)
                if c["expect"] == "block":
                    self.assertEqual(r.returncode, 1, r.stdout)
                    self.assertTrue(any(l.startswith(c["pattern"] + " line 1 ") for l in r.stdout.splitlines()), r.stdout)
                else:
                    self.assertEqual((r.returncode, r.stdout.strip()), (0, "0 hits"))

    def test_api_corpus(self):
        for c in self.corpus:
            with self.subTest(c["id"]):
                ids = {pid for pid, _ in self.mod.check_text(expand(c["text"]), self.terms)}
                if c["expect"] == "block":
                    self.assertIn(c["pattern"], ids)
                else:
                    self.assertEqual(ids, set())


class TextModeTests(Base):
    def test_allowed_ids_exit_0(self):
        r = guard("text", "-", env_terms=self.terms_file,
                  stdin="Fixes T-0515, see M8698, G-021, L-048, PR #309, v1.2.3, 127.0.0.1, 192.0.2.7, /home/user/x, a@example.com\n")
        self.assertEqual((r.returncode, r.stdout.strip()), (0, "0 hits"))

    def test_blocked_exit_1_and_masked(self):
        r = guard("text", "-", env_terms=self.terms_file,
                  stdin=f"host 10{D}1{D}2{D}3 and box{D}tail1234{D}ts{D}net and {TERM}\n")
        self.assertEqual(r.returncode, 1)
        lines = r.stdout.splitlines()
        for pid in ("rfc1918 ", "private-host ", "owner-term "):
            self.assertTrue(any(l.startswith(pid) for l in lines), r.stdout)
        self.assertEqual(lines[-1], "4 hits")  # the private IP is both rfc1918 and ipv4
        for secret in (TERM, "10" + D + "1" + D + "2" + D + "3", "tail1234", "box" + D + "tail1234" + D + "ts" + D + "net"):
            self.assertNotIn(secret, r.stdout + r.stderr)

    def test_terms_count_only(self):
        r = guard("text", "-", env_terms=self.terms_file, stdin="hi\n")
        self.assertIn("2 owner term(s) loaded", r.stderr)
        self.assertNotIn(TERM, r.stderr)

    def test_file_argument(self):
        f = self.tmp / "t.txt"
        f.write_text("ok\nip 10" + D + "9" + D + "8" + D + "7\n")
        r = guard("text", f, env_terms=self.terms_file)
        self.assertEqual(r.returncode, 1)
        self.assertTrue(r.stdout.startswith("rfc1918 line 2 "), r.stdout)

    def test_missing_file_exit_2(self):
        r = guard("text", self.tmp / "nope.txt", env_terms=self.terms_file)
        self.assertEqual(r.returncode, 2)

    def test_no_terms_fails_closed(self):
        r = guard("text", "-", stdin="hi\n")
        self.assertEqual(r.returncode, 2)
        self.assertIn("public-text-guard: no owner terms loaded", r.stderr)

    def test_empty_terms_file_fails_closed(self):
        empty = self.tmp / "empty.txt"
        empty.write_text("\n")
        r = guard("text", "-", stdin="hi\n", env_terms=empty)
        self.assertEqual(r.returncode, 2)
        self.assertIn("no owner terms loaded", r.stderr)

    def test_no_terms_flag(self):
        for args in (("--no-terms", "text", "-"), ("text", "-", "--no-terms")):
            r = guard(*args, stdin="hi\n")
            self.assertEqual((r.returncode, r.stdout.strip()), (0, "0 hits"), args)

    def test_terms_file_option(self):
        r = guard("--terms-file", self.terms_file, "text", "-", stdin=f"x {TERM}\n")
        self.assertEqual(r.returncode, 1)
        self.assertNotIn(TERM, r.stdout)

    def test_unreadable_terms_file_exit_2(self):
        r = guard("--terms-file", self.tmp / "missing.txt", "text", "-", stdin="hi\n")
        self.assertEqual(r.returncode, 2)

    def test_load_terms_or_fail_api(self):
        mod = load_guard()
        self.assertEqual(sorted(mod.load_terms_or_fail({"SCRUB_TERMS_FILE": str(self.terms_file)})), sorted(self.terms))
        self.assertEqual(sorted(mod.load_terms_or_fail({}, self.terms_file)), sorted(self.terms))
        with self.assertRaises(mod.NoTermsError):
            mod.load_terms_or_fail({}, None)

    def test_200kb_timing(self):
        mod = load_guard()
        text = ("Fixes T-0515 in the worker guard, version 1.2.3, see PR #309 and mail a@example.com\n" * 2500)[:200_000]
        self.assertGreaterEqual(len(text), 200_000)
        t = time.monotonic()
        hits = mod.check_text(text, self.terms)
        self.assertLess(time.monotonic() - t, 1.0)
        self.assertEqual(hits, [])
        t = time.monotonic()
        r = guard("text", "-", stdin=text, env_terms=self.terms_file)
        self.assertEqual(r.returncode, 0)
        self.assertLess(time.monotonic() - t, 1.0 + 0.5)  # includes interpreter start


class RobustnessTests(Base):
    def test_long_dotted_runs_scan_in_linear_time(self):
        mod = load_guard()
        for run in ("x.", "1.", "a.b.c."):
            text = run * (200_000 // len(run))
            t = time.monotonic()
            mod.check_text(text, self.terms)
            self.assertLess(time.monotonic() - t, 1.0, run)

    def test_zero_padded_address_is_a_hit(self):
        mod = load_guard()
        self.assertEqual([p for p, _ in mod.check_text("ip 010" + D + "1" + D + "1" + D + "1", self.terms)], ["ipv4"])


class RefModeTests(Base):
    def test_clean_ref(self):
        r = guard("--no-terms", "ref", "factory/t-0527", "v1.2.3")
        self.assertEqual((r.returncode, r.stdout.strip()), (0, "0 hits"))

    def test_ip_ref_blocked(self):
        r = guard("--no-terms", "ref", "factory/10" + D + "0" + D + "0" + D + "5")
        self.assertEqual(r.returncode, 1)
        self.assertTrue(r.stdout.startswith("rfc1918 ref:"), r.stdout)
        self.assertNotIn("10" + D + "0" + D + "0" + D + "5", r.stdout)

    def test_owner_term_ref_blocked_and_masked(self):
        r = guard("ref", f"factory/{TERM}-fix", env_terms=self.terms_file)
        self.assertEqual(r.returncode, 1)
        self.assertNotIn(TERM, r.stdout)


class PushModeTests(Base):
    def setUp(self):
        super().setUp()
        self.repo = self.tmp / "r"
        self.repo.mkdir()
        git(self.repo, "init", "-q", "-b", "main")
        self.write("README.md", "hello\n")
        self.base = self.commit("base")

    def write(self, rel, text):
        p = self.repo / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)

    def commit(self, msg, env=None):
        git(self.repo, "add", "-A")
        git(self.repo, "-c", "commit.gpgsign=false", "commit", "-q", "-m", msg, env=env)
        return git(self.repo, "rev-parse", "HEAD")

    def push_check(self, *extra):
        return guard("push", "--repo", self.repo, *extra, self.base, "HEAD", env_terms=self.terms_file)

    def test_push_clean_range_exit_0(self):
        self.write("a.txt", "fine\nversion 1.2.3 T-0527\n")
        self.commit("T-0527: add a (PR #5)")
        r = self.push_check()
        self.assertEqual((r.returncode, r.stdout.strip()), (0, "0 hits"), r.stdout + r.stderr)

    def test_push_message_hit(self):
        self.write("a.txt", "fine\n")
        sha = self.commit("fix thing on 10" + D + "2" + D + "3" + D + "4")
        r = self.push_check()
        self.assertEqual(r.returncode, 1)
        self.assertIn(f"rfc1918 commit:{sha[:8]}:message ", r.stdout)
        self.assertNotIn("10" + D + "2" + D + "3" + D + "4", r.stdout)

    def test_push_author_email_hit(self):
        self.write("a.txt", "fine\n")
        sha = self.commit("ok", env={"GIT_AUTHOR_EMAIL": "alice" + "@" + "corp.test", "GIT_AUTHOR_NAME": "Alice"})
        r = self.push_check()
        self.assertEqual(r.returncode, 1)
        self.assertIn(f"email commit:{sha[:8]}:author ", r.stdout)
        self.assertNotIn("alice" + "@" + "corp.test", r.stdout)

    def test_push_committer_owner_term_hit(self):
        self.write("a.txt", "fine\n")
        sha = self.commit("ok", env={"GIT_COMMITTER_NAME": TERM})
        r = self.push_check()
        self.assertEqual(r.returncode, 1)
        self.assertIn(f"owner-term commit:{sha[:8]}:committer ", r.stdout)
        self.assertNotIn(TERM, r.stdout)

    def test_push_added_line_hit(self):
        self.write("docs/a.md", "ok\nssh " + "box" + D + "tail1234" + D + "ts" + D + "net" + "\n")
        self.commit("docs")
        r = self.push_check()
        self.assertEqual(r.returncode, 1)
        self.assertIn("private-host docs/a.md:2 ", r.stdout)
        self.assertNotIn("tail1234", r.stdout)

    def test_push_removed_line_not_a_hit_but_intermediate_add_is(self):
        self.write("a.txt", "ip 10" + D + "9" + D + "9" + D + "9\n")
        self.commit("add")
        self.write("a.txt", "clean\n")
        self.commit("remove")
        r = self.push_check()
        self.assertEqual(r.returncode, 1, "a commit that is pushed publishes its content even if a later one removes it")

    def test_push_path_name_hit(self):
        self.write(f"notes-{TERM}.txt", "x\n")
        self.commit("add notes")
        r = self.push_check()
        self.assertEqual(r.returncode, 1)
        self.assertRegex(r.stdout, r"owner-term commit:[0-9a-f]{8}:path ")
        self.assertNotIn(TERM, r.stdout)

    def test_push_allowlisted_private_ip_stays_blocked_via_ipv4(self):
        self.write("fixtures/ip.txt", "lan 10" + D + "7" + D + "7" + D + "7\n")
        self.commit("fixture")
        for entry in ("fixtures/ip.txt:rfc1918:lan 10  # fake LAN address",
                      "fixtures/ip.txt:lan 10  # fake LAN address (legacy form)"):
            allow = self.tmp / "allow.txt"
            allow.write_text(entry + "\n")
            r = self.push_check("--allowlist", allow)
            self.assertEqual(r.returncode, 1, entry + "\n" + r.stdout)
            self.assertIn("ipv4 fixtures/ip.txt:1 ", r.stdout)
            self.assertNotIn("rfc1918 ", r.stdout)
            self.assertNotIn("7" + D + "7" + D + "7", r.stdout)

    def test_push_allowlisted_fixture_line_allowed(self):
        self.write("fixtures/f.txt", "token " + "sk" + "-ant-" + "api03-" + "A1b2C3d4E5F6g7H8i9J0\n")
        self.commit("fixture")
        self.assertEqual(self.push_check().returncode, 1)
        allow = self.tmp / "allow.txt"
        allow.write_text("fixtures/f.txt:anthropic-key:sk-ant-  # dummy fixture\n")
        r = self.push_check("--allowlist", allow)
        self.assertEqual((r.returncode, r.stdout.strip()), (0, "0 hits"), r.stdout + r.stderr)

    def test_push_allowlist_never_covers_owner_term_or_messages(self):
        self.write("fixtures/f.txt", f"{TERM}\n")
        self.commit("m")
        allow = self.tmp / "allow.txt"
        allow.write_text("fixtures/f.txt:email:.*  # reason\n")
        self.assertEqual(self.push_check("--allowlist", allow).returncode, 1)

    def test_push_bad_revision_exit_2(self):
        r = guard("push", "--repo", self.repo, "nope", "HEAD", env_terms=self.terms_file)
        self.assertEqual(r.returncode, 2)
        r = guard("push", "--repo", self.repo, "--output=x", "HEAD", env_terms=self.terms_file)
        self.assertEqual(r.returncode, 2)

    def merge_main_into_branch(self, evil=None):
        """main gains a file with an IPv4 after the branch forked; the branch merges main. Returns main's tip (BASE)."""
        git(self.repo, "checkout", "-q", "-b", "feature")
        self.write("feature.txt", "fine\n")
        self.commit("T-0527: feature")
        git(self.repo, "checkout", "-q", "main")
        self.write("base-only.txt", "ip 10" + D + "7" + D + "7" + D + "7\n")
        self.write("shared.txt", "one\ntwo\nthree\nfour\nfive\nsix\nseven\n")
        base = self.commit("T-0527: main moves on")
        git(self.repo, "checkout", "-q", "feature")
        git(self.repo, "-c", "commit.gpgsign=false", "merge", "-q", "--no-commit", "--no-ff", "main")
        if evil:
            self.write("shared.txt", "one\ntwo\nthree\nfour\nfive\nsix\nseven\n" + evil)
        git(self.repo, "add", "-A")
        git(self.repo, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "Merge main")
        return base

    def test_push_merge_of_base_reports_no_base_content(self):
        base = self.merge_main_into_branch()
        r = guard("push", "--repo", self.repo, base, "HEAD", env_terms=self.terms_file)
        self.assertEqual((r.returncode, r.stdout.strip()), (0, "0 hits"), r.stdout + r.stderr)
        hits = load_guard().check_push(self.repo, base, "HEAD", self.terms)
        self.assertEqual(hits, [])

    def test_push_merge_of_base_still_flags_the_branch_own_commits(self):
        git(self.repo, "checkout", "-q", "-b", "feature")
        self.write("feature.txt", "ip 10" + D + "8" + D + "8" + D + "8\n")
        self.commit("T-0527: feature")
        git(self.repo, "checkout", "-q", "main")
        self.write("other.txt", "x\n")
        base = self.commit("main moves on")
        git(self.repo, "checkout", "-q", "feature")
        git(self.repo, "-c", "commit.gpgsign=false", "merge", "-q", "--no-ff", "-m", "Merge main", "main")
        r = guard("push", "--repo", self.repo, base, "HEAD", env_terms=self.terms_file)
        self.assertEqual(r.returncode, 1)
        self.assertIn("feature.txt:1", r.stdout)
        self.assertNotIn("other.txt", r.stdout)

    def test_push_evil_merge_added_line_is_a_hit(self):
        base = self.merge_main_into_branch(evil="ip 10" + D + "9" + D + "9" + D + "9\n")
        r = guard("push", "--repo", self.repo, base, "HEAD", env_terms=self.terms_file)
        self.assertEqual(r.returncode, 1, r.stdout)
        self.assertIn("rfc1918 shared.txt:8", r.stdout)
        self.assertNotIn("base-only.txt", r.stdout)
        self.assertIn("2 hits", r.stdout)  # rfc1918 and ipv4 on the same address

    def test_push_merge_message_still_scanned(self):
        base = self.merge_main_into_branch()
        git(self.repo, "-c", "commit.gpgsign=false", "commit", "-q", "--amend", "-m", f"Merge main {TERM}")
        r = guard("push", "--repo", self.repo, base, "HEAD", env_terms=self.terms_file)
        self.assertEqual(r.returncode, 1)
        self.assertIn(":message", r.stdout)

    def test_push_api(self):
        mod = load_guard()
        self.write("a.txt", "ip 10" + D + "1" + D + "1" + D + "1\n")
        self.commit("m")
        hits = mod.check_push(self.repo, self.base, "HEAD", self.terms)
        self.assertEqual([h[0] for h in hits], ["rfc1918", "ipv4"])


if __name__ == "__main__":
    unittest.main()
