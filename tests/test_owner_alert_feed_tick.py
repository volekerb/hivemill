"""G-015 T-1120: a real cmd_tick calls owner_alert_feed_tick (flag on -> a feed row, off -> none) and a feed failure
does not stop the tick. cmd_tick runs in-process against a temp FACTORY_HOME (FactoryTestCase), clock via FACTORY_NOW."""
import os
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
from test_util_alerts import AlertCase  # noqa: E402

NOW = {"FACTORY_NOW": "2026-11-05T12:00:00"}


class TickCallsFeed(AlertCase):
    def setUp(self):
        super().setUp()
        self.con.execute("INSERT INTO goals(id,title,status,created,updated,org) VALUES('G-1','g','active','t',"
                         "'2026-11-01T10:00:00+01:00','claude')")
        # one owner_attention item: a task blocked on the owner
        self.con.execute("INSERT INTO tasks(id,goal_id,title,status,waits,created,updated) VALUES('T-1','G-1','t',"
                         "'blocked','owner','t','2026-11-01T11:00:00+01:00')")
        self.con.commit()

    def flag(self, v):
        self.con.execute("INSERT OR REPLACE INTO config(key,value) VALUES('owner_alert_feed',?)", (v,))
        self.con.commit()

    def tick(self):
        """Run the real cmd_tick; only the process boundary is stubbed (herdr, git/remote, lock, export)."""
        m = self.mod
        with mock.patch.dict(os.environ, NOW), \
                mock.patch.object(m, "herdr_agents", return_value=[]), \
                mock.patch.object(m, "herdr", return_value=None), \
                mock.patch.object(m, "caller_is_worker", return_value=None), \
                mock.patch.object(m, "tick_lock", return_value=None), \
                mock.patch.object(m, "export", return_value=None):
            m.cmd_tick(mock.Mock(), locked=True)

    def feed_rows(self):
        c = sqlite3_connect(self.env.home_dir)
        try:
            return c.execute("SELECT kind, ref FROM owner_alert_feed WHERE closed IS NULL").fetchall()
        finally:
            c.close()

    def test_flag_on_tick_writes_a_feed_row(self):
        self.flag("1")
        self.tick()
        self.assertEqual(len(self.feed_rows()), 1, self.feed_rows())

    def test_flag_off_tick_writes_no_row(self):
        self.flag("0")
        self.tick()
        self.assertEqual(self.feed_rows(), [])

    def test_feed_failure_does_not_stop_the_tick(self):
        self.flag("1")
        m = self.mod
        with mock.patch.object(m, "owner_attention", side_effect=RuntimeError("boom")), \
                mock.patch.object(m, "_pt_emit") as after:  # the step that runs after the feed
            self.tick()  # raises nothing
        after.assert_called_once()
        self.assertEqual(self.feed_rows(), [])


def sqlite3_connect(home):
    import sqlite3
    return sqlite3.connect(str(Path(home) / "db" / "factory.db"))


if __name__ == "__main__":
    unittest.main()
