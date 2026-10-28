"""G-010 T-A: the DB lives in HOME/db/factory.db; the tick migrates a legacy HOME/factory.db."""
import glob
import json
import os
import pwd
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import threading
import unittest
import urllib.request
from pathlib import Path

LIVE_HOME = os.path.join(pwd.getpwuid(os.getuid()).pw_dir, "factory")  # T-0212
from harness import FACTORY, REPO, FactoryEnv, FactoryTestCase, rendered_settings_dir
from test_worker_guard_reads import AGENT, OWN, OTHER, ReadsBase
from web import db as wdb
from web import server

# one command, also printed in docs/db-layout.md
DROP_TRIGGERS = ("import sqlite3,sys;c=sqlite3.connect(sys.argv[1],isolation_level=None);"
                 "[c.execute('DROP TRIGGER \"%s\"' % n) for (n,) in c.execute("
                 "\"SELECT name FROM sqlite_master WHERE type='trigger' AND name LIKE 'factory_moved_%'\").fetchall()]")


def fill_every_table(path):
    """One extra row in every table, so a migration that loses a table or rows shows up in the counts."""
    con = sqlite3.connect(path)
    for (t,) in con.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").fetchall():
        cols = [c for c in con.execute(f'PRAGMA table_info("{t}")').fetchall()]
        names = [c[1] for c in cols if not (c[5] and c[2].upper() == "INTEGER")]
        vals = ["1" if (c[2].upper() in ("INTEGER", "REAL")) else "zz-legacy" for c in cols
                if not (c[5] and c[2].upper() == "INTEGER")]
        con.execute(f'INSERT OR IGNORE INTO "{t}"({",".join(names)}) VALUES({",".join("?" * len(names))})', vals)
    con.commit()
    con.close()


