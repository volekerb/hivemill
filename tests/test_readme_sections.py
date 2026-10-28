"""Guard: README.md keeps a fixed set of top-level sections.

Feature docs live in docs/<feature>.md so parallel feature PRs never conflict
on README.md.
"""
import re
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

# Adding an entry needs a '# readme-section: <reason>' comment on the same or
# the previous line (checked by test_added_sections_have_reason).
README_SECTIONS = [
    "Quickstart",  # readme-section: G-012 T-0525 README v1 (owner M8819/M8828)
    "A task's life in commands",  # readme-section: G-012 T-0525 README v1 (owner M8819/M8828)
    "How it works",  # readme-section: G-012 T-0525 README v1 (owner M8819/M8828)
    "Features",  # readme-section: G-012 T-0525 README v1 (owner M8819/M8828)
    "How it compares",  # readme-section: G-012 T-0525 README v1 (owner M8819/M8828)
    "The web UI",  # readme-section: G-012 T-0525 README v1 (owner M8819/M8828)
    "Status and limits",  # readme-section: G-012 T-0525 README v1 (owner M8819/M8828)
    "Use it from your phone",  # readme-section: G-012 T-0525 README v1 (owner M8819/M8828)
    "Resources",  # readme-section: G-012 T-0525 README v1 (owner M8819/M8828)
    "Contributing",  # readme-section: G-012 T-0525 README v1 (owner M8819/M8828)
    "License",  # readme-section: G-012 T-0525 README v1 (owner M8819/M8828)
]
INITIAL_COUNT = 0


def readme_sections(text):
    """Top-level '## ' headings, ignoring lines inside ``` fenced blocks."""
    out = []
    fence = False
    for line in text.splitlines():
        if line.startswith("```"):
            fence = not fence
        elif not fence and line.startswith("## "):
            out.append(line[3:].strip())
    return out


def check_sections(text):
    """Return a list of problems; empty means the README passes."""
    found = readme_sections(text)
    problems = []
    for h in found:
        if h not in README_SECTIONS:
            problems.append(
                f"README has unlisted section '## {h}': document features in "
                f"docs/<feature>.md; to add a README section on purpose, add it to "
                f"README_SECTIONS with a '# readme-section: <reason>' comment")
    for h in README_SECTIONS:
        if h not in found:
            problems.append(f"README is missing listed section '## {h}'")
    return problems


class ReadmeSectionsTest(unittest.TestCase):
    def test_real_readme_passes(self):
        text = (ROOT / "README.md").read_text()
        self.assertEqual(check_sections(text), [])
        self.assertEqual(readme_sections(text), README_SECTIONS)

    def test_extra_heading_rejected_and_named(self):
        text = (ROOT / "README.md").read_text() + "\n## New thing\n\ntext\n"
        problems = check_sections(text)
        self.assertEqual(len(problems), 1)
        self.assertIn("New thing", problems[0])
        self.assertIn("docs/<feature>.md", problems[0])
        self.assertIn("readme-section:", problems[0])

    def test_heading_inside_fence_ignored(self):
        text = (ROOT / "README.md").read_text() + "\n```\n## Fake\n```\n"
        self.assertEqual(check_sections(text), [])

    def test_missing_listed_section_fails(self):
        text = "# t\n\n" + "".join(f"## {h}\n\n" for h in README_SECTIONS[:-1])
        problems = check_sections(text)
        self.assertEqual(len(problems), 1)
        self.assertIn(README_SECTIONS[-1], problems[0])

    def test_added_sections_have_reason(self):
        lines = Path(__file__).read_text().splitlines()
        start = next(i for i, l in enumerate(lines) if l.startswith("README_SECTIONS = ["))
        entries = []
        for i in range(start + 1, len(lines)):
            if lines[i].startswith("]"):
                break
            m = re.match(r'\s*"([^"]+)",\s*(#.*)?$', lines[i])
            if m:
                entries.append((i, m.group(1), m.group(2) or ""))
        self.assertEqual(len(entries), len(README_SECTIONS))
        for n, (i, name, same) in enumerate(entries):
            if n < INITIAL_COUNT:
                continue
            prev = lines[i - 1].strip()
            self.assertTrue("# readme-section:" in same or prev.startswith("# readme-section:"),
                            f"README_SECTIONS entry '{name}' needs a '# readme-section: <reason>' comment")
        self.assertIn("# readme-section:", lines[start + 1])

    def test_docs_start_with_heading(self):
        files = sorted((ROOT / "docs").glob("*.md"))
        self.assertTrue(files)
        for f in files:
            first = f.read_text().splitlines()[0]
            self.assertTrue(first.startswith("# "), f"{f.name} must start with a '# ' heading")


if __name__ == "__main__":
    unittest.main()
