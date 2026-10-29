import json
import re
import shutil
import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path

from harness import FactoryTestCase, REPO, ROLES

sys.path.insert(0, str(REPO))
from scout import db as scoutdb  # noqa: E402

TABLES = {
    "scout_candidates": ["id", "key", "kind", "repo", "path", "url", "licence", "last_commit", "default_branch",
                         "head_sha", "stars", "stars_per_day", "install_risk", "risk_detail", "sources",
                         "first_seen", "last_seen", "seen_count", "status"],
    "scout_triage": ["id", "candidate_id", "purpose", "target_roles", "overlap", "recommendation", "rationale",
                     "eval_suggested", "run_id", "author", "created"],
    "scout_stars": ["candidate_id", "ts", "stars"],
    "scout_cursors": ["source", "cursor", "updated"],
    "scout_runs": ["id", "slot", "agent", "status", "started", "ended", "tokens", "new_candidates", "triaged",
                   "stop_reason", "reported_msg"],
    "skill_trials": ["id", "candidate_id", "vendor_sha", "task", "condition", "status", "agent", "sandbox", "queued",
                     "started", "ended", "passed", "wall_s", "tokens_in", "tokens_out", "cache_read", "cache_write",
                     "skill_invoked", "check_output", "author", "void_reason", "void_why"],
}
WORKERS = ["coder", "qa", "reviewer", "judge", "cleaner", "pm"]
GOOD = ("--purpose", "p", "--roles", "coder,qa", "--overlap", "o", "--rec", "watch", "--why", "w")


def mem():
    con = sqlite3.connect(":memory:")
    con.row_factory = sqlite3.Row
    con.executescript(scoutdb.SCOUT_DDL)
    return con


