"""G-015 T-1136 (A3, F-50): the ntfy sender for the needs-you feed, flag off, plus closed-feed pruning.
No test touches the network: the sender is patched, or the urllib layer is. The topic is a fake value in a temp HOME."""
import datetime as dt
import json
import os
import sqlite3
import sys
import unittest
import urllib.error
import urllib.request
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_util_alerts import AlertCase  # noqa: E402

TOPIC = "faketopic_0123456789"  # obvious fake
MARK = "SUBJECTMARKER"
T0 = dt.datetime(2026, 11, 5, 12, 0, 0).astimezone()


def at(seconds=0):
    return (T0 + dt.timedelta(seconds=seconds)).isoformat(timespec="seconds")


class Ok:
    status = 200

    def __enter__(self):
        return self

    def __exit__(self, *a):
        return False


class SendCase(AlertCase):
    def setUp(self):
        super().setUp()
        self.home = self.env.user_home
        p = mock.patch.dict(os.environ, {"HOME": str(self.home)})
        p.start()
        self.addCleanup(p.stop)
        self.topic_path = self.home / ".config" / "factory" / "ntfy-topic"
        self.topic_path.parent.mkdir(parents=True, exist_ok=True)
        self.write_topic(TOPIC)
        self.set("owner_alert_feed", "1")
        self.set("owner_alert_send", "1")
        self.calls = []

    def write_topic(self, text, mode=0o600):
        self.topic_path.unlink(missing_ok=True)
        self.topic_path.write_text(text + "\n")
        os.chmod(self.topic_path, mode)

    def set(self, k, v):
        self.con.execute("INSERT OR REPLACE INTO config(key,value) VALUES(?,?)", (k, v))
        self.con.commit()

    def row(self, kind="task-owner-wait", ref="T-1", closed=None, sent=None, first_seen=None, since="2026-11-01T10:00:00+01:00"):
        cur = self.con.execute(
            "INSERT INTO owner_alert_feed(org,kind,ref,since,link,first_seen,closed,sent) VALUES('claude',?,?,?,?,?,?,?)",
            (kind, ref, since, "factory task show " + ref, first_seen or at(), closed, sent))
        self.con.commit()
        return cur.lastrowid

    def send(self, t=0, post=None):
        """One owner_alert_send_tick at T0+t with the sender patched (default: records the call, returns 200)."""
        def ok(topic, line):
            self.calls.append((topic, line, self.con.in_transaction))
            return 200
        with mock.patch.dict(os.environ, {"FACTORY_NOW": at(t)}), \
                mock.patch.object(self.mod, "_ntfy_post", side_effect=post or ok):
            self.mod.owner_alert_send_tick(self.con)
        self.con.commit()

    def sent(self):
        return {r["id"]: r["sent"] for r in self.con.execute("SELECT id, sent FROM owner_alert_feed")}

    def events(self):
        return [r["action"] for r in self.con.execute("SELECT action FROM events WHERE action LIKE 'owner-alert-send%'")]


class Flags(SendCase):
    def test_default_is_off_and_registry_matches(self):
        self.assertEqual(self.mod.DEFAULT_CONFIG["owner_alert_send"], "0")
        reg = {e["key"]: e for e in json.loads((ROOT / "bin" / "flags.json").read_text())["keys"]}
        self.assertEqual((reg["owner_alert_send"]["default"], reg["owner_alert_send"]["state"]), ("0", "off"))

    def test_either_flag_off_sends_nothing_and_reads_no_topic(self):
        self.row()
        for send, feed in (("0", "1"), ("1", "0"), ("0", "0")):
            self.set("owner_alert_send", send)
            self.set("owner_alert_feed", feed)
            with mock.patch.object(self.mod, "ntfy_topic", side_effect=AssertionError("topic read")):
                self.send()
            self.assertEqual(self.calls, [])
        self.assertEqual(self.sent(), {1: None})


