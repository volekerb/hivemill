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


class Limit(TallyCase):
    """G-015 T-1116 (F-74, L2): token_limit on goals, `goal edit --token-limit`, one warning at 80%."""

    def setUp(self):
        super().setUp()
        for n, role in (("claude-director", "director"), ("claude-lead-a", "lead"), ("claude-lead-z", "lead")):
            self.con.execute("INSERT INTO agents(name,role,status) VALUES(?,?,'live')", (n, role))
        self.con.execute("INSERT INTO teams(id,goal_id,lead_agent,status,created) VALUES('g-1-a','G-1','claude-lead-a','active','t')")

    def tally(self, used, gid="G-1"):
        self.con.execute("INSERT OR REPLACE INTO goal_token_tally(goal_id,input,output,cache_read,cache_write_5m,cache_write_1h,cost,computed_at)"
                         " VALUES(?,?,0,0,0,0,0,'t')", (gid, used))

    def limit(self, n, gid="G-1"):
        self.con.execute("UPDATE goals SET token_limit=? WHERE id=?", (n, gid))

    def warn(self):
        with mock.patch.dict(os.environ, NOW):
            self.mod.goal_token_warn_tick(self.con)

    def alerts(self):
        return [(r["recipient"], r["sender"], r["kind"], r["subject"], r["body"]) for r in self.con.execute(
            "SELECT * FROM messages WHERE kind='alert' ORDER BY id")]

    def throttle_rows(self):
        return self.con.execute("SELECT count(*) FROM throttle WHERE key LIKE 'goal-token-warn:%'").fetchone()[0]

    def test_one_warning_to_lead_and_director_then_none_and_rearm(self):
        self.limit(1000)
        self.tally(799)
        self.warn()
        self.assertEqual(self.alerts(), [])  # under 80%
        self.tally(800)
        self.warn()
        got = self.alerts()
        self.assertEqual(sorted(r[0] for r in got), ["claude-director", "claude-lead-a"])
        self.assertTrue(all(r[1] == "tick" for r in got))
        self.tally(900)
        self.warn()  # next tick: nothing new
        self.assertEqual(len(self.alerts()), 2)
        self.limit(2000)
        self.warn()  # 900 < 80% of 2000: no warning yet
        self.assertEqual(len(self.alerts()), 2)
        self.tally(1700)
        self.warn()  # new limit armed it again
        self.assertEqual(len(self.alerts()), 4)

    def test_no_lead_one_message_to_director(self):
        self.con.execute("DELETE FROM teams")
        self.limit(100)
        self.tally(90)
        self.warn()
        self.assertEqual([r[0] for r in self.alerts()], ["claude-director"])

    def test_text_is_fixed_no_title(self):
        self.con.execute("UPDATE goals SET title='ZZMARKER title', intent='ZZMARKER' WHERE id='G-1'")
        self.limit(100)
        self.tally(85)
        self.warn()
        got = self.alerts()
        self.assertEqual(len(got), 2)
        for r in got:
            self.assertEqual((r[3] + r[4]).count("ZZMARKER"), 0)
            self.assertIn("G-1", r[3] + r[4])
            self.assertIn("85 tokens of limit 100 (85%)", r[4])

    def test_no_limit_no_message_no_throttle_row(self):
        self.tally(10**9)
        self.warn()
        self.assertEqual((self.alerts(), self.throttle_rows()), ([], 0))

    def test_cursor_goal_with_limit_is_not_warned(self):
        self.limit(100, "G-9")
        self.tally(500, "G-9")
        self.warn()
        self.assertEqual((self.alerts(), self.throttle_rows()), ([], 0))

    def test_failure_never_raises(self):
        self.limit(100)
        self.tally(95)
        with mock.patch.object(self.mod, "send", side_effect=RuntimeError("boom")):
            self.warn()
        self.assertEqual((self.alerts(), self.throttle_rows()), ([], 0))
        self.warn()  # the failed send is retried on the next tick
        self.assertEqual(len(self.alerts()), 2)

    def test_cli_director_sets_lead_refused_zero_clears(self):
        def stored():
            return self.con.execute("SELECT token_limit FROM goals WHERE id='G-1'").fetchone()[0]
        p = self.env.run("goal", "edit", "G-1", "--token-limit", "5000", agent="claude-director", inproc=False)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(stored(), 5000)
        self.assertIn("token limit: 5000 tokens", self.cli("goal", "show", "G-1"))
        p = self.env.run("goal", "edit", "G-1", "--token-limit", "9", agent="claude-lead-a", inproc=False)
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertEqual(stored(), 5000)
        p = self.env.run("goal", "edit", "G-1", "--token-limit", "0", agent="claude-director", inproc=False)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIsNone(stored())
        self.assertNotIn("token limit", self.cli("goal", "show", "G-1"))
        p = self.env.run("goal", "edit", "G-1", "--token-limit", "-1", agent="claude-director", inproc=False)
        self.assertEqual(p.returncode, 2, p.stderr)


if __name__ == "__main__":
    unittest.main()
