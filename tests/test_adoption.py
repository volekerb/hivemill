import hashlib
import json
import os
import shutil
import subprocess
import sys
import unittest
from pathlib import Path

from harness import REPO, FactoryTestCase

FIX = Path(__file__).parent / "fixtures" / "adoption" / "proj"
URL = "https://example.com/o/r/pull/%d"


def pr(files, add=10, dele=0):
    return {"files": [{"path": p, "additions": 1, "deletions": 0} for p in files], "additions": add, "deletions": dele}


class AdoptionTests(FactoryTestCase):
    """web.adoption run as a subprocess against synthetic jsonl fixtures, a temp factory DB and the gh stub (TZ=UTC)."""

    def setUp(self):
        super().setUp()
        e = self.env
        self.projects = e.home / "claude-projects"
        shutil.copytree(FIX, self.projects / "proj")
        e.seed_goal()
        merged = {1: "2026-10-10", 2: "2026-10-12", 3: "2026-10-15", 4: "2026-09-01",
                  5: "2026-10-16", 6: "2026-10-16", 7: "2026-10-16", 8: "2026-10-16", 9: "2026-10-16",
                  10: "2026-10-16"}
        e.seed_task("T-0001", status="done", pr_url=URL % 1)
        e.sql("INSERT INTO verdicts(task_id,pr_url,role,verdict,created) VALUES('T-0001',?,'judge','reject',?)",
              (URL % 1, "2026-10-09T12:00:00+00:00"))
        for n, day in merged.items():
            tid = f"T-{n:04d}"
            if n != 1:
                e.seed_task(tid, status="done", pr_url=URL % n)
            e.sql("INSERT INTO verdicts(task_id,pr_url,role,verdict,created) VALUES(?,?,'judge','merge',?)",
                  (tid, URL % n, f"{day}T12:00:00+00:00"))
        for name, role, task, sid in (
                ("zz-coder-1", "coder", "T-0001", "sess-c1"), ("zz-reviewer-1", "reviewer", "T-0001", "sess-r1"),
                ("zz-qa-1", "qa", "T-0001", "sess-q1"), ("zz-judge-1", "judge", "T-0001", "sess-j1"),
                ("zz-coder-2", "coder", "T-0002", "sess-c2"), ("zz-reviewer-2", "reviewer", "T-0002", "sess-r2"),
                ("zz-coder-3", "coder", "T-0003", "sess-c3"), ("zz-coder-4", "coder", "T-0004", "sess-c4"),
                ("zz-cleaner-1", "cleaner", None, "sess-cl1"), ("zz-scout-1", "scout", None, "sess-sc1"),
                ("zz-cleaner-2", "cleaner", None, "sess-cl2")):
            e.seed_agent(name, role, goal="G-001", task=task)
            e.sql("UPDATE agents SET session_id=? WHERE name=?", (sid, name))
        self.prs = {
            URL % 1: pr(["docs/x.md"], 20),
            URL % 2: pr(["bin/factory", "docs/x.md"], 400),      # bin/factory alone no longer gates
            URL % 5: pr(["roles/hooks/x"]),
            URL % 6: pr(["roles/settings/x.json"]),
            URL % 7: pr(["ci/x"]),
            URL % 8: pr(["web/x.py"], 300, 201),                # 501 changed lines
            URL % 9: pr(["docs/y.md"], 250, 250),               # 500 changed lines: not over the limit
            URL % 10: pr([".github/workflows/x.yml"]),
        }                                               # PR 3 is missing: gh fails
        e.set_gh(prs=self.prs)

    def run_cli(self, *args, projects=None, extra=None, check=True):
        env = dict(self.env.env(extra=extra), TZ="UTC",
                   FACTORY_CLAUDE_PROJECTS=str(projects or self.projects))
        p = subprocess.run([sys.executable, "-m", "web.adoption", "report", *args], capture_output=True,
                           text=True, cwd=REPO, env=env, timeout=60)
        if check:
            self.assertEqual(p.returncode, 0, p.stderr)
        return p

    def report(self, *args, **kw):
        return json.loads(self.run_cli("--since", "2026-10-01", "--until", "2026-10-31", "--json", *args, **kw).stdout)

    def task(self, rep, tid):
        return next(t for t in rep["tasks"] if t["task"] == tid)

    def test_window_selects_merged_tasks(self):
        rep = self.report()
        self.assertEqual([t["task"] for t in rep["tasks"]], [f"T-{n:04d}" for n in (1, 2, 3, 5, 6, 7, 8, 9, 10)])
        self.assertEqual(rep["window"]["merged_tasks"], 9)    # T-0004 merged 2026-09-01 is out
        self.assertEqual(rep["window"]["unattributed_sessions"], 1)

    def test_tokens_per_merged_pr_by_role(self):
        # coder sess-c1: 1510 + 2000 + 1000 + 1000 + 500 (sidechain 99999 and the resumed duplicate not counted)
        k = self.task(self.report(), "T-0001")["tokens"]
        self.assertEqual((k["coder"], k["reviewer"], k["qa"], k["judge"]), (6010, 3000, 300, 200))
        self.assertEqual(k["other"], "unknown")
        self.assertEqual(k["total"], 9510)
        self.assertTrue(k["complete"])
        self.assertEqual(self.report()["window"]["duplicate_requests"], 1)

    def test_tokens_match_web_context_formula(self):
        sys.path.insert(0, str(REPO))
        from web import context
        con = context.connect_ro(self.env.db_path)
        try:
            ctx = context.baseline(self.projects, con)["input_per_task"]["coder"]
        finally:
            con.close()
        coders = [self.task(self.report(), t)["tokens"]["coder"] for t in ("T-0001", "T-0002")]
        # web.context sums context per task agent: sess-c1 6010, sess-c2 700, sess-c4 5000 (T-0003 has no usage)
        self.assertEqual(ctx["n"], 3)
        self.assertEqual(coders, [6010, 700])
        self.assertEqual(ctx["median"], 5000)

    def test_medians_skip_unknown(self):
        m = self.report()["medians"]["tokens_per_merged_pr"]
        self.assertEqual(m["coder"], (6010 + 700) // 2)
        self.assertEqual(m["reviewer"], (3000 + 800) // 2)
        self.assertEqual(m["total"], (9510 + 1500) // 2)      # T-0002: coder 700 + reviewer 800

    def test_reviewer_workflow_fields(self):
        t1 = self.task(self.report(), "T-0001")["reviewer"]
        # agent-1 (400 + 600) is linked to Workflow call w1; agent-2 (5000) belongs to Agent call a1 and is not counted
        self.assertEqual((t1["tokens"], t1["workflow_calls"], t1["workflow_subagent_tokens"]), (3000, 2, 1000))
        t2 = self.task(self.report(), "T-0002")["reviewer"]
        self.assertEqual((t2["workflow_calls"], t2["workflow_subagent_tokens"]), (1, "not logged"))
        self.assertEqual(self.report()["medians"]["reviewer_workflow_calls"], 1)   # median of 2, 1
        self.assertEqual(self.task(self.report(), "T-0003")["reviewer"]["workflow_calls"], "unknown")

    def test_coder_ci_wait_and_search(self):
        c = self.task(self.report(), "T-0001")["coder"]
        self.assertEqual(c["ci_checks_calls"], 2)
        self.assertEqual(c["ci_checks_background"], 1)
        self.assertEqual(c["turns_pr_create_to_done"], 2)      # two assistant turns between create and done
        self.assertEqual(c["explore_calls"], 1)                # general-purpose Agent call is not Explore
        self.assertEqual(c["search_results"], 3)               # Read, Grep, Glob; the Bash result and sidechain not
        self.assertEqual(c["search_result_chars"], 4 + 6 + 10)

    def test_scope_gate(self):
        rep = self.report()
        got = {t["task"]: t["reviewer"]["scope_gate"]["gated"] for t in rep["tasks"]}
        self.assertEqual(got, {"T-0001": False, "T-0002": False, "T-0003": "unknown", "T-0005": True,
                               "T-0006": True, "T-0007": True, "T-0008": True, "T-0009": False, "T-0010": True})
        self.assertEqual(self.task(rep, "T-0002")["reviewer"]["scope_gate"]["changed_lines"], 400)
        self.assertEqual(self.task(rep, "T-0005")["reviewer"]["scope_gate"]["reasons"], ["roles/hooks/x"])
        self.assertEqual(self.task(rep, "T-0008")["reviewer"]["scope_gate"]["reasons"], ["501 changed lines"])
        self.assertEqual(self.task(rep, "T-0010")["reviewer"]["scope_gate"]["reasons"], [".github/workflows/x.yml"])
        self.assertEqual(rep["scope_gate_summary"], {"gated": 5, "not_gated": 3, "unknown": 1})

    def test_missing_data_is_unknown_not_zero(self):
        rep = self.report()
        t3 = self.task(rep, "T-0003")
        self.assertEqual(t3["tokens"]["coder"], "unknown")      # session exists but has no usage
        self.assertEqual(t3["tokens"]["total"], "unknown")
        self.assertFalse(t3["tokens"]["complete"])
        self.assertEqual(t3["coder"]["ci_checks_calls"], "unknown")
        self.assertEqual(self.task(rep, "T-0002")["tokens"]["qa"], "unknown")   # no qa agent
        self.assertEqual(t3["reviewer"]["scope_gate"]["gated"], "unknown")      # gh failed, exit 0

    def test_cleaner_scout_cost(self):
        by = {s["agent"]: s for s in self.report()["cost_sessions"]}
        self.assertEqual(set(by), {"zz-cleaner-1", "zz-scout-1"})   # zz-cleaner-2 ran in September
        self.assertEqual(by["zz-cleaner-1"]["usd"], 0.6)          # 1M in * 0.10 + 1M out * 0.50
        self.assertEqual(by["zz-scout-1"]["usd"], "unknown")
        self.assertEqual(by["zz-scout-1"]["unpriced_models"], ["claude-mystery-9"])

    def test_gh_not_installed_is_unknown(self):
        e = self.env
        (e.stub_bin / "gh").unlink()
        empty = e.home / "empty-path"
        empty.mkdir()
        rep = json.loads(self.run_cli("--json", "--since", "2026-10-01", extra={"PATH": str(empty)}, check=False).stdout
                         or "{}")
        # python itself is found by absolute path; with no gh on PATH every gate is unknown
        self.assertEqual(rep["scope_gate_summary"], {"gated": 0, "not_gated": 0, "unknown": 9})

    def test_text_report(self):
        out = self.run_cli("--since", "2026-10-01", "--until", "2026-10-31").stdout
        for needle in ("tokens per merged PR", "T-0001", "9,510", "not logged", "unknown", "zz-cleaner-1"):
            self.assertIn(needle, out)

    def test_read_only(self):
        db = self.env.db_path

        def snap(root):
            return {str(p): hashlib.sha256(p.read_bytes()).hexdigest() + str(p.stat().st_mtime_ns)
                    for p in sorted(Path(root).rglob("*")) if p.is_file()}
        before_db = (hashlib.sha256(db.read_bytes()).hexdigest(), db.stat().st_mtime_ns)
        before_tr = snap(self.projects)
        self.run_cli("--json")
        self.assertEqual((hashlib.sha256(db.read_bytes()).hexdigest(), db.stat().st_mtime_ns), before_db)
        self.assertEqual(snap(self.projects), before_tr)
        # (a WAL database may get empty -shm/-wal side files from any reader; the db file itself is untouched)

    def test_db_opened_read_only(self):
        src = (REPO / "web" / "adoption.py").read_text()
        self.assertIn("connect_ro", src)
        sys.path.insert(0, str(REPO))
        from web import context
        con = context.connect_ro(self.env.db_path)
        try:
            with self.assertRaises(Exception):
                con.execute("UPDATE tasks SET title='x'")
        finally:
            con.close()

    def test_missing_db_exits_2(self):
        env = dict(self.env.env(), FACTORY_HOME=str(self.env.home))
        p = subprocess.run([sys.executable, "-m", "web.adoption", "report"], capture_output=True, text=True,
                           cwd=REPO, env=env, timeout=60)
        self.assertEqual(p.returncode, 2)
        self.assertIn("factory database not found", p.stderr)
        self.assertIn("FACTORY_HOME", p.stderr)

    def test_no_transcript_content_in_output(self):
        out = self.run_cli("--json").stdout
        self.assertNotIn("sidechain result not counted", out)

    def test_adoption_doc_exists(self):
        self.assertTrue((REPO / "docs" / "adoption-metrics.md").is_file())


CWD = "/zz/worktrees/G-001-T-%s"


def tline(sid, n, ts, tools=(), ctx=(0, 0, 0), cwd=None):
    """One assistant request with tool_use blocks [(id, name, input)] and usage (input, cache_read, cache_write)."""
    d = {"type": "assistant", "uuid": f"u-{sid}-{n}", "requestId": f"r-{sid}-{n}", "sessionId": sid,
         "timestamp": ts, "message": {"id": f"m-{sid}-{n}", "model": "claude-sonnet-5-5",
                                      "content": [{"type": "tool_use", "id": i, "name": nm, "input": inp}
                                                  for i, nm, inp in tools],
                                      "usage": {"input_tokens": ctx[0], "output_tokens": 5,
                                                "cache_read_input_tokens": ctx[1],
                                                "cache_creation_input_tokens": ctx[2]}}}
    if cwd:
        d["cwd"] = cwd
    return d


def tres(sid, tool_id, text):
    return {"type": "user", "sessionId": sid,
            "message": {"content": [{"type": "tool_result", "tool_use_id": tool_id, "content": text}]}}


def bash(i, cmd):
    return (f"b{i}", "Bash", {"command": cmd})


class SessionCoverageTests(FactoryTestCase):
    """T-0262: retry/respawn sessions, Bash-based coder search and turns across coder sessions (all synthetic)."""

    def setUp(self):
        super().setUp()
        e = self.env
        self.projects = e.home / "claude-projects"
        (self.projects / "proj").mkdir(parents=True)
        e.seed_goal()
        for n in (20, 21, 22):
            e.seed_task(f"T-00{n}", status="done", pr_url=URL % n)
            e.sql("INSERT INTO verdicts(task_id,pr_url,role,verdict,created) VALUES(?,?,'judge','merge',?)",
                  (f"T-00{n}", URL % n, "2026-10-20T12:00:00+00:00"))
        e.set_gh(prs={URL % 20: pr(["docs/x.md"], 20)})

    def agent(self, name, role, task, sid):
        self.env.seed_agent(name, role, goal="G-001", task=task)
        self.env.sql("UPDATE agents SET session_id=? WHERE name=?", (sid, name))

    def write(self, sid, records):
        (self.projects / "proj" / f"{sid}.jsonl").write_text("".join(json.dumps(r) + "\n" for r in records))

    def fallback(self, sid, name, task, records):
        """A session no agents row or tick event knows: only its cwd and its agent-name record identify it."""
        head = {"type": "agent-name", "agentName": name, "sessionId": sid}
        self.write(sid, [head] + [dict(r, cwd=CWD % task) if r["type"] == "assistant" else r for r in records])

    def report(self):
        env = dict(self.env.env(), TZ="UTC", FACTORY_CLAUDE_PROJECTS=str(self.projects))
        p = subprocess.run([sys.executable, "-m", "web.adoption", "report", "--json"], capture_output=True, text=True,
                           cwd=REPO, env=env, timeout=60)
        self.assertEqual(p.returncode, 0, p.stderr)
        return json.loads(p.stdout)

    def build_t20(self):
        """T-0020: 2 coder attempts (attempt 1 replaced its agents row), 2 reviewers, a judge."""
        self.agent("zz-coder-t20", "coder", "T-0020", "c-2")
        self.agent("zz-reviewer-t20", "reviewer", "T-0020", "r-2")
        self.agent("zz-judge-t20", "judge", "T-0020", "j-1")
        self.fallback("c-1", "zz-coder-t20", "0020", [
            tline("c-1", 1, "2026-10-19T10:00:00Z", [("t1", "Read", {"file_path": "x"})], (10, 1000, 100)),
            tres("c-1", "t1", "r" * 7),
            tline("c-1", 2, "2026-10-19T10:01:00Z", [bash(1, "head -20 web/x.py")], (0, 2000, 0)),
            tres("c-1", "b1", "h" * 11),
            tline("c-1", 3, "2026-10-19T10:02:00Z", [bash(2, "gh pr create --fill")], (0, 3000, 0)),
            tres("c-1", "b2", "created, not a search"),
        ])
        self.write("c-2", [
            tline("c-2", 1, "2026-10-20T10:00:00Z", [bash(3, "git status")], (5, 4000, 0)),
            tres("c-2", "b3", "s" * 99),
            tline("c-2", 2, "2026-10-20T10:01:00Z", [bash(4, "cd /zz/w && grep -rn foo web/")], (0, 4100, 0)),
            tres("c-2", "b4", "g" * 13),
            tline("c-2", 3, "2026-10-20T10:02:00Z", [bash(5, "cat > f.py <<'EOF'\nx\nEOF")], (0, 4200, 0)),
            tres("c-2", "b5", "w" * 99),
            tline("c-2", 4, "2026-10-20T10:03:00Z", [bash(6, "python3 -m unittest 2>&1 | tail -5")], (0, 4300, 0)),
            tres("c-2", "b6", "p" * 99),
            tline("c-2", 5, "2026-10-20T10:04:00Z", [bash(7, "sed -n 1,5p web/x.py")], (0, 4400, 0)),
            tres("c-2", "b7", "n" * 17),
            tline("c-2", 6, "2026-10-20T10:05:00Z", [bash(8, "sed -i s/a/b/ web/x.py")], (0, 4500, 0)),
            tres("c-2", "b8", "e" * 99),
            tline("c-2", 7, "2026-10-20T10:06:00Z", [bash(9, "ls web")], (0, 4600, 0)),
            tres("c-2", "b9", "l" * 19),
            tline("c-2", 8, "2026-10-20T10:07:00Z", [("t2", "Grep", {"pattern": "p"})], (0, 4700, 0)),
            tres("c-2", "t2", "G" * 23),
            tline("c-2", 9, "2026-10-20T10:08:00Z", [bash(10, "factory done 'x'")], (0, 4800, 0)),
        ])
        self.fallback("r-1", "zz-reviewer-t20", "0020", [
            tline("r-1", 1, "2026-10-19T12:00:00Z", (), (1, 500, 0))])
        self.write("r-2", [tline("r-2", 1, "2026-10-20T11:00:00Z", (), (2, 600, 0))])
        self.write("j-1", [tline("j-1", 1, "2026-10-20T12:00:00Z", (), (0, 700, 50))])

    def test_all_sessions_attributed_to_their_role_and_match_web_context(self):
        self.build_t20()
        rep = self.report()
        t = next(x for x in rep["tasks"] if x["task"] == "T-0020")["tokens"]
        coder = (10 + 1000 + 100) + 2000 + 3000 + sum(range(4000, 4900, 100)) + 5
        self.assertEqual((t["coder"], t["reviewer"], t["judge"]), (coder, 501 + 602, 750))
        self.assertEqual((t["qa"], t["other"]), ("unknown", "unknown"))
        self.assertEqual(t["total"], coder + 1103 + 750)
        self.assertEqual(coder, 45715)     # 6110 (attempt 1) + 39605 (attempt 2)
        self.assertEqual(rep["window"]["unattributed_sessions"], 0)
        sys.path.insert(0, str(REPO))
        from web import context
        con = context.connect_ro(self.env.db_path)
        try:
            per = context.per_pr(self.projects, con)["tasks"]
        finally:
            con.close()
        ctx = next(x for x in per if x["task"] == "T-0020")
        self.assertEqual((ctx["sessions"], ctx["input"]), (5, t["total"]))

    def test_coder_search_counts_bash_and_tool_results(self):
        self.build_t20()
        c = next(x for x in self.report()["tasks"] if x["task"] == "T-0020")["coder"]
        # Read 7 + head 11 | grep 13 + sed -n 17 + ls 19 + Grep 23; git status, cat > <<EOF, | tail, sed -i, gh not
        self.assertEqual(c["search_results"], 6)
        self.assertEqual(c["search_result_chars"], 7 + 11 + 13 + 17 + 19 + 23)

    def test_turns_across_coder_sessions_and_unknown_cases(self):
        self.build_t20()
        # T-0021: PR created, no `factory done` in any transcript. T-0022: `factory done` but no `gh pr create`.
        self.agent("zz-coder-t21", "coder", "T-0021", "c-21")
        self.agent("zz-coder-t22", "coder", "T-0022", "c-22")
        self.write("c-21", [tline("c-21", 1, "2026-10-20T10:00:00Z", [bash(1, "gh pr create --fill")], (0, 10, 0))])
        self.write("c-22", [tline("c-22", 1, "2026-10-20T10:00:00Z", [bash(1, "factory done 'x'")], (0, 10, 0))])
        rep = self.report()
        got = {x["task"]: x["coder"]["turns_pr_create_to_done"] for x in rep["tasks"]}
        # PR in attempt 1 (turn 3 of 3), done on turn 9 of attempt 2: 0 left + 8 turns before the done turn
        self.assertEqual(got, {"T-0020": 8, "T-0021": "unknown", "T-0022": "unknown"})

    def test_json_field_names_unchanged(self):
        self.build_t20()
        rep = self.report()
        t = rep["tasks"][0]
        self.assertEqual(sorted(rep), ["cost_sessions", "medians", "scope_gate_summary", "tasks", "window"])
        self.assertEqual(sorted(rep["window"]), ["duplicate_requests", "files", "malformed_lines", "merged_tasks",
                                                  "since", "unattributed_sessions", "until"])
        self.assertEqual(sorted(t), ["coder", "goal", "merged_day", "pr_url", "reviewer", "task", "tokens"])
        self.assertEqual(sorted(t["tokens"]), ["coder", "complete", "judge", "other", "qa", "reviewer", "total"])
        self.assertEqual(sorted(t["reviewer"]), ["scope_gate", "tokens", "workflow_calls", "workflow_subagent_tokens"])
        self.assertEqual(sorted(t["coder"]), ["ci_checks_background", "ci_checks_calls", "explore_calls",
                                              "search_result_chars", "search_results", "turns_pr_create_to_done"])
        self.assertEqual(sorted(rep["medians"]), ["coder", "reviewer_workflow_calls", "tokens_per_merged_pr"])


if __name__ == "__main__":
    unittest.main()
