"""G-015 T-1114 (SC3, F-74): the tick's per-goal token tally (goal_token_tally table) and `factory goal tokens`."""
import builtins
import contextlib
import io
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_util_alerts import AlertCase  # noqa: E402
from test_web_tokens import line, usage_row  # noqa: E402
from web import tokens  # noqa: E402

NOW = {"FACTORY_NOW": "2026-11-05T12:00:00"}
TS = "2026-11-05T10:00:00Z"
REAL_INDEX = tokens.TokenIndex


class TallyCase(AlertCase):
    def setUp(self):
        super().setUp()
        self.logs = Path(tempfile.mkdtemp())
        self.addCleanup(__import__("shutil").rmtree, self.logs, True)
        self.root = self.logs / "projects"
        (self.root / "p").mkdir(parents=True)
        self.con.execute("INSERT INTO goals(id,title,status,created,updated,org) VALUES('G-1','g','active','t','t','claude')")
        self.con.execute("INSERT INTO goals(id,title,status,created,updated,org) VALUES('G-9','c','active','t','t','cursor')")
        self.con.execute("INSERT INTO agents(name,role,status,goal_id,task_id,session_id,started) VALUES('coder-a','coder','live','G-1','T-1','s1',?)",
                         ("2026-11-05T09:00:00+00:00",))

    def session(self, sid, *rows):
        (self.root / "p" / f"{sid}.jsonl").write_text("".join(line(r) for r in rows))

    def set_every(self, v="600"):
        self.con.execute("INSERT OR REPLACE INTO config(key,value) VALUES('goal_token_tally_s',?)", (v,))

    def tick(self, now=NOW["FACTORY_NOW"]):
        with mock.patch.dict(os.environ, {"FACTORY_NOW": now}), \
                mock.patch("web.tokens.TokenIndex", lambda: REAL_INDEX(self.root)):
            self.mod.goal_token_tally_tick(self.con)

    def rows(self):
        return {r["goal_id"]: dict(r) for r in self.con.execute("SELECT * FROM goal_token_tally")}

    def cli(self, *args, code=0):
        p = self.env.run(*args, inproc=False)
        self.assertEqual(p.returncode, code, f"{args}\n{p.stdout}\n{p.stderr}")
        return p.stdout

    def fail_events(self):
        return self.con.execute("SELECT count(*) FROM events WHERE action='goal-token-tally-failed'").fetchone()[0]


