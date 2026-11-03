import importlib.util
import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "ci" / "state_load.py"
spec = importlib.util.spec_from_file_location("state_load", SCRIPT)
state_load = importlib.util.module_from_spec(spec)
spec.loader.exec_module(state_load)

SQL = """BEGIN TRANSACTION;
CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, actor TEXT, entity TEXT, entity_id TEXT, action TEXT NOT NULL, detail TEXT);
CREATE TABLE goals (id TEXT PRIMARY KEY, title TEXT);
INSERT INTO "goals" VALUES('G-001','one');
INSERT INTO "goals" VALUES('G-002','two');
COMMIT;
"""
EVENTS = [{"id": i, "ts": f"2026-01-01T00:00:0{i}", "actor": "a", "entity": "goal", "entity_id": "G-001",
           "action": "x", "detail": None if i == 1 else "d"} for i in range(1, 4)]


def run(home, state, set_home=True):
    env = {k: v for k, v in os.environ.items() if k != "FACTORY_HOME"}
    if set_home:
        env["FACTORY_HOME"] = str(home)
    return subprocess.run([sys.executable, str(SCRIPT), str(state)], env=env, capture_output=True, text=True)


class StateLoadTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        t = Path(self.tmp.name)
        self.state, self.home = t / "state", t / "home"
        self.state.mkdir()
        self.home.mkdir()
        (self.state / "factory.sql").write_text(SQL)
        (self.state / "events.jsonl").write_text("".join(json.dumps(e) + "\n" for e in EVENTS))

    def test_load_succeeds_and_prints_counts(self):
        r = run(self.home, self.state)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("goals\t2", r.stdout)
        self.assertIn("events\t3", r.stdout)
        con = sqlite3.connect(self.home / "db" / "factory.db")
        self.assertEqual(con.execute("SELECT COUNT(*) FROM goals").fetchone()[0], 2)
        self.assertIsNone(con.execute("SELECT detail FROM events WHERE id=1").fetchone()[0])
        con.close()

    def test_events_rows_equal_jsonl_lines(self):
        run(self.home, self.state)
        lines = len((self.state / "events.jsonl").read_text().splitlines())
        con = sqlite3.connect(self.home / "db" / "factory.db")
        self.assertEqual(con.execute("SELECT COUNT(*) FROM events").fetchone()[0], lines)
        con.close()

    def test_empty_export_events_file(self):
        (self.state / "events.jsonl").write_text("\n")  # what the export writes with zero events
        r = run(self.home, self.state)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("events\t0", r.stdout)

    def test_refuses_unset_factory_home(self):
        r = run(self.home, self.state, set_home=False)
        self.assertEqual(r.returncode, 1)
        self.assertIn("FACTORY_HOME is unset", r.stderr)
        self.assertFalse((self.home / "db").exists())

    def test_refuses_existing_db(self):
        (self.home / "db").mkdir()
        db = self.home / "db" / "factory.db"
        db.write_bytes(b"keep")
        r = run(self.home, self.state)
        self.assertEqual(r.returncode, 1)
        self.assertIn("already exists", r.stderr)
        self.assertEqual(db.read_bytes(), b"keep")

    def test_refuses_legacy_db(self):
        (self.home / "factory.db").write_bytes(b"keep")
        r = run(self.home, self.state)
        self.assertEqual(r.returncode, 1)
        self.assertFalse((self.home / "db").exists())

    def test_refuses_live_home(self):
        live = Path(next(iter(state_load.live_homes())))
        existed = live.exists()
        r = run(live, self.state)
        self.assertEqual(r.returncode, 1)
        self.assertIn("live factory home", r.stderr)
        self.assertEqual(live.exists(), existed)

    def test_refuses_symlink_to_live_home(self):
        live = Path(next(iter(state_load.live_homes())))
        link = Path(self.tmp.name) / "link"
        link.symlink_to(live)
        r = run(link, self.state)
        self.assertEqual(r.returncode, 1)
        self.assertIn("live factory home", r.stderr)

    def test_live_homes_reads_live_home_file(self):
        base = Path(self.tmp.name) / "pw"
        (base / ".config" / "factory").mkdir(parents=True)
        (base / ".config" / "factory" / "live_home").write_text(str(self.home) + "\n")
        homes = state_load.live_homes(base=str(base))
        self.assertEqual(homes, {os.path.realpath(base / "factory"), os.path.realpath(self.home)})
        self.assertIsNotNone(state_load.check_home(str(self.home), homes)[1])

    def test_bad_sql_leaves_no_db(self):
        (self.state / "factory.sql").write_text("THIS IS NOT SQL;")
        r = run(self.home, self.state)
        self.assertEqual(r.returncode, 1)
        self.assertEqual(list((self.home / "db").iterdir()), [])

    def test_unknown_event_key_rejected_db_unchanged(self):
        bad = dict(EVENTS[0], **{'x"; DROP TABLE tasks;--': "v"})
        (self.state / "events.jsonl").write_text(json.dumps(bad) + "\n")
        r = run(self.home, self.state)
        self.assertEqual(r.returncode, 1)
        self.assertIn("unknown column", r.stderr)
        self.assertIn("DROP TABLE", r.stderr)
        self.assertEqual(list((self.home / "db").iterdir()), [])  # no DB written at all

    def test_unknown_event_key_raises_and_writes_no_db(self):
        bad = dict(EVENTS[0], evil="v")
        (self.state / "events.jsonl").write_text(json.dumps(bad) + "\n")
        with self.assertRaises(RuntimeError):
            state_load.load(self.home, self.state)
        self.assertEqual(list((self.home / "db").iterdir()), [])

    def test_known_event_keys_still_load(self):
        r = run(self.home, self.state)
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_missing_state_files(self):
        (self.state / "events.jsonl").unlink()
        r = run(self.home, self.state)
        self.assertEqual(r.returncode, 1)
        self.assertIn("events.jsonl", r.stderr)


if __name__ == "__main__":
    unittest.main()
