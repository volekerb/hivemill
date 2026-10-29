"""T-0733 (G-016): config lead_wake_debounce_s, the mail-wake debounce of parked leads (bypass kinds/senders, batching)."""
import datetime as dt
import json
import unittest

from harness import FactoryTestCase

LEAD = "lead-g-001-a"
TEAM = "g-001-a"
PARKED = "2026-01-01T00:00:00+00:00"
T0 = dt.datetime(2026, 6, 1, 12, 0, 0, tzinfo=dt.timezone.utc)


class DebounceBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(TEAM, lead=LEAD)
        e.seed_agent(LEAD, "lead", goal="G-001", team=TEAM, status="exited")
        e.seed_agent("zz-director", "director")
        e.seed_agent("zz-boss", "boss")
        self.repark()
        self.ok("config", "lead_parking", "1")
        self.ok("config", "lead_wake_debounce_s", "600")

    def repark(self):
        self.env.sql("UPDATE agents SET status='exited' WHERE name=?", (LEAD,))
        self.env.sql("UPDATE teams SET parked_at=?, resume_note='n' WHERE id=?", (PARKED, TEAM))

    def tick_at(self, secs):
        """Park the lead again and tick with the clock at T0 + secs."""
        self.repark()
        now = (T0 + dt.timedelta(seconds=secs)).isoformat()
        self.ok("tick", extra_env={"FACTORY_NOW": now})

    def mail(self, sender="zz-director", kind="request"):
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES(?,?,?,'q','b','2026-01-01T00:00:00')",
                     (sender, LEAD, kind))
        return self.env.sql("SELECT max(id) i FROM messages")[0]["i"]

    def events(self, action):
        return [json.loads(r["detail"]) for r in self.env.sql(
            "SELECT detail FROM events WHERE entity='team' AND entity_id=? AND action=? ORDER BY id", (TEAM, action))]

    def first_wake(self):
        self.mail()
        self.tick_at(0)
        self.assertEqual(len(self.events("lead-wake")), 1)


class DebounceTests(DebounceBase):
    def test_batches_mail_into_one_wake_after_window(self):
        self.first_wake()
        first = self.mail()
        self.tick_at(60)
        self.mail()
        self.tick_at(300)
        self.assertEqual(len(self.events("lead-wake")), 1)
        self.tick_at(601)
        wakes = self.events("lead-wake")
        self.assertEqual(len(wakes), 2)
        self.assertEqual(wakes[1]["trigger"], f"M{self.env.sql('SELECT min(id) i FROM messages WHERE read_at IS NULL')[0]['i']}")
        kicks = [r["body"] for r in self.env.sql("SELECT body FROM messages WHERE recipient=? AND sender='factory' ORDER BY id", (LEAD,))]
        # unread mail at the wake: the first request, the two held back, and the first wake's kickoff
        self.assertIn("unread: 4", kicks[-1])  # all pending mail covered by this one wake
        self.assertGreater(first, 0)

    def test_bypass(self):
        cases = [("zz-director", "escalation"), ("zz-director", "decision"), ("owner", "request"), ("zz-boss", "report")]
        for sender, kind in cases:
            with self.subTest(sender=sender, kind=kind):
                self.env.sql("DELETE FROM messages")
                self.env.sql("DELETE FROM events")
                self.env.sql("DELETE FROM throttle")
                self.first_wake()
                self.mail(sender, kind)
                self.tick_at(130)  # inside the debounce window and past the 120 s wake throttle
                self.assertEqual(len(self.events("lead-wake")), 2)

    def test_plain_mail_is_debounced_without_bypass(self):
        self.first_wake()
        self.mail("zz-director", "report")
        self.tick_at(130)
        self.assertEqual(len(self.events("lead-wake")), 1)

    def test_event_once_per_window(self):
        self.first_wake()
        self.mail()
        for s in (130, 200, 300):
            self.tick_at(s)
        self.assertEqual(len(self.events("wake-debounced")), 1)
        self.tick_at(601)
        self.mail()
        for s in (800, 900):
            self.tick_at(s)
        self.assertEqual(len(self.events("wake-debounced")), 2)

    def test_wake_throttle_still_applies_with_debounce_on(self):
        self.first_wake()
        self.env.sql("UPDATE throttle SET ts='-100000' WHERE key LIKE 'wakedb:%'")  # window long over
        self.mail()
        self.tick_at(60)  # past the window but inside the 120 s throttle
        self.assertEqual(len(self.events("lead-wake")), 1)
        self.assertEqual(self.events("wake-debounced"), [])

    def test_flag_off_drain_unaffected(self):
        self.ok("config", "lead_parking", "0")
        self.tick_at(0)
        self.assertEqual([w["trigger"] for w in self.events("lead-wake")], ["flag-off"])
        self.env.sql("UPDATE messages SET read_at='2026-01-01T00:00:00'")
        self.tick_at(130)  # no unread mail: the drain wake is not debounced
        self.assertEqual([w["trigger"] for w in self.events("lead-wake")], ["flag-off", "flag-off"])
        self.assertEqual(self.events("wake-debounced"), [])

    def test_non_finite_window_means_off(self):
        for bad in ("inf", "nan"):
            with self.subTest(bad=bad):
                self.env.sql("DELETE FROM messages")
                self.env.sql("DELETE FROM events")
                self.env.sql("DELETE FROM throttle")
                self.ok("config", "lead_wake_debounce_s", bad)
                self.first_wake()
                self.mail()
                self.tick_at(130)
                self.assertEqual(len(self.events("lead-wake")), 2)

    def test_off_by_default_wakes_every_throttle_window(self):
        self.ok("config", "lead_wake_debounce_s", "0")
        self.first_wake()
        self.mail()
        self.tick_at(130)
        self.assertEqual(len(self.events("lead-wake")), 2)
        self.assertEqual(self.events("wake-debounced"), [])


if __name__ == "__main__":
    unittest.main()
