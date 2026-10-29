"""T-0637: tests that spawn git must never reach the real repo through an inherited GIT_DIR (it once wrote
core.bare=true and user.email=t@example.com into the shared .git/config)."""
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import _gitenv

AT = chr(64)  # no literal email-looking text in the fixtures (scrub gate)
TESTS = Path(__file__).resolve().parent
REPO = TESTS.parent
SPAWNS_GIT = re.compile(r"""\[\s*['"]git['"]|['"]git['"]|def git\b|_git\(|\.git\(""")
# git-heavy modules that finish in seconds; the rest are covered by the static check below.
RUN_WITH_GIT_DIR = ["test_affected.py", "test_changelog.py", "test_scrub_clean.py", "test_worker_guard_repocfg.py"]


def snapshot(git_dir):
    refs = subprocess.run(["git", "--git-dir", str(git_dir), "for-each-ref"], capture_output=True, text=True,
                          check=True).stdout
    return (Path(git_dir) / "config").read_bytes(), refs


class ScrubTest(unittest.TestCase):
    def test_drops_repo_pointing_vars_keeps_identity(self):
        env = {"PATH": "/bin", "GIT_DIR": "/x", "GIT_WORK_TREE": "/y", "GIT_COMMON_DIR": "/z", "GIT_INDEX_FILE": "/i",
               "GIT_AUTHOR_NAME": "a", "GIT_COMMITTER_EMAIL": "c@example.invalid", "GIT_CONFIG_GLOBAL": "/elsewhere/cfg"}
        out = _gitenv.scrub(env)
        for k in ("GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE"):
            self.assertNotIn(k, out)
        self.assertEqual(out["PATH"], "/bin")
        self.assertEqual(out["GIT_AUTHOR_NAME"], "a")
        self.assertEqual(out["GIT_COMMITTER_EMAIL"], "c@example.invalid")
        self.assertNotIn("GIT_CONFIG_GLOBAL", out)

    def test_isolate_config_points_user_and_system_config_at_devnull(self):
        out = _gitenv.scrub({"GIT_CONFIG_GLOBAL": "/elsewhere/cfg"}, isolate_config=True)
        self.assertEqual(out["GIT_CONFIG_GLOBAL"], os.devnull)
        self.assertEqual(out["GIT_CONFIG_SYSTEM"], os.devnull)

    def test_process_env_has_no_repo_pointing_vars(self):
        self.assertEqual(_gitenv.leaked(os.environ), [])

    def test_leaked_names_repo_pointing_vars_not_identity_or_allowlist(self):
        env = {"GIT_DIR": "/x", "GIT_INDEX_FILE": "/i", "GIT_AUTHOR_NAME": "a", "GIT_EDITOR": "true", "PATH": "/bin"}
        self.assertEqual(_gitenv.leaked(env), ["GIT_DIR", "GIT_INDEX_FILE"])

    def test_importing_with_git_dir_set_raises(self):
        r = subprocess.run([sys.executable, "-c", "import _gitenv"], cwd=TESTS, capture_output=True, text=True,
                           env=dict(os.environ, GIT_DIR="/x/.git"))
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("GitEnvLeak", r.stderr)
        self.assertIn("GIT_DIR", r.stderr)


class RemoteGuardTest(unittest.TestCase):
    """R-b (M11068): no test may reach a real or network remote."""

    def test_network_and_foreign_remotes_are_refused(self):
        for url in ("https://github.com/volekerb/hivemill.git", "git" + AT + "github.com:volekerb/hivemill.git",
                    "ssh://git" + AT + "github.com/volekerb/hivemill", "/srv/someone/factory", "relative/path", str(REPO)):
            with self.assertRaises(_gitenv.RemoteRefused, msg=url):
                _gitenv.assert_local_remote(url, root=tempfile.gettempdir())

    def test_local_path_under_tmp_is_accepted(self):
        with tempfile.TemporaryDirectory() as d:
            _gitenv.assert_local_remote(str(Path(d) / "origin.git"))
            _gitenv.assert_local_remote("file://" + d + "/origin.git")

    def test_harness_git_refuses_push_clone_and_remote_add_to_network(self):
        import harness
        with tempfile.TemporaryDirectory() as d:
            for args in (("clone", "https://github.com/volekerb/hivemill.git", d + "/c"),
                         ("remote", "add", "origin", "git" + AT + "github.com:volekerb/hivemill.git"),
                         ("push", "https://github.com/volekerb/hivemill.git", "HEAD:main")):
                with self.assertRaises(_gitenv.RemoteRefused, msg=str(args)):
                    harness.git(*args, cwd=d)

    def test_harness_git_refuses_push_when_origin_is_not_under_tmp(self):
        import harness
        with tempfile.TemporaryDirectory() as d:
            subprocess.run(["git", "init", "-q", d], check=True)
            subprocess.run(["git", "-C", d, "remote", "add", "origin", "https://github.com/volekerb/hivemill.git"], check=True)
            with self.assertRaises(_gitenv.RemoteRefused):
                harness.git("push", "origin", "HEAD:main", cwd=d)

    def test_set_gh_refuses_a_real_repo_url(self):
        import harness
        env = harness.TestEnv() if hasattr(harness, "TestEnv") else None
        with self.assertRaises(_gitenv.RemoteRefused):
            harness.FactoryEnv.set_gh(env, repo_url="https://github.com/volekerb/hivemill.git")

    def test_inert_remote_opt_in_only_in_the_url_parsing_modules(self):
        users = sorted(p.name for p in TESTS.glob("test_*.py") if p.name != Path(__file__).name
                       and "inert_remote=True" in p.read_text())
        self.assertEqual(users, ["test_git_hardening.py", "test_project_remote.py", "test_remote_url_check.py"])

    def test_inert_remote_does_not_excuse_a_network_push(self):
        import harness
        with tempfile.TemporaryDirectory() as d:
            subprocess.run(["git", "init", "-q", d], check=True)
            harness.git("remote", "add", "origin", "https://github.com/example-org/x.git", cwd=d, inert_remote=True)
            with self.assertRaises(_gitenv.RemoteRefused):
                harness.git("push", "origin", "HEAD:main", cwd=d, inert_remote=True)


