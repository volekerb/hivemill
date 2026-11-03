"""T-0859 (G-028): factory res prints per-org agent mix (same buckets as Resources org_mix)."""
import re
import unittest

from test_lead_caps import CapBase

_ZERO = "live 0 · workers 0 · leads 0 · hq 0 · queued 0"
_ORG_LINE = re.compile(
    r"^org (?P<org>claude|cursor): live (?P<live>\d+) · workers (?P<workers>\d+) · "
    r"leads (?P<leads>\d+) · hq (?P<hq>\d+) · queued (?P<queued>\d+)$"
)


class ResOrgTests(CapBase):
    def org_lines(self, text=None):
        lines = (text or self.ok("res").stdout).splitlines()
        claude = next(l for l in lines if l.startswith("org claude: "))
        cursor = next(l for l in lines if l.startswith("org cursor: "))
        idx_c, idx_u = lines.index(claude), lines.index(cursor)
        self.assertLess(idx_c, idx_u, lines)
        return claude, cursor

    def parse(self, line):
        m = _ORG_LINE.match(line)
        self.assertIsNotNone(m, line)
        return {k: int(m.group(k)) if k != "org" else m.group(k) for k in ("org", "live", "workers", "leads", "hq", "queued")}

    def test_no_agents_both_orgs_zero_claude_first(self):
        claude, cursor = self.org_lines()
        self.assertEqual(claude, f"org claude: {_ZERO}")
        self.assertEqual(cursor, f"org cursor: {_ZERO}")

    def test_live_cursor_coder(self):
        self.env.seed_agent("cursor-coder-t-1", "coder", goal="G-001")
        claude, cursor = self.org_lines()
        self.assertEqual(claude, f"org claude: {_ZERO}")
        self.assertEqual(self.parse(cursor), {"org": "cursor", "live": 1, "workers": 1, "leads": 0, "hq": 0, "queued": 0})

    def test_live_cursor_boss(self):
        self.env.seed_agent("cursor-boss", "boss")
        _, cursor = self.org_lines()
        self.assertEqual(self.parse(cursor), {"org": "cursor", "live": 1, "workers": 0, "leads": 0, "hq": 1, "queued": 0})

    def test_queued_cursor_lead(self):
        self.env.seed_agent("cursor-lead-g-001-a", "lead", goal="G-001", status="queued")
        _, cursor = self.org_lines()
        self.assertEqual(self.parse(cursor), {"org": "cursor", "live": 0, "workers": 0, "leads": 0, "hq": 0, "queued": 1})

    def test_claude_lead_and_cursor_coder_separate_lines(self):
        self.env.seed_agent("claude-lead-g-001-a", "lead", goal="G-001")
        self.env.seed_agent("cursor-coder-t-1", "coder", goal="G-001")
        claude, cursor = self.org_lines()
        self.assertEqual(self.parse(claude), {"org": "claude", "live": 1, "workers": 0, "leads": 1, "hq": 0, "queued": 0})
        self.assertEqual(self.parse(cursor), {"org": "cursor", "live": 1, "workers": 1, "leads": 0, "hq": 0, "queued": 0})
        self.assertNotIn("cursor", claude)
        self.assertNotIn("claude", cursor.replace("org cursor:", ""))


if __name__ == "__main__":
    unittest.main()
