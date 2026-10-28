import unittest

from harness import FactoryTestCase


class InboxTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a")
        e.seed_agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        e.seed_agent("zz-test-director", "director")

    def recipient(self, to):
        p = self.ok("msg", to, "subj", "body", agent="zz-test-coder")
        self.assertRegex(p.stdout, r"^M\d+ → ")
        return self.env.sql("SELECT recipient FROM messages ORDER BY id DESC LIMIT 1")[0]["recipient"]

    def test_inbox_recipient_agent_name(self):
        self.assertEqual(self.recipient("zz-test-lead"), "zz-test-lead")

    def test_inbox_recipient_role_name(self):
        self.assertEqual(self.recipient("director"), "zz-test-director")

    def test_inbox_recipient_role_picks_newest_live(self):
        self.env.seed_agent("zz-test-lead2", "lead", status="live", started="2026-02-01T00:00:00+00:00")
        self.env.seed_agent("zz-test-lead3", "lead", status="exited", started="2026-03-01T00:00:00+00:00")
        self.assertEqual(self.recipient("lead"), "zz-test-lead2")

    def test_inbox_recipient_lead_team(self):
        self.assertEqual(self.recipient("lead:g-001-a"), "zz-test-lead")

    def test_inbox_recipient_lead_team_without_lead_fails(self):
        self.env.seed_team("g-001-b", lead=None)
        p = self.refused(1, "msg", "lead:g-001-b", "s", "b", agent="zz-test-coder")
        self.assertIn("no lead", p.stderr)

    def test_inbox_recipient_owner_goes_to_boss(self):
        self.assertEqual(self.recipient("owner"), "boss")

    def test_inbox_recipient_boss_without_agent_row(self):
        self.assertEqual(self.recipient("boss"), "boss")

    def test_inbox_recipient_unknown_fails(self):
        p = self.refused(1, "msg", "zz-test-nobody", "s", "b", agent="zz-test-coder")
        self.assertIn("unknown recipient", p.stderr)

    def test_inbox_marks_read(self):
        self.ok("msg", "zz-test-lead", "first", "body one", agent="zz-test-coder")
        p = self.ok("inbox", agent="zz-test-lead")
        self.assertIn("first", p.stdout)
        self.assertIn("from zz-test-coder", p.stdout)
        self.assertIn("body one", p.stdout)
        self.assertIn("inbox empty", self.ok("inbox", agent="zz-test-lead").stdout)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE read_at IS NULL")[0]["c"], 0)

    def test_inbox_peek_does_not_mark_read(self):
        self.ok("msg", "zz-test-lead", "first", "b", agent="zz-test-coder")
        self.assertIn("first", self.ok("inbox", "--peek", agent="zz-test-lead").stdout)
        self.assertIn("first", self.ok("inbox", agent="zz-test-lead").stdout)

    def test_inbox_all_shows_read(self):
        self.ok("msg", "zz-test-lead", "first", "b", agent="zz-test-coder")
        self.ok("inbox", agent="zz-test-lead")
        self.assertIn("inbox empty", self.ok("inbox", agent="zz-test-lead").stdout)
        self.assertIn("first", self.ok("inbox", "--all", agent="zz-test-lead").stdout)

    def test_inbox_agent_flag_does_not_mark_read(self):
        self.ok("msg", "zz-test-lead", "first", "b", agent="zz-test-coder")
        p = self.ok("inbox", "--agent", "zz-test-lead", agent="zz-test-director")
        self.assertIn("first", p.stdout)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE read_at IS NULL")[0]["c"], 1)
        self.assertIn("first", self.ok("inbox", agent="zz-test-lead").stdout)

    def test_inbox_only_own_messages(self):
        self.ok("msg", "zz-test-lead", "for lead", "b", agent="zz-test-coder")
        self.assertIn("inbox empty", self.ok("inbox", agent="zz-test-director").stdout)

    def test_inbox_two_dash_args_exit_2(self):
        p = self.refused(2, "goal", "new", "--title", "zz-test", "--intent", "-", "--success", "-", stdin="text\n")
        self.assertIn("only one argument per call can be '-'", p.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM goals")[0]["c"], 1)

    def test_inbox_single_dash_reads_stdin(self):
        p = self.ok("msg", "zz-test-lead", "long", "-", stdin="from stdin\n", agent="zz-test-coder")
        self.assertEqual(self.env.sql("SELECT body FROM messages WHERE id=1")[0]["body"], "from stdin")

    def test_inbox_message_kind_and_ids_shown(self):
        self.env.seed_task(status="todo", pr_url=None)
        self.ok("msg", "zz-test-lead", "s", "b", "--kind", "report", "--goal", "G-001", "--task", "T-0001",
                agent="zz-test-coder")
        out = self.ok("inbox", agent="zz-test-lead").stdout
        self.assertIn("[report]", out)
        self.assertIn("goal=G-001 task=T-0001", out)


BODY10 = "\n".join(f"line {i}" for i in range(1, 11))


class InboxTerseTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a")
        e.seed_agent("zz-test-coder2", "coder", goal="G-001", team="g-001-a")
        e.seed_agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        e.seed_agent("zz-test-director", "director")
        e.seed_agent("boss", "boss")

    def send(self, to, kind="report", subject="subj", body=BODY10, sender="zz-test-coder"):
        self.ok("msg", to, subject, body, "--kind", kind, agent=sender)
        return self.env.sql("SELECT id FROM messages ORDER BY id DESC LIMIT 1")[0]["id"]

    def test_hq_report_collapsed(self):
        mid = self.send("director")
        out = self.ok("inbox", agent="zz-test-director").stdout
        self.assertIn("line 1", out)
        self.assertIn(f"(+9 lines: factory inbox --show M{mid})", out)
        self.assertNotIn("line 5", out)

    def test_hq_other_kinds_full(self):
        for kind in ("request", "decision", "escalation", "alert", "msg"):
            with self.subTest(kind=kind):
                self.send("director", kind=kind, sender="zz-test-lead")
                out = self.ok("inbox", agent="zz-test-director").stdout
                self.assertIn("line 10", out)
                self.assertNotIn("--show", out)

    def test_hq_short_report_full(self):
        self.send("director", body="a\nb")
        out = self.ok("inbox", agent="zz-test-director").stdout
        self.assertIn("   a\n   b", out)
        self.assertNotIn("--show", out)

    def test_hq_long_single_line_report_collapsed(self):
        self.send("lead:g-001-a", body="x" * 500)
        out = self.ok("inbox", agent="zz-test-lead").stdout
        self.assertIn(f"(truncated: factory inbox --show M", out)
        self.assertNotIn("+0 lines", out)
        self.assertNotIn("x" * 201, out)

    def test_daily_digest_full(self):
        self.send("boss", subject="daily digest 2026-10-25")
        self.assertIn("line 10", self.ok("inbox", agent="boss").stdout)

    def test_boss_report_collapsed(self):
        self.send("boss")
        self.assertNotIn("line 5", self.ok("inbox", agent="boss").stdout)

    def test_worker_report_full(self):
        self.ok("msg", "zz-test-coder", "subj", BODY10, "--kind", "report", agent="zz-test-coder2")
        out = self.ok("inbox", agent="zz-test-coder").stdout
        self.assertIn("line 10", out)
        self.assertNotIn("--show", out)

    def test_full_flag(self):
        self.send("director")
        self.assertIn("line 10", self.ok("inbox", "--full", agent="zz-test-director").stdout)

    def test_show_recipient_keeps_read_at(self):
        self.ok("msg", "zz-test-coder", "subj", BODY10, agent="zz-test-coder2")
        mid = self.env.sql("SELECT id FROM messages")[0]["id"]
        for arg in (f"M{mid}", str(mid)):
            out = self.ok("inbox", "--show", arg, agent="zz-test-coder").stdout
            self.assertIn("line 10", out)
        self.assertIsNone(self.env.sql("SELECT read_at FROM messages")[0]["read_at"])

    def test_show_other_worker_refused(self):
        mid = self.send("director")
        p = self.refused(3, "inbox", "--show", f"M{mid}", agent="zz-test-coder2")
        self.assertIn("not your message", p.stderr)
        self.assertNotIn("line 1", p.stdout)

    def test_show_director_allowed(self):
        mid = self.send("lead:g-001-a")
        self.assertIn("line 10", self.ok("inbox", "--show", f"M{mid}", agent="zz-test-director").stdout)

    def test_show_unknown_id(self):
        self.refused(1, "inbox", "--show", "M999", agent="zz-test-director")

    def test_limit_marks_only_printed(self):
        for i in range(60):
            self.send("zz-test-lead", kind="msg", subject=f"s{i}", body="b")
        out = self.ok("inbox", agent="zz-test-lead").stdout
        self.assertTrue(out.startswith("(10 older unread not shown"), out[:80])
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE read_at IS NULL")[0]["c"], 10)
        out2 = self.ok("inbox", agent="zz-test-lead").stdout
        self.assertNotIn("older unread", out2)
        for i in range(10):
            self.assertIn(f"s{i}\n", out2)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE read_at IS NULL")[0]["c"], 0)

    def test_limit_zero_refused(self):
        self.send("zz-test-lead", kind="msg", body="b")
        self.refused(1, "inbox", "--limit", "0", agent="zz-test-lead")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE read_at IS NULL")[0]["c"], 1)


if __name__ == "__main__":
    unittest.main()