NOREPLY = re.compile(r"([\w.+-]+)@users\.noreply\.github\.com")
OWNER_NOREPLY_LOCAL = re.compile(r"(\d+\+)?volekerb")  # the owner's own noreply is the only allowed one


class NoRoutableIdentityTest(unittest.TestCase):
    """M11099: a test identity under *@users.noreply.github.com maps to a real GitHub user and shows up as a contributor
    of the public repo. Tests use zz-test@example.invalid; the owner's own noreply is allowed where behaviour needs one."""

    def test_no_other_users_noreply_address_in_tests(self):
        bad = []
        for p in sorted(TESTS.rglob("*")):
            if p.is_file() and p.suffix in (".py", ".json", ".sh", ".txt") and p.name != Path(__file__).name:
                for n, line in enumerate(p.read_text(errors="replace").splitlines(), 1):
                    bad += ["%s:%d %s" % (p.relative_to(REPO), n, m.group(0)) for m in NOREPLY.finditer(line)
                            if not OWNER_NOREPLY_LOCAL.fullmatch(m.group(1))]
        self.assertEqual(bad, [], "use zz-test@example.invalid (or the owner's noreply where a test needs one)")

    def test_pattern_flags_other_users_and_passes_owner(self):
        self.assertFalse(OWNER_NOREPLY_LOCAL.fullmatch("dev"))
        self.assertFalse(OWNER_NOREPLY_LOCAL.fullmatch("1+dev"))
        self.assertTrue(OWNER_NOREPLY_LOCAL.fullmatch("1+volekerb"))
        self.assertTrue(OWNER_NOREPLY_LOCAL.fullmatch("volekerb"))


class EveryGitModuleUsesTheHelperTest(unittest.TestCase):
    def test_modules_that_spawn_git_import_harness_or_gitenv(self):
        missing = []
        for p in sorted(TESTS.glob("test_*.py")):
            text = p.read_text()
            if p.name == Path(__file__).name or not SPAWNS_GIT.search(text):
                continue
            if not re.search(r"^\s*(import harness|from harness|import _gitenv)", text, re.M):
                missing.append(p.name)
        self.assertEqual(missing, [], "add `import _gitenv  # noqa: F401` to these test modules")


class InheritedGitDirTest(unittest.TestCase):
    """Run real test modules with GIT_DIR pointing at a scratch repo; its config and refs must not change."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.scratch = Path(self.tmp.name) / "scratch.git"
        env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_DIR")}
        subprocess.run(["git", "init", "-q", "--bare", "-b", "main", str(self.scratch)], check=True, env=env)

    def run_module(self, name):
        env = dict(os.environ, GIT_DIR=str(self.scratch), GIT_WORK_TREE=self.tmp.name)
        r = subprocess.run([sys.executable, "-m", "unittest", "discover", "-s", "tests", "-p", name], cwd=REPO,
                           env=env, capture_output=True, text=True, timeout=600)
        self.assertNotEqual(r.returncode, 0, name + " ran with GIT_DIR set instead of refusing")
        self.assertIn("GitEnvLeak", r.stderr, name)

    def test_modules_refuse_inherited_git_dir_and_leave_scratch_repo_untouched(self):
        before = snapshot(self.scratch)
        for name in RUN_WITH_GIT_DIR:
            self.run_module(name)
        self.assertEqual(snapshot(self.scratch), before)

    def test_unscrubbed_git_does_write_to_inherited_git_dir(self):
        """Control: without the scrub the same inherited GIT_DIR is written to, so the test above can fail."""
        before = snapshot(self.scratch)
        env = dict(os.environ, GIT_DIR=str(self.scratch))
        subprocess.run(["git", "-C", self.tmp.name, "config", "user.email", "t@example.com"], env=env, check=True)
        self.assertNotEqual(snapshot(self.scratch), before)


if __name__ == "__main__":
    unittest.main()
