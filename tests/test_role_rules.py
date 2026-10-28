import re
import unittest

from harness import REPO

ROLES_DIR = REPO / "roles"
HEADING = "## Standing rules (lessons)"

# file -> distinctive phrases that must appear under the standing-rules heading
RULES = {
    "_common.md": ["Only `factory spawn` starts Claude", "owner exception", "M347",
                   "does not print your own agent name", "A denied command stays denied",
                   "L-022", "L-024"],
    "boss.md": ["Only the boss creates goals", "Batch owner questions", "M833", "verbatim",
                "every automated writer", "factory relaunch director --when-idle",
                "owner-reserved", "2 daytime hours", "L-025"],
    "director.md": ["Never create goals", "verbatim", "Approve internal designs yourself", "restriction-check",
                    "factory relaunch <name> --when-idle", "mergeable", "M833", "2 daytime hours",
                    "owner-decisions", "L-025"],
    "lead.md": ["factory stop", "spec-change", "worker-brief", "read-only `goal show <own goal>`",
                "merge origin/main", "mergeable", "pr-triage", "owner exception", "L-020", "L-025"],
    "coder.md": ["git merge origin/main", "Ran N", "`test_` prefixes"],
    "reviewer.md": ["test count with origin/main", "def test_"],
}


def standing(name):
    text = (ROLES_DIR / name).read_text()
    assert text.count(HEADING) == 1, f"{name}: expected one '{HEADING}' heading"
    return text.split(HEADING, 1)[1]


class StandingRulesTests(unittest.TestCase):
    def test_rules_present_in_section(self):
        for name, needles in RULES.items():
            section = standing(name)
            for needle in needles:
                with self.subTest(file=name, needle=needle):
                    self.assertIn(needle, section)

    def test_section_is_last_and_rules_cite_sources(self):
        for name in RULES:
            with self.subTest(file=name):
                lines = [l for l in standing(name).splitlines() if l.strip()]
                self.assertTrue(lines)
                for line in lines:
                    self.assertTrue(line.startswith("- "), f"{name}: not a rule line: {line}")
                    self.assertRegex(line, r"\((?:[A-Z]-?\d+[^()]*)\)\.?$", f"{name}: no source: {line}")

    def test_titles_kept(self):
        for name in RULES:
            first = (ROLES_DIR / name).read_text().splitlines()[0]
            with self.subTest(file=name):
                if name == "_common.md":
                    self.assertTrue(first.startswith("# Factory operating rules"), first)
                else:
                    self.assertTrue(first.startswith("# Role:"), first)

    def test_m833_old_rule_gone(self):
        self.assertNotIn("keep the question open", (ROLES_DIR / "director.md").read_text())

    def test_director_delivery_ownership(self):
        director = (ROLES_DIR / "director.md").read_text()
        for line in (
            "## Delivery ownership (owner M7486)",
            "- **D1** You own on-time delivery of every active goal; leads are optional helpers, not gatekeepers.",
            "- **D2** You may spawn pm/coder/qa/reviewer for any goal yourself, inside owner caps (factory config max_live_agents / max_live_leads; M5894).",
            "- **D3** Keep a delivery plan per active goal current: remaining tasks, target date, critical path, risks.",
            "- **D4** Daily report: each goal's done/total vs plan and every slip with its recovery action; a slip without one counts against R7.",
            "- **D5** Lead-less dispatch: a ready task whose lead is parked or idle gets its worker from you, P1 first (M7462).",
            "use the delivery skill.",
        ):
            self.assertIn(line, director)

    def test_director_plugin_skills(self):
        base = ROLES_DIR / "plugins" / "director"
        self.assertTrue((base / "README.md").is_file())
        self.assertTrue((base / "factory-director" / ".claude-plugin" / "plugin.json").is_file())
        skills = base / "factory-director" / "skills"
        self.assertEqual(sorted(p.name for p in skills.iterdir()), ["close-goal", "cycle-procedures", "delivery", "owner-decisions", "restriction-check", "utilization"])
        texts = {}
        for d in skills.iterdir():
            text = (d / "SKILL.md").read_text()
            texts[d.name] = text
            m = re.match(r"---\n(.*?)\n---\n", text, re.S)
            self.assertTrue(m, d.name)
            fm = m.group(1)
            self.assertEqual(re.search(r"^name:\s*(\S+)\s*$", fm, re.M).group(1), d.name)
            desc = re.search(r"^description:\s*(.+)$", fm, re.M).group(1)
            self.assertIn("Use when", desc, d.name)
            self.assertLess(len(text.splitlines()), 70, d.name)
        od = texts["owner-decisions"]
        for needle in ("M833", "M407", "third-party code", "security changes", "new recurring cost",
                       "irreversible", "how the owner works", "2 daytime hours", "NEVER defaulted"):
            self.assertIn(needle, od)
        self.assertNotIn("kept open", od)
        for needle in ("M7486", "M5894", "M7462", "recovery action", "R7"):
            self.assertIn(needle, texts["delivery"])
        self.assertIn("MemAvailable is under 1.5 GB", texts["cycle-procedures"])
        self.assertIn("Weekly usage over 75%", texts["cycle-procedures"])
        self.assertIn("The old status-to-done shortcut is refused for you", texts["close-goal"])
        rc = texts["restriction-check"]
        for needle in ("tick", "judge", "cleaner", "factory-tick", "M733"):
            self.assertIn(needle, rc)


if __name__ == "__main__":
    unittest.main()
