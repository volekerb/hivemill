"""G-018 T-0609: owner-only blockers raised outside daytime ping the boss at once (tick) and show in `factory res`."""
import argparse
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
TMP_FULL = "10485760000,1048576000"  # 90% used
TMP_OK = "10485760000,8388608000"    # 20% used
PANES = (False, "8/8 agent panes live", {})
LOAD = (False, "load1 9.0 > 1.5 x 8 CPUs", {})


class OwnerBlockers(AlertCase):
    def esc(self, subject, created, to="boss", kind="escalation"):
        return self.con.execute("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('lead-x',?,?,?,'b',?)",
                                (to, kind, subject, created)).lastrowid

    def tick(self, when, tmp=None, gate=OPEN):
        env = {"FACTORY_NOW": when}
        if tmp:
            env["FACTORY_TMP_STATVFS"] = tmp
        with mock.patch.dict(os.environ, env), mock.patch.object(self.mod, "gate", return_value=gate):
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
        self.tick("2026-10-29T02:00:00", TMP_OK)  # cleared; a new closure is a new blocker
        self.tick("2026-10-29T02:10:00", TMP_FULL)
        self.assertEqual(len(self.boss()), 2)

    def test_non_owner_gate_cause_not_pinged(self):
        self.tick("2026-10-29T01:37:00", TMP_OK, LOAD)
        self.assertEqual(self.boss(), [])

    def test_other_gate_cause_does_not_rearm_tmp_blocker(self):
        self.tick("2026-10-29T01:00:00", TMP_FULL)
        self.tick("2026-10-29T01:01:00", TMP_FULL, PANES)  # pane cap masks /tmp in gate() for one tick
        self.tick("2026-10-29T01:02:00", TMP_FULL)
        self.assertEqual(len(self.boss()), 1)

    def test_res_lists_blockers_with_age(self):
        self.esc("OWNER ACTION: approve C5", "2026-10-29T00:12:00+01:00")
        out = io.StringIO()
        with mock.patch.dict(os.environ, {"FACTORY_NOW": "2026-10-29T02:12:00+01:00", "FACTORY_TMP_STATVFS": TMP_FULL}), \
                contextlib.redirect_stdout(out):
            self.mod.cmd_res(argparse.Namespace())
        text = out.getvalue()
        self.assertIn("owner-only blockers: 2", text)
        self.assertIn("M1 OWNER ACTION: approve C5 (open 120 min)", text)
        self.assertIn("spawn gate closed: /tmp", text)

    def test_read_escalation_stays_open_until_decision_quotes_it(self):
        mid = self.esc("OWNER ACTION: a", "2026-10-29T00:12:00+01:00")
        self.con.execute("UPDATE messages SET read_at='2026-10-29T00:20:00+01:00' WHERE id=?", (mid,))

        def open_now():
            with mock.patch.dict(os.environ, {"FACTORY_NOW": "2026-10-29T02:12:00+01:00", "FACTORY_TMP_STATVFS": TMP_OK}):
                return self.mod.owner_blockers(self.con)
        self.assertEqual([b["text"] for b in open_now()], [f"M{mid} OWNER ACTION: a"])  # read, owner not yet acted
        self.con.execute("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('boss','director','decision',?,'x',?)",
                         (f"owner answered M{mid}0", "2026-10-29T02:00:00+01:00"))  # M{id}0 is another message: not a match
        self.assertEqual(len(open_now()), 1)
        self.con.execute("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('boss','director','decision',?,'x',?)",
                         (f"owner answered M{mid}: approved", "2026-10-29T02:05:00+01:00"))
        self.assertEqual(open_now(), [])


if __name__ == "__main__":
    unittest.main()
