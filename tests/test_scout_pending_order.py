import json
import sqlite3
import sys
import unittest

from harness import REPO

sys.path.insert(0, str(REPO))
from scout import db as scoutdb  # noqa: E402


def mem():
    con = sqlite3.connect(":memory:")
    con.row_factory = sqlite3.Row
    con.executescript(scoutdb.SCOUT_DDL)
    return con


def add(con, n, kind="skill", risk="prompt-only", sources=("skills",), spd=None, first="2026-10-01T00:00:00+00:00"):
    con.execute("INSERT INTO scout_candidates(id,key,kind,install_risk,sources,stars_per_day,first_seen,last_seen) "
                "VALUES(?,?,?,?,?,?,?,?)",
                (f"C-{n:04d}", f"gh:o/r{n}", kind, risk, json.dumps(list(sources)), spd, first, first))


def ids(con, limit=50):
    return [r["id"] for r in scoutdb.pending(con, limit)]


class PendingOrderTests(unittest.TestCase):
    def test_skills_before_marketplace_plugins(self):
        con = mem()
        add(con, 1, kind="plugin", sources=("marketplace",), first="2026-09-01T00:00:00+00:00")
        add(con, 2, kind="mcp", sources=("marketplace",), first="2026-09-02T00:00:00+00:00")
        add(con, 3, kind=None, sources=("marketplace",), first="2026-09-03T00:00:00+00:00")
        add(con, 4, kind="agent", first="2026-10-05T00:00:00+00:00")
        add(con, 5, kind="collection", first="2026-10-06T00:00:00+00:00")
        add(con, 6, kind="skill", first="2026-10-07T00:00:00+00:00")
        self.assertEqual(ids(con), ["C-0004", "C-0005", "C-0006", "C-0001", "C-0002", "C-0003"])

    def test_prompt_only_before_flagged(self):
        con = mem()
        add(con, 1, risk="hooks,scripts", first="2026-09-01T00:00:00+00:00")
        add(con, 2, risk=None, first="2026-09-02T00:00:00+00:00")
        add(con, 3, risk="prompt-only", first="2026-10-01T00:00:00+00:00")
        self.assertEqual(ids(con), ["C-0003", "C-0001", "C-0002"])

    def test_non_marketplace_source_before_marketplace_only(self):
        con = mem()
        add(con, 1, sources=("marketplace",), first="2026-09-01T00:00:00+00:00")
        add(con, 2, sources=("marketplace", "awesome"), first="2026-10-01T00:00:00+00:00")
        add(con, 3, sources=(), first="2026-09-02T00:00:00+00:00")
        self.assertEqual(ids(con)[0], "C-0002")  # empty sources counts as marketplace-only: first_seen decides
        self.assertEqual(ids(con), ["C-0002", "C-0001", "C-0003"])

    def test_stars_per_day_then_first_seen_then_id(self):
        con = mem()
        add(con, 1, spd=None, first="2026-09-01T00:00:00+00:00")
        add(con, 2, spd=1.5, first="2026-10-01T00:00:00+00:00")
        add(con, 3, spd=9.0, first="2026-10-02T00:00:00+00:00")
        add(con, 5, spd=1.5, first="2026-09-15T00:00:00+00:00")
        add(con, 4, spd=1.5, first="2026-09-15T00:00:00+00:00")
        self.assertEqual(ids(con), ["C-0003", "C-0004", "C-0005", "C-0002", "C-0001"])
        self.assertEqual(ids(con), ids(con))

    def test_limit_applies_and_nothing_is_dropped(self):
        con = mem()
        for n in range(1, 8):
            add(con, n, kind="plugin" if n % 2 else "skill", sources=("marketplace",) if n % 3 else ("hn",))
        full = ids(con)
        self.assertEqual(len(full), 7)
        self.assertEqual(ids(con, 3), full[:3])
        seen = []
        while True:  # triage the head of the queue, then page again: every candidate comes out once
            batch = scoutdb.pending(con, 2)
            if not batch:
                break
            for r in batch:
                con.execute("INSERT INTO scout_triage(id,candidate_id,purpose,target_roles,overlap,recommendation,"
                            "rationale,eval_suggested,author,created) VALUES(?,?,'p','coder','o','watch','w',0,'a','t')",
                            (len(seen) + 1, r["id"]))
                seen.append(r["id"])
        self.assertEqual(seen, full)


if __name__ == "__main__":
    unittest.main()
