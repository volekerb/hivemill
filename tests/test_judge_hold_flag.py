"""G-027 T-0835: tasks.judge_hold_reason — lead/director hold blocks judge autospawn and queued judge dequeue."""
import io
import unittest
from contextlib import redirect_stdout

from harness import PR_URL, SHA_A, FactoryTestCase

TEAM, LEAD = "g-001-a", "zz-lead"
T = "T-0835"
T2 = "T-0835b"

GREEN = [{"__typename": "CheckRun", "name": "unit", "status": "COMPLETED", "conclusion": "SUCCESS"}]
PENDING = [{"__typename": "CheckRun", "name": "unit", "status": "IN_PROGRESS", "conclusion": ""}]


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(TEAM, lead=LEAD)
        self.env.seed_agent(LEAD, "lead", goal="G-001", team=TEAM, status="exited")
        self.env.seed_task(T, team=TEAM, status="in_review")
        self.env.seed_pr(checks=GREEN)

    def seed_verdict(self, role, verdict, summary="ok", sha=SHA_A, tid=T):
        self.env.sql(
            "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
            "VALUES(?,?,?,?,?,?,?,?)",
            (tid, PR_URL, sha, role, verdict, summary, "zz", "t"),
        )

    def rq_pass(self, tid=T, sha=SHA_A):
        for role in ("reviewer", "qa"):
            self.seed_verdict(role, "pass", "ok", sha, tid)

    def rq_fail_then_pass(self, tid=T, sha=SHA_A):
        for role in ("reviewer", "qa"):
            self.seed_verdict(role, "fail", "bad", sha, tid)
            self.seed_verdict(role, "pass", "ok", sha, tid)

    def judge_agent_name(self):
        with self.env.load_factory() as f:
            con = f.db()
            o = f.org_for(con, "G-001", T)
            return f.oname(con, o, f.slug(f"judge-{T}"))

    def hold(self, reason="wait", tid=T, agent=LEAD):
        return self.ok("task", "update", tid, "--judge-hold", reason, agent=agent)

    def row(self, tid=T):
        return self.env.sql("SELECT judge_hold_reason FROM tasks WHERE id=?", (tid,))[0]

    def judge_autospawn_step(self, gate_open=True, ignore_task_hold=False):
        calls = []
        buf = io.StringIO()
        with self.env.load_factory() as f:
            con = f.db()
            f.gate = lambda con, **k: (gate_open, "" if gate_open else "closed", {})
            if ignore_task_hold:
                f.task_judge_hold_reason = lambda con, task: None

            def fake_spawn(con, role, goal=None, team=None, task=None, name=None, **kw):
                calls.append({"role": role, "task": task, "name": name})
                con.execute(
                    "INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started) VALUES(?,?,?,?,?,?,'t')",
                    (name, role, goal, team, task, "live"),
                )

            f.spawn = fake_spawn
            with redirect_stdout(buf):
                f.judge_autospawn_tick(con)
            con.commit()
        return calls, buf.getvalue()

    def events(self, action, tid=T):
        return self.env.sql("SELECT detail FROM events WHERE entity='task' AND entity_id=? AND action=? ORDER BY id",
                            (tid, action))


class MigrationTests(Base):
    def test_old_db_gets_judge_hold_reason_column(self):
        con = self.env.db()
        try:
            con.execute("ALTER TABLE tasks DROP COLUMN judge_hold_reason")
            con.commit()
        finally:
            con.close()
        self.ok("task", "show", T)
        info = [r for r in self.env.sql("PRAGMA table_info(tasks)") if r["name"] == "judge_hold_reason"]
        self.assertEqual(len(info), 1)


class CliTests(Base):
    def test_show_prints_reason_only_while_held(self):
        self.assertNotIn("judge_hold", self.ok("task", "show", T).stdout)
        self.hold("scope check")
        self.assertIn("\njudge_hold_reason: scope check\n", self.ok("task", "show", T).stdout)
        self.ok("task", "update", T, "--judge-hold-clear", agent=LEAD)
        self.assertNotIn("judge_hold", self.ok("task", "show", T).stdout)

    def test_workers_cannot_set_hold(self):
        self.hold("wait")
        self.env.seed_agent("zz-coder", "coder", goal="G-001", team=TEAM, task=T)
        self.refused(3, "task", "update", T, "--judge-hold", "nope", agent="zz-coder")
        self.assertEqual((self.row()["judge_hold_reason"] or "").strip(), "wait")

    def test_lead_may_set_and_clear(self):
        self.hold("wait")
        self.assertEqual((self.row()["judge_hold_reason"] or "").strip(), "wait")
        self.ok("task", "update", T, "--judge-hold-clear", agent=LEAD)
        self.assertIsNone(self.row()["judge_hold_reason"])


