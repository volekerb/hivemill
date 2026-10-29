"""The test-count rule is worded once (merge-tree count vs origin/main) in every file that states it,
and qa.md / judge.md carry the merged-tree recipe without telling the worker to run git merge."""
import re
import unittest
from pathlib import Path

ROLES = Path(__file__).resolve().parent.parent / "roles"

RULE = ("Test count: count `def test_` on the tree of `git merge-tree --write-tree origin/main HEAD` "
        "(the PR merged into current main) and on origin/main; the merged count must be >= the main count. "
        "Also confirm the PR removes or renames no `def test_` relative to its merge-base "
        "(`git diff $(git merge-base origin/main HEAD)..HEAD`).")

RECIPE = [
    "do not run git merge",
    "T=$(git merge-tree --write-tree origin/main <pr-head>)",
    "C=$(git commit-tree $T -p origin/main -p <pr-head> -m tmp)",
    "factory src $C <scratchpad>/merged",
    "rm -rf <scratchpad>/merged",
]

# Phrases that state the rule; any file using one must carry RULE.
STATES_RULE = re.compile(r"test count|fewer than main|count >= main|merged count", re.I)

# Files that must state the rule.
MUST_STATE = [
    "lead.md",
    "qa.md",
    "reviewer.md",
    "plugins/lead/factory-lead/skills/worker-brief/SKILL.md",
    "plugins/coder/coder-skills/skills/pre-pr-check/SKILL.md",
]

# Any git merge mention except the explicit "do not run git merge" phrasing.
ANY_MERGE = re.compile(r"(?<!do not run )\bgit merge\b(?!-)", re.I)


class PromptRulesTestCount(unittest.TestCase):
    def test_required_files_carry_rule(self):
        for rel in MUST_STATE:
            self.assertIn(RULE, (ROLES / rel).read_text(), f"{rel}: merge-tree test-count wording missing")

    def test_any_file_stating_rule_uses_merge_tree_wording(self):
        offenders = []
        for p in sorted(ROLES.rglob("*.md")):
            text = p.read_text()
            if STATES_RULE.search(text) and RULE not in text:
                # allowed: pointers that name the rule rather than restate it
                for line in text.splitlines():
                    if STATES_RULE.search(line) and "merge-tree" not in line and "pre-pr-check" not in line \
                            and "rule in" not in line and "rule (" not in line:
                        offenders.append(f"{p.relative_to(ROLES)}: {line.strip()[:100]}")
        self.assertEqual(offenders, [], "role text states the test-count rule without the merge-tree wording")

    def test_old_ambiguous_wording_gone(self):
        for p in ROLES.rglob("*.md"):
            self.assertNotIn("test count >= main", p.read_text(), f"{p}: old ambiguous wording")

    def test_qa_and_judge_have_recipe_without_git_merge(self):
        for name in ("qa.md", "judge.md"):
            text = (ROLES / name).read_text()
            for piece in RECIPE:
                self.assertIn(piece, text, f"{name}: recipe piece missing: {piece!r}")
            self.assertNotIn("git worktree add", text, f"{name}: use factory src, not git worktree")
            self.assertIsNone(ANY_MERGE.search(text), f"{name}: mentions git merge beyond the do-not-run phrasing")

    def test_coder_points_to_the_rule(self):
        text = (ROLES / "coder.md").read_text()
        self.assertIn("merged tree", text)
        self.assertNotIn("for your branch", text)


if __name__ == "__main__":
    unittest.main()
