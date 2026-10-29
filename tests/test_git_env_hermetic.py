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

    def test_process_env_is_already_scrubbed(self):
        for k in os.environ:
            self.assertTrue(not k.startswith("GIT_") or k.startswith(_gitenv.KEEP_PREFIXES), k)


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
        self.assertEqual(r.returncode, 0, name + ":\n" + r.stderr[-2000:])
        self.assertRegex(r.stderr, r"Ran [1-9]\d* test", name)

    def test_modules_leave_scratch_repo_untouched(self):
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