class AutospawnHoldTests(Base):
    def test_hold_blocks_judge_autospawn_and_logs(self):
        self.rq_pass()
        self.hold("not yet")
        calls, out = self.judge_autospawn_step()
        self.assertEqual(calls, [])
        self.assertIn(f"judge-hold {T}", out)
        self.assertTrue(self.events("judge-hold"))

    def test_clear_then_ci_hold_then_spawn_when_green(self):
        self.rq_pass()
        self.hold("wait")
        self.judge_autospawn_step()
        self.ok("task", "update", T, "--judge-hold-clear", agent=LEAD)
        self.env.seed_pr(checks=PENDING)
        self.env.sql("DELETE FROM config WHERE key LIKE 'judge_ci:%'")
        calls, _ = self.judge_autospawn_step()
        self.assertEqual(calls, [])
        self.env.seed_pr(checks=GREEN)
        self.env.sql("DELETE FROM config WHERE key LIKE 'judge_ci:%'")
        calls, _ = self.judge_autospawn_step()
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0]["role"], "judge")
        self.assertEqual(calls[0]["task"], T)

    def test_other_task_not_blocked_by_first_hold(self):
        self.env.seed_task(T2, team=TEAM, status="in_review")
        self.rq_pass(T)
        self.rq_pass(T2)
        self.hold("hold A", tid=T)
        calls, _ = self.judge_autospawn_step()
        self.assertEqual([c["task"] for c in calls], [T2])

    def test_c4_ignoring_hold_in_autospawn_would_spawn(self):
        """With the hold lookup removed, the hold test's expectation (no spawn) would fail."""
        self.rq_pass()
        self.hold("not yet")
        calls, out = self.judge_autospawn_step(ignore_task_hold=True)
        self.assertEqual(len(calls), 1)
        self.assertNotIn("judge-hold", out)

    def test_historical_fails_latest_pass_spawns_when_hold_clear(self):
        """Regression @96b1939f: mech_dispute dropped the task before latest PASS was considered."""
        self.rq_fail_then_pass()
        calls, _ = self.judge_autospawn_step()
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0]["task"], T)

    def test_historical_fails_latest_pass_hold_still_logs(self):
        """Regression @96b1939f: held task with R+Q PASS must log judge-hold even after earlier FAILs."""
        self.rq_fail_then_pass()
        self.hold("not yet")
        calls, out = self.judge_autospawn_step()
        self.assertEqual(calls, [])
        self.assertIn(f"judge-hold {T}", out)

    def test_judge_line_in_pass_summary_still_spawns(self):
        """Regression @96b1939f: judge: in an old or PASS summary must not skip when latest R+Q are PASS."""
        self.seed_verdict("reviewer", "pass", "ok\njudge: look at the migration")
        self.seed_verdict("qa", "pass", "ok")
        calls, _ = self.judge_autospawn_step()
        self.assertEqual(len(calls), 1)

    def test_exited_judge_row_does_not_block_autospawn(self):
        """Regression @96b1939f: an exited judge-<task> row must not suppress a new spawn attempt."""
        self.rq_pass()
        self.env.seed_agent(
            self.judge_agent_name(), "judge", goal="G-001", team=TEAM, task=T, status="exited",
        )
        calls, _ = self.judge_autospawn_step()
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0]["task"], T)


class DequeueHoldTests(Base):
    def test_queued_judge_stays_queued_when_task_held(self):
        self.hold("wait")
        out_spawn = self.ok("spawn", "judge", "--name", "zz-jd", "--goal", "G-001", "--team", TEAM, "--task", T).stdout
        self.assertIn("QUEUED", out_spawn)
        self.ok("config", "max_live_agents", "5")
        self.ok("tick")
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-jd'")[0]["status"], "queued")
        ev = self.events("judge-hold")
        self.assertEqual(len(ev), 1)
        self.assertEqual(ev[0]["detail"], "wait")


if __name__ == "__main__":
    unittest.main()
