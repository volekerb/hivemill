"""pm/lead planning rules live in skills; markers and the lead.md pointer stay present."""
import subprocess
import unittest
from pathlib import Path

ROLES = Path(__file__).resolve().parent.parent / "roles"
PM_SKILL = ROLES / "plugins/pm/pm-skills/skills/plan-quality/SKILL.md"
BRIEF_SKILL = ROLES / "plugins/lead/factory-lead/skills/worker-brief/SKILL.md"
LEAD_SKILL = ROLES / "plugins/lead/factory-lead/skills/fix-loop/SKILL.md"
ACCEPT_SKILL = ROLES / "plugins/pm/pm-skills/skills/acceptance-template/SKILL.md"

MARKERS = {
    PM_SKILL: {
        "P1": "the spec must list every consumer",
        "P2": "a case that gets blocked and a case that is let through",
        "P3": "Tasks that edit the same file run in sequence",
        "P4": "never only a note, a message or a reviewer nit",
        "P5": "lists every code path that reads that column",
        "P6": "restate the whole rule with at least one input -> expected output example",
        "P7": "never edit the same docs page or page list",
        "P8": "Never compress existing rule wording to fit",
        "P1-P8": "against P1-P8 before you report the plan ready",
    },
    ACCEPT_SKILL: {
        "A1": "Every acceptance item is `N. <claim> - <runnable command> -> <expected result>`",
        "A2": "checked with `grep -nF '<exact text>' <file>`",
        "A3": "git diff origin/main -- <file> | grep '^-' | grep -v '^--- a/'",
        "A4": "A role prompt delta states the headroom (plan-quality P8)",
        "A5": "A spec without RISKS is not ready.",
        "A6": "see plan-quality P2; do not restate it",
        "rules-line": "never call the 'advisor' tool.\"",
    },
    BRIEF_SKILL: {
        "B-P4": "numbered acceptance items in the DB before spawn (P4)",
        "B-judge": "Spawn the judge only after `gh pr checks <url>` shows no pending check",
    },
    LEAD_SKILL: {
        "L1": "it produced nothing (L1)",
        "L2": "\"attempt N of 3\"",
        "L3": "Diagnose before the third attempt (L3)",
        "L4": "do not respawn a coder (L4)",
        "L6": "Respawn the same verifier, not a coder (L6)",
    },
}

LEAD_EXISTING = ["factory spawn coder --task T", "factory spawn reviewer --task T", "factory spawn qa --task T",
                 "factory spawn judge --task T", "factory spawn pm --task", "**Three attempts", "factory report"]


class PromptRulesPlanning(unittest.TestCase):
    def test_skill_markers(self):
        for path, rules in MARKERS.items():
            text = path.read_text()
            for rule, marker in rules.items():
                self.assertIn(marker, text, f"{path.name}: rule {rule} missing (marker {marker!r})")

    def test_pm_prompt_points_at_acceptance_template(self):
        self.assertIn("pm-skills:acceptance-template", (ROLES / "pm.md").read_text())

    def test_additions_only_grep_catches_every_deleted_line(self):
        """The documented additions-only check must print a deleted line that starts with '-' (a Markdown bullet)."""
        line = next(l for l in ACCEPT_SKILL.read_text().splitlines() if l.startswith("3. additions only"))
        pipeline = line.split("| ", 1)[1].split(" -> ")[0]
        self.assertEqual(pipeline, "grep '^-' | grep -v '^--- a/'")
        self.assertNotIn("grep '^-[^-]'", ACCEPT_SKILL.read_text())  # no stale weak form anywhere in the skill
        for deleted in ("-- bullet", "-", "-plain"):
            diff = f"--- a/f\n+++ b/f\n@@ -1 +1 @@\n{deleted}\n+new\n"
            out = subprocess.run(["sh", "-c", pipeline], input=diff, capture_output=True, text=True)
            self.assertEqual(out.stdout, deleted + "\n", deleted)
        additions = subprocess.run(["sh", "-c", pipeline], input="--- a/f\n+++ b/f\n@@ -1 +1,2 @@\n+new\n",
                                   capture_output=True, text=True)
        self.assertEqual(additions.stdout, "")

    def test_l3_uses_allowed_channel_and_spec_change(self):
        text = LEAD_SKILL.read_text()
        self.assertIn("factory msg staff-engineer", text, "L3")
        self.assertIn("--kind request", text, "L3")
        self.assertIn("spec-change skill", text, "L3")

    def test_lead_pointer_line_and_existing_steps(self):
        text = (ROLES / "lead.md").read_text()
        self.assertIn("Retries, stalls and the third attempt: follow the fix-loop skill.", text)
        for line in LEAD_EXISTING:
            self.assertIn(line, text, f"lead.md: existing step lost: {line!r}")


if __name__ == "__main__":
    unittest.main()
