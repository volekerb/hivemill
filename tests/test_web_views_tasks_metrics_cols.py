"""T-1242: /tasks sortable Cost, Tokens and Wall time columns (web/views/tasks.py list part), on fixture rows in a temp DB."""
import re
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_queries import NOW, make_db, ts  # noqa: E402
from web import db as wdb  # noqa: E402
from web.views import tasks  # noqa: E402

SONNET = "claude-sonnet-5-5"


def trow(sid, inp):
    return {"session": sid, "model": SONNET, "day": "2026-11-09", "fast": False, "input": inp, "output": 0,
            "cache_read": 0, "cache_write_5m": 0, "cache_write_1h": 0, "requests": 1}


class MetricsCols(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.path = str(Path(tmp.name) / "f.db")
        w = make_db(self.path, seed=False)
        self.addCleanup(w.close)
        x = w.execute
        x("INSERT INTO projects(id,name,created) VALUES('p1','P','x')")
        x("INSERT INTO goals VALUES('G-1','p1','g',NULL,NULL,'active',1,'owner',NULL,?,?)", (ts(7, 0), ts(7, 0)))
        x("INSERT INTO goals VALUES('G-2','p1','c',NULL,NULL,'active',1,'owner',NULL,?,?)", (ts(7, 0), ts(7, 0)))
        x("ALTER TABLE goals ADD COLUMN org TEXT")
        x("UPDATE goals SET org='cursor' WHERE id='G-2'")
        T = "INSERT INTO tasks(id,goal_id,title,role,status,created,updated) VALUES(?,?,?,?,?,?,?)"
        for tid, goal, status in (("T-1", "G-1", "done"), ("T-2", "G-1", "done"), ("T-3", "G-1", "in_progress"),
                                  ("T-4", "G-1", "done"), ("T-5", "G-2", "done")):
            x(T, (tid, goal, "title " + tid, "coder", status, ts(8, 0), ts(9, 0)))
        E = "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,'judge','task',?,'merged','@abc12345')"
        x(E, (ts(9, 0), "T-1"))    # 1h
        x(E, (ts(10, 0), "T-2"))   # 2h
        x(E, (ts(8, 30), "T-5"))
        # T-4 is merged with no tokens; T-3 is open
        A = "INSERT INTO agents(name,role,goal_id,task_id,status,session_id,started) VALUES(?,?,?,?,?,?,?)"
        x(A, ("coder-1", "coder", "G-1", "T-1", "done", "s1", ts(8, 5)))
        x(A, ("coder-2", "coder", "G-1", "T-2", "done", "s2", ts(8, 5)))
        x(A, ("coder-3", "coder", "G-1", "T-3", "done", "s3", ts(8, 5)))
        self.rows = [trow("s1", 1_000_000), trow("s2", 3_000_000), trow("s3", 2_000_000)]

    def ctx(self, ready=True, query=None):
        con = wdb.connect(self.path)
        self.addCleanup(con.close)
        snap = {"ready": ready, "rows": self.rows,
                "sessions": {r["session"]: {"agent_name": None, "first_ts": None, "project": "", "cwd": ""} for r in self.rows}}
        return SimpleNamespace(db=con, now=NOW, usage_fn=None, mem_fn=None, tokens=lambda: snap, defaults={},
                               factory_home=None, query=query or {})

    def order(self, sort, d, **kw):
        c = self.ctx(query={"sort": sort, "dir": d}, **kw)
        data = tasks.data(c)
        html = str(tasks.render(c, data))
        return [t["id"] for t in tasks._sorted(data["tasks"], (sort, d))], data, html

    def test_cost_desc_order(self):
        ids, _, _ = self.order("cost", "desc")
        self.assertEqual(ids[:3], ["T-2", "T-3", "T-1"])

    def test_unknown_sorts_last_in_both_directions(self):
        for sort in ("cost", "tokens", "wall"):
            for d in ("asc", "desc"):
                ids, data, _ = self.order(sort, d)
                unknown = {"cost": {"T-4", "T-5"}, "tokens": {"T-4", "T-5"}, "wall": {"T-3", "T-4"}}
                n = len(unknown[sort])
                by = {t["id"]: t for t in data["tasks"]}
                field = tasks.METRIC_KEYS[sort]
                got_unknown = [i for i in ids if by[i][field] is None]
                self.assertEqual(ids[len(ids) - len(got_unknown):], got_unknown, (sort, d))
                self.assertGreaterEqual(len(got_unknown), n)
                self.assertNotIn("T-1", got_unknown)

    def test_wall_asc(self):
        ids, _, _ = self.order("wall", "asc")
        self.assertEqual(ids[:3], ["T-5", "T-1", "T-2"])   # 30m, 1h, 2h; T-3 and T-4 have no merge time and sort last

    def test_cursor_task_shows_na_and_never_zero(self):
        _, data, html = self.order("cost", "desc")
        t5 = next(t for t in data["tasks"] if t["id"] == "T-5")
        self.assertIsNone(t5["cost"])
        self.assertTrue(t5["na"])
        self.assertIn("n/a", html)
        self.assertNotIn("$0.00", html)

    def test_headers_and_values(self):
        _, _, html = self.order("cost", "desc")
        for h in ("Cost", "Tokens", "Wall time"):
            self.assertGreaterEqual(html.count(h), 1)
        self.assertIn("$6.00", html)          # 3M sonnet input tokens at list price
        self.assertIn("3,000,000", html)
        self.assertIn("1h 00m", html)

    def test_not_ready_shows_dash_and_does_not_wait(self):
        _, data, html = self.order("tokens", "desc", ready=False)
        self.assertTrue(all(t["tokens"] is None and t["cost"] is None for t in data["tasks"]))
        self.assertNotIn("$0.00", html)

    def test_bad_sort_input_falls_back(self):
        c = self.ctx(query={"sort": "cost; DROP TABLE tasks", "dir": "desc"})
        self.assertEqual(tasks.data(c)["sort"]["key"], "updated")


if __name__ == "__main__":
    unittest.main()