class Tally(TallyCase):
    def test_same_figure_as_the_page_and_tally_definition(self):
        self.session("s1", usage_row("s1", "m1", "r1", TS, 100, 20, 5000, 7, 3),
                     usage_row("s1", "m2", "r2", TS, 1, 2, 3, 4, 5))
        self.set_every()
        self.tick()
        idx = REAL_INDEX(self.root)
        idx.refresh()
        snap = idx.snapshot()
        want = tokens.rollup(snap, tokens.attribute(snap, self.con))["by_goal"]["G-1"]
        out = self.cli("goal", "tokens", "G-1")
        for k in ("input", "output", "cache_write_5m", "cache_write_1h"):
            self.assertIn(f"  {k}: {want[k]}", out)
        self.assertIn(f"cache_read: {want['cache_read']} (printed apart", out)
        total = want["input"] + want["output"] + want["cache_write_5m"] + want["cache_write_1h"]
        self.assertEqual(total, 101 + 22 + 11 + 8)  # known counters, cache_read 5003 not in it
        self.assertIn(f"G-1 tally: {total} tokens", out)
        r = self.rows()["G-1"]
        self.assertEqual({k: r[k] for k in ("input", "output", "cache_read", "cache_write_5m", "cache_write_1h")},
                         {k: want[k] for k in ("input", "output", "cache_read", "cache_write_5m", "cache_write_1h")})
        self.assertAlmostEqual(r["cost"], want["cost"])

    def test_unattributed_on_its_own_line(self):
        self.session("s1", usage_row("s1", "m1", "r1", TS, 10, 10))
        self.session("s2", usage_row("s2", "m2", "r2", TS, 40, 2, 99))
        self.set_every()
        self.tick()
        out = self.cli("goal", "tokens", "G-1")
        lines = [ln for ln in out.splitlines() if "not counted in any goal" in ln]
        self.assertEqual(len(lines), 1)
        self.assertTrue(lines[0].startswith("unattributed: 42 tokens"), lines[0])
        self.assertNotIn("not counted", out.replace(lines[0], ""))

    def test_off_by_default_no_log_opened_no_rows(self):
        self.session("s1", usage_row("s1", "m1", "r1", TS))
        self.assertEqual(self.mod.cfg(self.con, "goal_token_tally_s"), "0")
        with mock.patch("web.tokens.TokenIndex", side_effect=AssertionError("built")) as ti:
            with mock.patch.dict(os.environ, NOW):
                self.mod.goal_token_tally_tick(self.con)
        ti.assert_not_called()
        self.assertEqual(self.rows(), {})
        self.assertEqual(self.fail_events(), 0)

    def test_reading_opens_no_jsonl(self):
        self.session("s1", usage_row("s1", "m1", "r1", TS, 7, 3))
        self.set_every()
        self.tick()
        real_open = builtins.open

        def guard(path, *a, **k):
            if str(path).endswith(".jsonl"):
                raise AssertionError(f"opened {path}")
            return real_open(path, *a, **k)
        with mock.patch.object(builtins, "open", guard), mock.patch.object(tokens, "TokenIndex", side_effect=AssertionError):
            with contextlib.redirect_stdout(io.StringIO()):
                self.mod.goal_tokens(self.con, "G-1")  # raises if a log is touched
        self.assertIn("G-1 tally: 10 tokens", self.cli("goal", "tokens", "G-1"))

    def test_tally_raising_never_breaks_tick_one_throttled_event(self):
        self.set_every("1")
        with mock.patch("web.tokens.TokenIndex", side_effect=RuntimeError("boom")):
            for now in ("2026-11-05T12:00:00", "2026-11-05T12:00:05", "2026-11-05T12:00:10"):
                with mock.patch.dict(os.environ, {"FACTORY_NOW": now}):
                    self.mod.goal_token_tally_tick(self.con)
        self.assertEqual(self.fail_events(), 1)
        self.assertEqual(self.rows(), {})

    def test_big_file_is_drained_whole(self):
        rs = [usage_row("s1", f"m{i}", f"r{i}", TS, 1, 1) for i in range(40)]
        self.session("s1", *rs)
        self.set_every()
        size = (self.root / "p" / "s1.jsonl").stat().st_size
        with mock.patch.object(tokens, "MAX_READ_BYTES", size // 4):
            self.tick()
        self.assertEqual((self.rows()["G-1"]["input"], self.rows()["G-1"]["output"]), (40, 40))

    def test_deadline_stores_nothing_keeps_old_rows_one_event(self):
        self.session("s1", usage_row("s1", "m1", "r1", TS, 5, 5))
        self.set_every("1")
        self.tick()
        old = self.rows()
        self.assertEqual(old["G-1"]["input"], 5)
        self.session("s1", *[usage_row("s1", f"m{i}", f"r{i}", TS, 5, 5) for i in range(30)])
        size = (self.root / "p" / "s1.jsonl").stat().st_size
        with mock.patch.object(self.mod, "GOAL_TOKEN_DEADLINE_S", -1), mock.patch.object(tokens, "MAX_READ_BYTES", size // 3):
            self.tick("2026-11-05T13:00:00")
            self.tick("2026-11-05T13:00:05")
        self.assertEqual(self.rows(), old)
        self.assertEqual(self.fail_events(), 1)

    def test_attempt_clock_blocks_a_second_pass_after_failure(self):
        self.set_every("600")
        with mock.patch("web.tokens.TokenIndex", side_effect=RuntimeError("boom")):
            with mock.patch.dict(os.environ, {"FACTORY_NOW": "2026-11-05T12:00:00"}):
                self.mod.goal_token_tally_tick(self.con)
        with mock.patch("web.tokens.TokenIndex", side_effect=AssertionError("built")) as ti:
            with mock.patch.dict(os.environ, {"FACTORY_NOW": "2026-11-05T12:05:00"}):
                self.mod.goal_token_tally_tick(self.con)
        ti.assert_not_called()
        self.session("s1", usage_row("s1", "m1", "r1", TS, 2, 2))
        self.tick("2026-11-05T12:11:00")  # past the interval: runs again
        self.assertEqual(self.rows()["G-1"]["input"], 2)


class Cli(TallyCase):
    def test_no_cache_row_says_no_tally_yet(self):
        out = self.cli("goal", "tokens", "G-1")
        self.assertIn("no tally yet", out)

    def test_cursor_goal_says_agent_hours(self):
        self.session("s1", usage_row("s1", "m1", "r1", TS, 7, 3))
        self.set_every()
        self.tick()
        out = self.cli("goal", "tokens", "G-9")
        self.assertIn("agent-hours", out)
        self.assertNotIn("tally:", out)
        self.assertNotIn("unattributed", out)

    def test_unknown_goal_fails(self):
        self.assertNotEqual(self.env.run("goal", "tokens", "G-404", inproc=False).returncode, 0)

    def test_table_and_flag_registered(self):
        flags = json.loads((ROOT / "bin" / "flags.json").read_text())["keys"]
        self.assertEqual([e["default"] for e in flags if e["key"] == "goal_token_tally_s"], ["0"])


if __name__ == "__main__":
    unittest.main()
