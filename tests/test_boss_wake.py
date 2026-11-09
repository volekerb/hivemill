"""T-1199 (G-036 SC4): tick step 3 wakes the boss pane on stale unread escalation/request mail."""
import datetime as dt
import json
import os
import sys
import unittest
from unittest import mock

from harness import FACTORY, FactoryTestCase
from test_auto_clear import PERM, WARN

T0 = dt.datetime(2026, 6, 1, 12, 0, 0, tzinfo=dt.timezone.utc)
BOSS = "boss"
PANE = "p-boss"


class BossWakeBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_agent(BOSS, "boss", pane_id=PANE)
        e.seed_agent("zz-director", "director", pane_id="p-dir")
        self.herdr_idle()
        self.ok("config", "boss_wake_after_s", "900")
        self.ok("config", "boss_wake_cooldown_s", "1800")
        self.env.sql("UPDATE agents SET last_nudge=NULL WHERE name=?", (BOSS,))

    def herdr_idle(self, status="idle"):
        self.env.set_herdr(agents=[{"name": BOSS, "pane_id": PANE, "agent_status": status,
                                    "agent_session": {"value": "s-boss"}}],
                           read_text={BOSS: ""})

    def tick(self, when=None):
        extra = {"FACTORY_NOW": (when or T0).isoformat()}
        self.ok("tick", extra_env=extra)

    def mail(self, kind="escalation", age_min=16, to=BOSS):
        created = (T0 - dt.timedelta(minutes=age_min)).isoformat()
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x',?,?,?,?,?)",
                     (to, kind, "subj", "", created))
        return self.env.sql("SELECT max(id) i FROM messages")[0]["i"]

    def prompts(self):
        return [c["argv"] for c in self.env.calls("herdr")
                if c["argv"][:2] == ["agent", "prompt"] and len(c["argv"]) > 2 and c["argv"][2] == BOSS]

    def send_keys(self):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "send-keys"]]

    def wake_events(self):
        return self.env.sql("SELECT detail, action FROM events WHERE entity='agent' AND entity_id=? AND action='boss-wake'",
                            (BOSS,))

    def badge(self):
        meta = [c["argv"] for c in self.env.calls("herdr")
                if tuple(c["argv"][:2]) == ("pane", "report-metadata") and len(c["argv"]) > 2 and c["argv"][2] == PANE]
        self.assertTrue(meta, "expected sidebar badge call")
        return meta[-1][meta[-1].index("--display-agent") + 1]

    def last_nudge(self):
        return self.env.sql("SELECT last_nudge FROM agents WHERE name=?", (BOSS,))[0]["last_nudge"]


class BossWakeTests(BossWakeBase):
    def test_old_escalation_wakes_once(self):
        mid = self.mail("escalation", 16)
        self.tick()
        self.assertEqual(len(self.prompts()), 1)
        self.assertIn("oldest 16 min", self.prompts()[0][3])
        self.assertIn("[escalation]", self.prompts()[0][3])
        ev = self.wake_events()
        self.assertEqual(len(ev), 1)
        detail = json.loads(ev[0]["detail"])
        self.assertEqual(detail, {"count": 1, "oldest": f"M{mid}", "kinds": "escalation"})
        self.assertIsNotNone(self.last_nudge())

    def test_young_escalation_no_wake(self):
        self.mail("escalation", 10)
        self.tick()
        self.assertEqual(self.prompts(), [])
        self.assertEqual(self.wake_events(), [])

    def test_non_escalation_kinds_no_wake(self):
        for kind in ("report", "alert", "decision"):
            with self.subTest(kind=kind):
                self.env.sql("DELETE FROM messages")
                self.mail(kind, 120)
                self.tick()
                self.assertEqual(self.prompts(), [], kind)

    def test_unsent_input_blocks_wake(self):
        self.mail("escalation", 16)
        self.env.set_herdr(read_text={BOSS: "❯ owner draft here\n"})
        self.tick()
        self.assertEqual(self.prompts(), [])

    def test_busy_pane_no_wake(self):
        self.mail("escalation", 16)
        self.herdr_idle(status="blocked")
        self.tick()
        self.assertEqual(self.prompts(), [])

    def test_cooldown_blocks_second_wake(self):
        self.mail("escalation", 16)
        self.tick()
        n_after_first = len(self.prompts())
        self.assertEqual(n_after_first, 1)
        self.mail("request", 20)
        self.tick(T0 + dt.timedelta(minutes=5))
        self.assertEqual(len(self.prompts()), n_after_first)

    def test_sidebar_badge_with_mail(self):
        self.mail("escalation", 16)
        self.tick()
        self.assertIn("📬1", self.badge())  # label prefix is " 📬1"

    def test_old_request_wakes(self):
        self.mail("request", 16)
        self.tick()
        self.assertEqual(len(self.prompts()), 1)
        self.assertIn("[request]", self.prompts()[0][3])

    def test_permission_dialog_no_wake(self):
        self.env.sql("UPDATE agents SET spawn_args=? WHERE name=?", ('{"runtime":"cursor"}', BOSS))
        self.mail("request", 16)
        self.env.set_herdr(agents=[{"name": BOSS, "pane_id": PANE, "agent_status": "idle",
                                    "agent_session": {"value": "s-boss"}}],
                           read_text={BOSS: PERM})
        self.tick()
        self.assertEqual(self.prompts(), [])
        self.assertEqual(self.send_keys(), [])
        self.assertEqual(self.wake_events(), [])

    def test_settings_warning_no_wake(self):
        self.env.sql("UPDATE agents SET spawn_args=? WHERE name=?", ('{"runtime":"cursor"}', BOSS))
        self.mail("escalation", 16)
        self.env.set_herdr(agents=[{"name": BOSS, "pane_id": PANE, "agent_status": "idle",
                                    "agent_session": {"value": "s-boss"}}],
                           read_text={BOSS: WARN})
        self.tick()
        self.assertEqual(self.prompts(), [])
        self.assertEqual(self.send_keys(), [])
        self.assertEqual(self.wake_events(), [])


class BossWakeUnreadable(FactoryTestCase):
    INPROC = True

    def setUp(self):
        super().setUp()
        self.env.seed_agent(BOSS, "boss", pane_id=PANE)
        self.env.set_herdr(agents=[{"name": BOSS, "pane_id": PANE, "agent_status": "idle",
                                    "agent_session": {"value": "s-boss"}}])
        self.ok("config", "boss_wake_after_s", "900")
        created = (T0 - dt.timedelta(minutes=16)).isoformat()
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x','boss','escalation','s','',?)",
                     (created,))

    def test_unreadable_pane_no_wake(self):
        with self.env.load_factory() as mod:
            real = mod.herdr

            def herdr(*args, check=True, timeout=mod.HERDR_TIMEOUT_S):
                if len(args) >= 2 and args[:2] == ("agent", "read"):
                    return None
                return real(*args, check=check, timeout=timeout)

            argv = [str(FACTORY), "tick"]
            os.environ["FACTORY_NOW"] = T0.isoformat()
            saved = os.environ.get("FACTORY_AGENT")
            os.environ["FACTORY_AGENT"] = "tick"
            with mock.patch.object(mod, "herdr", side_effect=herdr):
                old = sys.argv
                sys.argv = argv
                try:
                    mod.main()
                finally:
                    sys.argv = old
                    if saved is None:
                        os.environ.pop("FACTORY_AGENT", None)
                    else:
                        os.environ["FACTORY_AGENT"] = saved
        prompts = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "prompt"]]
        self.assertEqual(prompts, [])


if __name__ == "__main__":
    unittest.main()
