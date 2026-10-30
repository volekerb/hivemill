"""Tests for ci/release_check.py: every PASS and FAIL of checks 1-7, the printed owner commands, exit codes, no mutation.

Most tests inject a fake runner (the process boundary: git, gh, python children); the real-git tests run on a temp repo."""
import io
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)
import harness

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "ci"))
import release_check as rc  # noqa: E402

SHA = "a" * 40
GIT_ENV = {"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t",
           "GIT_COMMITTER_EMAIL": "t@example.com", "PATH": os.environ["PATH"], "HOME": "/nonexistent"}
CHECKS = ("on-main", "version", "ci-green", "notes", "fresh-install-smoke", "public-text", "tag-free")
GREEN_RUN = json.dumps([{"status": "completed", "conclusion": "success", "databaseId": 777}])


class Fake:
    """Fake runner: green by default; `over` maps a key (see key()) to (rc, out, err)."""

    def __init__(self, version="0.3.1", over=None):
        self.version, self.over, self.calls, self.envs = version, over or {}, [], []

    @staticmethod
    def key(argv):
        a = argv
        if a[0] == "gh":
            return "gh"
        if a[0] == "git":
            rest = a[3:] if a[1] == "-C" else a[1:]
            return "git-" + rest[0]
        if a[0] == sys.executable:
            s = a[1]
            if s.endswith("bin/factory"):
                return "factory-" + ("version" if a[2] == "--version" else a[2])
            if "--check-version" in a:
                return "check-version"
            if "--release-notes" in a:
                return "release-notes"
            return os.path.basename(s)
        return a[0]

    def __call__(self, argv, cwd=None, env=None, timeout=300):
        self.calls.append(list(argv))
        self.envs.append(env)
        k = self.key(argv)
        if k in self.over:
            return self.over[k]
        return {"git-rev-parse": (0, SHA + "\n", ""), "git-show": (0, self.version + "\n", ""),
                "git-remote": (0, "https://github.com/acme/widget.git\n", ""), "git-rev-list": (0, f"{SHA}\n{'b' * 40}\n", ""),
                "git-ls-remote": (0, "", ""), "gh": (0, GREEN_RUN, ""),
                "factory-version": (0, f"hivemill {self.version} (git abc)\n", ""), "release-notes": (0, "# Notes\nAll good\n", ""),
                }.get(k, (0, "", ""))

    def probe(self, argv, env, port):
        self.calls.append(list(argv))
        self.envs.append(env)
        return self.over.get("probe", (True, "HTTP 200 on /"))


def go(fake, *args):
    lines = []
    code = rc.main([SHA, *args], run=fake, probe=fake.probe, out=lines.append, env={"PATH": "/usr/bin", "FACTORY_AGENT": "x"})
    if code != 0:
        assert not [ln for ln in lines if "tag -a" in ln], lines  # no owner commands unless every check passed
    return code, lines


def status(lines, name):
    got = [ln for ln in lines if ln.split(" ", 2)[1:2] == [name + ":"]]
    assert len(got) == 1, lines
    return got[0].split(" ", 1)[0]