class Sending(SendCase):
    def test_two_ticks_one_send_per_row(self):
        self.row(ref="T-1")
        self.row(kind="goal-blocked", ref="G-2")
        self.send()
        self.send(10)
        self.assertEqual(len(self.calls), 2)
        self.assertTrue(all(v for v in self.sent().values()))

    def test_payload_exact_one_line_without_subject(self):
        self.con.execute("INSERT INTO goals(id,title,status,created,updated,org) VALUES('G-1',?,?,?,?,?)",
                         (MARK, "active", "t", "t", "claude"))
        self.con.execute("INSERT INTO tasks(id,goal_id,title,status,waits,created,updated) VALUES('T-1','G-1',?,'blocked','owner','t','t')",
                         (MARK,))
        rid = self.row(since="2026-11-01T10:00:00+01:00")
        self.send()
        topic, line, _ = self.calls[0]
        self.assertEqual(topic, TOPIC)
        self.assertEqual(line, f"id={rid} org=claude kind=task-owner-wait ref=T-1 since=2026-11-01T10:00:00+01:00 "
                               "link=factory task show T-1")
        self.assertNotIn(MARK, line)
        self.assertNotIn("\n", line)

    def test_request_shape_through_the_urllib_layer(self):
        self.row()
        seen = {}

        def fake_open(opener, req, data=None, timeout=None):
            seen.update(req=req, timeout=timeout)
            return Ok()
        with mock.patch.dict(os.environ, {"FACTORY_NOW": at()}), \
                mock.patch.object(urllib.request.OpenerDirector, "open", fake_open):
            self.mod.owner_alert_send_tick(self.con)
        req = seen["req"]
        self.assertEqual(req.get_method(), "POST")
        self.assertEqual(req.full_url, "https://ntfy.sh/" + TOPIC)
        self.assertEqual(seen["timeout"], 5)
        self.assertEqual(req.data.decode().split(" ")[0], "id=1")
        self.assertEqual({k.lower() for k in req.headers}, {"content-type"})  # no Title, Tags, Click or other ntfy header
        self.assertIsNotNone(self.sent()[1])

    def test_redirect_is_not_followed(self):
        # a 3xx from the server surfaces as HTTPError (no redirect handler), so it is a failure
        self.row()

        def boom(opener, req, data=None, timeout=None):
            raise urllib.error.HTTPError(req.full_url, 302, "Found", {}, None)
        with mock.patch.dict(os.environ, {"FACTORY_NOW": at()}), mock.patch.object(urllib.request.OpenerDirector, "open", boom):
            self.mod.owner_alert_send_tick(self.con)
        self.assertEqual(self.sent(), {1: None})
        det = self.con.execute("SELECT detail FROM events WHERE action='owner-alert-send-failed'").fetchone()["detail"]
        self.assertEqual(det, "HTTP 302")


class Topic(SendCase):
    def blocked(self):
        self.row()
        self.send()
        self.assertEqual(self.calls, [])
        self.assertEqual(set(self.sent().values()), {None})
        self.assertEqual(self.events(), ["owner-alert-send-blocked"])

    def test_mode_644_blocks(self):
        self.write_topic(TOPIC, 0o644)
        self.blocked()

    def test_symlink_to_a_600_file_blocks(self):
        real = self.home / "real-topic"
        real.write_text(TOPIC + "\n")
        os.chmod(real, 0o600)
        self.topic_path.unlink()
        self.topic_path.symlink_to(real)
        self.blocked()

    def test_bad_content_blocks(self):
        for bad in ("short", "has/slash_0123456789abc", "x" * 65, ""):
            self.con.execute("DELETE FROM events")
            self.con.execute("DELETE FROM throttle")
            self.calls.clear()
            self.write_topic(bad)
            with self.subTest(bad=bad[:8]):
                self.con.execute("DELETE FROM owner_alert_feed")
                self.blocked()

    def test_mode_600_regular_file_sends(self):
        self.row()
        self.send()
        self.assertEqual(len(self.calls), 1)

    def test_blocked_event_once_per_hour(self):
        self.write_topic(TOPIC, 0o644)
        self.row()
        self.send(0)
        self.send(60)
        self.assertEqual(self.events(), ["owner-alert-send-blocked"])
        self.send(3700)
        self.assertEqual(self.events(), ["owner-alert-send-blocked"] * 2)


