import os
import re
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HEADINGS = ["## Summary", "## Why", "## Changes", "## Test evidence", "## Risk / rollback"]
FOOTER = "🤖 Generated with [Claude Code](https://claude.com/claude-code)"
WALK = "## Acceptance walk"
SKILL = os.path.join(ROOT, "roles", "plugins", "coder", "coder-skills", "skills", "pr-description", "SKILL.md")


def headings(text):
    return re.findall(r"^#{1,6} .*$", text, re.M)


def read(*parts):
    with open(os.path.join(ROOT, *parts), encoding="utf-8") as f:
        return f.read()


class TemplateTests(unittest.TestCase):
    def template(self):
        return read(".github", "pull_request_template.md")

    def test_sections_in_order_then_acceptance_walk(self):
        self.assertEqual(headings(self.template()), HEADINGS + [WALK])

    def test_acceptance_walk_line_is_exact_and_inside_details(self):
        text = self.template()
        self.assertIn("\n" + WALK + "\n", text)
        self.assertGreater(text.index(WALK), text.index("<details>"))
        self.assertLess(text.index(WALK), text.index("</details>"))

    def test_footer_is_absent(self):
        self.assertNotIn("Generated with", self.template())
        self.assertEqual(self.template().rstrip().splitlines()[-1], "</details>")

    def test_word_target_stated_outside_details(self):
        text = self.template()
        self.assertRegex(text[: text.index("<details>")], r"<!--[^>]*150-250 words")

    def test_checks_fail_when_a_part_is_removed(self):
        text = self.template()
        for part in (HEADINGS[2], WALK):
            mutated = text.replace(part, "")
            self.assertNotEqual(headings(mutated), HEADINGS + [WALK], part)

    def test_template_rejects_a_body_with_gates_heading(self):
        body = self.template() + "\n## Gates\n"
        self.assertNotEqual(headings(body), HEADINGS + [WALK])
        self.assertEqual(headings(body)[-1], "## Gates")


class SkillTests(unittest.TestCase):
    def test_front_matter_and_size(self):
        text = read(SKILL)
        m = re.match(r"---\n(.*?)\n---\n", text, re.S)
        self.assertTrue(m)
        self.assertEqual(re.search(r"^name:\s*(\S+)\s*$", m.group(1), re.M).group(1), "pr-description")
        self.assertIn("Use when", re.search(r"^description:\s*(.+)$", m.group(1), re.M).group(1))
        self.assertLessEqual(len(text.splitlines()), 60)

    def test_skill_states_the_standard(self):
        text = read(SKILL)
        self.assertNotIn(FOOTER, text)
        for needle in HEADINGS + ["150-250 words", "## Gates", "factory verdict", "Co-Authored-By", "marketing tone"]:
            self.assertIn(needle, text)


class RoleLineTests(unittest.TestCase):
    def test_pointer_and_enforcement_lines(self):
        self.assertIn("pr-description skill", read("roles", "coder.md"))
        self.assertIn("is a blocking finding", read("roles", "reviewer.md"))
        self.assertIn(".github/pull_request_template.md", read("roles", "reviewer.md"))
        self.assertIn("No merge without a template-conformant PR body.", read("roles", "judge.md"))

    def test_role_pairs_within_budget(self):
        common = len(read("roles", "_common.md").encode())
        m = re.search(r"^ROLE_PROMPT_BUDGET_BYTES\s*=\s*(\d+)", read("tests", "test_repo_hygiene.py"), re.M)
        self.assertTrue(m, "ROLE_PROMPT_BUDGET_BYTES not found in tests/test_repo_hygiene.py")
        for r in ("coder", "reviewer", "judge"):
            self.assertLessEqual(common + len(read("roles", r + ".md").encode()), int(m.group(1)), r)

    def test_docs_have_source_and_two_examples(self):
        text = read("docs", "pr-descriptions.md")
        self.assertIn("Source: owner request M2502; G-007, our own wording.", text)
        self.assertEqual(len(re.findall(r"^## Example \d", text, re.M)), 2)


if __name__ == "__main__":
    unittest.main()
