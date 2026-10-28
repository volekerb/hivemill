"""G-004 B3: goal close gate and goal status gating."""
import os
import unittest

from harness import FactoryTestCase

GATE_ROLES = ("coder", "qa", "reviewer", "judge", "lead", "boss")


class GoalCloseTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_goal("G-002")
        e.seed_team("g-001-a", "G-001", lead="zz-test-lead")
        e.seed_task("T-0001", status="done")
        e.seed_task("T-0002", status="cancelled")
        e.seed_task("T-0003", role="pm", status="in_progress", pr_url=None)  # the audit pm's own task
        e.seed_task("T-0009", goal="G-002", role="pm", status="in_progress", pr_url=None)
        e.seed_agent("zz-test-pm", "pm", goal="G-001", task="T-0003")
        e.seed_agent("zz-test-pm2", "pm", goal="G-002", task="T-0009")
        for name, role in (("zz-test-lead", "lead"), ("zz-test-director", "director"), ("zz-test-boss", "boss"),
                           ("zz-test-coder", "coder"), ("zz-test-qa", "qa"), ("zz-test-reviewer", "reviewer"),
                           ("zz-test-judge", "judge"), ("zz-test-cleaner", "cleaner")):
            e.seed_agent(name, role, goal="G-001")

    def signed(self, audit_task_done=False):
        """audit v1 (ready, recorded by the pm) + director sign-off; T-0003 is the audit task"""
        if audit_task_done:
            self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0003'")
        self.ok("goal", "audit", "G-001", "--ready", "--report", "-", stdin="ready", agent="zz-test-pm")
        self.ok("goal", "signoff", "G-001", "--audit", "1", "--reason", "ok", agent="zz-test-director")

    def close(self, agent="zz-test-director", goal="G-001"):
        return self.env.run("goal", "close", goal, "--reason", "audit v1 ready", agent=agent)

    def status(self, gid="G-001"):
        return self.env.sql("SELECT status FROM goals WHERE id=?", (gid,))[0]["status"]

    def events(self, action, gid="G-001"):
        return self.env.sql("SELECT * FROM events WHERE entity='goal' AND entity_id=? AND action=?", (gid, action))

    def refused_close(self, *needles, agent="zz-test-director"):
        p = self.close(agent)
        self.assertEqual(p.returncode, 4, p.stderr)
        for n in needles:
            self.assertIn(n, p.stderr)
        self.assertEqual(self.status(), "active")
        self.assertEqual(len(self.events("close-refused")), 1)
        return p

    def test_pm_closes_own_goal_after_signoff(self):
        self.signed()
        p = self.ok("goal", "close", "G-001", "--reason", "done", agent="zz-test-pm")
        self.assertIn("G-001 → done", p.stdout)
        self.assertEqual(self.status(), "done")
        ev = self.events("goal close")
        self.assertEqual(len(ev), 1)
        so = self.env.sql("SELECT id FROM signoffs")[0]["id"]
        self.assertIn(f"signoff {so}", ev[0]["detail"])
        self.assertIn("audit 1", ev[0]["detail"])
        self.assertEqual(len(self.events("status")), 1)
        msgs = self.env.sql("SELECT recipient,kind FROM messages WHERE subject='G-001 closed' ORDER BY recipient")
        self.assertEqual([(m["recipient"], m["kind"]) for m in msgs], [("zz-test-director", "report"), ("zz-test-lead", "report")])

    def test_director_closes_goal_after_signoff(self):
        self.signed()
        self.ok("goal", "close", "G-001", "--reason", "done", agent="zz-test-director")
        self.assertEqual(self.status(), "done")

    def test_director_closes_after_audit_pm_done_post_signoff(self):
        self.signed()
        self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0003'")  # the audit pm ran factory done
        self.ok("goal", "close", "G-001", "--reason", "done", agent="zz-test-director")
        self.assertEqual(self.status(), "done")

    def test_note_only_update_after_signoff_does_not_block_close(self):
        self.signed()
        self.env.sql("UPDATE tasks SET updated='2099-01-01T00:00:00+00:00'")
        self.ok("task", "update", "T-0001", "--note", "late note", agent="zz-test-lead")
        self.ok("goal", "close", "G-001", "--reason", "done", agent="zz-test-director")
        self.assertEqual(self.status(), "done")

    def test_goal_close_without_signoff_refused(self):
        self.ok("goal", "audit", "G-001", "--ready", "--report", "-", stdin="r", agent="zz-test-pm")
        self.refused_close("no director sign-off for audit v1 (factory goal signoff G-001 --audit 1)")

    def test_goal_close_without_audit_refused(self):
        self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0003'")
        self.refused_close("no audit for G-001: run factory goal audit")

    def test_goal_close_with_gaps_audit_refused(self):
        self.ok("goal", "audit", "G-001", "--gaps", "--report", "-", stdin="gap", agent="zz-test-pm")
        self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0003'")
        self.refused_close("latest audit v1 reports gaps")

    def test_goal_close_refused_with_open_tasks(self):
        self.signed()
        self.env.seed_task("T-0004", status="todo", pr_url=None)
        self.refused_close("open tasks: T-0004(todo)")

    def test_goal_close_allows_audit_task_still_open(self):
        self.signed()
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0003'")[0]["status"], "in_progress")
        self.ok("goal", "close", "G-001", "--reason", "done", agent="zz-test-pm")

    def test_goal_close_refused_when_task_status_changed_after_signoff(self):
        self.signed()
        self.env.sql("UPDATE tasks SET status='cancelled' WHERE id='T-0001'")  # closed states, but not the snapshot's
        self.refused_close("T-0001 changed after sign-off of audit v1 (done -> cancelled)",
                           "re-audit and get a new sign-off")

    def test_goal_close_refused_when_task_reopened_after_signoff(self):
        self.signed()
        self.env.sql("UPDATE tasks SET pr_url=NULL WHERE id='T-0001'")
        self.ok("task", "reopen", "T-0001", "--reason", "x", agent="zz-test-director")
        p = self.close()
        self.assertEqual(p.returncode, 4, p.stderr)
        self.assertIn("open tasks: T-0001(todo)", p.stderr)  # check 3 fires before check 6
        self.assertEqual(len(self.events("close-refused")), 1)

    def test_goal_close_refused_when_non_audit_task_status_changed_to_other_done(self):
        self.signed()
        self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0002'")
        self.refused_close("T-0002 changed after sign-off of audit v1 (cancelled -> done)")

    def test_audit_task_may_only_move_to_done(self):
        self.signed()
        self.env.sql("UPDATE tasks SET status='cancelled' WHERE id='T-0003'")
        self.refused_close("T-0003 changed after sign-off of audit v1 (in_progress -> cancelled)")

    def test_goal_close_refused_when_task_added_after_signoff(self):
        self.signed()
        self.env.seed_task("T-0004", status="done", pr_url=None)  # a follow-up filed and finished
        self.refused_close("T-0004 is new", "re-audit and get a new sign-off")

    def test_goal_close_refused_when_new_audit_after_signoff(self):
        self.signed()
        self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0003'")
        self.ok("goal", "audit", "G-001", "--ready", "--report", "-", stdin="again", agent="zz-test-director")
        self.refused_close("no director sign-off for audit v2")

    def test_goal_close_refused_for_wrong_role(self):
        self.signed()
        for who in ("coder", "qa", "reviewer", "judge", "lead", "boss"):
            p = self.close(f"zz-test-{who}")
            self.assertEqual(p.returncode, 3, (who, p.stderr))
            self.assertIn(f"role '{who}' may not close goals; allowed: pm (own goal), director", p.stderr)
        self.assertEqual(self.status(), "active")

    def test_pm_cannot_close_other_goal(self):
        self.signed()
        p = self.close("zz-test-pm2")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertEqual(self.status(), "active")

    def test_goal_close_refused_when_already_done(self):
        self.signed()
        self.ok("goal", "close", "G-001", "--reason", "done", agent="zz-test-director")
        p = self.close()
        self.assertEqual(p.returncode, 4, p.stderr)
        self.assertIn("goal G-001 is already done", p.stderr)

    def test_lead_goal_status_done_refused(self):
        p = self.refused(3, "goal", "status", "G-001", "done", agent="zz-test-lead")
        self.assertIn("use factory goal close G-001", p.stderr)
        self.assertEqual(self.status(), "active")

    def test_director_goal_status_done_refused_points_to_close(self):
        for who in ("director", "boss"):
            p = self.refused(3, "goal", "status", "G-001", "done", agent=f"zz-test-{who}")
            self.assertIn("closing a goal needs a director sign-off (factory goal signoff)", p.stderr)
        self.assertEqual(self.status(), "active")

    def test_owner_goal_status_done_override_logged(self):
        self.ok("goal", "status", "G-001", "done", "--note", "owner says so")
        self.assertEqual(self.status(), "done")
        self.assertEqual(len(self.events("owner-override-close")), 1)

    def test_director_reopens_done_goal_with_note(self):
        self.env.sql("UPDATE goals SET status='done'")
        self.ok("goal", "status", "G-001", "active", "--note", "reopen", agent="zz-test-director")
        self.assertEqual(self.status(), "active")
        self.env.sql("UPDATE goals SET status='done'")
        self.ok("goal", "status", "G-001", "active", "--note", "reopen")  # owner
        self.assertEqual(self.status(), "active")

    def test_reopen_done_goal_refused_for_lead_boss_pm(self):
        self.env.sql("UPDATE goals SET status='done'")
        for who in ("lead", "boss"):
            p = self.refused(3, "goal", "status", "G-001", "active", "--note", "n", agent=f"zz-test-{who}")
            self.assertIn("only the director reopens a done goal", p.stderr)
        p = self.refused(3, "goal", "status", "G-001", "active", "--note", "n", agent="zz-test-pm")  # worker belt
        self.assertIn("workers may not run `factory goal status`", p.stderr)
        self.assertEqual(self.status(), "done")

    def test_reopen_done_goal_requires_note(self):
        self.env.sql("UPDATE goals SET status='done'")
        p = self.refused(2, "goal", "status", "G-001", "active", agent="zz-test-director")
        self.assertIn("--note is required to reopen a done goal", p.stderr)
        self.assertEqual(self.status(), "done")

    def test_close_refused_event_recorded(self):
        self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0003'")
        self.refused_close("no audit for G-001")
        ev = self.events("close-refused")[0]
        self.assertEqual(ev["actor"], "zz-test-director")
        self.assertIn("no audit for G-001", ev["detail"])


if __name__ == "__main__":
    unittest.main()