class Failure(SendCase):
    def test_raise_500_redirect_then_recover(self):
        self.row()
        for t, post in ((0, mock.Mock(side_effect=OSError("down"))), (301, mock.Mock(return_value=500)),
                        (602, mock.Mock(side_effect=urllib.error.HTTPError("u", 302, "Found", {}, None)))):
            self.send(t, post=post)
            self.assertEqual(post.call_count, 1, "one attempt only in a failing tick")
            self.assertEqual(self.sent(), {1: None})
        ok = mock.Mock(return_value=200)
        self.send(602 + 299, post=ok)  # inside the 300 s pause
        self.assertEqual(ok.call_count, 0)
        self.send(602 + 301, post=ok)
        self.assertEqual(ok.call_count, 1)
        self.assertIsNotNone(self.sent()[1])

    def test_first_failure_stops_the_tick(self):
        self.row(ref="T-1")
        self.row(ref="T-2")
        post = mock.Mock(side_effect=OSError("x"))
        self.send(post=post)
        self.assertEqual(post.call_count, 1)

    def test_failure_event_once_per_hour_with_class_or_status_only(self):
        self.row()
        self.send(0, post=mock.Mock(side_effect=OSError("down")))
        self.send(301, post=mock.Mock(return_value=500))
        rows = [r["detail"] for r in self.con.execute("SELECT detail FROM events WHERE action='owner-alert-send-failed'")]
        self.assertEqual(rows, ["OSError"])

    def test_topic_and_url_never_stored(self):
        self.row()
        leak = f"https://ntfy.sh/{TOPIC} {TOPIC}"
        self.send(0, post=mock.Mock(side_effect=RuntimeError(leak)))
        self.send(301, post=mock.Mock(side_effect=urllib.error.HTTPError("https://ntfy.sh/" + TOPIC, 503, leak, {}, None)))
        self.send(602)  # succeeds
        self.assertIsNotNone(self.sent()[1])
        for table in ("events", "messages", "throttle", "config", "owner_alert_feed"):
            dump = json.dumps([tuple(r) for r in self.con.execute(f"SELECT * FROM {table}")], default=str)
            self.assertNotIn(TOPIC, dump, table)
            self.assertNotIn("ntfy.sh", dump, table)


class Rows(SendCase):
    def test_cap_five_per_tick_oldest_first(self):
        for n in range(7):
            self.row(ref=f"T-{n}", first_seen=at(n))
        self.send(100)
        self.assertEqual([c[1].split()[3] for c in self.calls], [f"ref=T-{n}" for n in range(5)])
        self.calls.clear()
        self.send(101)
        self.assertEqual([c[1].split()[3] for c in self.calls], ["ref=T-5", "ref=T-6"])

    def test_closed_row_is_never_sent(self):
        self.row(ref="T-1", closed=at(5))
        self.row(ref="T-2")
        self.send(10)
        self.assertEqual([c[1].split()[3] for c in self.calls], ["ref=T-2"])

    def test_no_transaction_open_and_a_second_connection_can_write(self):
        self.row()
        db = self.con.execute("PRAGMA database_list").fetchone()["file"]
        wrote = []

        def post(topic, line):
            self.assertFalse(self.con.in_transaction)
            other = sqlite3.connect(db, timeout=0)
            try:
                other.execute("INSERT INTO config(key,value) VALUES('probe','1') ON CONFLICT(key) DO UPDATE SET value='1'")
                other.commit()
                wrote.append(True)
            finally:
                other.close()
            return 200
        self.send(post=post)
        self.assertEqual(wrote, [True])
        self.assertIsNotNone(self.sent()[1])

    def test_flap_guard_24h(self):
        self.row(kind="goal-blocked", ref="G-2", closed=at(100), sent=at(50), first_seen=at(0))
        self.row(kind="goal-blocked", ref="G-2", first_seen=at(200))  # back within 24 h
        self.send(3600)
        self.assertEqual(self.calls, [])
        self.send(50 + 24 * 3600 + 1)  # still open after 24 h
        self.assertEqual(len(self.calls), 1)

    def test_id_gaps_do_not_matter(self):
        for i in (1, 2, 4, 303):
            self.con.execute("INSERT INTO owner_alert_feed(id,org,kind,ref,since,link,first_seen) VALUES(?,?,?,?,?,?,?)",
                             (i, "claude", "task-owner-wait", f"T-{i}", at(), "l", at(i)))
        self.con.commit()
        self.send(10)
        self.assertEqual(len(self.calls), 4)


