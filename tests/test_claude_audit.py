"""G-006 A6: the tick audit of Claude processes started under a factory agent outside `factory spawn`, and of
agent processes resumed without --system-prompt-snapshot off. Fake /proc via the harness."""
import json
import time
import unittest

from harness import FactoryTestCase

CODER = "zz-coder-x"
SESS = "sess-coder-x"
SNAP = ["--system-prompt-snapshot", "off"]


class ClaudeAuditCase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team()
        e.seed_task()
        e.seed_agent("zz-director", "director", pane_id="p-dir")
        e.seed_agent(CODER, "coder", goal="G-001", task="T-0001", pane_id="p-c")
        e.sql("UPDATE agents SET session_id=? WHERE name=?", (SESS, CODER))
        e.set_herdr(agents=[{"name": CODER, "agent": "claude", "pane_id": "p-c", "agent_status": "working",
                             "agent_session": {"value": SESS}}])

    def tree(self, own=("claude", "--session-id", SESS)):
        self.env.fake_claude(100, ppid=1, argv=own)
        self.env.fake_claude(101, ppid=100, argv=("bash", "-c", "x"))
        self.env.fake_claude(102, ppid=101, argv=("claude", "-p", "hi"))

    def events(self):
        return self.env.sql("SELECT entity_id, detail FROM events WHERE action='claude-outside-spawn'")

    def alerts(self):
        return self.env.sql("SELECT sender, kind, subject, body FROM messages WHERE recipient IN ('director','zz-director') AND kind='alert'"
                            " AND subject LIKE 'claude %'")

    def reset_throttle(self):
        self.env.sql("DELETE FROM throttle WHERE key='claude-audit'")

    def test_child_claude_flagged_once(self):
        self.tree()
        self.ok("tick")
        ev = self.events()
        self.assertEqual(len(ev), 1)
        self.assertEqual(ev[0]["entity_id"], CODER)
        self.assertIn('"pid": 102', ev[0]["detail"])
        al = self.alerts()
        self.assertEqual(len(al), 1)
        self.assertEqual(al[0]["sender"], "tick")
        self.assertIn(f"under {CODER} (task T-0001): pid 102 claude -p hi", al[0]["body"])
        self.assertIn("Only factory spawn starts Claude.", al[0]["body"])
        self.reset_throttle()
        self.ok("tick")
        self.assertEqual(len(self.events()), 1)
        self.assertEqual(len(self.alerts()), 1)

    def test_no_findings_for_legit_trees(self):
        e = self.env
        e.fake_claude(100, argv=("claude", "--session-id", SESS))  # the agent's own process
        e.fake_claude(200, argv=("claude",))                       # owner's top-level pane
        e.fake_claude(300, argv=("zsh",))                          # non-agent shell
        e.fake_claude(301, ppid=300, argv=("claude", "-p", "x"))
        e.fake_claude(400, argv=("claude", "--resume", "sess-gone", *SNAP))
        e.sql("UPDATE agents SET status='done' WHERE name=?", (CODER,))
        e.fake_claude(401, ppid=400, argv=("claude", "-p", "y"))   # under a session of no live agent
        self.ok("tick")
        self.assertEqual(self.events(), [])
        self.assertEqual(self.alerts(), [])

    def test_child_of_done_agent_not_flagged(self):
        self.tree()
        self.env.sql("UPDATE agents SET status='done' WHERE name=?", (CODER,))
        self.ok("tick")
        self.assertEqual(self.events(), [])

    def test_unreadable_or_empty_proc_dir(self):
        self.ok("tick")  # empty
        self.assertEqual(self.events(), [])
        self.env.proc_dir.rmdir()
        self.reset_throttle()
        self.ok("tick")  # gone
        self.assertEqual(self.events(), [])

    def test_throttled_300s(self):
        self.ok("tick")
        self.tree()
        self.ok("tick")  # within 300 s of the first run: skipped
        self.assertEqual(self.events(), [])
        self.env.sql("UPDATE throttle SET ts=? WHERE key='claude-audit'", (str(time.time() - 301),))
        self.ok("tick")
        self.assertEqual(len(self.events()), 1)

    def test_resume_without_snapshot_flag_flagged(self):
        """The spawn resume path: the agent's own process, with role settings but without the flag."""
        e = self.env
        e.fake_claude(100, argv=("claude", "--settings", "roles/settings/coder.json", "--resume", SESS))
        self.ok("tick")
        ev = self.events()
        self.assertEqual([(r["entity_id"], json.loads(r["detail"])["pid"], json.loads(r["detail"])["reasons"]) for r in ev],
                         [(CODER, 100, ["resume-without-snapshot-off"])])
        al = self.alerts()
        self.assertEqual(len(al), 1)
        self.assertIn("resumed without --system-prompt-snapshot off", al[0]["subject"])
        self.assertNotIn("outside factory spawn", al[0]["subject"])

    def test_agent_own_resume_not_flagged(self):
        self.env.fake_claude(100, argv=("claude", "--settings", "roles/settings/coder.json", "--resume", SESS, *SNAP))
        self.ok("tick")
        self.assertEqual(self.events(), [])

    def test_child_resume_reasons(self):
        e = self.env
        e.fake_claude(100, argv=("claude", "--session-id", SESS))
        e.fake_claude(101, ppid=100, argv=("bash",))
        e.fake_claude(110, ppid=101, argv=("claude", "--resume", "other"))
        e.fake_claude(111, ppid=101, argv=("claude", "--resume", "other", *SNAP))
        self.ok("tick")
        reasons = {json.loads(r["detail"])["pid"]: json.loads(r["detail"])["reasons"] for r in self.events()}
        self.assertEqual(reasons, {110: ["outside-spawn", "resume-without-snapshot-off"], 111: ["outside-spawn"]})
        self.assertTrue(all("outside factory spawn" in a["subject"] for a in self.alerts()))

    def test_non_claude_commands_mentioning_claude_not_flagged(self):
        e = self.env
        e.fake_claude(100, argv=("claude", "--session-id", SESS))
        e.fake_claude(101, ppid=100, argv=("bash",))
        e.fake_claude(102, ppid=101, argv=("grep", "-r", "claude", "."))
        e.fake_claude(103, ppid=101, argv=("tail", "-f", "claude.log"))
        self.ok("tick")
        self.assertEqual(self.events(), [])

    def test_node_claude_cli_child_flagged(self):
        e = self.env
        e.fake_claude(100, argv=("claude", "--session-id", SESS))
        e.fake_claude(101, ppid=100, argv=("bash",))
        e.fake_claude(102, ppid=101, argv=("node", "/usr/lib/claude", "-p", "hi"))
        self.ok("tick")
        self.assertEqual([json.loads(r["detail"])["pid"] for r in self.events()], [102])


if __name__ == "__main__":
    unittest.main()
