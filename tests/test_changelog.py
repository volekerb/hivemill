"""Tests for ci/changelog.py: grouping, entry format, scrub drop, --check, run through the real CLI."""
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)
import harness

ROOT = Path(__file__).resolve().parent.parent
TOOL = str(ROOT / "ci" / "changelog.py")
GIT_ENV = {"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t",
           "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_AUTHOR_DATE": "2026-01-02T03:04:05Z",
           "GIT_COMMITTER_DATE": "2026-01-02T03:04:05Z", "PATH": os.environ["PATH"], "HOME": "/nonexistent"}
HOME_SUBJECT = "T-0004: read /ho" + "me/alice/notes (#4)"  # split: not a scrub hit in this file


def git(repo, *args):
    harness.git(*args, cwd=repo, env=GIT_ENV)


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
        generated = out[out.index("### Added"):]  # the curated release notes above it also use bullets
        entries = [l for l in generated.splitlines() if l.startswith("- ")]
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

    def test_release_notes_prints_curated_block(self):
        r = changelog(self.repo, "--release-notes", "0.1.0")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout, (ROOT / "ci" / "release-notes" / "0.1.0.md").read_text(encoding="utf-8"))
        for heading in ("Highlights", "Features", "Known limits", "Install"):
            self.assertIn(f"\n### {heading}\n", r.stdout)

    def test_release_notes_missing_file_is_error(self):
        r = changelog(self.repo, "--release-notes", "9.9.9")
        self.assertEqual(r.returncode, 2)
        self.assertIn("ci/release-notes/9.9.9.md", r.stderr)

    def test_prerelease(self):
        for version, expected in (("0.1.0", "true"), ("1.0.0", "false"), ("1.2.0-rc1", "true")):
            r = changelog(self.repo, "--prerelease", version)
            self.assertEqual((r.returncode, r.stdout.strip()), (0, expected), version)

    def test_write_puts_curated_block_under_heading(self):
        out = changelog(self.repo).stdout
        notes = (ROOT / "ci" / "release-notes" / "0.1.0.md").read_text(encoding="utf-8").strip()
        self.assertEqual(len(re.findall(r"^### Highlights$", out, re.M)), 1)
        self.assertLess(out.index("## [0.1.0]"), out.index(notes))
        self.assertLess(out.index(notes), out.index("### Added"))

    def test_committed_changelog_has_curated_block_first(self):
        text = (ROOT / "CHANGELOG.md").read_text(encoding="utf-8")
        self.assertEqual(len(re.findall(r"^### Highlights$", text, re.M)), 1)
        self.assertLess(text.index("## [0.1.0]"), text.index("### Highlights"))

    def test_release_notes_are_public_text(self):
        text = (ROOT / "ci" / "release-notes" / "0.1.0.md").read_text(encoding="utf-8")
        self.assertIsNone(re.search(r"\b[GT]-\d{3,4}\b", text))
        self.assertGreaterEqual(len(re.findall(r"^- \*\*", text, re.M)), 6)
        for needle in ("Hivemill v0.1.0", "Pre-release.", "SECURITY.md", "On-demand", "sandbox"):
            self.assertIn(needle, text)

    def test_release_notes_links_are_absolute(self):
        # The same text lands in CHANGELOG.md and the GitHub release body, so a relative link breaks in one of them.
        for path in sorted((ROOT / "ci" / "release-notes").glob("*.md")):
            text = path.read_text(encoding="utf-8")
            self.assertEqual(re.findall(r"\]\((?:\.\.?/)[^)]*\)", text), [], path.name)
            for target in re.findall(r"\]\(([^)]*)\)", text):
                self.assertRegex(target, r"^https://", f"{path.name}: {target}")


class WorkflowTextTest(unittest.TestCase):
    def read(self, name):
        return (ROOT / ".github" / "workflows" / name).read_text(encoding="utf-8")

    def test_release_workflow(self):
        text = self.read("release.yml")
        self.assertRegex(text, r"(?m)^    tags:\n      - 'v\*'$")
        self.assertEqual(text.count("contents: write"), 1)
        self.assertGreaterEqual(text.count("--prerelease"), 2)
        self.assertIn('--title "Hivemill $GITHUB_REF_NAME"', text)
        self.assertIn("GH_TOKEN: ${{ github.token }}", text)
        run_lines = [l for l in text.splitlines() if "${{" in l]
        self.assertEqual([l.strip() for l in run_lines], ["GH_TOKEN: ${{ github.token }}"])

    def test_tests_workflow_runs_on_pull_request_and_main_push(self):
        text = self.read("tests.yml")
        self.assertIn("  pull_request:\n", text)
        self.assertIn("  push:\n", text)
        self.assertIn("  push:\n    branches: [main]\n", text)


if __name__ == "__main__":
    unittest.main()
