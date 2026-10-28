"""Tests for ci/changelog.py: grouping, entry format, scrub drop, --check, run through the real CLI."""
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
TOOL = str(ROOT / "ci" / "changelog.py")
GIT_ENV = {"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t",
           "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_AUTHOR_DATE": "2026-01-02T03:04:05Z",
           "GIT_COMMITTER_DATE": "2026-01-02T03:04:05Z", "PATH": os.environ["PATH"], "HOME": "/nonexistent"}
HOME_SUBJECT = "T-0004: read /ho" + "me/alice/notes (#4)"  # split: not a scrub hit in this file


def git(repo, *args):
    subprocess.run(["git", "-C", repo, *args], check=True, capture_output=True, env=GIT_ENV)


def make_repo(root, subjects):
    git(root, "init", "-q", "-b", "main")
    for i, s in enumerate(subjects):
        git(root, "commit", "-q", "--allow-empty", "-m", s)


def changelog(repo, *args):
    return subprocess.run([sys.executable, TOOL, "--repo", repo, "--ref", "main", *args],
                          capture_output=True, text=True, cwd=repo)


class ChangelogTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.repo = tmp.name
        make_repo(self.repo, ["T-0001: add widget (#1)", "T-0002: fix crash on 2026-10-25 (#2)",
                              "T-0003: plain commit without a PR", HOME_SUBJECT, "T-0005: tighten deny rule (#5)"])

    def test_grouping_format_and_drop(self):
        r = changelog(self.repo)
        self.assertEqual(r.returncode, 0, r.stderr)
        out = r.stdout
        self.assertEqual(len(re.findall(r"^## ", out, re.M)), 1)
        self.assertIn("## [0.1.0] - Initial public release\n\nFirst public release of the factory, published from a fresh repository.", out)
        self.assertIn("v0.1.0 summarises the changes merged during private development.", out)
        self.assertNotIn("Unreleased", out)
        self.assertRegex(out, r"### Added\n\n- T-0001: add widget\n")
        self.assertRegex(out, r"### Fixed\n\n- T-0002: fix crash on\n")
        self.assertRegex(out, r"### Security\n\n- T-0005: tighten deny rule\n")
        self.assertNotIn("### Changed", out)
        self.assertNotIn("T-0003", out)  # no (#N): not a merged PR
        self.assertNotIn("alice", out)  # scrub hit dropped
        self.assertIn("1 subject(s) dropped", r.stderr)
        entries = [l for l in out.splitlines() if l.startswith("- ")]
        self.assertEqual(len(entries), 3)
        for l in entries:
            self.assertNotIn("(#", l)
            self.assertIsNone(re.search(r"\d{4}-\d{2}-\d{2}", l))

    def test_since_limits_range(self):
        sha = subprocess.run(["git", "-C", self.repo, "rev-parse", "HEAD~1"], capture_output=True, text=True).stdout.strip()
        out = changelog(self.repo, "--since", sha).stdout
        self.assertIn("T-0005", out)
        self.assertNotIn("T-0001", out)

    def test_check_passes_then_fails_after_edit(self):
        f = os.path.join(self.repo, "CHANGELOG.md")
        self.assertEqual(changelog(self.repo, "--write", "--file", f).returncode, 0)
        self.assertEqual(changelog(self.repo, "--check", "--file", f).returncode, 0)
        with open(f, "a", encoding="utf-8") as fh:
            fh.write("stray line\n")
        r = changelog(self.repo, "--check", "--file", f)
        self.assertEqual(r.returncode, 1)
        self.assertIn("out of date", r.stderr)

    def test_check_missing_file_is_error(self):
        r = changelog(self.repo, "--check", "--file", os.path.join(self.repo, "nope.md"))
        self.assertEqual(r.returncode, 2)

    def test_bad_ref_is_error(self):
        r = subprocess.run([sys.executable, TOOL, "--repo", self.repo, "--ref", "nosuchref"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 2)
        self.assertIn("git log", r.stderr)


if __name__ == "__main__":
    unittest.main()
