"""The lead's fix-loop skill keeps its staleness step (G-013 B)."""
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FIX_LOOP = ROOT / "roles/plugins/lead/factory-lead/skills/fix-loop/SKILL.md"
DOC = ROOT / "docs/staleness.md"


class StalenessRules(unittest.TestCase):
    def test_fix_loop_markers(self):
        text = FIX_LOOP.read_text()
        for marker in ("merge-only", "not an attempt", "factory refresh T", "git pull",
                       "delta re-verdict", "STALE-BASE", "CONFLICT-ONLY"):
            self.assertIn(marker, text, f"fix-loop/SKILL.md: missing {marker!r}")

    def test_doc_links_and_argument(self):
        text = DOC.read_text()
        for marker in ("docs/refresh.md", "docs/verdict-carry-over.md", "E2276", "STALE-ONLY",
                       "cmd_judge", "not weakened"):
            self.assertIn(marker, text, f"docs/staleness.md: missing {marker!r}")


if __name__ == "__main__":
    unittest.main()
