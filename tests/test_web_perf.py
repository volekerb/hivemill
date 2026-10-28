"""Cheap, CI-safe perf guards for the web UI: page size, idle refresh cost, DB connection hygiene (no wall-clock asserts)."""
import contextlib
import io
import shutil
import sys
import tempfile
import threading
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_queries import make_db  # noqa: E402
from test_web_server import ALL_ROUTES, PAGES, get  # noqa: E402
from test_web_tokens import line, usage_row  # noqa: E402
from web import db as wdb  # noqa: E402
from web import server  # noqa: E402
from web.tokens import TokenIndex  # noqa: E402

MAX_PAGE_BYTES = 300 * 1024


class PerfCase(unittest.TestCase):
    def setUp(self):
        tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, tmp, True)
        self.root = tmp / "projects"
        (self.root / "p").mkdir(parents=True)
        rows = "".join(line(usage_row("s1", f"m{i}", f"r{i}", "2026-10-20T10:00:00Z", 5, 6)) for i in range(50))
        (self.root / "p" / "s1.jsonl").write_text(rows)
        self.index = TokenIndex(self.root)
        self.index.refresh()
        self.db_path = tmp / "factory.db"
        con = make_db(self.db_path)
        self.addCleanup(con.close)
        self.httpd = server.make_server(self.db_path, "127.0.0.1", 0, (), tokens=self.index.snapshot,
                                        usage_fn=lambda: {"five_hour": 10.0, "seven_day": 20.0, "fresh": True, "age_min": 1},
                                        mem_fn=lambda: 2048, defaults={"max_agents": "6"})
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        self.addCleanup(lambda: (self.httpd.shutdown(), self.httpd.server_close()))

    def get(self, path):
        with contextlib.redirect_stderr(io.StringIO()):
            return get(self.httpd, path)


class TestPerfGuards(PerfCase):
    def test_every_page_is_under_300kb(self):
        for p in PAGES + ["/static/app.css"]:
            with self.subTest(p):
                st, _, body = self.get(p)
                self.assertEqual(st, 200)
                self.assertLess(len(body), MAX_PAGE_BYTES)

    def test_refresh_with_no_changed_files_reads_zero_bytes(self):
        self.assertGreater(self.index.snapshot()["bytes_read"], 0)
        self.index.refresh()
        snap = self.index.snapshot()
        self.assertEqual(snap["bytes_read"], 0)
        self.assertEqual(snap["files"], 1)

    def test_db_connections_return_to_zero(self):
        opened = []
        real = wdb.connect

        class Tracked:
            def __init__(self, con):
                self._con = con
                opened.append(self)
                self.closed = False

            def close(self):
                self.closed = True
                self._con.close()

            def __getattr__(self, n):
                return getattr(self._con, n)

            def __setattr__(self, n, v):
                if n in ("_con", "closed"):
                    object.__setattr__(self, n, v)
                else:
                    setattr(self._con, n, v)

        # patch web.db.connect only (not the global sqlite3 module) so connections opened by other threads/tests are not counted
        with mock.patch.object(wdb, "connect", lambda *a, **k: Tracked(real(*a, **k))):
            for i in range(len(ALL_ROUTES)):
                self.assertEqual(self.get(ALL_ROUTES[i])[0], 200, ALL_ROUTES[i])
        self.assertGreaterEqual(len(opened), 10)
        self.assertEqual(sum(1 for c in opened if not c.closed), 0)

    def test_reader_closed_before_response_is_written(self):
        """Every view route (HTML and /api JSON) closes its connection before the client can see the response."""
        real = wdb.connect
        opened = []

        class TrackedCon:
            def __init__(self, con):
                self._con = con
                self.closed = False
                opened.append(self)

            def close(self):
                self._con.close()
                self.closed = True

            def __getattr__(self, n):
                return getattr(self._con, n)

        sent = []
        real_send = server.Handler._send

        def spy(handler, *a, **k):
            sent.append([c.closed for c in opened])
            return real_send(handler, *a, **k)

        with mock.patch.object(wdb, "connect", lambda *a, **k: TrackedCon(real(*a, **k))), \
                mock.patch.object(server.Handler, "_send", spy):
            for p in PAGES + ["/api/home.json"]:
                with self.subTest(p):
                    del opened[:], sent[:]
                    self.assertEqual(self.get(p)[0], 200)
                    self.assertEqual(len(opened), 1)
                    self.assertEqual(sent, [[True]])


if __name__ == "__main__":
    unittest.main()
