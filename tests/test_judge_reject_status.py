"""T-0837: after judge --reject, DB status in_progress, coder notification and kickoff match; autospawn skips until in_review."""
import io
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

from harness import PR_URL, VALID_PR_BODY, FactoryTestCase, gates_summary

TEAM = "g-001-a"
CODER = "zz-test-coder"
JUDGE = "zz-test-judge"
T = "T-0837"


class Base(FactoryTestCase):
    INPROC = True

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(TEAM)
        e.seed_task(T, team=TEAM, status="in_review", pr_url=PR_URL)
        e.sql("UPDATE tasks SET assignee=? WHERE id=?", (CODER, T))
        e.seed_agent(CODER, "coder", goal="G-001", team=TEAM, task=T)
        e.seed_agent(JUDGE, "judge", goal="G-001", task=T, status="exited")
        e.seed_agent("zz-test-reviewer", "reviewer", goal="G-001", task=T)
        e.seed_agent("zz-test-qa", "qa", goal="G-001", task=T)
        e.use_git_head()
        e.seed_pr(body=VALID_PR_BODY)
        for role, agent in (("reviewer", "zz-test-reviewer"), ("qa", "zz-test-qa")):
            self.ok("verdict", T, "--pass", "--summary", gates_summary(), agent=agent)

    def reject(self):
        return self.ok("judge", T, "--reject", "--reason", "scope", agent=JUDGE)

    def judge_autospawn_calls(self):
        calls = []
        with self.env.load_factory() as f:
            con = f.db()
            f.gate = lambda con, **k: (True, "", {})

            def fake_spawn(con, role, goal=None, team=None, task=None, name=None, **kw):
                calls.append({"role": role, "task": task})

            f.spawn = fake_spawn
            with redirect_stdout(io.StringIO()):
                f.judge_autospawn_tick(con)
            con.commit()
        return calls


class RejectNotifyTests(Base):
    def test_reject_sets_in_progress_and_messages_coder(self):
        self.reject()
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id=?", (T,))[0]["status"], "in_progress")
        show = self.ok("task", "show", T).stdout
        self.assertIn("status: in_progress", show)
        self.assertNotIn("status: in_review", show.split("## spec")[0])
        msgs = self.env.sql("SELECT subject, body FROM messages WHERE recipient=? ORDER BY id", (CODER,))
        self.assertTrue(msgs, "assignee must get a message")
        sub, body = msgs[-1]["subject"], msgs[-1]["body"]
        self.assertIn("in_progress", sub)
        self.assertIn("in_progress", body)
        self.assertIn("factory task update", body)
        self.assertIn("--status in_review", body)
        self.assertNotIn("status is in_review", body.split("After the fix")[0])

    def test_kickoff_matches_db_after_reject(self):
        self.reject()
        with self.env.load_factory() as f:
            con = f.db()
            kick = f.kickoff_text("coder", CODER, "G-001", TEAM, T, None)
            kick = f.coder_judge_reject_kickoff(con, "coder", T, kick)
        self.assertIn("in_progress", kick)
        self.assertIn("--status in_review", kick)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id=?", (T,))[0]["status"], "in_progress")

    def test_kickoff_skipped_after_lead_retry_in_progress(self):
        self.reject()
        self.env.sql(
            "INSERT INTO events(ts, entity, entity_id, action, detail) VALUES(?,?,?,?,?)",
            ("2099-01-01T00:00:00+00:00", "task", T, "update", "status=in_progress"),
        )
        with self.env.load_factory() as f:
            con = f.db()
            extra = f.coder_judge_reject_kickoff(con, "coder", T, "brief")
        self.assertEqual(extra, "brief")

    def test_in_review_update_and_autospawn_while_in_progress(self):
        self.reject()
        self.assertEqual(self.judge_autospawn_calls(), [])
        p = self.ok("task", "update", T, "--status", "in_review", agent=CODER)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id=?", (T,))[0]["status"], "in_review")
        self.assertIn("status: in_review", self.ok("task", "show", T).stdout)
        calls = self.judge_autospawn_calls()
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0]["task"], T)


class StatusOnlyRejectTests(Base):
    """T-1055: judge --reject --no-code-change keeps the task in_review; the default reject is unchanged."""

    def status_only(self):
        return self.env.run("judge", T, "--reject", "--no-code-change", "--reason", "CI still running", agent=JUDGE)

    def test_status_only_reject_stays_in_review_and_merge_reaches_gate(self):
        p = self.status_only()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("status-only", p.stdout)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id=?", (T,))[0]["status"], "in_review")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action='judge-reject-status-only'")[0]["c"], 1)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE recipient=?", (CODER,))[0]["c"], 0)
        m = self.env.run("judge", T, "--merge", "--reason", "retry", agent=JUDGE)
        self.assertNotIn("no longer in_review", m.stderr)

    def test_status_only_reject_counts_as_reject_in_scorecard(self):
        self.status_only()
        v = self.env.sql("SELECT verdict, summary FROM verdicts WHERE role='judge'")
        self.assertEqual([r["verdict"] for r in v], ["reject"])
        self.assertTrue(v[0]["summary"].startswith("status-only: "))

    def test_code_change_reject_still_in_progress(self):
        self.reject()
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id=?", (T,))[0]["status"], "in_progress")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action='judge-reject-status-only'")[0]["c"], 0)

    def test_no_code_change_needs_reject(self):
        p = self.env.run("judge", T, "--merge", "--no-code-change", "--reason", "x", agent=JUDGE)
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id=?", (T,))[0]["status"], "in_review")

    def test_autospawn_holds_until_newer_verdict(self):
        self.assertEqual(len(self.judge_autospawn_calls()), 1)
        self.status_only()
        self.assertEqual(self.judge_autospawn_calls(), [])
        self.ok("verdict", T, "--pass", "--summary", gates_summary(), agent="zz-test-qa")
        self.assertEqual(len(self.judge_autospawn_calls()), 1)


class C4ScratchTest(unittest.TestCase):
    def test_removing_reject_message_fails(self):
        from harness import FACTORY_SRC, REPO

        block = (
            '        if t["assignee"]:\n'
            '            send(con, t["assignee"], f"judge reject: {a.task} in_progress",\n'
            '                 judge_reject_coder_notice(a.task, reason), "request", t["goal_id"], a.task, sender=actor())'
        )
        text = FACTORY_SRC.read_text()
        self.assertIn(block, text)
        scratch = Path(tempfile.mkdtemp(prefix="t0837-scratch-"))
        try:
            for name in ("bin", "tests", "roles", "scout", "ci"):
                src = REPO / name
                if src.is_dir():
                    shutil.copytree(src, scratch / name, symlinks=True)
            if (REPO / "VERSION").is_file():
                shutil.copy2(REPO / "VERSION", scratch / "VERSION")
            cli = scratch / "bin" / "factory_cli.py"
            cli.write_text(text.replace(block, "        pass  # C4: reject notify removed", 1))
            p = subprocess.run(
                [sys.executable, "-m", "unittest", "tests.test_judge_reject_status.RejectNotifyTests."
                 "test_reject_sets_in_progress_and_messages_coder", "-v"],
                cwd=scratch,
                capture_output=True,
                text=True,
                timeout=120,
                env={**os.environ, "FACTORY_TEST_NO_TEMPLATE": "1", "PYTHONDONTWRITEBYTECODE": "1"},
            )
            self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
        finally:
            shutil.rmtree(scratch)


if __name__ == "__main__":
    unittest.main()
