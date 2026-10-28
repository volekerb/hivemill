"""G-013 F2: the drift standing-rule lines stay in the lead and pm prompts."""
import unittest
from pathlib import Path

ROLES = Path(__file__).resolve().parent.parent / "roles"

MARKERS = {
    "lead.md": "Status is mechanical but drift alerts are yours: fix the status or the task the same hour. (G-013)",
    "pm.md": "Set status true before `done`. (G-013)",
}


class PromptRulesDrift(unittest.TestCase):
    def test_drift_lines_present_and_short(self):
        for name, marker in MARKERS.items():
            text = (ROLES / name).read_text()
            self.assertIn(marker, text, f"{name}: drift rule missing")
            line = next(l for l in text.splitlines() if marker in l)
            self.assertLessEqual(len(line.encode()), 160, f"{name}: drift line too long")

    def test_lead_line_sits_under_standing_rules(self):
        text = (ROLES / "lead.md").read_text()
        self.assertGreater(text.index(MARKERS["lead.md"]), text.index("## Standing rules (lessons)"))


if __name__ == "__main__":
    unittest.main()