class CheckTest(unittest.TestCase):
    def assert_fail(self, name, over, version="0.3.1", *args):
        fake = Fake(version, over)
        code, lines = go(fake, *args)
        self.assertEqual(status(lines, name), "FAIL", lines)
        self.assertEqual(code, 1)
        self.assertFalse([ln for ln in lines if "tag -a" in ln])
        for other in CHECKS:  # all checks still ran
            self.assertIn(status(lines, other), ("PASS", "FAIL"))
        return lines

    def test_all_green_prints_exact_commands(self):
        fake = Fake()
        code, lines = go(fake)
        self.assertEqual(code, 0)
        self.assertEqual([status(lines, c) for c in CHECKS], ["PASS"] * 7)
        self.assertEqual(lines[-5:], [
            "Release v0.3.1 is ready. Owner, run:", "  git -C ~/factory fetch origin",
            f"  git -C ~/factory tag -a v0.3.1 -m v0.3.1 {SHA}", "  git -C ~/factory push origin v0.3.1",
            "Do not run `gh release create`: the tag push makes release.yml create the release."])
        self.assertNotIn("freshbox", "\n".join(lines))
        self.assertIn("777", [ln for ln in lines if "ci-green" in ln][0])

    def test_minor_release_prints_freshbox_reminder_before_commands(self):
        code, lines = go(Fake("0.3.0"))
        self.assertEqual(code, 0)
        self.assertTrue(lines[-7].startswith("Reminder: v0.3.0"))
        self.assertIn("ci/freshbox_test.py", lines[-6])
        self.assertEqual(lines[-5], "Release v0.3.0 is ready. Owner, run:")

    def test_on_main_fail(self):
        self.assert_fail("on-main", {"git-rev-list": (0, "b" * 40 + "\n", "")})

    def test_on_main_fail_when_rev_list_errors(self):
        self.assert_fail("on-main", {"git-rev-list": (128, "", "bad ref")})

    def test_version_fail_mismatch(self):
        self.assert_fail("version", {"git-show": (0, "0.9.9\n", "")}, "0.3.1", "--version", "0.3.1")

    def test_version_fail_format(self):
        fake = Fake("v1", {"git-show": (0, "v1\n", "")})
        code, lines = go(fake)
        self.assertEqual((status(lines, "version"), code), ("FAIL", 1))

    def test_version_flag_overrides_default(self):
        fake = Fake("0.3.1", {"git-show": (0, "0.2.0\n", "")})
        code, lines = go(fake, "--version", "0.3.1")
        self.assertEqual((status(lines, "version"), code), ("FAIL", 1))  # equals VERSION at the SHA is still required

    def test_ci_green_fail_no_run(self):
        self.assert_fail("ci-green", {"gh": (0, "[]", "")})

    def test_ci_green_fail_pending(self):
        self.assert_fail("ci-green", {"gh": (0, json.dumps([{"status": "in_progress", "conclusion": "", "databaseId": 5}]), "")})

    def test_ci_green_fail_failure(self):
        self.assert_fail("ci-green", {"gh": (0, json.dumps([{"status": "completed", "conclusion": "failure", "databaseId": 5}]), "")})

    def test_ci_green_fail_gh_error(self):
        self.assert_fail("ci-green", {"gh": (1, "", "no auth")})

    def test_ci_green_asks_for_push_runs_of_the_sha(self):
        fake = Fake()
        go(fake)
        gh = [c for c in fake.calls if c[0] == "gh"][0]
        self.assertEqual(gh[:3], ["gh", "run", "list"])
        for want in (["-R", "acme/widget"], ["--workflow", "tests.yml"], ["--commit", SHA], ["--event", "push"]):
            self.assertEqual(gh[gh.index(want[0]) + 1], want[1])

    def test_notes_fail_check_version(self):
        self.assert_fail("notes", {"check-version": (1, "", "notes missing")})

    def test_notes_fail_placeholders_name_lines(self):
        lines = self.assert_fail("notes", {"release-notes": (0, "ok\nTODO write\nfine <version> here __NAME__\n", "")})
        msg = [ln for ln in lines if ln.startswith("FAIL notes")][0]
        for want in ("line 2: TODO", "line 3: <version>", "line 3: __NAME__"):
            self.assertIn(want, msg)

    def test_smoke_fail_version_output(self):
        self.assert_fail("fresh-install-smoke", {"factory-version": (0, "hivemill 9.9.9\n", "")})

    def test_smoke_version_must_match_exactly(self):
        self.assert_fail("fresh-install-smoke", {"factory-version": (0, "hivemill 0.3.10 (git abc)\n", "")})
        code, lines = go(Fake("0.3.1", {"factory-version": (0, "hivemill 0.3.1\n", "")}))
        self.assertEqual(status(lines, "fresh-install-smoke"), "PASS", lines)

    def test_notes_placeholders_each_pattern_and_clean(self):
        for text in ("TODO", "TBD", "FIXME", "XXX", "__FOO__", "<fill me>", "<!-- c -->", "TODOs", "XXXL"):
            lines = self.assert_fail("notes", {"release-notes": (0, f"ok\n{text}\n", "")})
            self.assertIn("line 2:", [ln for ln in lines if ln.startswith("FAIL notes")][0], text)
        code, lines = go(Fake("0.3.1", {"release-notes": (0, "# Notes\nAll good, a < b and c > d\n", "")}))
        self.assertEqual(status(lines, "notes"), "PASS", lines)

    def test_freshbox_reminder_prints_absolute_cwd(self):
        code, lines = go(Fake("0.3.0"))
        cmd = [ln for ln in lines if "ci/freshbox_test.py" in ln][0]
        self.assertIn(f"cd {rc.ROOT} && ", cmd)
        self.assertTrue(os.path.isabs(str(rc.ROOT)))

    def test_smoke_fail_demo_seed(self):
        self.assert_fail("fresh-install-smoke", {"demo_seed.py": (1, "", "boom")})

    def test_smoke_fail_serve(self):
        self.assert_fail("fresh-install-smoke", {"probe": (False, "HTTP 500 on /")})

    def test_smoke_fail_when_checkout_fails(self):
        lines = self.assert_fail("fresh-install-smoke", {"git-clone": (128, "", "no space")})
        self.assertIn("no space", "\n".join(lines))

    def test_public_text_fail_hits(self):
        self.assert_fail("public-text", {"public_text_guard.py": (1, "", "hit")})

    def test_public_text_without_terms_fails_closed(self):
        lines = self.assert_fail("public-text", {"public_text_guard.py": (2, "", "public-text-guard: no owner terms loaded")})
        self.assertIn("fails closed", [ln for ln in lines if ln.startswith("FAIL public-text")][0])

    def test_tag_free_fail(self):
        self.assert_fail("tag-free", {"git-ls-remote": (0, f"{SHA}\trefs/tags/v0.3.1\n", "")})

    def test_tag_free_fail_ls_remote_error(self):
        self.assert_fail("tag-free", {"git-ls-remote": (128, "", "no network")})

    def test_unresolvable_sha_exit_2(self):
        fake = Fake(over={"git-rev-parse": (128, "", "Needed a single revision")})
        code, lines = go(fake)
        self.assertEqual((code, lines), (2, []))

    def test_usage_exit_2(self):
        for argv in ([], ["--bogus"]):
            self.assertEqual(rc.main(argv, run=Fake(), out=lambda s: None), 2)

    def test_repo_url_parsing_and_override(self):
        fake = Fake()
        go(fake, "--repo", "o/r")
        self.assertIn("o/r", [c for c in fake.calls if c[0] == "gh"][0])
        fake = Fake(over={"git-remote": (0, "https://example.com/x\n", "")})
        self.assertEqual(rc.main([SHA], run=fake, out=lambda s: None), 2)

    def test_never_mutates(self):
        for over in ({}, {"gh": (0, "[]", "")}):
            fake = Fake(over=over)
            go(fake)
            for argv in fake.calls:
                words = [w for w in argv if not w.startswith("-")]
                if argv[0] == "git":
                    sub = argv[3] if argv[1] == "-C" else argv[1]
                    self.assertNotIn(sub, ("tag", "push"), argv)
                self.assertFalse(argv[:2] == ["gh", "release"], argv)
                self.assertNotIn("create", words if argv[0] == "gh" else [], argv)

    def test_smoke_isolated_and_temp_removed(self):
        live = "/live/factory/home"
        fake = Fake()
        lines = []
        rc.main([SHA], run=fake, probe=fake.probe, out=lines.append,
                env={"PATH": "/usr/bin", "HOME": "/live/home", "FACTORY_HOME": live, "FACTORY_AGENT": "coder-x"})
        smoke_envs = [e for argv, e in zip(fake.calls, fake.envs) if e and ("bin/factory" in " ".join(argv) or "demo_seed.py" in " ".join(argv))]
        self.assertEqual(len(smoke_envs), 3)  # --version, demo_seed, serve
        tmp = None
        for e in smoke_envs:
            self.assertNotEqual(e["HOME"], "/live/home")
            self.assertNotEqual(e["FACTORY_HOME"], live)
            self.assertEqual(e["FACTORY_AGENT"], "coder-x")  # passed through untouched
            tmp = tmp or Path(e["HOME"]).parent
            self.assertTrue(e["HOME"].startswith(str(tmp)) and e["FACTORY_HOME"].startswith(str(tmp)))
        self.assertFalse(tmp.exists())
        self.assertNotIn(live, " ".join(" ".join(c) for c in fake.calls))

    def test_temp_removed_after_failure(self):
        fake = Fake(over={"probe": (False, "x")})
        go(fake)
        tmp = [Path(c[-1]).parent for c in fake.calls if c[:2] == ["git", "clone"] or "clone" in c][0]
        self.assertFalse(tmp.exists())


