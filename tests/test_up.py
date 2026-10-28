from harness import FactoryTestCase


class UpTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        for name, role, sess, team in (("zz-test-director", "director", "sess-dir", None),
                                       ("zz-test-boss", "boss", "sess-boss", None),
                                       ("zz-test-lead", "lead", "sess-lead", "g-001-a")):
            e.seed_agent(name, role, goal="G-001" if team else None, team=team, status="lost")
            e.sql("UPDATE agents SET session_id=? WHERE name=?", (sess, name))

    def starts(self):
        return {c["argv"][2]: c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]}

    def test_up_resumes_hq_and_leads(self):
        self.ok("up")
        starts = self.starts()
        self.assertEqual(set(starts), {"zz-test-director", "zz-test-boss", "zz-test-lead"})
        for name, sess in (("zz-test-director", "sess-dir"), ("zz-test-boss", "sess-boss"), ("zz-test-lead", "sess-lead")):
            argv = starts[name]
            self.assertEqual(argv[argv.index("--resume") + 1], sess)

    def test_up_idempotent(self):
        self.set_agents(("zz-test-director", None), ("zz-test-boss", None), ("zz-test-lead", None))
        self.ok("up")
        self.assertEqual(self.starts(), {})

    def test_up_skips_closed_team(self):
        self.env.sql("UPDATE teams SET status='closed' WHERE id='g-001-a'")
        self.ok("up")
        self.assertEqual(set(self.starts()), {"zz-test-director", "zz-test-boss"})

    def test_up_herdr_down_fails(self):
        self.env.set_herdr(fail=True)
        p = self.refused(1, "up")
        self.assertIn("herdr is unreachable", p.stderr)

    def test_up_skips_agents_resumed_by_herdr(self):
        # herdr restored its layout and resumed the panes itself, under its own names
        self.set_agents(("restored-1", "sess-dir"), ("restored-2", "sess-boss"), ("restored-3", "sess-lead"))
        self.ok("up")
        self.assertEqual(self.starts(), {})

    def test_up_adopts_live_rows_resumed_by_herdr(self):
        # rows are live with stale pane ids; herdr resumed the sessions under other names and panes
        for name in ("zz-test-director", "zz-test-boss", "zz-test-lead"):
            self.env.sql("UPDATE agents SET status='live', pane_id='stale' WHERE name=?", (name,))
        self.set_agents(("restored-1", "sess-dir"), ("restored-2", "sess-boss"), ("restored-3", "sess-lead"))
        self.ok("up")
        self.assertEqual(self.starts(), {})
        rows = dict(tuple(r) for r in self.env.sql("SELECT name, status || ':' || pane_id FROM agents"))
        self.assertEqual(rows, {"zz-test-director": "live:px0", "zz-test-boss": "live:px1", "zz-test-lead": "live:px2"})
        self.assertEqual(self.env.sql("SELECT count(*) FROM messages WHERE subject LIKE '%vanished%'")[0][0], 0)

    def test_up_starts_only_missing(self):
        self.set_agents(("zz-test-director", None), ("restored-2", "sess-boss"))
        self.ok("up")
        self.assertEqual(set(self.starts()), {"zz-test-lead"})

    def set_agents(self, *pairs):
        self.env.set_herdr(agents=[{"name": n, "agent": "claude", "pane_id": f"px{i}", "agent_status": "idle",
                                    "agent_session": {"value": s} if s else {}} for i, (n, s) in enumerate(pairs)])
