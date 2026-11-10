"""G-038 F-44: the tick offers claude-boss a goal move to cursor while the Claude weekly gate is closed."""
import json
import os
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

from test_util_alerts import AlertCase  # noqa: E402

NOW = {"FACTORY_NOW": "2026-11-10T09:00:00+01:00"}


class CapFallbackOfferTests(AlertCase):
    def setUp(self):
        super().setUp()
        self.usage_file = Path(tempfile.mkdtemp()) / "usage-limits.json"
        self.addCleanup(lambda: self.usage_file.unlink(missing_ok=True))
        p = mock.patch.object(self.mod, "USAGE_FILE", self.usage_file)
        p.start()
        self.addCleanup(p.stop)
        e = self.env
        e.seed_goal()
        e.sql("UPDATE goals SET priority=1, org='claude' WHERE id='G-001'")
        e.seed_task(status="todo", pr_url=None)
        self.set("orgs_enabled", "claude,cursor")
        self.set("cap_fallback_offer", "1")
        self.usage(95)

    def set(self, key, value):
        self.con.execute("INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (key, value))
        self.con.commit()

    def usage(self, seven_day, age_s=0):
        self.usage_file.write_text(json.dumps({"five_hour_pct": 1, "seven_day_pct": seven_day, "updated_at": time.time() - age_s}))

    def tick(self):
        with mock.patch.dict(os.environ, NOW):
            self.mod.cap_fallback_offer_tick(self.con)
        self.con.commit()

    def offers(self):
        return [dict(r) for r in self.con.execute("SELECT recipient, subject, body FROM messages WHERE subject LIKE 'cap:%'")]

    def sql(self, q):
        self.con.execute(q)
        self.con.commit()

    def org(self):
        return self.con.execute("SELECT org FROM goals WHERE id='G-001'").fetchone()["org"]

    def test_flag_off_sends_nothing(self):
        self.set("cap_fallback_offer", "0")
        self.tick()
        self.assertEqual(self.offers(), [])

    def test_flag_on_one_offer_with_exact_command(self):
        self.tick()
        rows = self.offers()
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["recipient"], "boss")  # org_prefix_default=0 in a temp home; claude-boss when prefixed
        self.assertEqual(rows[0]["subject"], "cap: G-001 could move to cursor")
        self.assertIn("factory goal org G-001 cursor", rows[0]["body"])
        self.assertIn("zz-test goal", rows[0]["body"])
        self.assertEqual(self.org(), "claude")

    def test_second_tick_same_window_no_second_offer(self):
        self.tick()
        self.tick()
        self.assertEqual(len(self.offers()), 1)

    def test_new_window_after_usage_dropped_offers_again(self):
        self.tick()
        self.usage(50)
        self.tick()
        self.usage(95)
        self.tick()
        self.assertEqual(len(self.offers()), 2)
        self.assertEqual(self.org(), "claude")

    def test_below_cap_no_offer(self):
        self.usage(50)
        self.tick()
        self.assertEqual(self.offers(), [])

    def test_cursor_not_enabled_no_offer(self):
        self.set("orgs_enabled", "claude")
        self.tick()
        self.assertEqual(self.offers(), [])

    def test_cursor_limited_no_offer(self):
        self.set("cursor_model_limits", json.dumps({"composer-2.5": {"status": "limited", "reset": "2026-11-12"}}))
        self.tick()
        self.assertEqual(self.offers(), [])
        self.assertEqual(self.org(), "claude")

    def test_stale_usage_no_offer(self):
        self.usage(95, age_s=4 * 3600)
        self.tick()
        self.assertEqual(self.offers(), [])

    def test_unreadable_usage_no_offer(self):
        self.usage_file.write_text("{not json")
        self.tick()
        self.assertEqual(self.offers(), [])

    def test_ineligible_goals_skipped(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001")
        self.con.commit()
        self.tick()
        self.assertEqual(self.offers(), [], "live agent")
        self.sql("UPDATE agents SET status='exited'")
        self.sql("UPDATE goals SET priority=3")
        self.tick()
        self.assertEqual(self.offers(), [], "priority 3")
        self.sql("UPDATE goals SET priority=1, org='cursor'")
        self.tick()
        self.assertEqual(self.offers(), [], "already cursor")
        self.sql("UPDATE goals SET org='claude'")
        self.sql("UPDATE tasks SET status='done'")
        self.tick()
        self.assertEqual(self.offers(), [], "no todo task")

    def test_real_tick_runs_the_hook(self):
        e = self.env
        e.run("config", "orgs_enabled", "claude,cursor")
        e.run("config", "cap_fallback_offer", "1")
        e.write_usage(seven_day=95)
        p = e.run("tick", extra_env=NOW)
        self.assertEqual(p.returncode, 0, p.stderr)
        n = e.sql("SELECT count(*) c FROM messages WHERE subject='cap: G-001 could move to cursor'")[0]["c"]
        self.assertEqual(n, 1)


if __name__ == "__main__":
    unittest.main()