class RealGitTest(unittest.TestCase):
    """Checks 1 and 5 against a real temp repo; gh, the guard and the web probe stay fake."""

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.repo = tmp.name
        (Path(self.repo) / "bin").mkdir()
        (Path(self.repo) / "ci").mkdir()
        (Path(self.repo) / "bin" / "factory").write_text("import sys\nprint('hivemill 0.3.1 (git x)' if sys.argv[1:] == ['--version'] else '')\n")
        (Path(self.repo) / "ci" / "demo_seed.py").write_text("import sys,os\nos.makedirs(sys.argv[2])\n")
        (Path(self.repo) / "VERSION").write_text("0.3.1\n")
        self.git("init", "-q", "-b", "main")
        self.git("add", ".")
        self.git("commit", "-q", "-m", "one")
        self.sha = self.git("rev-parse", "HEAD").strip()
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        self.git("checkout", "-q", "-b", "side")
        self.git("commit", "-q", "--allow-empty", "-m", "side")
        self.side = self.git("rev-parse", "HEAD").strip()

    def git(self, *args):
        return harness.git(*args, cwd=self.repo, env=GIT_ENV)

    def runner(self, argv, cwd=None, env=None, timeout=300):
        if argv[0] == "gh":
            return 0, GREEN_RUN, ""
        if argv[:2] == ["git", "-C"] and argv[3] == "ls-remote":
            return 0, "", ""
        if len(argv) > 2 and argv[1].endswith("changelog.py") or argv[1:2] == ["ci/changelog.py"] or "public_text_guard.py" in " ".join(argv):
            return 0, "notes ok\n", ""
        return rc.run_cmd(argv, cwd=cwd, env=env, timeout=timeout)

    def test_on_main_and_smoke_pass_on_real_repo(self):
        lines = []
        env = {"PATH": os.environ["PATH"], **{k: v for k, v in GIT_ENV.items() if k.startswith("GIT_")}}
        code = rc.main([self.sha, "--version", "0.3.1", "--repo", "acme/widget"], run=self.runner, probe=lambda a, e, p: (True, "HTTP 200 on /"),
                       root=self.repo, out=lines.append, env=env)
        self.assertEqual((status(lines, "on-main"), status(lines, "fresh-install-smoke"), code), ("PASS", "PASS", 0), lines)

    def test_commit_off_main_fails_on_main_real_repo(self):
        lines = []
        code = rc.main([self.side, "--version", "0.3.1", "--repo", "acme/widget"], run=self.runner, probe=lambda a, e, p: (True, "ok"),
                       root=self.repo, out=lines.append, env=dict(os.environ))
        self.assertEqual((status(lines, "on-main"), code), ("FAIL", 1))
        self.assertFalse([ln for ln in lines if "tag -a" in ln])

    def test_smoke_fails_when_cli_is_broken_real_repo(self):
        (Path(self.repo) / "bin" / "factory").write_text("print('hivemill 0.0.0')\n")
        self.git("commit", "-q", "-am", "break")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")
        sha = self.git("rev-parse", "HEAD").strip()
        lines = []
        code = rc.main([sha, "--version", "0.3.1", "--repo", "acme/widget"], run=self.runner, probe=lambda a, e, p: (True, "ok"),
                       root=self.repo, out=lines.append, env=dict(os.environ))
        self.assertEqual(status(lines, "fresh-install-smoke"), "FAIL")
        self.assertEqual(code, 1)
        self.assertFalse([ln for ln in lines if "tag -a" in ln])


class EntryPointTest(unittest.TestCase):
    def test_cli_usage_and_bad_sha_exit_2(self):
        tool = str(ROOT / "ci" / "release_check.py")
        for args in ([], ["not-a-sha-xyz"]):
            p = subprocess.run([sys.executable, tool, *args], capture_output=True, text=True, cwd=ROOT)
            self.assertEqual(p.returncode, 2, p.stderr)
            self.assertNotIn("tag -a", p.stdout)


if __name__ == "__main__":
    unittest.main()
