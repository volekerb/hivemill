"""Reviewer/QA/judge prompt rules stay present, and no existing step was lost."""
import unittest
from pathlib import Path

ROLES = Path(__file__).resolve().parent.parent / "roles"

MARKERS = {
    "reviewer.md": {
        "R1": "Nits never cause FAIL",
        "R2": "RESOLVED, OPEN or DECLINED",
        "R3": "never claim \"X is unused\"",
        "R4": "every file the diff deletes",
        "R5": "at least 3 ways around the change",
        "R6": "in a task whose spec does not ask for it",
        "R7": "copied verbatim from a third-party repo",
        "S1": "Stale base (S1)",
    },
    "qa.md": {
        "Q1": "0 tests ran",
        "Q2": "failing with the fix reverted",
        "Q3": "RESOLVED or OPEN before the full pass",
        "Q4": "An untested acceptance criterion is blocking",
        "Q5": "lists EVERY numbered acceptance item",
        "S1": "Stale base (S1)",
        "Q5-stale": "a coder who never merged main is still FAIL",
    },
    "judge.md": {
        "J1": "\"PASS\" is a gate state",
        "J2": "do not run your own investigation",
        "J3": "insufficient evidence:",
        "J4": "Stale base (J4)",
        "J4-carry-over": "docs/verdict-carry-over.md",
        "J4-mechanical": "not code authorship",
        "J4-merge-call": "one plain Bash call via run_in_background; no ; && | & or redirect; read the exit status from the tool result",
    },
}

EXISTING = {
    "reviewer.md": ["factory task show <T>", "gh pr view <url>", "gh pr diff <url>",
                    "gh pr comment <url> --body-file -", "factory verdict <T> --pass|--fail",
                    "## Gates", "factory done"],
    "qa.md": ["factory task show <T>", "gh pr view <url>", "gh pr diff <url>",
              "factory verdict <T> --pass --summary -", "## Gates", "--fail",
              "gh pr comment <url> --body-file -", "factory done"],
    "judge.md": ["factory task show <T>", "gh pr view <url> --comments", "gh pr checks <url>",
                 "factory judge <T> --merge", "factory judge <T> --reject", "factory done"],
}


class PromptRulesReview(unittest.TestCase):
    def _check(self, name):
        text = (ROLES / name).read_text()
        for rule, marker in MARKERS[name].items():
            self.assertIn(marker, text, f"{name}: rule {rule} missing (marker {marker!r})")
        for line in EXISTING[name]:
            self.assertIn(line, text, f"{name}: existing step lost: {line!r}")

    def test_reviewer(self):
        self._check("reviewer.md")

    def test_qa(self):
        self._check("qa.md")

    def test_judge(self):
        self._check("judge.md")


if __name__ == "__main__":
    unittest.main()