class ScoutLibTests(unittest.TestCase):
    def test_dedupe_two_sources(self):
        con = mem()
        a, created = scoutdb.upsert_candidate(con, "gh:Owner/Repo", "ghsearch", stars=5)
        b, created2 = scoutdb.upsert_candidate(con, "gh:owner/repo.git/", "awesome", stars=7, licence="MIT")
        self.assertEqual((a, created, b, created2), ("C-0001", True, "C-0001", False))
        rows = con.execute("SELECT * FROM scout_candidates").fetchall()
        self.assertEqual(len(rows), 1)
        r = rows[0]
        self.assertEqual((r["key"], r["seen_count"], r["status"], r["stars"], r["licence"]), ("gh:owner/repo", 2, "new", 7, "MIT"))
        self.assertEqual(sorted(json.loads(r["sources"])), ["awesome", "ghsearch"])
        c, _ = scoutdb.upsert_candidate(con, "gh:a/b/Skills/X", "s")
        self.assertEqual(c, "C-0002")
        self.assertEqual(con.execute("SELECT key FROM scout_candidates WHERE id='C-0002'").fetchone()[0], "gh:a/b/Skills/X")

    def test_bad_key_and_fact(self):
        con = mem()
        for k in ("", "owner/repo", "gh:onlyowner"):
            with self.assertRaises(scoutdb.ScoutError):
                scoutdb.upsert_candidate(con, k, "s")
        with self.assertRaises(scoutdb.ScoutError):
            scoutdb.upsert_candidate(con, "gh:a/b", "s", bogus=1)

    def test_star_trend(self):
        con = mem()
        cid, _ = scoutdb.upsert_candidate(con, "gh:a/b", "s")
        self.assertIsNone(scoutdb.snapshot_stars(con, cid, 100, "2026-10-01T00:00:00+00:00"))
        rate = scoutdb.snapshot_stars(con, cid, 130, "2026-10-04T00:00:00+00:00")
        self.assertAlmostEqual(rate, 10.0, delta=0.01)
        r = con.execute("SELECT stars, stars_per_day FROM scout_candidates").fetchone()
        self.assertEqual(r["stars"], 130)
        self.assertAlmostEqual(r["stars_per_day"], 10.0, delta=0.01)

    def test_star_trend_window_14_days(self):
        con = mem()
        cid, _ = scoutdb.upsert_candidate(con, "gh:a/b", "s")
        scoutdb.snapshot_stars(con, cid, 0, "2026-09-01T00:00:00+00:00")  # outside the window
        scoutdb.snapshot_stars(con, cid, 100, "2026-10-01T00:00:00+00:00")
        rate = scoutdb.snapshot_stars(con, cid, 114, "2026-10-08T00:00:00+00:00")
        self.assertAlmostEqual(rate, 2.0, delta=0.01)

    def test_star_trend_across_dst_switch(self):
        """2026-10-25 02:30+02:00 is 00:30Z, 02:10+01:00 is 01:10Z: later in time, earlier as text."""
        con = mem()
        cid, _ = scoutdb.upsert_candidate(con, "gh:a/b", "s")
        scoutdb.snapshot_stars(con, cid, 100, "2026-10-25T02:30:00+02:00")
        rate = scoutdb.snapshot_stars(con, cid, 110, "2026-10-25T02:10:00+01:00")
        self.assertAlmostEqual(rate, 10 / (40 / 1440), delta=0.01)  # +10 stars in 40 minutes
        self.assertEqual(con.execute("SELECT stars FROM scout_candidates").fetchone()[0], 110)
        self.assertEqual([r["stars"] for r in con.execute("SELECT stars FROM scout_stars ORDER BY julianday(ts)")], [100, 110])
        self.assertEqual([r["stars"] for r in con.execute("SELECT stars FROM scout_stars ORDER BY ts")], [110, 100])

    def test_snapshot_stars_mixes_naive_and_aware(self):
        con = mem()
        cid, _ = scoutdb.upsert_candidate(con, "gh:a/b", "s")
        scoutdb.snapshot_stars(con, cid, 100, "2026-10-01T00:00:00")  # naive = local
        rate = scoutdb.snapshot_stars(con, cid, 130, scoutdb._parse_ts("2026-10-04T00:00:00").isoformat())
        self.assertAlmostEqual(rate, 10.0, delta=0.01)
        for r in con.execute("SELECT ts FROM scout_stars"):
            self.assertIsNotNone(scoutdb._parse_ts(r["ts"]).utcoffset())
            self.assertRegex(r["ts"], r"[+-]\d\d:\d\d$")
        with self.assertRaises(scoutdb.ScoutError):
            scoutdb.snapshot_stars(con, cid, 1, "not a time")

    def test_pending_and_since_use_instants(self):
        con = mem()
        for cid, key, first in (("C-0001", "gh:a/late", "2026-10-25T02:30:00+02:00"),
                                ("C-0002", "gh:a/early", "2026-10-25T02:10:00+01:00")):
            con.execute("INSERT INTO scout_candidates(id,key,sources,first_seen,last_seen) VALUES(?,?,'[]',?,?)",
                        (cid, key, first, first))
        self.assertEqual([r["id"] for r in scoutdb.pending(con)], ["C-0001", "C-0002"])  # 00:30Z before 01:10Z
        got = lambda since: [r["id"] for r in scoutdb.list_candidates(con, since=since)]  # noqa: E731
        self.assertEqual(got("2026-10-25T01:00:00+00:00"), ["C-0002"])
        self.assertEqual(got("2026-10-25T00:00:00+00:00"), ["C-0001", "C-0002"])
        with self.assertRaises(scoutdb.ScoutError):
            got("yesterday")
        with self.assertRaises(scoutdb.ScoutError):
            scoutdb.pending(con, 0)

    def test_cursor(self):
        con = mem()
        self.assertIsNone(scoutdb.get_cursor(con, "gh"))
        scoutdb.set_cursor(con, "gh", "a")
        scoutdb.set_cursor(con, "gh", "b")
        self.assertEqual(scoutdb.get_cursor(con, "gh"), "b")
        self.assertEqual(con.execute("SELECT count(*) FROM scout_cursors").fetchone()[0], 1)

    def test_no_network_in_scout_db(self):
        src = (REPO / "scout" / "db.py").read_text()
        imports = re.findall(r"^\s*(?:import|from)\s+(\w+)", src, re.M)
        for bad in ("urllib", "requests", "subprocess", "socket", "http"):
            self.assertNotIn(bad, imports)
        self.assertNotIn("gh api", src)


class ScoutCliTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        for r in ("director", "lead", "boss", *WORKERS):
            self.env.seed_agent(f"zz-test-{r}", r)

    def cand(self, key="gh:a/b", **kw):
        self.env.sql("INSERT INTO scout_candidates(id,key,sources,first_seen,last_seen,status,stars,install_risk) "
                     "VALUES(?,?,'[]','2026-10-01','2026-10-01',?,?,?)",
                     (kw.get("id", "C-0001"), key, kw.get("status", "new"), kw.get("stars", 5), kw.get("risk", "prompt-only")))

    def test_help_lists_actions(self):
        out = self.ok("scout", "--help").stdout
        for w in ("pending", "triage", "list", "show", "runs"):
            self.assertIn(w, out)

    def test_init_creates_tables(self):
        for t, cols in TABLES.items():
            got = [r["name"] for r in self.env.sql(f"PRAGMA table_info({t})")]
            self.assertEqual(got, cols, t)

    def test_migrate_adds_tables_idempotent(self):
        for t in TABLES:
            self.env.sql(f"DROP TABLE {t}")
        self.ok("scout", "runs", agent="owner")  # db() migrates
        self.ok("scout", "runs", agent="owner")
        for t, cols in TABLES.items():
            self.assertEqual([r["name"] for r in self.env.sql(f"PRAGMA table_info({t})")], cols)

    def test_triage_valid(self):
        self.cand()
        self.ok("scout", "triage", "C-0001", *GOOD, "--suggest-eval", agent="zz-test-director")
        t = self.env.sql("SELECT * FROM scout_triage")[0]
        self.assertEqual((t["candidate_id"], t["target_roles"], t["recommendation"], t["eval_suggested"], t["author"]),
                         ("C-0001", "coder,qa", "watch", 1, "zz-test-director"))
        self.assertEqual(self.env.sql("SELECT status FROM scout_candidates")[0]["status"], "triaged")

    def test_triage_stored_verbatim(self):
        self.cand()
        evil = "x'); DROP TABLE scout_candidates;-- $(id) `id`"
        self.ok("scout", "triage", "C-0001", "--purpose", evil, "--roles", "none", "--overlap", "o", "--rec", "skip",
                "--why", "w", agent="owner")
        self.assertEqual(self.env.sql("SELECT purpose FROM scout_triage")[0]["purpose"], evil)

    def test_triage_validation_exit_2(self):
        self.cand()
        base = dict(zip(("--purpose", "--roles", "--overlap", "--rec", "--why"), ("p", "coder", "o", "adopt", "w")))
        bad = [("--rec", "maybe"), ("--roles", "wizard"), ("--roles", "coder,none"), ("--roles", ""),
               ("--purpose", ""), ("--overlap", ""), ("--why", ""), ("--purpose", "x" * 601),
               ("--overlap", "x" * 601), ("--why", "x" * 601)]
        for k, v in bad:
            with self.subTest(k=k, v=v[:10]):
                args = [x for kv in {**base, k: v}.items() for x in kv]
                self.refused(2, "scout", "triage", "C-0001", *args, agent="owner")
        self.refused(2, "scout", "triage", "C-0099", *GOOD, agent="owner")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM scout_triage")[0]["c"], 0)
        self.assertEqual(self.env.sql("SELECT status FROM scout_candidates")[0]["status"], "new")
        self.ok("scout", "triage", "C-0001", *[x for kv in {**base, "--purpose": "x" * 600}.items() for x in kv], agent="owner")

    def test_roles_validated_against_bin_factory_roles(self):
        self.assertIn("coder", ROLES)
        self.cand()
        self.ok("scout", "triage", "C-0001", "--purpose", "p", "--roles", ",".join(ROLES), "--overlap", "o",
                "--rec", "adopt", "--why", "w", agent="owner")

    def test_triage_permissions(self):
        self.cand()
        for r in ("coder", "qa", "reviewer", "pm", "lead", "boss", "judge", "cleaner"):
            self.refused(3, "scout", "triage", "C-0001", *GOOD, agent=f"zz-test-{r}")
        self.refused(3, "scout", "triage", "C-0001", *GOOD, agent="zz-test-nobody")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM scout_triage")[0]["c"], 0)
        self.ok("scout", "triage", "C-0001", *GOOD, agent="zz-test-director")
        self.ok("scout", "triage", "C-0001", *GOOD, agent="owner")

    def test_read_permissions(self):
        self.cand()
        for act in (("pending",), ("list",), ("show", "C-0001"), ("runs",)):
            for r in WORKERS:
                with self.subTest(act=act, role=r):
                    self.refused(3, "scout", *act, agent=f"zz-test-{r}")
            for r in ("director", "lead", "boss"):
                self.ok("scout", *act, agent=f"zz-test-{r}")
            self.ok("scout", *act, agent="owner")
            self.refused(3, "scout", *act, agent="zz-test-nobody")

    def test_pending_only_untriaged_limit_and_inventory(self):
        for i in range(1, 6):
            self.cand(key=f"gh:a/r{i}", id=f"C-{i:04d}")
        self.ok("scout", "triage", "C-0001", *GOOD, agent="owner")
        out = self.ok("scout", "pending", "--limit", "2", agent="zz-test-lead").stdout
        self.assertNotIn("C-0001", out)
        self.assertIn("C-0002", out)
        self.assertIn("C-0003", out)
        self.assertNotIn("C-0004", out)
        self.assertIn("## our inventory", out)
        self.assertIn("skill ", out)
        self.assertIn("plugin ", out)

    def _count_pending(self, *args):
        out = self.ok("scout", "pending", *args, agent="zz-test-lead").stdout
        return len(re.findall(r"^C-\d{4}\b", out, re.M))

    def test_pending_cap_comes_from_config(self):
        for i in range(1, 41):
            self.cand(key=f"gh:a/r{i}", id=f"C-{i:04d}")
        self.assertEqual(self._count_pending(), 8)
        self.ok("config", "scout_triage_per_run", "36", agent="owner")
        self.assertEqual(self._count_pending(), 36)
        self.assertEqual(self._count_pending("--limit", "3"), 3)

    def test_triage_per_run_invalid_refused(self):
        for bad in ("0", "51", "-2", "abc", "1.5", ""):
            with self.subTest(bad=bad):
                r = self.refused(2, "config", "scout_triage_per_run", bad, agent="owner")
                self.assertIn("scout_triage_per_run", r.stderr)
        self.assertIn("scout_triage_per_run = 8", self.ok("config", agent="owner").stdout)
        self.ok("config", "scout_triage_per_run", "50", agent="owner")
        self.ok("config", "scout_triage_per_run", "1", agent="owner")

    def test_limit_below_one_exit_2(self):
        self.cand()
        for bad in ("0", "-1"):
            r = self.refused(2, "scout", "pending", "--limit", bad, agent="owner")
            self.assertIn("--limit", r.stderr)
        self.ok("scout", "pending", "--limit", "1", agent="owner")

    def test_list_bad_since_exit_2(self):
        self.cand()
        self.refused(2, "scout", "list", "--since", "yesterday", agent="owner")

    def test_show_snapshots_in_time_order(self):
        self.cand()
        self.env.sql("INSERT INTO scout_stars VALUES('C-0001','2026-10-25T02:30:00+02:00',100)")
        self.env.sql("INSERT INTO scout_stars VALUES('C-0001','2026-10-25T02:10:00+01:00',110)")
        out = self.ok("scout", "show", "C-0001", agent="owner").stdout
        self.assertLess(out.index("02:30:00+02:00"), out.index("02:10:00+01:00"))

    def test_triage_at_path_restricted_to_cwd(self):
        self.cand()
        inside = self.env.home / "why.txt"  # the harness runs the CLI with cwd = self.env.home
        inside.write_text("from a file\n")
        other = Path(tempfile.mkdtemp(prefix="zz-test-outside-")).resolve()
        self.addCleanup(shutil.rmtree, other, ignore_errors=True)
        outside = other / "outside-why.txt"
        outside.write_text("secret\n")
        (self.env.home / "link.txt").symlink_to(outside)
        args = lambda w: ("--purpose", "p", "--roles", "coder", "--overlap", "o", "--rec", "watch", "--why", w)  # noqa: E731
        for bad in (f"@{outside}", "@link.txt", f"@../{other.name}/outside-why.txt"):
            with self.subTest(bad=bad):
                r = self.refused(2, "scout", "triage", "C-0001", *args(bad), agent="owner")
                self.assertIn("inside the current directory", r.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM scout_triage")[0]["c"], 0)
        self.ok("scout", "triage", "C-0001", *args("@why.txt"), agent="owner")
        self.assertEqual(self.env.sql("SELECT rationale FROM scout_triage")[0]["rationale"], "from a file")

    def test_worker_role_refused_without_stub_binary(self):
        """FACTORY_ROLE alone marks the caller a worker; only a tests/stubs multiplexer + non-live home exempts it."""
        self.cand()
        bare = {"PATH": "/usr/bin:/bin", "FACTORY_ROLE": "coder"}
        for act in (("list",), ("pending",), ("triage", "C-0001", *GOOD), ("fetch",)):
            with self.subTest(act=act):
                r = self.refused(3, "scout", *act, agent="owner", extra_env=bare)
                self.assertIn("may not run `factory scout", r.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM scout_triage")[0]["c"], 0)
        self.ok("scout", "list", agent="owner", extra_env={**bare, "FACTORY_ROLE": "scout"})

    def test_list_filters_show_runs(self):
        self.cand(key="gh:a/one", id="C-0001")
        self.cand(key="gh:a/two", id="C-0002", risk="hooks,scripts")
        self.ok("scout", "triage", "C-0002", *GOOD, agent="owner")
        ls = lambda *a: self.ok("scout", "list", *a, agent="owner").stdout  # noqa: E731
        self.assertIn("C-0001", ls("--new"))
        self.assertNotIn("C-0002", ls("--new"))
        self.assertIn("C-0002", ls("--rec", "watch"))
        self.assertNotIn("C-0001", ls("--rec", "watch"))
        self.assertIn("C-0002", ls("--risk", "hooks"))
        self.assertNotIn("C-0001", ls("--risk", "hooks"))
        self.assertIn("C-0001", ls("--since", "2026-09-01"))
        self.assertEqual(ls("--since", "2027-01-01").strip(), "(none)")
        self.assertIn("rec=watch", self.ok("scout", "show", "C-0002", agent="owner").stdout)
        self.refused(2, "scout", "show", "C-0099", agent="owner")
        self.assertEqual(self.ok("scout", "runs", agent="owner").stdout.strip(), "(none)")


if __name__ == "__main__":
    unittest.main()
