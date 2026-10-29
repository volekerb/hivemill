"""G-014 on-demand leads 4/5: workers live vs leads live/parked in res, scorecard, digest and the web views."""
import datetime as dt
import json
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from harness import FactoryTestCase  # noqa: E402
from web import db as wdb  # noqa: E402
from web.views import agents as web_agents  # noqa: E402
from web.views import resources  # noqa: E402

FROZEN = dt.datetime(2026, 10, 24, 12, 0).astimezone()
FROZEN_ENV = {"FACTORY_NOW": FROZEN.isoformat()}
PARKED_AT = "2026-10-24T09:30:00+02:00"


class LeadVisibilityBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal(title="zz-test goal")
        e.seed_team("g-001-a", lead="zz-test-lead-a")      # live lead
        e.seed_team("g-001-b", lead=None)                  # parked
        e.seed_team("g-001-c", lead="zz-test-lead-c")      # lead queued for a wake
        e.seed_team("g-001-d", lead=None)                  # no lead
        e.seed_agent("zz-test-lead-a", "lead", goal="G-001", team="g-001-a")
        e.seed_agent("zz-test-lead-x", "lead", goal="G-001", team="g-001-a")
        e.seed_agent("zz-test-lead-c", "lead", goal="G-001", team="g-001-c", status="queued")
        for i, role in enumerate(("coder", "qa", "reviewer")):
            e.seed_agent(f"zz-test-w{i}", role, goal="G-001", team="g-001-a")
        e.sql("UPDATE teams SET parked_at=?, resume_note='n' WHERE id='g-001-b'", (PARKED_AT,))
        self.ok("config", "max_live_agents", "18")
        self.ok("config", "lead_parking", "1")


class ResTests(LeadVisibilityBase):
    def test_res_line_counts(self):
        out = self.ok("res").stdout
        self.assertIn("workers live 3 · leads live 2/4 (parked 1, wake-queued 1) · hq ", out)
        self.assertIn("(reserved for workers 10)", out)
        self.assertNotIn("lead parking off", out)

    def test_res_line_follows_agents_line(self):
        lines = self.ok("res").stdout.splitlines()
        i = next(n for n, l in enumerate(lines) if l.startswith("agents live "))
        self.assertTrue(lines[i + 1].startswith("workers live "), lines[i + 1])

    def test_res_line_parking_off(self):
        self.ok("config", "lead_parking", "0")
        line = next(l for l in self.ok("res").stdout.splitlines() if l.startswith("workers live "))
        self.assertTrue(line.endswith(" · lead parking off"), line)


class ScorecardTests(LeadVisibilityBase):
    def test_lead_column(self):
        rows = {}
        table = self.ok("scorecard").stdout.split("\n\n")[0]  # the director line follows the table after a blank line
        for line in table.splitlines()[1:]:
            parts = line.split()
            rows[parts[0]] = parts[-1]
        self.assertEqual(rows, {"g-001-a": "live", "g-001-b": "parked", "g-001-c": "queued", "g-001-d": "-"})

    def test_header_ends_with_lead(self):
        self.assertTrue(self.ok("scorecard").stdout.splitlines()[0].split()[-1] == "LEAD")


class DigestTests(LeadVisibilityBase):
    def wake(self, team, hours_ago):
        ts = (FROZEN - dt.timedelta(hours=hours_ago)).isoformat(timespec="seconds")
        self.env.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                     (ts, "tick", "team", team, "lead-wake", "mail"))

    def test_leads_line_and_parked_list(self):
        self.wake("g-001-b", 2)
        self.wake("g-001-b", 5)
        self.wake("g-001-b", 48)   # outside the 24h window
        self.wake("g-001-a", 1)    # another team
        out = self.ok("digest", extra_env=FROZEN_ENV).stdout
        self.assertIn("Leads: 2 live, 1 parked, 1 queued; workers live 3", out)
        self.assertIn("- g-001-b parked since 2026-10-24T09:30 (2 wakes in window)", out)

    def test_no_parked_list_when_none_parked(self):
        self.env.sql("UPDATE teams SET parked_at=NULL")
        out = self.ok("digest", extra_env=FROZEN_ENV).stdout
        self.assertIn("Leads: 2 live, 0 parked, 1 queued; workers live 3", out)
        self.assertNotIn(" parked since ", out)


class WebTests(LeadVisibilityBase):
    def ctx(self):
        con = wdb.connect(self.env.db_path)
        self.addCleanup(con.close)
        return SimpleNamespace(db=con, now=FROZEN, usage_fn=None, mem_fn=None, tokens=None, defaults={},
                               factory_home=None, query={})

    def test_gate_check_matches_cli(self):
        self.assertIn("workers live 3 · leads live 2/4 (parked 1)", resources.derive_gate(self.ctx())["checks"])
        self.assertIn("workers live 3 · leads live 2/4 (parked 1, wake-queued 1)", self.ok("res").stdout)

    def test_gate_check_uses_config_cap(self):
        self.ok("config", "max_live_leads", "6")
        self.assertIn("workers live 3 · leads live 2/6 (parked 1)", resources.derive_gate(self.ctx())["checks"])

    def test_agents_page_lead_state(self):
        c = self.ctx()
        d = web_agents.data(c)
        json.dumps(d)
        self.assertEqual({t["id"]: t["lead_state"] for t in d["teams"]},
                         {"g-001-a": "live", "g-001-b": "parked", "g-001-c": "queued", "g-001-d": "-"})
        html = str(web_agents.render(c, d))
        self.assertIn("Parked", html)   # B3: the lead state shows the human label
        self.assertIn('st fg-neutral', html)   # B3: status() replaces the muted badge


if __name__ == "__main__":
    unittest.main()