def counts(path):
    con = sqlite3.connect(path)
    try:
        out = {t: con.execute(f'SELECT count(*) FROM "{t}"').fetchone()[0]
               for (t,) in con.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")}
        # the tick keeps this bookkeeping key (util_alerts); it is not a setting the test changes
        out["config"] = con.execute("SELECT count(*) FROM config WHERE key != 'util_cap_breach_last_id'").fetchone()[0]
        return out
    finally:
        con.close()


class LegacyCase(FactoryTestCase):
    """A temp home in the pre-migration layout: HOME/factory.db, no db/ dir."""
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.legacy = self.env.home_dir / "factory.db"
        self.new = self.env.db_path
        con = sqlite3.connect(self.new)
        con.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        con.close()
        shutil.move(self.new, self.legacy)
        for ext in ("-wal", "-shm"):
            Path(str(self.new) + ext).unlink(missing_ok=True)
        self.new.parent.rmdir()
        fill_every_table(self.legacy)

    def events(self, action, path=None):
        con = sqlite3.connect(path or self.new)
        try:
            return con.execute("SELECT detail FROM events WHERE action=?", (action,)).fetchall()
        finally:
            con.close()

    def migrated(self):
        return sorted(glob.glob(str(self.legacy) + ".migrated-*"))


class LayoutTests(FactoryTestCase):
    def test_init_creates_db_dir_only(self):
        ls = os.listdir(self.env.home_dir)
        self.assertTrue(self.env.db_path.is_file())
        self.assertNotIn("factory.db", ls)
        self.assertEqual(self.env.db_path.parent.stat().st_mode & 0o777, 0o700)
        self.assertIn("owner", self.ok("whoami").stdout)
        self.ok("goal", "list")

    def test_no_implicit_create(self):
        home = Path(tempfile.mkdtemp(prefix="zz-test-nodb-"))
        self.addCleanup(shutil.rmtree, home, True)
        env = {**self.env.env("owner"), "FACTORY_HOME": str(home)}
        p = subprocess.run([sys.executable, str(FACTORY), "goal", "list"], capture_output=True, text=True, env=env, cwd=home)
        self.assertEqual(p.returncode, 1, p.stderr)
        self.assertIn("run `factory init`", p.stderr)
        self.assertEqual(os.listdir(home), [])

    def test_db_open_is_rw_only_no_create(self):
        with self.env.load_factory() as f:
            for f.DB in (self.env.home_dir / "nope" / "x.db", self.env.home_dir / "x.db"):
                with self.assertRaises(sqlite3.OperationalError):
                    f.db()
                self.assertFalse(f.DB.exists())  # a missing file is an error, never an empty new DB
            self.assertFalse(f.DB.parent.parent.joinpath("nope").exists())

    def test_factory_db_env_has_no_effect(self):
        other = self.env.home_dir / "other" / "f.db"
        other.parent.mkdir()
        x = {"FACTORY_DB": str(other)}
        self.ok("config", "nudge_cooldown_s", "77", extra_env=x)
        q = "SELECT value FROM config WHERE key='nudge_cooldown_s'"
        self.assertEqual(self.env.sql(q)[0][0], "77")  # the home's DB took the write
        self.assertFalse(other.exists())
        self.assertEqual(os.listdir(other.parent), [])

    def test_settings_have_db_and_legacy_deny_lines(self):
        files = sorted(glob.glob(str(rendered_settings_dir() / "*.json")))
        self.assertEqual(len(files), 12)
        for f in files:
            deny = json.loads(Path(f).read_text())["permissions"]["deny"]
            for tool in ("Edit", "Write"):
                for tail in ("db/**", "factory.db*"):
                    self.assertIn(f"{tool}(/{LIVE_HOME}/{tail})", deny, f)


class NoOverrideTests(FactoryTestCase):
    """T-0159 restructure: no env var but FACTORY_HOME picks the DB. The temp home under self.env.home is the
    "live" layout here; its db/factory.db holds goal G-001 (the marker a leak would show)."""

    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.live = self.env.db_path
        self.live_home = self.env.home_dir
        self.snap = self.live.read_bytes()
        self.other = self.env.home / "other-home"
        self.other.mkdir()
        self.messages = sqlite3.connect(self.live).execute("SELECT count(*) FROM messages").fetchone()[0]

    def sh(self, script, home, agent=None, db=True):
        e = self.env.env(agent, None)
        e["FACTORY_HOME"] = str(home)
        if db:
            e["FACTORY_DB"] = str(self.live)
        return subprocess.run(["bash", "-c", script], capture_output=True, text=True, env=e, cwd=self.env.home, timeout=60)

    def untouched(self, p):
        self.assertNotIn("G-001", p.stdout + p.stderr)
        self.assertEqual(sqlite3.connect(self.live).execute("SELECT count(*) FROM messages").fetchone()[0],
                         self.messages)
        self.assertEqual(self.live.read_bytes(), self.snap)

    def test_a_factory_db_has_no_effect(self):
        control = self.sh(f"python3 {FACTORY} goal list", self.live_home, db=False)
        self.assertIn("G-001", control.stdout)  # the marker is visible through the real home
        p = self.sh(f"python3 {FACTORY} goal list", self.other)  # FACTORY_DB=<live db> exported by sh()
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("run `factory init`", p.stderr)
        self.untouched(p)
        p = self.sh(f"export FACTORY_DB={self.live}; python3 {FACTORY} init && python3 {FACTORY} goal list", self.other)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertTrue((self.other / "db" / "factory.db").is_file())  # init built the home's DB, not the override
        self.untouched(p)

    def test_b_parent_of_live_home_is_not_the_live_db(self):
        for parent in (self.env.home, f"{self.live_home}/..", "/"):
            for cmd in ("goal list", "agents"):
                p = self.sh(f"python3 {FACTORY} {cmd}", parent)
                self.assertNotEqual(p.returncode, 0, f"{parent} {cmd}: {p.stdout}{p.stderr}")
                self.untouched(p)

    def test_c_reviewer_b1_repro_msg_as_owner(self):
        for parent in (self.env.home, f"{self.live_home}/.."):
            p = self.sh(f"FACTORY_AGENT=owner FACTORY_HOME={parent} FACTORY_DB={self.live} python3 {FACTORY} msg boss x y",
                        parent, agent="owner")
            self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
            self.untouched(p)

    def test_d_hook_temp_home_exemption_cannot_reach_live_db(self):
        hook = str(REPO / "roles" / "hooks" / "worker-guard.py")
        parent = self.env.home
        for cmd in ("agents", "goal list", "res", "msg boss x y"):
            line = f"FACTORY_HOME={parent} FACTORY_DB={self.live} python3 {FACTORY} {cmd}"
            payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": line}})
            hk = subprocess.run([sys.executable, hook, "--role", "coder"], input=payload, capture_output=True, text=True,
                                env={**os.environ, "FACTORY_AGENT": "zz-coder", "FACTORY_HOME": str(self.live_home)}, timeout=30)
            if hk.returncode == 0:  # the exemption lets the command through: the CLI must still not reach the live DB
                p = self.sh(line, parent)
                self.assertNotEqual(p.returncode, 0, f"{cmd}: {p.stdout}{p.stderr}")
                self.untouched(p)

    def test_web_context_picks_the_home_only(self):
        from web import context
        home = self.live_home
        keep = {k: os.environ.get(k) for k in ("FACTORY_HOME", "FACTORY_DB")}
        self.addCleanup(lambda: [os.environ.pop(k, None) if v is None else os.environ.__setitem__(k, v)
                                 for k, v in keep.items()])
        os.environ["FACTORY_HOME"] = str(home)
        os.environ["FACTORY_DB"] = str(self.env.home / "elsewhere.db")
        self.assertEqual(context.db_path(), home / "db" / "factory.db")
        self.live.rename(home / "factory.db")  # legacy layout until the tick migrates it
        self.assertEqual(context.db_path(), home / "factory.db")
        os.environ["FACTORY_HOME"] = str(self.other)
        self.assertEqual(context.db_path(), self.other / "db" / "factory.db")

    def test_no_factory_db_reads_left(self):
        for d in ("bin", "web", "scout", "roles"):
            r = subprocess.run(["git", "grep", "-n", "FACTORY_DB", "--", d], cwd=REPO, capture_output=True, text=True)
            self.assertEqual(r.stdout, "", r.stdout)


class MigrationTests(LegacyCase):
    def test_resolution_prefers_legacy_until_migrated(self):
        with self.env.load_factory() as f:
            self.assertEqual(f.resolve_db(), self.legacy)
        self.ok("tick")
        with self.env.load_factory() as f:
            self.assertEqual(f.resolve_db(), self.new)

    def test_tick_migrates_once(self):
        before = counts(self.legacy)
        self.assertTrue(all(n > 0 for n in before.values()), before)
        self.ok("tick")
        after = counts(self.new)
        self.assertEqual(set(after), set(before))  # the rest of the tick may add rows (throttle, events): never lose any
        self.assertTrue(all(after[t] >= n for t, n in before.items()), (before, after))
        self.assertFalse(self.legacy.exists())
        self.assertEqual(len(self.migrated()), 1)
        ev = self.events("db-migrated")
        self.assertEqual(len(ev), 1)
        self.assertEqual(json.loads(ev[0][0])["rows"], before)
        self.ok("tick")
        self.assertEqual(len(self.events("db-migrated")), 1)
        self.assertEqual(len(self.migrated()), 1)
        self.assertFalse((self.new.parent / "factory.db.tmp").exists())

    def test_late_writer_blocked_and_allowed(self):
        late = sqlite3.connect(self.legacy, timeout=5)
        late.execute("SELECT count(*) FROM config").fetchall()  # opened before the migration
        self.ok("tick")
        with self.assertRaises(sqlite3.DatabaseError) as cm:
            late.execute("INSERT INTO config VALUES('zz-late','1')")
        self.assertIn("factory.db moved", str(cm.exception))
        late.close()
        q = "SELECT count(*) FROM config WHERE key='zz-late'"
        self.assertEqual(self.env.sql(q)[0][0], 0)
        old = sqlite3.connect(self.migrated()[0])
        self.assertEqual(old.execute(q).fetchone()[0], 0)
        old.close()
        self.ok("config", "nudge_cooldown_s", "55")  # 5b: the CLI after migration lands in db/factory.db
        self.assertEqual(self.env.sql("SELECT value FROM config WHERE key='nudge_cooldown_s'")[0][0], "55")

    def test_failure_keeps_legacy_authoritative(self):
        (self.env.home_dir / "db").write_text("a file where the db dir should be")
        before = counts(self.legacy)
        self.ok("tick")
        self.ok("tick")
        self.assertTrue(self.legacy.is_file())
        self.assertEqual(self.migrated(), [])
        self.assertFalse(glob.glob(str(self.env.home_dir / "db*" / "*.tmp")))
        self.assertEqual(len(self.events("db-migrate-failed", self.legacy)), 2)
        con = sqlite3.connect(self.legacy)
        msgs = con.execute("SELECT recipient FROM messages WHERE subject LIKE '%migration%failed%'").fetchall()
        con.close()
        self.assertEqual([m[0] for m in msgs], ["director"])  # two failed ticks, one message
        self.ok("config", "nudge_cooldown_s", "66")  # still writable
        self.assertEqual(counts(self.legacy)["config"], before["config"])
        con = sqlite3.connect(self.legacy)
        self.assertEqual(con.execute("SELECT value FROM config WHERE key='nudge_cooldown_s'").fetchone()[0], "66")
        con.close()

    def test_rename_failure_after_commit_still_records_event(self):
        import unittest.mock
        real = os.rename

        def flaky(src, dst, *a, **k):
            if ".migrated-" in str(dst):
                raise PermissionError(13, "denied", str(dst))
            return real(src, dst, *a, **k)

        with self.env.load_factory() as f:
            with unittest.mock.patch.object(f.os, "rename", flaky):
                f.migrate_db_layout()  # must not raise
            self.assertEqual(f.DB, self.new)
        self.assertTrue(self.new.is_file())
        ev = self.events("db-migrated")
        self.assertEqual(len(ev), 1)
        self.assertTrue(any("rename factory.db" in p for p in json.loads(ev[0][0])["problems"]), ev)
        self.assertEqual(self.events("db-migrate-failed"), [])

    def test_stale_legacy_path_after_migration_says_moved(self):
        self.ok("tick")
        with self.env.load_factory() as f:
            f.DB = self.legacy  # a stale script or connection still naming the pre-migration file
            with self.assertRaises(sqlite3.OperationalError) as c:
                f.db()
        self.assertIn("factory.db moved to db/factory.db", str(c.exception))
        self.assertNotIn("factory init", str(c.exception))

    def test_missing_wal_after_commit_is_not_a_failure(self):
        # SQLite may delete -wal/-shm when the last connection closes: only the main file is left to rename
        self.ok("tick")
        self.assertEqual(len(self.events("db-migrated")), 1)
        self.assertEqual(json.loads(self.events("db-migrated")[0][0]).get("problems"), None)

    def test_rollback_steps(self):
        self.ok("tick")
        n = counts(self.new)
        # docs/db-layout.md: (tick timer stopped) checkpoint, move the file back, drop the -wal/-shm leftovers
        con = sqlite3.connect(self.new)
        con.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        con.close()
        shutil.move(self.new, self.legacy)
        for ext in ("-wal", "-shm"):
            Path(str(self.new) + ext).unlink(missing_ok=True)
        subprocess.run([sys.executable, "-c", DROP_TRIGGERS, str(self.legacy)], check=True)
        self.ok("config", "nudge_cooldown_s", "88")
        con = sqlite3.connect(self.legacy)
        self.assertEqual({t: c for t, c in counts(self.legacy).items() if t != "events"},
                         {t: c for t, c in n.items() if t != "events"})
        self.assertEqual(con.execute("SELECT value FROM config WHERE key='nudge_cooldown_s'").fetchone()[0], "88")
        con.close()
        self.ok("goal", "list")

    def test_rollback_from_migrated_file_drops_triggers(self):
        self.ok("tick")
        old = Path(self.migrated()[0])
        con = sqlite3.connect(old)
        with self.assertRaises(sqlite3.DatabaseError):
            con.execute("INSERT INTO config VALUES('zz','1')")
        con.close()
        subprocess.run([sys.executable, "-c", DROP_TRIGGERS, str(old)], check=True)
        con = sqlite3.connect(old)
        con.execute("INSERT INTO config VALUES('zz','1')")
        con.commit()
        con.close()


class WebReaderTests(LegacyCase):
    def get(self, httpd, path):
        port = httpd.server_address[1]
        return urllib.request.urlopen(f"http://127.0.0.1:{port}{path}", timeout=10).read().decode()

    def test_reader_follows_migration(self):
        with self.env.load_factory() as f:
            resolve = f.resolve_db
            httpd = server.make_server(resolve, "127.0.0.1", 0, [], tokens=lambda: {"rows": []}, defaults=f.DEFAULT_CONFIG,
                                       factory_home=self.env.home_dir)
        threading.Thread(target=httpd.serve_forever, daemon=True).start()
        self.addCleanup(httpd.server_close)
        self.addCleanup(httpd.shutdown)
        self.assertEqual(httpd.app.current_db(), self.legacy)
        self.assertIn("ok", self.get(httpd, "/healthz"))
        self.ok("tick")
        self.ok("config", "nudge_cooldown_s", "99")
        self.assertEqual(httpd.app.current_db(), self.new)
        self.assertNotIn(".migrated", str(httpd.app.current_db()))
        with wdb.reader(httpd.app.current_db()) as con:  # row written after the move is visible
            self.assertEqual(con.execute("SELECT value FROM config WHERE key='nudge_cooldown_s'").fetchone()[0], "99")
        self.assertIn("ok", self.get(httpd, "/healthz"))

    def test_reader_on_migrated_home(self):
        self.ok("tick")
        with wdb.reader(self.new) as con:
            self.assertGreater(con.execute("SELECT count(*) FROM goals").fetchone()[0], 0)


class GuardLayoutTests(ReadsBase):
    """The real hook resolves the DB like bin/factory: db/, then legacy; FACTORY_DB is ignored."""
    def decide(self):
        self.blocked(f"factory goal show {OTHER}")
        self.allowed(f"factory goal show {OWN}")

    def test_new_layout(self):
        self.assertTrue(os.path.isfile(os.path.join(self.home, "db", "factory.db")))
        self.assertFalse(os.path.exists(os.path.join(self.home, "factory.db")))
        self.decide()

    def test_legacy_layout(self):
        shutil.move(os.path.join(self.home, "db", "factory.db"), os.path.join(self.home, "factory.db"))
        self.decide()

    def test_factory_db_env_is_ignored(self):
        elsewhere = os.path.join(self.home, "elsewhere.db")
        shutil.move(os.path.join(self.home, "db", "factory.db"), elsewhere)
        self.blocked(f"factory goal show {OWN}")  # no DB at the default paths: fail closed
        os.environ["FACTORY_DB"] = elsewhere
        self.addCleanup(os.environ.pop, "FACTORY_DB", None)
        self.blocked(f"factory goal show {OWN}")  # FACTORY_DB does not point the hook at another DB


if __name__ == "__main__":
    unittest.main()
