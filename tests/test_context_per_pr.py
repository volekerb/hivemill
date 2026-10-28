import json
import os
import subprocess
import sys
import unittest

from harness import REPO, FactoryTestCase

SECRET = "SECRET-CONTENT-PERPR"
WT = "/home/x/factory/worktrees/G-001-T-0005"


def line(sid, n, ts, inp=0, out=0, cr=0, cw=0, model="claude-sonnet-5-5", cwd="/x", extra=None):
    d = {"type": "assistant", "uuid": f"u-{sid}-{n}", "requestId": f"r-{sid}-{n}", "sessionId": sid, "timestamp": ts,
         "cwd": cwd, "message": {"id": f"m-{sid}-{n}", "model": model, "content": [{"type": "text", "text": SECRET},],
                                 "usage": {"input_tokens": inp, "output_tokens": out, "cache_read_input_tokens": cr,
                                           "cache_creation_input_tokens": cw}}}
    d.update(extra or {})
    return json.dumps(d)


class ContextPerPrTests(FactoryTestCase):
    """web.context per-pr as a subprocess against jsonl written here and a temp factory DB (TZ=UTC)."""

    def setUp(self):
        super().setUp()
        e = self.env
        self.projects = e.home / "claude-projects"
        (self.projects / "p").mkdir(parents=True)
        e.seed_goal()
        # T-0001: merged, 3 sessions. T-0002: done without merge verdict. T-0003: cancelled (has a merge verdict).
        # T-0004: merged, 1 reviewer reject + 1 qa fail. T-0005: merged, session found by worktree cwd only.
        # T-0006: merged last.
        for tid, status in (("T-0001", "done"), ("T-0002", "done"), ("T-0003", "cancelled"), ("T-0004", "done"),
                            ("T-0005", "done"), ("T-0006", "done")):
            e.seed_task(tid, status=status, pr_url=f"https://example.invalid/pr/{tid}")
        for i, (name, role, task, sid) in enumerate((
                ("zz-coder-1", "coder", "T-0001", "s-c1"), ("zz-qa-1", "qa", "T-0001", "s-q1"),
                ("zz-rev-1", "reviewer", "T-0001", "s-r1"), ("zz-coder-2", "coder", "T-0002", "s-c2"),
                ("zz-coder-3", "coder", "T-0003", "s-c3"), ("zz-coder-4", "coder", "T-0004", "s-c4"),
                ("zz-coder-6", "coder", "T-0006", "s-c6"), ("zz-director", "director", None, "s-d"),
                ("lead-g-001-a", "lead", None, "s-l"))):
            e.seed_agent(name, role, goal="G-001", task=task)
            e.sql("UPDATE agents SET session_id=? WHERE name=?", (sid, name))
        self.verdict("T-0001", "judge", "merge", "2026-10-20T12:00:00+00:00")
        self.verdict("T-0002", "judge", "reject", "2026-10-20T12:30:00+00:00")
        self.verdict("T-0003", "judge", "merge", "2026-10-20T13:00:00+00:00")
        self.verdict("T-0004", "reviewer", "reject", "2026-10-20T14:00:00+00:00")
        self.verdict("T-0004", "qa", "fail", "2026-10-20T15:00:00+00:00")
        self.verdict("T-0004", "qa", "pass", "2026-10-20T16:00:00+00:00")
        self.verdict("T-0004", "judge", "merge", "2026-10-21T12:00:00+00:00")
        self.verdict("T-0005", "judge", "merge", "2026-10-22T12:00:00+00:00")
        self.verdict("T-0006", "judge", "merge", "2026-10-23T12:00:00+00:00")
        lines = [
            line("s-c1", 1, "2026-10-19T10:00:00Z", inp=10, out=100, cr=1000, cw=500),
            line("s-c1", 2, "2026-10-19T10:05:00Z", inp=20, out=50, cr=2000, cw=0),
            line("s-c1", 3, "2026-10-19T10:06:00Z", inp=999, out=999, extra={"isSidechain": True}),
            line("s-q1", 1, "2026-10-19T11:00:00Z", inp=1, out=10, cr=300, cw=40),
            line("s-r1", 1, "2026-10-19T12:00:00Z", inp=2, out=20, cr=400, cw=0),
            line("s-c2", 1, "2026-10-19T12:00:00Z", inp=7777),
            line("s-c3", 1, "2026-10-19T12:00:00Z", inp=8888),
            line("s-c4", 1, "2026-10-20T09:00:00Z", inp=5, out=5, cr=5, cw=5),
            line("s-w", 1, "2026-10-21T09:00:00Z", inp=3, out=4, cr=100, cwd=WT + "/web"),
            line("s-c6", 1, "2026-10-22T09:00:00Z", inp=9, out=1),
            line("s-d", 1, "2026-10-20T08:00:00Z", inp=10, cr=90000),
            line("s-l", 1, "2026-10-21T08:00:00Z", inp=5, cw=4995),
            line("s-d", 2, "2026-10-24T08:00:00Z", inp=1000),
            line("s-u", 1, "2026-10-20T08:00:00Z", inp=5555, cwd="/x"),
        ]
        (self.projects / "p" / "all.jsonl").write_text("\n".join(lines) + "\n")

    def verdict(self, task, role, verdict, created):
        self.env.sql("INSERT INTO verdicts(task_id,role,verdict,created) VALUES(?,?,?,?)", (task, role, verdict, created))

    def run_ctx(self, *args, check=True):
        env = dict(os.environ, TZ="UTC", FACTORY_HOME=str(self.env.home_dir),
                   FACTORY_CLAUDE_PROJECTS=str(self.projects), PYTHONDONTWRITEBYTECODE="1")
        p = subprocess.run([sys.executable, "-m", "web.context", "per-pr", *args], capture_output=True, text=True,
                           cwd=REPO, env=env, timeout=60)
        if check:
            self.assertEqual(p.returncode, 0, p.stderr)
        return p

    def report(self, *args):
        return json.loads(self.run_ctx("--json", *args).stdout)

    def task(self, rep, tid):
        (m,) = [m for m in rep["tasks"] if m["task"] == tid]
        return m

    def test_help_lists_options(self):
        p = self.run_ctx("--help")
        for opt in ("--since", "--until", "--last", "--json"):
            self.assertIn(opt, p.stdout)
        self.assertIn("Sidechain (subagent) requests are excluded", p.stdout)

    def test_three_sessions_summed_and_unmerged_excluded(self):
        rep = self.report()
        m = self.task(rep, "T-0001")
        # c1: 10+1000+500 + 20+2000 = 3530 (sidechain skipped); q1: 1+300+40 = 341; r1: 2+400 = 402
        self.assertEqual((m["sessions"], m["input"], m["output"]), (3, 3530 + 341 + 402, 100 + 50 + 10 + 20))
        self.assertEqual(m["goal"], "G-001")
        self.assertEqual(m["pr_url"], "https://example.invalid/pr/T-0001")
        self.assertEqual(m["merged"], "2026-10-20T12:00:00+00:00")
        ids = [t["task"] for t in rep["tasks"]]
        self.assertNotIn("T-0002", ids)      # done, no judge merge verdict
        self.assertNotIn("T-0003", ids)      # cancelled
        self.assertEqual(ids, ["T-0006", "T-0005", "T-0004", "T-0001"])

    def test_cost_uses_price_table(self):
        # sonnet: in 2, out 10, cache_read 0.1, cache_write_5m 2.5 per Mtok; T-0004: 5 each
        expected = (5 * 2 + 5 * 10 + 5 * 0.1 + 5 * 2.5) / 1e6
        self.assertAlmostEqual(self.task(self.report(), "T-0004")["cost"], round(expected, 4), places=4)

    def test_unpriced_model_gives_null_cost(self):
        (self.projects / "p" / "x.jsonl").write_text(line("s-c6", 9, "2026-10-22T10:00:00Z", inp=5, model="mystery") + "\n")
        rep = self.report()
        self.assertIsNone(self.task(rep, "T-0006")["cost"])
        self.assertIsNotNone(self.task(rep, "T-0004")["cost"])

    def test_rejects_split_by_role(self):
        m = self.task(self.report(), "T-0004")
        self.assertEqual((m["rejects"], m["rejects_by_role"]), (2, {"qa": 1, "reviewer": 1}))
        self.assertEqual(self.task(self.report(), "T-0001")["rejects"], 0)
        self.assertEqual(self.report()["summary"]["rejects"], 2)

    def test_worktree_cwd_attributes_unclaimed_session(self):
        m = self.task(self.report(), "T-0005")
        self.assertEqual((m["sessions"], m["input"]), (1, 103))

    def test_unattributed_session_without_worktree_not_counted(self):
        self.assertNotIn(5555, [t["input"] for t in self.report()["tasks"]])

    def test_hq_not_in_tasks_and_reported_separately(self):
        rep = self.report()
        self.assertNotIn(90010, [t["input"] for t in rep["tasks"]])
        # director 90010 + 1000, lead 5000: no window, so everything
        self.assertEqual(rep["summary"]["hq_input"], 90010 + 5000 + 1000)
        self.assertEqual(rep["summary"]["hq_input_per_pr"], round(96010 / 4))
        self.assertIn("HQ input per merged PR", self.run_ctx().stdout)

    def test_hq_window_follows_since_until(self):
        s = self.report("--since", "2026-10-21", "--until", "2026-10-23")["summary"]
        self.assertEqual((s["n"], s["hq_input"]), (3, 5000))     # only the lead request on the 21st

    def test_last_n_most_recent_by_merge_verdict(self):
        rep = self.report("--last", "2")
        self.assertEqual([t["task"] for t in rep["tasks"]], ["T-0006", "T-0005"])
        self.assertEqual(rep["summary"]["n"], 2)
        # HQ window starts on the oldest selected merge's day (22nd): only the director's request on the 24th
        self.assertEqual(rep["summary"]["hq_input"], 1000)

    def test_since_until_filter_on_merge_time(self):
        ids = lambda *a: [t["task"] for t in self.report(*a)["tasks"]]
        self.assertEqual(ids("--since", "2026-10-21"), ["T-0006", "T-0005", "T-0004"])
        self.assertEqual(ids("--until", "2026-10-21"), ["T-0004", "T-0001"])
        self.assertEqual(ids("--since", "2026-10-21", "--until", "2026-10-22"), ["T-0005", "T-0004"])
        self.assertEqual(ids("--since", "2026-11-01"), [])

    def test_summary_keys_and_values(self):
        s = self.report()["summary"]
        for k in ("n", "median_input", "p75_input", "median_sessions", "median_cost", "rejects", "hq_input_per_pr"):
            self.assertIn(k, s)
        inputs = sorted([4273, 15, 103, 9])
        self.assertEqual(s["n"], 4)
        self.assertEqual(s["median_input"], (inputs[1] + inputs[2]) // 2)
        self.assertEqual(s["p75_input"], inputs[2])
        self.assertEqual(s["median_sessions"], 1)

    def test_median_input_not_truncated(self):
        e = self.env
        e.seed_task("T-0007", status="done", pr_url="https://example.invalid/pr/T-0007")
        e.seed_agent("zz-coder-7", "coder", goal="G-001", task="T-0007")
        e.sql("UPDATE agents SET session_id='s-c7' WHERE name='zz-coder-7'")
        self.verdict("T-0007", "judge", "merge", "2026-10-24T12:00:00+00:00")
        with open(self.projects / "p" / "all.jsonl", "a") as f:
            f.write(line("s-c7", 1, "2026-10-23T09:00:00Z", inp=4) + "\n")
        s = self.report("--since", "2026-10-23")["summary"]       # T-0006 input 9, T-0007 input 4
        self.assertEqual((s["n"], s["median_input"]), (2, 6.5))

    def test_merge_time_compares_instants_across_dst_offsets(self):
        e = self.env
        e.seed_task("T-0008", status="done", pr_url="https://example.invalid/pr/T-0008")
        # 02:30+02:00 = 00:30Z, 02:10+01:00 = 01:10Z: the +01:00 one is later though its text sorts lower
        self.verdict("T-0008", "judge", "merge", "2026-10-25T02:30:00+02:00")
        self.verdict("T-0008", "judge", "merge", "2026-10-25T02:10:00+01:00")
        self.assertEqual(self.task(self.report(), "T-0008")["merged"], "2026-10-25T01:10:00+00:00")

    def test_empty_window_summary_is_null_not_crash(self):
        s = self.report("--since", "2030-01-01")["summary"]
        self.assertEqual((s["n"], s["median_input"], s["hq_input_per_pr"]), (0, None, None))
        self.assertIn("n merged tasks: 0", self.run_ctx("--since", "2030-01-01").stdout)

    def test_bad_last_rejected(self):
        for bad in ("0", "-1", "x"):
            self.assertEqual(self.run_ctx("--last", bad, check=False).returncode, 2)

    def test_message_content_never_printed(self):
        self.assertIn(SECRET, (self.projects / "p" / "all.jsonl").read_text())
        for args in ((), ("--json",)):
            p = self.run_ctx(*args)
            self.assertNotIn(SECRET, p.stdout + p.stderr)

    def test_read_only(self):
        db = self.env.db_path
        files = [db, *self.projects.rglob("*.jsonl")]
        before = {f: (f.stat().st_mtime_ns, f.stat().st_size) for f in files}
        self.run_ctx()
        self.run_ctx("--json")
        self.assertEqual(before, {f: (f.stat().st_mtime_ns, f.stat().st_size) for f in files})


if __name__ == "__main__":
    unittest.main()
