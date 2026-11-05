"""G-015 T-1111: the dark needs-you feed (owner_alert_feed table, tick step, `factory alerts`)."""
import json
import os
import sys
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_util_alerts import AlertCase  # noqa: E402

MARK = "LEAKMARKER"
KEYS = {"id", "org", "kind", "ref", "since", "link"}
NOW = {"FACTORY_NOW": "2026-11-05T12:00:00"}
TMP_FULL = "10485760000,1048576000"  # 90% used: opens the tmp-gate item


class FeedCase(AlertCase):
    def setUp(self):
        super().setUp()
        self.con.execute("INSERT INTO goals(id,title,status,created,updated,org) VALUES('G-1',?,?,?,?,?)",
                         (MARK + " goal", "active", "t", "2026-11-01T10:00:00+01:00", "claude"))

    def task(self, tid, status="todo", waits="internal", title=MARK):
        self.con.execute("INSERT INTO tasks(id,goal_id,title,status,waits,created,updated) VALUES(?,?,?,?,?,?,?)",
                         (tid, "G-1", title, status, waits, "t", "2026-11-01T11:00:00+01:00"))

    def msg(self, subject=MARK, body=MARK):
        return self.con.execute("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,created) "
                                "VALUES('lead-x','boss','escalation',?,?,'G-1','2026-11-02T09:00:00+01:00')",
                                (subject + " owner action", body)).lastrowid

    def seed_all_kinds(self):
        self.task("T-1", status="blocked", waits="owner")
        self.task("T-2", status="in_review")
        self.con.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                         ("2026-11-03T08:00:00+01:00", "x", "task", "T-2", "judge-blocked", "auto_merge=0"))
        self.msg()
        self.con.execute("INSERT INTO goals(id,title,status,created,updated,org) VALUES('G-2',?,?,?,?,?)",
                         (MARK, "blocked", "t", "2026-11-01T10:00:00+01:00", "claude"))
        self.con.commit()

    def tick(self, tmp=None):
        env = dict(NOW)
        if tmp:
            env["FACTORY_TMP_STATVFS"] = tmp
        with mock.patch.dict(os.environ, env):
            self.mod.owner_alert_feed_tick(self.con)
        self.con.commit()

    def on(self):
        self.con.execute("INSERT OR REPLACE INTO config(key,value) VALUES('owner_alert_feed','1')")
        self.con.commit()

    def count(self, table):
        return self.con.execute(f"SELECT count(*) FROM {table}").fetchone()[0]

    def cli(self, *args):
        self.con.commit()
        p = self.env.run(*args, inproc=False)
        self.assertEqual(p.returncode, 0, f"{args}\n{p.stdout}\n{p.stderr}")
        return p.stdout


class Feed(FeedCase):
    def test_row_shape_five_kinds_through_cli(self):
        self.on()
        self.seed_all_kinds()
        self.tick(tmp=TMP_FULL)
        rows = json.loads(self.cli("alerts", "--json"))
        self.assertEqual({r["kind"] for r in rows},
                         {"task-owner-wait", "judge-hold", "escalation", "tmp-gate", "goal-blocked"})
        for r in rows:
            self.assertEqual(set(r), KEYS)
        self.assertEqual([r["id"] for r in rows], sorted((r["id"] for r in rows), reverse=True))  # newest first
        self.assertIn("KIND", self.cli("alerts"))  # table output has the six columns
        self.assertEqual(self.cli("alerts").splitlines()[0].split(), [k.upper() for k in ("id", "org", "kind", "ref", "since", "link")])

    def test_no_text_leak_json_and_table(self):
        self.on()
        self.seed_all_kinds()
        self.tick(tmp=TMP_FULL)
        self.assertEqual(self.cli("alerts", "--json").count(MARK), 0)
        out = self.cli("alerts")
        self.assertIn("task-owner-wait", out)
        self.assertEqual(out.count(MARK), 0)

    def test_dedupe_close_and_return(self):
        self.on()
        self.task("T-1", status="blocked", waits="owner")
        self.con.commit()
        self.tick()
        self.tick()
        rows = json.loads(self.cli("alerts", "--json"))
        self.assertEqual(len(rows), 1)
        first = rows[0]["id"]
        self.con.execute("UPDATE tasks SET status='in_progress' WHERE id='T-1'")
        self.tick()
        self.assertEqual(json.loads(self.cli("alerts", "--json")), [])
        self.con.execute("UPDATE tasks SET status='blocked' WHERE id='T-1'")
        self.tick()
        rows = json.loads(self.cli("alerts", "--json"))
        self.assertEqual(len(rows), 1)
        self.assertGreater(rows[0]["id"], first)

    def test_alerts_is_read_only(self):
        self.on()
        self.seed_all_kinds()
        self.tick()
        tables = ("messages", "events", "throttle", "owner_alert_feed")
        before = {t: self.count(t) for t in tables}
        self.cli("alerts", "--json")
        self.cli("alerts")
        self.assertEqual({t: self.count(t) for t in tables}, before)

    def test_tick_sends_nothing(self):
        self.on()
        self.seed_all_kinds()
        before = self.count("messages")
        with mock.patch.object(self.mod, "send") as send, mock.patch.object(self.mod, "herdr") as herdr:
            self.tick()
            self.tick()
        self.assertEqual(self.count("messages"), before)
        send.assert_not_called()
        herdr.assert_not_called()
        self.assertGreater(self.count("owner_alert_feed"), 0)

    def test_flag_off_default_writes_nothing_and_skips_selector(self):
        self.seed_all_kinds()
        self.assertEqual(self.mod.cfg(self.con, "owner_alert_feed"), "0")
        with mock.patch.object(self.mod, "owner_attention") as oa:
            self.tick()
        oa.assert_not_called()
        self.assertEqual(self.count("owner_alert_feed"), 0)

    def test_flag_registered(self):
        flags = json.loads((ROOT / "bin" / "flags.json").read_text())["keys"]
        e = [k for k in flags if k["key"] == "owner_alert_feed"]
        self.assertEqual(len(e), 1)
        self.assertEqual((e[0]["kind"], e[0]["default"]), ("flag", self.mod.DEFAULT_CONFIG["owner_alert_feed"]))

    def test_tick_failure_is_one_throttled_event(self):
        self.on()
        with mock.patch.object(self.mod, "owner_attention", side_effect=RuntimeError("boom")):
            self.tick()
            self.tick()
        ev = self.con.execute("SELECT detail FROM events WHERE action='owner-alert-feed-failed'").fetchall()
        self.assertEqual(len(ev), 1)
        self.assertIn("boom", ev[0]["detail"])


if __name__ == "__main__":
    unittest.main()
