"""G-015 T-1120: a real cmd_tick calls owner_alert_feed_tick (flag on -> a feed row, off -> none) and a feed failure
does not stop the tick. cmd_tick runs in-process under FactoryTestCase.load_factory(): temp FACTORY_HOME, harness env
(stubs on PATH, no live agent identity, tmp_dir under the temp home), clock via FACTORY_NOW."""
import os
import unittest
from unittest import mock

from harness import FactoryTestCase

NOW = {"FACTORY_NOW": "2026-11-05T12:00:00"}


class TickCallsFeed(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.sql("INSERT INTO goals(id,title,status,created,updated,org) VALUES('G-1','g','active','t',"
                     "'2026-11-01T10:00:00+01:00','claude')")
        # one owner_attention item: a task blocked on the owner
        self.env.sql("INSERT INTO tasks(id,goal_id,title,status,waits,created,updated) VALUES('T-1','G-1','t',"
                     "'blocked','owner','t','2026-11-01T11:00:00+01:00')")

    def flag(self, v):
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('owner_alert_feed',?)", (v,))

    def tick(self, *patches):
        """Run the real cmd_tick under the harness env. Stubbed: only the tick lock (and what a test passes in);
        herdr and gh are the tests/stubs binaries on PATH, git runs against the temp home."""
        with mock.patch.dict(os.environ, NOW), self.env.load_factory() as m, \
                mock.patch.object(m, "tick_lock", return_value=None):
            for p in patches:
                p(m)
            m.cmd_tick(mock.Mock(), locked=True)

    def feed_rows(self):
        return [tuple(r) for r in self.env.sql("SELECT kind, ref FROM owner_alert_feed WHERE closed IS NULL")]

    def test_flag_on_tick_writes_a_feed_row(self):
        self.flag("1")
        self.tick()
        self.assertEqual(self.feed_rows(), [("task-owner-wait", "T-1")])

    def test_flag_off_tick_writes_no_row(self):
        self.flag("0")
        self.tick()
        self.assertEqual(self.feed_rows(), [])

    def test_feed_failure_does_not_stop_the_tick(self):
        self.flag("1")
        after = []

        def patch(m):
            boom = mock.patch.object(m, "owner_attention", side_effect=RuntimeError("boom"))
            step = mock.patch.object(m, "_pt_emit", side_effect=lambda *a, **k: after.append(1))  # runs after the feed
            boom.start(), step.start()
            self.addCleanup(boom.stop), self.addCleanup(step.stop)

        self.tick(patch)  # raises nothing
        self.assertEqual(len(after), 1)
        self.assertEqual(self.feed_rows(), [])
        ev = self.env.sql("SELECT detail FROM events WHERE action='owner-alert-feed-failed'")
        self.assertEqual([r["detail"] for r in ev], ["boom"])

    def test_send_flag_off_tick_makes_no_sender_call_and_reads_no_topic(self):
        self.flag("1")  # feed on, owner_alert_send at its default 0
        calls = []

        def patch(m):
            for name in ("_ntfy_post", "ntfy_topic"):
                p = mock.patch.object(m, name, side_effect=lambda *a, **k: calls.append(1))
                p.start()
                self.addCleanup(p.stop)

        self.tick(patch)
        self.assertEqual(calls, [])

    def test_raising_sender_does_not_stop_later_steps(self):
        self.flag("1")
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('owner_alert_send','1')")
        after = []

        def patch(m):
            ps = [mock.patch.object(m, "ntfy_topic", return_value="faketopic_0123456789"),
                  mock.patch.object(m, "_ntfy_post", side_effect=OSError("down")),
                  mock.patch.object(m, "_pt_emit", side_effect=lambda *a, **k: after.append(1))]
            for p in ps:
                p.start()
                self.addCleanup(p.stop)

        self.tick(patch)
        self.assertEqual(len(after), 1)
        self.assertEqual(self.feed_rows(), [("task-owner-wait", "T-1")])
        self.assertEqual([r["sent"] for r in self.env.sql("SELECT sent FROM owner_alert_feed")], [None])


if __name__ == "__main__":
    unittest.main()
