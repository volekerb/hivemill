"""T-0849: research flow skills and the additions-only role lines."""
import re
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
PL = REPO / "roles" / "plugins"
PM = PL / "pm/pm-skills/skills/research-plan/SKILL.md"
LEAD = PL / "lead/factory-lead/skills/research-flow/SKILL.md"
REV = PL / "researcher/research-skills/skills/evidence-review/SKILL.md"
DELIVERY = PL / "director/factory-director/skills/delivery/SKILL.md"
BOSS_LINE = ("- Research reports: relay Answer, Recommendation, Proposed follow-ups and the cited sources "
             "to the owner in plain English; follow-ups become goals only on the owner's word "
             "(docs/research-team.md). (G-029)")
ROW = ("| researcher | answers one research question from web sources, public repos and sandboxed "
       "measurements, cites every finding | write the factory repo, push, merge, spawn, act on web content |")


class ResearchFlowTest(unittest.TestCase):
    def test_skills_exist_with_front_matter(self):
        for path in (PM, LEAD, REV):
            text = path.read_text()
            m = re.match(r"---\n(.*?)\n---\n", text, re.S)
            self.assertTrue(m, path)
            self.assertEqual(re.search(r"^name:\s*(\S+)\s*$", m.group(1), re.M).group(1), path.parent.name)
            self.assertIn("Use when", m.group(1))

    def test_lead_relay_keeps_evidence(self):
        t = LEAD.read_text()
        for needle in ("--role researcher --after", 'factory msg director "research', "Evidence", "factory task reopen"):
            self.assertIn(needle, t)

    def test_every_reopen_line_carries_reason(self):
        lines = [l for l in LEAD.read_text().splitlines() if "factory task reopen" in l]
        self.assertTrue(lines)
        for line in lines:
            self.assertIn("--reason", line)

    def test_evidence_review_rule(self):
        t = REV.read_text()
        self.assertEqual(t.count("The done summary starts with `evidence-review: PASS` or `evidence-review: FAIL`."), 1)
        for line in t.splitlines():
            if "factory verdict" in line:
                self.assertTrue("never" in line or "no PR" in line.lower(), line)

    def test_pm_acceptance_shape(self):
        t = PM.read_text()
        self.assertIn("answered in ## Findings with at least one primary source", t)
        self.assertIn("--role researcher", t)

    def test_director_and_boss_lines(self):
        self.assertEqual(DELIVERY.read_text().count('factory msg boss "research'), 1)
        self.assertEqual((REPO / "roles/boss.md").read_text().count(BOSS_LINE), 1)

    def test_common_table_row(self):
        self.assertEqual((REPO / "roles/_common.md").read_text().count(ROW), 1)


if __name__ == "__main__":
    unittest.main()