class Prune(SendCase):
    def tick(self, t=0):
        with mock.patch.dict(os.environ, {"FACTORY_NOW": at(t)}):
            self.mod.owner_alert_feed_tick(self.con)
        self.con.commit()

    def seed(self):
        day = 86400
        self.row(ref="old", closed=at(-31 * day), first_seen=at(-40 * day))
        self.row(ref="young", closed=at(-29 * day), first_seen=at(-40 * day))
        self.row(ref="openold", first_seen=at(-90 * day))
        # open rows stay open only while owner_attention lists them; patch it so the mirror does not close them
        return mock.patch.object(self.mod, "owner_attention", return_value=[
            {"org": "claude", "kind": "task-owner-wait", "ref": "openold", "since": "s", "link": "l"}])

    def refs(self):
        return {r["ref"] for r in self.con.execute("SELECT ref FROM owner_alert_feed")}

    def test_flag_on_prunes_only_old_closed_rows(self):
        with self.seed():
            self.tick()
        self.assertEqual(self.refs(), {"young", "openold"})

    def test_flag_off_deletes_nothing(self):
        self.set("owner_alert_feed", "0")
        with self.seed():
            self.tick()
        self.assertEqual(self.refs(), {"old", "young", "openold"})


class Migration(SendCase):
    def test_old_table_without_sent_migrates_and_rows_count_unsent(self):
        self.con.execute("DROP TABLE owner_alert_feed")
        self.con.execute("CREATE TABLE owner_alert_feed (id INTEGER PRIMARY KEY AUTOINCREMENT, org TEXT NOT NULL, kind TEXT NOT NULL, "
                         "ref TEXT NOT NULL, since TEXT NOT NULL, link TEXT NOT NULL, first_seen TEXT NOT NULL, closed TEXT)")
        self.con.execute("INSERT INTO owner_alert_feed(id,org,kind,ref,since,link,first_seen) VALUES(303,'claude','k','r','s','l',?)",
                         (at(),))
        self.con.commit()
        self.mod.migrate(self.con)
        self.mod.migrate(self.con)  # idempotent
        self.assertIn("sent", {r["name"] for r in self.con.execute("PRAGMA table_info(owner_alert_feed)")})
        self.assertEqual(self.sent(), {303: None})
        self.send()
        self.assertEqual(len(self.calls), 1)

    def test_alerts_json_keeps_exactly_six_fields(self):
        self.row(sent=at())
        out = self.env.run("alerts", "--json", inproc=False)
        self.assertEqual(out.returncode, 0, out.stderr)
        rows = json.loads(out.stdout)
        self.assertEqual({tuple(sorted(r)) for r in rows}, {("id", "kind", "link", "org", "ref", "since")})


if __name__ == "__main__":
    unittest.main()
