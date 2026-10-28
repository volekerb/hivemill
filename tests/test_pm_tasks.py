import sqlite3
import unittest

from harness import FactoryTestCase


class PmTaskTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_goal("G-002")
        e.seed_task("T-0001", role="pm", status="done", pr_url=None)
        e.seed_task("T-0002", status="done")  # merged PR task
        e.seed_task("T-0003", role="pm", status="todo", pr_url=None)
        e.seed_task("T-0004", status="in_review")  # open PR task
        e.seed_task("T-0009", goal="G-002", role="pm", status="done", pr_url=None)
        e.seed_agent("zz-test-pm", "pm", goal="G-001")
        e.seed_agent("zz-test-pm2", "pm", goal="G-002")
        for name, role in (("zz-test-lead", "lead"), ("zz-test-director", "director"), ("zz-test-coder", "coder"),
                           ("zz-test-qa", "qa"), ("zz-test-reviewer", "reviewer"), ("zz-test-judge", "judge"),
                           ("zz-test-cleaner", "cleaner")):
            e.seed_agent(name, role, goal="G-001")

    def task(self, tid):
        return self.env.sql("SELECT * FROM tasks WHERE id=?", (tid,))[0]

    def events(self, tid, action):
        return [r["detail"] for r in self.env.sql(
            "SELECT detail FROM events WHERE entity='task' AND entity_id=? AND action=?", (tid, action))]

    # -- reopen ----------------------------------------------------------
    def test_pm_reopens_done_non_pr_task_in_own_goal(self):
        self.ok("task", "reopen", "T-0001", "--reason", "acceptance 2 unmet", agent="zz-test-pm")
        t = self.task("T-0001")
        self.assertEqual((t["status"], t["attempts"]), ("todo", 0))
        self.assertEqual(self.events("T-0001", "reopen"), ["acceptance 2 unmet"])

    def test_lead_and_director_can_reopen(self):
        for who, tid in (("zz-test-lead", "T-0001"), ("zz-test-director", "T-0009")):
            self.ok("task", "reopen", tid, "--reason", "r", agent=who)
            self.assertEqual(self.task(tid)["status"], "todo")

    def test_reopen_requires_reason(self):
        p = self.refused(2, "task", "reopen", "T-0001", agent="zz-test-pm")
        self.assertIn("--reason is required", p.stderr)
        self.refused(2, "task", "reopen", "T-0001", "--reason", " ", agent="zz-test-pm")
        self.assertEqual(self.task("T-0001")["status"], "done")

    def test_reopen_refuses_non_done_task(self):
        for st in ("todo", "in_progress", "in_review", "cancelled"):
            self.env.sql("UPDATE tasks SET status=? WHERE id='T-0003'", (st,))
            p = self.refused(2, "task", "reopen", "T-0003", "--reason", "r", agent="zz-test-pm")
            self.assertIn(f"T-0003 is {st}, not done: only done tasks are reopened", p.stderr)

    def test_reopen_refuses_task_with_pr_points_to_follow_up(self):
        p = self.refused(2, "task", "reopen", "T-0002", "--reason", "r", agent="zz-test-lead")
        self.assertIn("merged work is not reopened: file a follow-up with factory task add --follow-up-of T-0002",
                      p.stderr)
        self.assertEqual(self.task("T-0002")["status"], "done")

    def test_reopen_refused_when_goal_done(self):
        self.env.sql("UPDATE goals SET status='done' WHERE id='G-001'")
        p = self.refused(2, "task", "reopen", "T-0001", "--reason", "r", agent="zz-test-pm")
        self.assertIn("goal G-001 is done; the director must reopen the goal first", p.stderr)

    def test_reopen_refused_for_wrong_role(self):
        for who in ("coder", "qa", "reviewer", "judge", "cleaner"):
            p = self.refused(3, "task", "reopen", "T-0001", "--reason", "r", agent=f"zz-test-{who}")
            self.assertIn(f"role '{who}' may not reopen tasks; allowed: pm (own goal), lead, director, owner", p.stderr)
        self.assertEqual(self.task("T-0001")["status"], "done")

    def test_pm_reopen_refused_for_other_goal(self):
        p = self.refused(3, "task", "reopen", "T-0009", "--reason", "r", agent="zz-test-pm")
        self.assertIn("zz-test-pm works on G-001; T-0009 is in G-002", p.stderr)

    # -- follow-up -------------------------------------------------------
    def add_follow_up(self, parent, agent, goal="G-001"):
        return self.env.run("task", "add", "--goal", goal, "--title", "fix", "--follow-up-of", parent,
                            "--spec", "-", stdin="spec", agent=agent)

    def test_follow_up_links_parent_and_shows_in_task_show(self):
        p = self.add_follow_up("T-0002", "zz-test-pm")
        self.assertEqual(p.returncode, 0, p.stderr)
        tid = p.stdout.strip()
        self.assertEqual(self.task(tid)["follow_up_of"], "T-0002")
        self.assertIn("follow-up of T-0002", self.events(tid, "follow-up"))
        self.assertIn("follow_up_of: T-0002", self.ok("task", "show", tid).stdout)

    def test_follow_up_parent_must_exist_in_same_goal_and_be_done(self):
        for parent, msg in (("T-9999", "--follow-up-of: no task T-9999 in goal G-001"),
                            ("T-0009", "--follow-up-of: no task T-0009 in goal G-001"),
                            ("T-0003", "--follow-up-of: T-0003 is todo; follow-ups are filed against done tasks")):
            p = self.add_follow_up(parent, "zz-test-lead")
            self.assertEqual(p.returncode, 2, p.stderr)
            self.assertIn(msg, p.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) n FROM tasks")[0]["n"], 5)

    def test_follow_up_refused_for_pm_of_other_goal(self):
        p = self.add_follow_up("T-0001", "zz-test-pm2")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("zz-test-pm2 works on G-002", p.stderr)

    def test_follow_up_refused_for_coder(self):
        p = self.add_follow_up("T-0001", "zz-test-coder")
        self.assertEqual(p.returncode, 3, p.stderr)

    # -- pm status narrowing ---------------------------------------------
    def test_pm_marks_non_pr_task_done_with_note(self):
        for st in ("todo", "in_progress", "blocked"):
            self.env.sql("UPDATE tasks SET status=? WHERE id='T-0003'", (st,))
            self.ok("task", "update", "T-0003", "--status", "done", "--note", "evidence", agent="zz-test-pm")
            self.assertEqual(self.task("T-0003")["status"], "done")

    def test_pm_done_requires_note(self):
        p = self.refused(2, "task", "update", "T-0003", "--status", "done", agent="zz-test-pm")
        self.assertIn("pm must give --note with the evidence when closing a task", p.stderr)
        self.assertEqual(self.task("T-0003")["status"], "todo")

    def test_pm_cannot_mark_pr_task_done(self):
        p = self.refused(3, "task", "update", "T-0004", "--status", "done", "--note", "n", agent="zz-test-pm")
        self.assertIn("a coder task with a PR is only done when the judge merges it", p.stderr)
        self.assertEqual(self.task("T-0004")["status"], "in_review")

    def test_pm_cannot_set_other_statuses(self):
        for st in ("in_progress", "in_review", "blocked", "cancelled", "todo"):
            p = self.refused(3, "task", "update", "T-0003", "--status", st, "--note", "n", agent="zz-test-pm")
            self.assertIn("pm changes task status only with factory task reopen or --status done", p.stderr)
        self.assertEqual(self.task("T-0003")["status"], "todo")
        self.assertEqual(self.task("T-0003")["attempts"], 0)

    def test_pm_cannot_update_status_in_other_goal(self):
        self.env.sql("UPDATE tasks SET status='todo' WHERE id='T-0009'")
        p = self.refused(3, "task", "update", "T-0009", "--status", "done", "--note", "n", agent="zz-test-pm")
        self.assertIn("zz-test-pm works on G-001; T-0009 is in G-002", p.stderr)

    def test_lead_status_updates_unchanged(self):
        self.ok("task", "update", "T-0003", "--status", "in_progress", agent="zz-test-lead")
        self.assertEqual(self.task("T-0003")["status"], "in_progress")

    MSG = ("pm may not close a coder task without a PR: reopen it (factory task reopen T --reason ...) or file a "
           "follow-up (factory task add --follow-up-of T ...); only a lead, director or the owner may close it")

    def test_pm_close_coder_task_without_pr_refused_points_to_reopen_or_follow_up(self):
        self.env.seed_task("T-0010", status="todo", pr_url=None)
        p = self.refused(3, "task", "update", "T-0010", "--status", "done", "--note", "evidence", agent="zz-test-pm")
        self.assertIn(self.MSG, p.stderr)
        self.assertEqual(self.task("T-0010")["status"], "todo")
        self.assertEqual(self.events("T-0010", "pm-closed-coder-task-no-pr"), [])

    def test_pm_close_coder_task_without_pr_refused_even_with_note(self):
        self.env.seed_task("T-0010", status="todo", pr_url=None)
        for extra in ((), ("--note", "n")):
            p = self.refused(3, "task", "update", "T-0010", "--status", "done", *extra, agent="zz-test-pm")
            self.assertIn(self.MSG, p.stderr)
        self.assertEqual(self.task("T-0010")["status"], "todo")

    def test_lead_director_owner_can_still_close_coder_task_without_pr(self):
        for i, who in enumerate(("zz-test-lead", "zz-test-director", None)):
            tid = f"T-001{i}"
            self.env.seed_task(tid, status="todo", pr_url=None)
            self.ok("task", "update", tid, "--status", "done", agent=who)
            self.assertEqual(self.task(tid)["status"], "done")

    def test_pm_close_coder_task_in_other_goal_refused(self):
        self.env.seed_task("T-0010", goal="G-002", status="todo", pr_url=None)
        self.refused(3, "task", "update", "T-0010", "--status", "done", "--note", "n", agent="zz-test-pm")
        self.assertEqual(self.task("T-0010")["status"], "todo")

    def test_pm_close_coder_task_with_pr_still_refused(self):
        p = self.refused(3, "task", "update", "T-0004", "--status", "done", "--note", "n", agent="zz-test-pm")
        self.assertIn("a coder task with a PR is only done when the judge merges it", p.stderr)
        self.assertEqual(self.events("T-0004", "pm-closed-coder-task-no-pr"), [])

    def test_pm_factory_done_still_closes_own_task(self):
        self.env.sql("UPDATE tasks SET status='in_progress' WHERE id='T-0003'")
        self.env.seed_agent("zz-test-pm3", "pm", goal="G-001", task="T-0003")
        self.ok("done", "finished", agent="zz-test-pm3")
        self.assertEqual(self.task("T-0003")["status"], "done")

    # -- migrate ---------------------------------------------------------
    def test_migrate_adds_follow_up_of_to_old_db(self):
        path = self.env.db_path
        con = sqlite3.connect(path)
        con.execute("ALTER TABLE tasks DROP COLUMN follow_up_of")
        con.commit()
        con.close()
        for _ in range(2):
            self.ok("task", "show", "T-0001")
            cols = [r["name"] for r in self.env.sql("PRAGMA table_info(tasks)")]
            self.assertEqual(cols.count("follow_up_of"), 1)


if __name__ == "__main__":
    unittest.main()
