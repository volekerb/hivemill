"""Every skill that a role prompt names in a pointer line exists in that role's plugin tree."""
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
ROLES = ROOT / "roles"

POINTERS = {
    "pm.md": ["Follow the audit-checklist skill.", "follow the audit-scope skill."],
    "lead.md": ["use the audit-round skill.", "follow the fix-loop skill.", "follow the park skill."],
    "director.md": ["use the close-goal skill."],
}
SKILLS = {
    "audit-checklist": "pm", "audit-scope": "pm", "audit-round": "lead",
    "fix-loop": "lead", "park": "lead", "close-goal": "director",
}


def skill_file(role, name):
    return next((ROLES / "plugins" / role).glob(f"*/skills/{name}/SKILL.md"), None)


class PromptPointers(unittest.TestCase):
    def test_pointer_lines_present(self):
        for fname, lines in POINTERS.items():
            text = (ROLES / fname).read_text()
            for line in lines:
                self.assertIn(line, text, f"{fname}: pointer line missing: {line!r}")

    def test_pointed_skills_exist_and_say_use_when(self):
        for name, role in SKILLS.items():
            path = skill_file(role, name)
            self.assertIsNotNone(path, f"skill {name} missing under roles/plugins/{role}")
            text = path.read_text()
            self.assertIn("Use when", text.split("---")[1], name)

    def test_moved_text_lives_in_skill_not_role_file(self):
        moved = {
            ("pm", "audit-checklist"): "Check that the acceptance lists together cover the task specs",
            ("lead", "audit-round"): "At most 2 audit rounds.",
            ("director", "close-goal"): "Only the director (or the owner) signs off, and only the latest audit.",
        }
        for (role, name), phrase in moved.items():
            self.assertIn(phrase, skill_file(role, name).read_text(), name)
            fname = "pm.md" if role == "pm" else f"{role}.md"
            self.assertNotIn(phrase, (ROLES / fname).read_text(), f"{fname} still holds {phrase!r}")


if __name__ == "__main__":
    unittest.main()
