import subprocess
import sys
import unittest

from harness import FACTORY, FactoryTestCase


class DoneUnreadTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_task(status="in_progress", team="g-001-a")
        e.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", task="T-0001")
        e.seed_agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")

    def order(self, kind, body="do it"):
        self.ok("msg", "zz-test-coder", "order", body, "--kind", kind, agent="zz-test-lead")

    def status(self):
        return self.env.sql("SELECT status FROM agents WHERE name='zz-test-coder'")[0]["status"]

    def test_done_blocks_on_unread_decision(self):
        self.order("decision", "switch to plan B")
        p = self.refused(3, "done", "all good", agent="zz-test-coder")
        self.assertIn("switch to plan B", p.stdout)
        self.assertIn("unread orders: read and act on them, then run factory done again", p.stderr)
        self.assertEqual(self.status(), "live")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE sender='zz-test-coder'")[0]["c"], 0)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE read_at IS NULL")[0]["c"], 0)

    def test_done_blocks_on_unread_request(self):
        self.order("request")
        self.refused(3, "done", "all good", agent="zz-test-coder")
        self.assertEqual(self.status(), "live")

    def test_done_second_call_closes(self):
        self.order("decision")
        self.refused(3, "done", "all good", agent="zz-test-coder")
        self.ok("done", "all good", agent="zz-test-coder")
        self.assertEqual(self.status(), "done")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM events WHERE action='done-forced'")[0]["c"], 0)

    def test_done_ignores_unread_reports(self):
        self.order("report")
        self.ok("done", "all good", agent="zz-test-coder")
        self.assertEqual(self.status(), "done")

    def test_done_no_messages_closes(self):
        self.ok("done", "all good", agent="zz-test-coder")
        self.assertEqual(self.status(), "done")

    def test_done_force_records_event(self):
        self.order("decision")
        self.order("request")
        self.ok("done", "--force", "all good", agent="zz-test-coder")
        self.assertEqual(self.status(), "done")
        ids = [r["id"] for r in self.env.sql("SELECT id FROM messages WHERE kind IN ('decision','request')")]
        ev = self.env.sql("SELECT detail FROM events WHERE action='done-forced'")
        self.assertEqual(len(ev), 1)
        for i in ids:
            self.assertIn(f"M{i}", ev[0]["detail"])
        told = self.env.sql("SELECT subject FROM messages WHERE recipient='zz-test-lead' AND subject LIKE '%--force%'")
        self.assertEqual(len(told), 1)

    def test_done_force_marks_listed_messages_read(self):
        self.order("decision")
        self.order("request")
        self.ok("done", "--force", "all good", agent="zz-test-coder")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE recipient='zz-test-coder' "
                                      "AND read_at IS NULL")[0]["c"], 0)

    def test_done_order_arriving_mid_call_not_consumed(self):
        self.order("decision", "first order")
        # runs bin/factory in-process with textwrap.indent (called while printing a listed order,
        # i.e. between the SELECT and the mark-read) inserting a second order for the caller
        script = (
            "import runpy, sqlite3, sys, textwrap, os\n"
            "orig = textwrap.indent\n"
            "def late(text, prefix):\n"
            "    c = sqlite3.connect(os.environ['FACTORY_HOME'] + '/db/factory.db')\n"
            "    c.execute(\"INSERT INTO messages(sender,recipient,kind,subject,body,created) \"\n"
            "              \"VALUES('zz-test-lead','zz-test-coder','decision','late order','late body','t')\")\n"
            "    c.commit(); c.close()\n"
            "    textwrap.indent = orig\n"
            "    return orig(text, prefix)\n"
            "textwrap.indent = late\n"
            "sys.argv = [sys.argv[1], 'done', 'all good']\n"
            "runpy.run_path(sys.argv[0], run_name='__main__')\n"
        )
        p = subprocess.run([sys.executable, "-c", script, str(FACTORY)], capture_output=True, text=True,
                           env=self.env.env("zz-test-coder"), cwd=self.env.home, timeout=60)
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("first order", p.stdout)
        self.assertNotIn("late body", p.stdout)
        late = self.env.sql("SELECT read_at FROM messages WHERE subject='late order'")
        self.assertEqual(len(late), 1)
        self.assertIsNone(late[0]["read_at"])
        self.assertEqual(self.status(), "live")
        p = self.refused(3, "done", "all good", agent="zz-test-coder")
        self.assertIn("late body", p.stdout)
        self.ok("done", "all good", agent="zz-test-coder")
        self.assertEqual(self.status(), "done")


class DoneTaskStatusTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")

    def finish(self, role, status):
        self.env.seed_task(role=role, status=status, team="g-001-a", pr_url=None)
        self.env.seed_agent("zz-test-w", role, goal="G-001", team="g-001-a", task="T-0001")
        self.ok("done", "finished", agent="zz-test-w")
        st = self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"]
        ev = self.env.sql("SELECT action, detail FROM events WHERE entity='task' AND entity_id='T-0001' "
                          "AND action IN ('done','done-ignored')")
        return st, ev

    def test_open_states_flip_to_done(self):
        for role in ("pm", "qa", "cleaner"):
            for status in ("todo", "in_progress", "in_review"):
                with self.subTest(role=role, status=status):
                    self.setUp()
                    st, ev = self.finish(role, status)
                    self.assertEqual(st, "done")
                    self.assertEqual([e["action"] for e in ev], ["done"])
                    self.assertIn("zz-test-w", ev[0]["detail"])

    def test_cancelled_blocked_done_stay(self):
        for status in ("cancelled", "blocked", "done"):
            with self.subTest(status=status):
                self.setUp()
                st, ev = self.finish("pm", status)
                self.assertEqual(st, status)
                self.assertEqual([e["action"] for e in ev], ["done-ignored"])
                self.assertIn(status, ev[0]["detail"])
                self.assertIn("zz-test-w", ev[0]["detail"])

    def test_non_matching_role_writes_no_task_event(self):
        for role in ("reviewer", "qa", "judge", "cleaner"):
            with self.subTest(role=role):
                self.setUp()
                self.env.seed_task(role="coder", status="in_review", team="g-001-a", pr_url=None)
                self.env.seed_agent("zz-test-w", role, goal="G-001", team="g-001-a", task="T-0001")
                before = self.env.sql("SELECT count(*) c FROM events WHERE entity='task' AND entity_id='T-0001'")[0]["c"]
                self.ok("done", "finished", agent="zz-test-w")
                after = self.env.sql("SELECT count(*) c FROM events WHERE entity='task' AND entity_id='T-0001'")[0]["c"]
                self.assertEqual(after, before)
                self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "in_review")

    def test_agent_side_unchanged_when_ignored(self):
        self.finish("pm", "cancelled")
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-test-w'")[0]["status"], "done")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE recipient='zz-test-lead' "
                                      "AND kind='report'")[0]["c"], 1)


if __name__ == "__main__":
    unittest.main()
