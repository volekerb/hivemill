"""Generic scrub scan (no owner terms) must stay clean on this tree. See docs/tests-and-ci.md."""
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)
import harness

ROOT = Path(__file__).resolve().parent.parent
SCAN = ROOT / "ci" / "scrub_scan.py"
BAD_LINE = "path = '/ho" + "me/someone/x'\n"  # split so this file is not a hit itself


def scan(tree):
    env = {k: v for k, v in os.environ.items() if k != "SCRUB_TERMS_FILE"}
    return subprocess.run([sys.executable, str(SCAN), "--tree", str(tree)], capture_output=True, text=True, env=env)


class ScrubCleanTests(unittest.TestCase):
    def test_repo_tree_is_clean(self):
        r = scan(ROOT)
        self.assertEqual(r.returncode, 0,
                         "generic scrub scan has hits; fix the line or add a reviewed entry with a reason to "
                         f"ci/scrub_allowlist.txt (docs/tests-and-ci.md):\n{r.stdout}{r.stderr}")

    def test_added_home_path_file_fails(self):
        with tempfile.TemporaryDirectory() as d:
            repo = Path(d)
            env = {**os.environ, "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1"}
            def git(*a):
                harness.git("-c", "user.name=t", "-c", "user.email=t@example.com", *a, cwd=d, env=env)
            git("init", "-q")
            (repo / "ok.txt").write_text("fine\n")
            git("add", ".")
            git("commit", "-q", "-m", "base")
            self.assertEqual(scan(repo).returncode, 0)
            (repo / "bad.txt").write_text(BAD_LINE)
            git("add", ".")
            git("commit", "-q", "-m", "add")
            r = scan(repo)
            self.assertEqual(r.returncode, 1, r.stdout + r.stderr)
            self.assertIn("home-path bad.txt:1", r.stdout)


if __name__ == "__main__":
    unittest.main()
