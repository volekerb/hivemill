import json
import shutil
import sqlite3
import tempfile
import unittest
from pathlib import Path

from web import prices
from web.prices import cost
from web.tokens import TokenIndex, attribute, rollup

FIX = Path(__file__).parent / "fixtures" / "web_tokens" / "projects"
CANARY = "SECRET-CANARY-B"


def usage_row(sid, mid, rid, ts, inp=1, out=1, cr=0, w5=0, w1=0, model="claude-sonnet-5-5", cwd="/tmp/x", iters=True, **kw):
    u = {"input_tokens": inp, "output_tokens": out, "cache_read_input_tokens": cr,
         "cache_creation_input_tokens": w5 + w1,
         "cache_creation": {"ephemeral_5m_input_tokens": w5, "ephemeral_1h_input_tokens": w1}}
    if iters:
        u["iterations"] = [dict(u)]
    return {"type": "assistant", "uuid": "u-" + mid, "requestId": rid, "sessionId": sid, "timestamp": ts, "cwd": cwd,
            "message": {"id": mid, "model": model, "usage": u}, **kw}


def line(row):
    return json.dumps(row, separators=(",", ":")) + "\n"


class TmpMixin(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.root = self.tmp / "projects"
        self.root.mkdir()

    def write(self, rel, text, mode="w"):
        p = self.root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        with open(p, mode) as f:
            f.write(text)
        return p

    def totals(self, snap, sid):
        t = {k: 0 for k in ("input", "output", "cache_read", "cache_write_5m", "cache_write_1h", "requests")}
        for r in snap["rows"]:
            if r["session"] == sid:
                for k in t:
                    t[k] += r[k]
        return t


class IndexTests(TmpMixin):
    def test_dedupe_triple(self):
        r = usage_row("s1", "m1", "r1", "2026-10-20T10:00:00Z", 5, 6)
        self.write("p/s1.jsonl", line(r) * 3)
        i = TokenIndex(self.root)
        i.refresh()
        t = self.totals(i.snapshot(), "s1")
        self.assertEqual((t["input"], t["output"], t["requests"]), (5, 6, 1))

    def test_dedupe_falls_back_to_uuid(self):
        r = usage_row("s1", "m1", "r1", "2026-10-20T10:00:00Z", 5, 6)
        del r["requestId"]
        self.write("p/s1.jsonl", line(r) * 2)
        i = TokenIndex(self.root)
        i.refresh()
        self.assertEqual(self.totals(i.snapshot(), "s1")["requests"], 1)

    def test_iterations_ignored(self):
        r = usage_row("s1", "m1", "r1", "2026-10-20T10:00:00Z", 5, 6, 7, 1, 2)
        self.write("p/s1.jsonl", line(r))
        i = TokenIndex(self.root)
        i.refresh()
        t = self.totals(i.snapshot(), "s1")
        self.assertEqual((t["input"], t["output"], t["cache_read"]), (5, 6, 7))

    def test_cache_write_split(self):
        self.write("p/s1.jsonl", line(usage_row("s1", "m1", "r1", "2026-10-20T10:00:00Z", w5=200, w1=300)))
        # no split given: everything counts as 5m
        r = usage_row("s1", "m2", "r2", "2026-10-20T10:00:01Z")
        r["message"]["usage"].pop("cache_creation")
        r["message"]["usage"]["cache_creation_input_tokens"] = 40
        self.write("p/s1.jsonl", line(r), "a")
        i = TokenIndex(self.root)
        i.refresh()
        t = self.totals(i.snapshot(), "s1")
        self.assertEqual((t["cache_write_5m"], t["cache_write_1h"]), (240, 300))

    def test_subagent_counts_for_parent(self):
        self.write("p/s1.jsonl", line(usage_row("s1", "m1", "r1", "2026-10-20T10:00:00Z", 1, 1)))
        self.write("p/s1/subagents/agent-a.jsonl", line(usage_row("s1", "m2", "r2", "2026-10-20T10:01:00Z", 10, 20)))
        i = TokenIndex(self.root)
        i.refresh()
        snap = i.snapshot()
        self.assertEqual(list(snap["sessions"]), ["s1"])
        t = self.totals(snap, "s1")
        self.assertEqual((t["input"], t["output"], t["requests"]), (11, 21, 2))

    def test_partial_trailing_line(self):
        full = line(usage_row("s1", "m1", "r1", "2026-10-20T10:00:00Z", 5, 5))
        second = line(usage_row("s1", "m2", "r2", "2026-10-20T10:01:00Z", 7, 7))
        p = self.write("p/s1.jsonl", full + second[:-20])
        i = TokenIndex(self.root)
        i.refresh()
        self.assertEqual(self.totals(i.snapshot(), "s1")["requests"], 1)
        self.assertEqual(i.snapshot()["errors"], 0)
        self.write("p/s1.jsonl", second[-20:], "a")
        i.refresh()
        t = self.totals(i.snapshot(), "s1")
        self.assertEqual((t["requests"], t["input"]), (2, 12))
        self.assertEqual(i.snapshot()["bytes_read"], len(second))

    def test_truncate_resets_without_double_count(self):
        a = line(usage_row("s1", "m1", "r1", "2026-10-20T10:00:00Z", 5, 5))
        b = line(usage_row("s1", "m2", "r2", "2026-10-20T10:01:00Z", 7, 7))
        self.write("p/s1.jsonl", a + b)
        i = TokenIndex(self.root)
        i.refresh()
        self.assertEqual(self.totals(i.snapshot(), "s1")["input"], 12)
        self.write("p/s1.jsonl", a)           # rewritten smaller
        i.refresh()
        t = self.totals(i.snapshot(), "s1")
        self.assertEqual((t["input"], t["requests"]), (5, 1))

    def test_append_reads_only_new_bytes(self):
        a = line(usage_row("s1", "m1", "r1", "2026-10-20T10:00:00Z"))
        b = line(usage_row("s1", "m2", "r2", "2026-10-20T10:01:00Z"))
        self.write("p/s1.jsonl", a)
        i = TokenIndex(self.root)
        i.refresh()
        self.assertEqual(i.snapshot()["bytes_read"], len(a))
        i.refresh()
        self.assertEqual(i.snapshot()["bytes_read"], 0)
        self.write("p/s1.jsonl", b, "a")
        i.refresh()
        snap = i.snapshot()
        self.assertEqual(snap["bytes_read"], len(b))
        self.assertEqual(snap["bytes_total"], len(a) + len(b))
        self.assertEqual(self.totals(snap, "s1")["requests"], 2)

    def test_bad_json_counts_error(self):
        self.write("p/s1.jsonl", '{"usage": broken\n' + line(usage_row("s1", "m1", "r1", "2026-10-20T10:00:00Z")))
        i = TokenIndex(self.root)
        i.refresh()
        snap = i.snapshot()
        self.assertEqual(snap["errors"], 1)
        self.assertEqual(self.totals(snap, "s1")["requests"], 1)

    def test_metadata_and_cost_state(self):
        self.write("p/s1.jsonl", line({"type": "agent-name", "agentName": "a1"})
                   + line(usage_row("s1", "m1", "r1", "2026-10-20T10:00:00Z", cwd="/w/one"))
                   + line({"type": "cost-state", "totalCostUSD": 1.0})
                   + line({"type": "cost-state", "totalCostUSD": 2.5}))
        i = TokenIndex(self.root)
        i.refresh()
        s = i.snapshot()["sessions"]["s1"]
        self.assertEqual((s["agent_name"], s["cwd"], s["cost_state"], s["project"]), ("a1", "/w/one", 2.5, "p"))
        self.assertEqual(s["first_ts"], "2026-10-20T10:00:00+00:00")

    def test_background_thread(self):
        self.write("p/s1.jsonl", line(usage_row("s1", "m1", "r1", "2026-10-20T10:00:00Z")))
        i = TokenIndex(self.root)
        self.assertFalse(i.snapshot()["ready"])
        i.start_background(interval=3600)
        for _ in range(100):
            if i.snapshot()["ready"]:
                break
            import time
            time.sleep(0.05)
        self.assertTrue(i.snapshot()["ready"])

    def test_privacy_canary(self):
        i = TokenIndex(FIX)
        i.refresh()
        snap = i.snapshot()
        self.assertTrue(snap["rows"])
        self.assertNotIn(CANARY, json.dumps(snap))
        con = make_db()
        self.assertNotIn(CANARY, json.dumps(attribute(snap, con)))
        self.assertNotIn(CANARY, json.dumps(rollup(snap, attribute(snap, con))))

    def test_fixture_totals(self):
        i = TokenIndex(FIX)
        i.refresh()
        t = self.totals(i.snapshot(), "synth-sess-0001")
        self.assertEqual(t, {"input": 117, "output": 58, "cache_read": 1000, "cache_write_5m": 200,
                             "cache_write_1h": 300, "requests": 3})


def make_db():
    con = sqlite3.connect(":memory:")
    con.row_factory = sqlite3.Row
    con.executescript("""
    CREATE TABLE agents (name TEXT PRIMARY KEY, role TEXT, goal_id TEXT, task_id TEXT, session_id TEXT,
                         started TEXT, ended TEXT);
    CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT, actor TEXT, entity TEXT, entity_id TEXT,
                         action TEXT, detail TEXT);""")
    return con


def snap_for(**sessions):
    """Minimal snapshot: sid -> dict(first_ts, agent_name, project, cwd)."""
    return {"rows": [], "sessions": {sid: {"first_ts": s.get("first_ts"), "last_ts": None, "agent_name": s.get("agent_name"),
                                           "project": s.get("project", "x"), "cwd": s.get("cwd"), "cost_state": None}
                                     for sid, s in sessions.items()}}


class AttributionTests(unittest.TestCase):
    def test_step1_agent_session_id(self):
        con = make_db()
        con.execute("INSERT INTO agents VALUES('coder-t-1','coder','G-1','T-1','sess-a','2026-10-20T09:00:00+00:00',NULL)")
        r = attribute(snap_for(**{"sess-a": {}}), con)["sess-a"]
        self.assertEqual((r["agent"], r["role"], r["task"], r["goal"], r["via"]), ("coder-t-1", "coder", "T-1", "G-1", "session"))

    def test_step2_session_change_event(self):
        con = make_db()
        con.execute("INSERT INTO agents VALUES('qa-t-2','qa','G-1','T-2','sess-new',NULL,NULL)")
        con.execute("INSERT INTO events(entity,entity_id,action,detail) VALUES('agent','qa-t-2','session-mismatch',?)",
                    (json.dumps({"name": "qa-t-2", "stored": "sess-old", "herdr": "sess-new"}),))
        r = attribute(snap_for(**{"sess-old": {}}), con)["sess-old"]
        self.assertEqual((r["agent"], r["via"]), ("qa-t-2", "event"))

    def test_step3_name_within_window(self):
        con = make_db()
        con.execute("INSERT INTO agents VALUES('staff-engineer','staff-engineer','G-1',NULL,'other',"
                    "'2026-10-20T10:00:00+00:00','2026-10-20T12:00:00+00:00')")
        s = snap_for(**{"in": {"agent_name": "staff-engineer", "first_ts": "2026-10-20T11:00:00+00:00"},
                        "out": {"agent_name": "staff-engineer", "first_ts": "2026-10-21T11:00:00+00:00"},
                        "before": {"agent_name": "staff-engineer", "first_ts": "2026-10-20T09:00:00+00:00"}})
        r = attribute(s, con)
        self.assertEqual((r["in"]["agent"], r["in"]["via"]), ("staff-engineer", "name"))
        for k in ("out", "before"):       # name reuse outside started..ended is not attributed
            self.assertIsNone(r[k]["agent"])
            self.assertEqual(r[k]["via"], "unattributed")

    def test_step3_open_ended_row(self):
        con = make_db()
        con.execute("INSERT INTO agents VALUES('lead-x','lead','G-1',NULL,NULL,'2026-10-20T10:00:00+00:00',NULL)")
        r = attribute(snap_for(s={"agent_name": "lead-x", "first_ts": "2026-10-22T00:00:00+00:00"}), con)["s"]
        self.assertEqual(r["via"], "name")

    def test_step4_worktree_regex(self):
        con = make_db()
        s = snap_for(a={"project": "-home-v-factory-worktrees-G-001-T-0002"},
                     b={"project": "-home-v", "cwd": "/home/v/factory/worktrees/G-002-T-0007"})
        r = attribute(s, con)
        self.assertEqual((r["a"]["goal"], r["a"]["task"], r["a"]["via"], r["a"]["agent"]), ("G-001", "T-0002", "worktree", None))
        self.assertEqual((r["b"]["goal"], r["b"]["task"], r["b"]["via"]), ("G-002", "T-0007", "worktree"))

    def test_step5_unattributed(self):
        r = attribute(snap_for(z={}), make_db())["z"]
        self.assertEqual((r["agent"], r["task"], r["goal"], r["via"]), (None, None, None, "unattributed"))

    def test_rollup_and_model_main(self):
        i = TokenIndex(FIX)
        i.refresh()
        snap = i.snapshot()
        att = attribute(snap, make_db())
        self.assertEqual(att["synth-sess-0001"]["model_main"], "claude-sonnet-5-5")
        self.assertEqual(att["synth-sess-0002"]["task"], "T-9000")
        roll = rollup(snap, att)
        self.assertEqual(roll["by_task"]["T-9000"]["requests"], 1)
        self.assertEqual(roll["by_task"]["unattributed"]["requests"], 3)
        self.assertEqual(roll["total"]["requests"], 4)
        self.assertEqual(set(roll["by_model"]), {"claude-sonnet-5-5", "claude-opus-5-5"})
        # sonnet 107 in, 53 out, 1000 cr, 200 w5, 300 w1 (+1/1 from sess-0002); opus fast 10/5
        sonnet = (108 * 2 + 54 * 10 + 1000 * 0.10 + 200 * 2.5 + 300 * 4) / 1e6
        opus = 2 * (10 * 4 + 5 * 20) / 1e6
        self.assertAlmostEqual(roll["total"]["cost"], sonnet + opus, places=9)

    def test_rollup_zero_token_unpriced_rows_and_all_unknown(self):
        def row(model, inp, out=0, day="2026-10-20"):
            return {"session": "s", "model": model, "day": day, "fast": False, "input": inp, "output": out,
                    "cache_read": 0, "cache_write_5m": 0, "cache_write_1h": 0, "requests": 1}
        snap = {"rows": [row("claude-sonnet-5-5", 1_000_000), row("<synthetic>", 0),
                         row("claude-nope", 500, day="2026-10-21"), row("<synthetic>", 0, day="2026-10-22")],
                "sessions": {}}
        roll = rollup(snap, {})
        tot = roll["total"]
        self.assertTrue(tot["cost_unknown"])             # the real unpriced row still counts
        self.assertAlmostEqual(tot["cost"], 2.0)
        self.assertFalse(roll["by_model"]["<synthetic>"]["cost_unknown"])   # zero tokens: nothing to price
        self.assertEqual(roll["by_model"]["<synthetic>"]["cost"], 0.0)
        self.assertEqual(roll["by_model"]["<synthetic>"]["requests"], 2)
        self.assertIsNone(roll["by_model"]["claude-nope"]["cost"])          # all-unknown bucket: n/a, not 0.0
        self.assertTrue(roll["by_model"]["claude-nope"]["cost_unknown"])
        self.assertIsNone(roll["by_day"]["2026-10-21"]["cost"])
        self.assertEqual(roll["by_day"]["2026-10-22"]["cost"], 0.0)
        only_synth = rollup({"rows": [row("<synthetic>", 0)], "sessions": {}}, {})
        self.assertFalse(only_synth["total"]["cost_unknown"])
        self.assertEqual(only_synth["total"]["cost"], 0.0)


class PriceTests(unittest.TestCase):
    U = {"input": 1_000_000, "output": 1_000_000, "cache_read": 1_000_000, "cache_write_5m": 1_000_000, "cache_write_1h": 1_000_000}

    def test_models(self):
        self.assertAlmostEqual(cost("claude-opus-5-5", self.U), 4 + 20 + 0.2 + 5 + 8)
        self.assertAlmostEqual(cost("claude-sonnet-5-5", self.U), 2 + 10 + 0.1 + 2.5 + 4)
        self.assertAlmostEqual(cost("claude-haiku-5-5", self.U), 0.1 + 0.5 + 0.01 + 0.125 + 0.2)

    def test_rate_table_matches_pricing_page(self):
        # per MTok, platform.claude.com pricing page checked 2026-10-25
        want = {
            "claude-opus-5-5": (4.00, 5.00, 8.00, 0.20, 20.00),
            "claude-sonnet-5-5": (2.00, 2.50, 4.00, 0.10, 10.00),
            "claude-haiku-5-5": (0.10, 0.125, 0.20, 0.01, 0.50),
        }
        for model, (inp, w5, w1, hit, out) in want.items():
            p = prices.PRICES[model]
            got = (p["input"], p["cache_write_5m"], p["cache_write_1h"], p["cache_read"], p["output"])
            self.assertEqual(got, (inp, w5, w1, hit, out), model)
        self.assertEqual(prices.HAIKU_LONG_PROMPT, {"input": 0.50, "cache_write_5m": 0.625, "cache_write_1h": 1.00,
                                                    "cache_read": 0.05, "output": 2.50})
        self.assertEqual(prices.FAST_MULTIPLIER, {"claude-opus-5-5": 2.0})  # fast: $8 in / $40 out

    def test_each_model_cost_one_unit_per_field(self):
        for model, one_of_each in (("claude-opus-5-5", 37.2), ("claude-sonnet-5-5", 18.6), ("claude-haiku-5-5", 0.935)):
            self.assertAlmostEqual(cost(model, self.U), one_of_each, places=9, msg=model)

    def test_sonnet_only_session_cost(self):
        u = {"input": 74, "output": 27009, "cache_read": 3_556_200, "cache_write_5m": 0, "cache_write_1h": 84_742}
        self.assertEqual(u["cache_write_5m"] + u["cache_write_1h"], 84_742)
        # 74*2 + 27009*10 + 3556200*0.10 + 84742*4 = 964826 / 1e6
        self.assertEqual(round(cost("claude-sonnet-5-5", u), 4), 0.9648)

    def test_partial_usage_and_fast(self):
        self.assertAlmostEqual(cost("claude-opus-5-5", {"input": 1000, "output": 500}), (4000 + 10000) / 1e6)
        self.assertAlmostEqual(cost("claude-opus-5-5", self.U, speed="fast"), 2 * 37.2)
        self.assertAlmostEqual(cost("claude-opus-5-5", self.U, speed="standard"), 37.2)

    def test_unknown_model(self):
        self.assertIsNone(cost("claude-nope", self.U))
        self.assertIsNone(cost("<synthetic>", {}))

    def test_as_of(self):
        self.assertEqual(prices.PRICES_AS_OF, "2026-10-25")


class UnloggedCallsGap(TmpMixin):
    """T-0134: Claude Code's cost-state also counts API calls (most likely auxiliary or background ones) that never get an
    assistant row in the JSONL. The indexer counts only logged, deduplicated responses, so it sits slightly below."""

    def test_counts_logged_responses_only(self):
        sid = "gap-sess"
        rows = [
            usage_row(sid, "m1", "r1", "2026-10-25T10:00:00Z", inp=10, out=100, cr=1000, w5=50),
            usage_row(sid, "m1", "r1", "2026-10-25T10:00:01Z", inp=10, out=100, cr=1000, w5=50),   # duplicate
            usage_row(sid, "m2", "r2", "2026-10-25T10:00:05Z", inp=12, out=200, cr=2000, w5=0),
        ]
        extra = [
            {"type": "api-request", "id": "req-3", "sessionId": sid, "querySource": "repl_main_thread"},  # no usage row
            {"type": "cost-state", "sessionId": sid, "totalCostUSD": 0.5,
             "modelUsage": {"claude-sonnet-5-5": {"inputTokens": 540, "outputTokens": 305,
                                                  "cacheReadInputTokens": 3300, "cacheCreationInputTokens": 60}}},
        ]
        self.write("proj/%s.jsonl" % sid, "".join(line(r) for r in rows + extra))
        idx = TokenIndex(self.root)
        idx.refresh()
        snap = idx.snapshot()
        t = self.totals(snap, sid)
        self.assertEqual((t["input"], t["output"], t["cache_read"], t["cache_write_5m"], t["requests"]),
                         (22, 300, 3000, 50, 2))
        self.assertEqual(snap["sessions"][sid]["cost_state"], 0.5)   # kept for display, never added to the tokens


if __name__ == "__main__":
    unittest.main()
