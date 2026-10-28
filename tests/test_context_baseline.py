import json
import os
import shutil
import subprocess
import sys
import unittest
from pathlib import Path

from harness import REPO, FactoryTestCase

FIX = Path(__file__).parent / "fixtures" / "context" / "proj"
SECRET = "SECRET-CONTENT-123"


class ContextBaselineTests(FactoryTestCase):
    """web.context run as a subprocess against jsonl fixtures and a temp factory DB (TZ=UTC for local days)."""

    def setUp(self):
        super().setUp()
        e = self.env
        self.projects = e.home / "claude-projects"
        shutil.copytree(FIX, self.projects / "proj")
        e.seed_goal()
        e.seed_agent("zz-coder-a", "coder", goal="G-001", task="T-0001")
        e.seed_agent("zz-coder-b", "coder", goal="G-001", task="T-0002")
        e.seed_agent("zz-director", "director")
        for name, sid in (("zz-coder-a", "sess-a"), ("zz-director", "sess-h")):
            e.sql("UPDATE agents SET session_id=? WHERE name=?", (sid, name))
        # sess-b is attributed through the tick's session-change event, not agents.session_id
        e.sql("INSERT INTO events(ts,entity,entity_id,action,detail) VALUES('t','agent','zz-coder-b','session',?)",
              (json.dumps({"stored": "sess-b"}),))

    def run_ctx(self, *args, projects=None, check=True):
        env = dict(os.environ, TZ="UTC", FACTORY_HOME=str(self.env.home_dir),
                   FACTORY_CLAUDE_PROJECTS=str(projects or self.projects), PYTHONDONTWRITEBYTECODE="1")
        p = subprocess.run([sys.executable, "-m", "web.context", "baseline", *args], capture_output=True,
                           text=True, cwd=REPO, env=env, timeout=60)
        if check:
            self.assertEqual(p.returncode, 0, p.stderr)
        return p

    def report(self, *args):
        return json.loads(self.run_ctx("--json", *args).stdout)

    def test_help_lists_options(self):
        out = self.run_ctx("--help").stdout
        for opt in ("--since", "--until", "--json"):
            self.assertIn(opt, out)

    def test_json_top_level_keys(self):
        self.assertEqual(set(self.report()), {"window", "fixed_prefix", "input_per_task", "persistent"})

    def test_fixed_prefix_is_first_request_context(self):
        # sess-a first request: input 10 + cache_read 1000 + cache_write 500 = 1510 (later requests are 2000, 3000)
        fp = self.report()["fixed_prefix"]["coder"]
        self.assertEqual(fp["n"], 2)                       # sess-a (1510) and sess-b (500)
        self.assertEqual((fp["min"], fp["max"]), (500, 1510))
        self.assertEqual(fp["median"], 1005)
        self.assertEqual(self.report()["fixed_prefix"]["director"]["median"], 100000)

    def test_resumed_session_has_one_prefix(self):
        # a-resume.jsonl repeats a1 and adds a4 under the same session: still one prefix for sess-a
        rep = self.report()
        self.assertEqual(rep["fixed_prefix"]["coder"]["n"], 2)
        self.assertEqual(rep["input_per_task"]["coder"]["n"], 2)

    def test_sidechain_excluded_from_prefix_and_counted(self):
        # the sidechain line (99999 tokens) is 1 minute before a1; it must not become the prefix
        rep = self.report()
        self.assertEqual(rep["fixed_prefix"]["coder"]["max"], 1510)
        self.assertEqual(rep["window"]["sidechain_requests"], 1)

    def test_duplicate_request_across_files_counted_once(self):
        rep = self.report()
        self.assertEqual(rep["window"]["duplicate_requests"], 2)   # b1 in b2.jsonl, a1 in a-resume.jsonl
        self.assertEqual(rep["window"]["requests"], 10)            # a:4 b:2 h:4
        # zz-coder-b: 500 + 700 = 1200 (b1 not doubled); zz-coder-a: 1510+2000+3000+4000 = 10510
        t = self.report()["input_per_task"]["coder"]
        self.assertEqual((t["n"], t["median"], t["p90"]), (2, (1200 + 10510) // 2, 10510))

    def test_malformed_line_skipped_and_counted(self):
        rep = self.report()
        self.assertEqual(rep["window"]["malformed_lines"], 1)
        self.assertEqual(rep["window"]["empty_sessions"], 1)

    def test_malformed_line_without_usage_is_counted(self):
        (self.projects / "proj" / "bad.jsonl").write_text('{"type":"user","message":{broken}\n')
        self.assertEqual(self.report()["window"]["malformed_lines"], 2)

    def test_persistent_first_respects_since(self):
        d = {"type": "assistant", "uuid": "u-h5", "requestId": "req-h5", "sessionId": "sess-h",
             "timestamp": "2026-10-21T08:00:00Z", "cwd": "/x",
             "message": {"id": "h5", "usage": {"input_tokens": 7, "cache_read_input_tokens": 0,
                                               "cache_creation_input_tokens": 0}}}
        (self.projects / "proj" / "h2.jsonl").write_text(json.dumps(d) + "\n")
        (p,) = self.report()["persistent"]
        self.assertEqual(p["first"], 100000)                      # no window: the session's first request
        (p,) = self.report("--since", "2026-10-21")["persistent"]
        self.assertEqual((p["requests"], p["first"], p["last"]), (1, 7, 7))

    def test_help_states_sidechain_rule(self):
        out = self.run_ctx("--help").stdout
        self.assertIn("Sidechain", out)
        self.assertIn("excluded from fixed_prefix", out)

    def test_unattributed_session_reported_and_excluded(self):
        rep = self.report()
        self.assertEqual(rep["window"]["unattributed_sessions"], 1)    # sess-u
        self.assertNotIn(9000, [rep["fixed_prefix"][r]["max"] for r in rep["fixed_prefix"]])
        self.assertEqual(rep["fixed_prefix"]["coder"]["n"], 2)

    def test_since_until_filter(self):
        rep = self.report("--since", "2026-10-21")
        self.assertEqual(rep["window"]["requests"], 1)                 # only a4
        self.assertEqual(rep["window"]["since"], "2026-10-21")
        self.assertNotIn("coder", rep["fixed_prefix"])                 # a's first request is on the 20th
        self.assertEqual(self.report("--until", "2026-10-20")["window"]["requests"], 9)
        self.assertEqual(self.report("--since", "2026-10-22")["window"]["requests"], 0)

    def test_persistent_hourly_series_and_big_count(self):
        (p,) = self.report()["persistent"]
        self.assertEqual((p["agent"], p["session"], p["requests"]), ("zz-director", "sess-h", 4))
        self.assertEqual((p["first"], p["last"], p["max"]), (100000, 50000, 300000))
        self.assertEqual(p["hourly_max"], {"2026-10-20T08:00": 300000, "2026-10-20T09:00": 260000})
        self.assertEqual(p["over_250k"], 2)

    def test_input_per_task_sums_per_agent(self):
        rep = self.report()
        self.assertEqual(rep["input_per_task"]["coder"], {"n": 2, "median": 5855, "p90": 10510})
        self.assertNotIn("director", rep["input_per_task"])            # no task_id

    def test_message_content_never_printed(self):
        self.assertIn(SECRET, (FIX / "a.jsonl").read_text())
        for args in ((), ("--json",)):
            p = self.run_ctx(*args)
            self.assertNotIn(SECRET, p.stdout + p.stderr)

    def test_text_report_sections(self):
        out = self.run_ctx().stdout
        for s in ("window:", "fixed_prefix", "input_per_task", "persistent", "zz-director"):
            self.assertIn(s, out)

    def test_read_only(self):
        db = self.env.db_path
        files = [db, *self.projects.rglob("*.jsonl")]
        before = {f: (f.stat().st_mtime_ns, f.stat().st_size) for f in files}
        self.run_ctx()
        self.run_ctx("--json")
        self.assertEqual(before, {f: (f.stat().st_mtime_ns, f.stat().st_size) for f in files})
        self.assertIn("mode=ro", (REPO / "web" / "context.py").read_text())

    def test_missing_db_fails_with_hint(self):
        env = dict(os.environ, FACTORY_HOME=str(self.env.home), PYTHONDONTWRITEBYTECODE="1")
        p = subprocess.run([sys.executable, "-m", "web.context", "baseline"], capture_output=True, text=True,
                           cwd=REPO, env=env, timeout=60)
        self.assertEqual(p.returncode, 2)
        self.assertIn("factory database not found", p.stderr)
        self.assertIn("FACTORY_HOME", p.stderr)
        self.assertEqual(sorted(p.name for p in self.env.home.glob("*.db")), [])

    def test_bad_date_rejected(self):
        self.assertEqual(self.run_ctx("--since", "yesterday", check=False).returncode, 2)


if __name__ == "__main__":
    unittest.main()
