"""T-1510 (G-031 SC3): a task agent started with an explicit --host-backend keeps that backend when the tick starts the task's
agents again (coder resume on red CI, review rerun, review autospawn, judge autospawn). Without the flag the config decides."""
import json
import os
import unittest

from harness import FactoryTestCase
from test_ci_handoff_red import Base, SHA, RED, pr_url

T = "T-0001"
CODER = "coder-t-0001"


class PinBase(Base):
    def setUp(self):
        super().setUp()
        self.ok("config", "terminal_backend", "herdr", agent="owner")
        self.ok("config", "max_live_agents", "99", agent="owner")
        self.hc_state = self.env.home / "hc.json"
        self.hc_state.write_text("{}")
        os.environ["FAKE_HC_STATE"] = str(self.hc_state)  # in-process spawn() runs the hc stub as a child
        self.addCleanup(os.environ.pop, "FAKE_HC_STATE", None)
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "w"}],
            tabs=[{"tab_id": "t1", "workspace_id": "w1", "label": "x", "pane_count": 1}],
            panes=[{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1"}],
            agents=[])

    def spawn_cli(self, *extra):
        self.ok("spawn", "coder", "--goal", "G-001", "--team", "g-001-a", "--task", T, "--name", CODER, *extra,
                agent="owner", extra_env={"FAKE_HC_STATE": str(self.hc_state)})

    def args(self, name=CODER):
        return json.loads(self.env.sql("SELECT spawn_args FROM agents WHERE name=?", (name,))[0][0])

    def hc_runs(self):
        return [c for c in self.env.calls("hc") if c["argv"][:2] == ["run", "--bg"] and CODER in c["argv"]]

    def herdr_starts(self):
        return [c for c in self.env.calls("herdr") if c["argv"][:3] == ["agent", "start", CODER]]

    def red_resume(self):
        """Task in_review with a red head and a done coder row (as left by spawn_cli), then the real-spawn resume tick."""
        self.env.sql("UPDATE agents SET status='done', pane_id=NULL, session_id='sess-1' WHERE name=?", (CODER,))
        self.env.sql("UPDATE tasks SET status='in_review', pr_url=?, branch=?, worktree=? WHERE id=?",
                     (pr_url(1), "factory/t-0001", str(self.wt), T))
        self.tick({pr_url(1): (SHA, RED)}, real_spawn=True)


class ResumeKeepsBackendTests(PinBase):
    def setUp(self):
        super().setUp()
        self.env.seed_task(T, status="todo", pr_url=None, team="g-001-a")

    def test_a_canary_pinned_hc_coder_resumes_on_hc_under_herdr_config(self):
        self.spawn_cli("--host-backend", "hc")
        a = self.args()
        self.assertEqual((a.get("host_backend"), a.get("host_backend_pinned")), ("hc", True))
        n_hc, n_herdr = len(self.hc_runs()), len(self.herdr_starts())
        self.assertEqual((n_hc, n_herdr), (1, 0))
        self.env.sql("DELETE FROM throttle")
        self.red_resume()
        self.assertEqual(len(self.hc_runs()), 2, self.env.calls("hc"))  # the first spawn plus exactly one resume
        self.assertEqual(self.herdr_starts(), [])
        self.assertEqual(self.args().get("host_backend"), "hc")
        self.assertTrue(self.args().get("host_backend_pinned"))

    def test_b_control_unpinned_coder_resumes_on_herdr(self):
        self.spawn_cli()
        self.assertNotIn("host_backend_pinned", self.args())
        self.assertEqual(len(self.herdr_starts()), 1)
        self.env.sql("DELETE FROM throttle")
        self.red_resume()
        self.assertEqual(len(self.herdr_starts()), 2)
        self.assertEqual(self.hc_runs(), [])
        self.assertEqual(self.args().get("host_backend"), "herdr")
        self.assertNotIn("host_backend_pinned", self.args())


class TickCallersPassPinTests(PinBase):
    def pin(self, backend="hc"):
        self.env.seed_task(T, status="in_review", pr_url=pr_url(1), team="g-001-a")
        self.env.seed_agent("coder-old", "coder", goal="G-001", team="g-001-a", task=T, status="done")
        self.env.sql("UPDATE agents SET spawn_args=? WHERE name='coder-old'",
                     (json.dumps({"host_backend": backend, "host_backend_pinned": True}),))

    def test_c_review_rerun_spawns_reviewer_and_qa_on_the_pinned_backend(self):
        self.pin()
        seen = []
        with self.env.load_factory() as f:
            con = f.db()
            t = con.execute("SELECT * FROM tasks WHERE id=?", (T,)).fetchone()
            f.review_rerun_candidates = lambda con: [t]
            f._review_repo_mismatch = lambda con, t: None
            f._review_dead_recent = lambda con, t, clock: False
            f._ci_remote_head = lambda branch, batch: SHA
            f._review_alert_no_verdict = lambda *a, **k: None
            f._review_head_done = lambda *a, **k: False
            f._review_hold = lambda *a, **k: False
            f._review_probe = lambda *a, **k: {"headRefOid": SHA, "statusCheckRollup": [], "state": "OPEN"}
            f._review_pr_problem = lambda *a, **k: False
            f.ci_verdict = lambda r: ("green", "")
            f._rerun_agents = lambda *a, **k: []
            f._review_verdict_fresh = lambda *a, **k: False
            f._rerun_missing = lambda con, tid, sha: ["reviewer", "qa"]
            f.gate = lambda con, **k: (True, "", {})
            f.spawn = lambda con, role, *a, **kw: seen.append((role, kw.get("host_backend")))
            f.review_rerun_tick(con)
        self.assertEqual(seen, [("reviewer", "hc"), ("qa", "hc")])

    def test_d_judge_autospawn_passes_the_pinned_backend(self):
        self.pin()
        seen = []
        with self.env.load_factory() as f:
            con = f.db()
            for role in ("reviewer", "qa"):
                con.execute("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)",
                            (T, pr_url(1), SHA, role, "pass", "ok", "zz", "t"))
            con.commit()
            f.gate = lambda con, **k: (True, "", {})
            f._ci_wake_probe = lambda url, timeout: {"number": 1, "state": "OPEN", "headRefOid": SHA,
                                                      "statusCheckRollup": [], "mergeStateStatus": "CLEAN"}
            f.judge_pr_ready = lambda con, url, behind_ok=False: (True, "CI green")
            f.gh_json = lambda *a, **k: {"url": "https://example.invalid/zz/repo"}
            f.spawn = lambda con, role, *a, **kw: seen.append((role, kw.get("host_backend")))
            f.judge_autospawn_tick(con)
        self.assertEqual(seen, [("judge", "hc")])


class QueueKeepsPinTests(PinBase):
    def test_e_pin_survives_the_gate_queue(self):
        self.env.seed_task(T, status="todo", pr_url=None, team="g-001-a")
        self.ok("config", "max_live_agents", "0", agent="owner")
        self.spawn_cli("--host-backend", "hc")
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name=?", (CODER,))[0][0], "queued")
        self.assertTrue(self.args().get("host_backend_pinned"))
        self.ok("config", "max_live_agents", "99", agent="owner")
        with self.env.load_factory() as f:
            con = f.db()
            # the tick's queue drain passes the queued row's host_backend back in
            f.spawn(con, "coder", name=CODER, from_queue=True, host_backend="hc")
            con.commit()
        self.assertEqual(len(self.hc_runs()), 1)
        self.assertEqual(self.herdr_starts(), [])
        a = self.args()
        self.assertEqual((a.get("host_backend"), a.get("host_backend_pinned")), ("hc", True))


class HelperTests(FactoryTestCase):
    def row(self, name, started, args):
        self.env.seed_agent(name, "coder", goal="G-001", team="g-001-a", task=T, status="done", started=started)
        self.env.sql("UPDATE agents SET spawn_args=? WHERE name=?", (args, name))

    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_task(T, team="g-001-a")

    def pinned(self, task=T):
        with self.env.load_factory() as f:
            return f.task_pinned_host_backend(f.db(), task)

    def test_f_newest_pinned_row_wins_and_no_pin_is_none(self):
        self.assertIsNone(self.pinned())
        self.row("c1", "2026-01-01T00:00:00+00:00", json.dumps({"host_backend": "hc", "host_backend_pinned": True}))
        self.row("c2", "2026-01-02T00:00:00+00:00", json.dumps({"host_backend": "herdr"}))  # newer but not pinned
        self.row("c3", "2026-01-03T00:00:00+00:00", "{not json")  # unparsable counts as unpinned
        self.assertEqual(self.pinned(), "hc")
        self.row("c4", "2026-01-04T00:00:00+00:00", json.dumps({"host_backend": "herdr", "host_backend_pinned": True}))
        self.assertEqual(self.pinned(), "herdr")
        self.assertIsNone(self.pinned(None))


if __name__ == "__main__":
    unittest.main()
