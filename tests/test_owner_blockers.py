"""G-018 T-0609: owner-only blockers raised outside daytime ping the boss at once (tick) and show in `factory res`."""
import contextlib
import io
import os
import sys
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_util_alerts import AlertCase  # noqa: E402

OPEN = (True, "", {})
TMP_FULL = (False, "/tmp 9000/10000 MB used (90%) ≥ 80%", {})
LOAD = (False, "load1 9.0 > 1.5 x 8 CPUs", {})


class OwnerBlockers(AlertCase):
    def esc(self, subject, created, to="boss", kind="escalation"):
        return self.con.execute("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('lead-x',?,?,?,'b',?)",
                                (to, kind, subject, created)).lastrowid

    def tick(self, when, gate=OPEN):
        with mock.patch.dict(os.environ, {"FACTORY_NOW": when}), mock.patch.object(self.mod, "gate", return_value=gate):
            self.mod.owner_blocker_alerts(self.con)

    def boss(self):
        return self.msgs("boss", "OWNER-ONLY BLOCKER%")

    def test_night_owner_action_escalation_pings_once(self):
        mid = self.esc("OWNER ACTION: approve C5", "2026-10-29T00:12:00+01:00")
        self.tick("2026-10-29T00:13:00")
        b = self.boss()
        self.assertEqual(len(b), 1)
        self.assertEqual((b[0]["kind"], b[0]["sender"]), ("escalation", "tick"))
        self.assertIn(f"M{mid}", b[0]["subject"])
        for t in ("2026-10-29T00:14:00", "2026-10-29T03:00:00"):
            self.tick(t)
        self.assertEqual(len(self.boss()), 1)  # suppressed per blocker

    def test_second_blocker_gets_its_own_ping(self):
        self.esc("owner action: a", "2026-10-29T00:12:00+01:00")
        self.tick("2026-10-29T00:13:00")
        self.esc("owner action: b", "2026-10-29T01:37:00+01:00")
        self.tick("2026-10-29T01:38:00")
        self.assertEqual(len(self.boss()), 2)

    def test_daytime_blocker_not_pinged(self):
        self.esc("owner action: a", "2026-10-29T12:00:00+01:00")
        self.tick("2026-10-29T12:01:00")
        self.assertEqual(self.boss(), [])

    def test_unmarked_or_other_recipient_not_pinged(self):
        self.esc("plain escalation", "2026-10-29T00:12:00+01:00")
        self.esc("owner action: x", "2026-10-29T00:12:00+01:00", to="director")
        self.esc("owner action: x", "2026-10-29T00:12:00+01:00", kind="msg")
        self.tick("2026-10-29T00:13:00")
        self.assertEqual(self.boss(), [])

    def test_day_hours_config_is_used(self):
        self.con.execute("INSERT INTO config(key,value) VALUES('day_end_hour','23') ON CONFLICT(key) DO UPDATE SET value='23'")
        self.esc("owner action: a", "2026-10-29T22:10:00+01:00")
        self.tick("2026-10-29T22:11:00")
        self.assertEqual(self.boss(), [])

    def test_tmp_gate_closed_at_night_pings_then_clears(self):
        self.tick("2026-10-29T01:37:00", TMP_FULL)
        b = self.boss()
        self.assertEqual(len(b), 1)
        self.assertIn("/tmp", b[0]["subject"])
        self.tick("2026-10-29T01:50:00", TMP_FULL)
        self.assertEqual(len(self.boss()), 1)
        self.tick("2026-10-29T02:00:00", OPEN)  # cleared; a new closure is a new blocker
        self.tick("2026-10-29T02:10:00", TMP_FULL)
        self.assertEqual(len(self.boss()), 2)

    def test_non_owner_gate_cause_not_pinged(self):
        self.tick("2026-10-29T01:37:00", LOAD)
        self.assertEqual(self.boss(), [])

    def test_res_lists_blockers_with_age(self):
        self.esc("OWNER ACTION: approve C5", "2026-10-29T00:12:00+01:00")
        out = io.StringIO()
        with mock.patch.dict(os.environ, {"FACTORY_NOW": "2026-10-29T02:12:00+01:00"}), \
                mock.patch.object(self.mod, "gate", return_value=(False, "/tmp 9000/10000 MB used (90%) ≥ 80%", {})), \
                mock.patch.object(self.mod, "db", return_value=self.con), contextlib.redirect_stdout(out):
            blockers = self.mod.owner_blockers(self.con)
        texts = [(b["text"], b["age_min"]) for b in blockers]
        self.assertIn(("M1 OWNER ACTION: approve C5", 120), texts)
        self.assertTrue(any(t.startswith("spawn gate closed: /tmp") and a == 0 for t, a in texts), texts)

    def test_read_escalation_is_no_longer_open(self):
        mid = self.esc("OWNER ACTION: a", "2026-10-29T00:12:00+01:00")
        self.con.execute("UPDATE messages SET read_at='2026-10-29T00:20:00+01:00' WHERE id=?", (mid,))
        with mock.patch.dict(os.environ, {"FACTORY_NOW": "2026-10-29T02:12:00+01:00"}), \
                mock.patch.object(self.mod, "gate", return_value=OPEN):
            self.assertEqual(self.mod.owner_blockers(self.con), [])


if __name__ == "__main__":
    unittest.main()
