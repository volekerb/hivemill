"""G-010 T-C2: a sandboxed agent whose claude runs in the host user namespace is closed (after start and in the
tick's claude audit). Fake /proc via the harness: ns/user symlinks decide."""
import json
import os
import shutil

from harness import FactoryTestCase
from test_sandbox_spawn import prep

HOST, SANDBOX = "user:[4026531837]", "user:[4026532999]"


class EscapeCase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_agent("zz-director", "director", pane_id="p-dir")

    def ns(self, pid, link):
        d = self.env.proc_dir / str(pid) / "ns"
        d.mkdir(parents=True, exist_ok=True)
        if link is not None:
            os.symlink(link, d / "user")

    def agent(self, name, sid, pid, link, sandbox=True, pane="p-c"):
        e = self.env
        e.seed_agent(name, "coder", goal="G-001", task="T-0001", pane_id=pane)
        args = json.dumps({"sandbox": True} if sandbox else {})
        e.sql("UPDATE agents SET session_id=?, spawn_args=? WHERE name=?", (sid, args, name))
        e.fake_claude(pid, argv=("claude", "--session-id", sid, "--settings", "/x/settings/coder.json"))
        self.ns(pid, link)
        e.set_herdr(agents=[{"name": "zz-director", "agent": "claude", "pane_id": "p-dir", "agent_status": "idle"},
                            {"name": name, "agent": "claude", "pane_id": pane, "agent_status": "working",
                             "agent_session": {"value": sid}}])

    def closes(self, pane):
        return [c for c in self.env.calls("herdr") if c["argv"][:3] == ["pane", "close", pane]]

    def escaped(self):
        return self.env.sql("SELECT entity_id, detail FROM events WHERE action='sandbox-escaped'")

    def alerts(self):
        return self.env.sql("SELECT body FROM messages WHERE recipient IN ('director','zz-director') AND subject LIKE 'sandbox escaped%'")

    def audit(self):
        self.env.sql("DELETE FROM throttle WHERE key='claude-audit'")
        self.ok("tick")


class AuditTests(EscapeCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_task()
        self.ns(1, HOST)

    def test_escaped_agent_closed_once_alerted_once(self):
        self.agent("zz-c", "s1", 100, HOST)
        self.audit()
        self.assertGreaterEqual(len(self.closes("p-c")), 1)
        ev = self.escaped()
        self.assertEqual([(r["entity_id"], json.loads(r["detail"])) for r in ev], [("zz-c", {"name": "zz-c", "pid": 100})])
        self.assertEqual(len(self.alerts()), 1)
        self.audit()
        self.assertEqual(len(self.escaped()), 1)
        self.assertEqual(len(self.alerts()), 1)

    def test_other_user_ns_is_left_alone(self):
        self.agent("zz-c", "s1", 100, SANDBOX)
        self.audit()
        self.assertEqual(self.closes("p-c"), [])
        self.assertEqual(self.escaped(), [])

    def test_non_sandboxed_agent_in_host_ns_is_never_touched(self):
        self.agent("zz-c", "s1", 100, HOST, sandbox=False)
        self.audit()
        self.assertEqual(self.closes("p-c"), [])
        self.assertEqual(self.escaped(), [])
        self.assertEqual(self.alerts(), [])

    def test_unreadable_agent_ns_decides_nothing(self):
        self.agent("zz-c", "s1", 100, None)
        self.audit()
        self.assertEqual(self.closes("p-c"), [])
        self.assertEqual(self.escaped(), [])


    def test_reused_pid_escaping_again_alerts_again(self):
        self.agent("zz-c", "s1", 100, HOST)
        self.audit()
        self.assertEqual(len(self.alerts()), 1)
        (self.env.proc_dir / "100" / "stat").write_text("100 (claude) S 1 100 100 0 -1 0 " + "0 " * 12 + "999\n")
        self.env.sql("UPDATE agents SET status='live', pane_id='p-c' WHERE name='zz-c'")  # relaunched on the reused pid
        self.env.set_herdr(agents=[{"name": "zz-director", "agent": "claude", "pane_id": "p-dir", "agent_status": "idle"},
                                   {"name": "zz-c", "agent": "claude", "pane_id": "p-c", "agent_status": "working",
                                    "agent_session": {"value": "s1"}}])
        self.audit()
        self.assertEqual(len(self.alerts()), 2)


class AfterStartTests(EscapeCase):
    def setUp(self):
        super().setUp()
        prep(self.env)
        self.ns(1, HOST)

    def start_with(self, link):
        """spawn(resume=...) in-process, the session's claude already running with this user ns."""
        self.ok("config", "sandbox_roles", "reviewer")
        self.env.fake_claude(200, argv=("claude", "--resume", "sess-r", "--settings", "/x/settings/reviewer.json"))
        self.ns(200, link)
        with self.env.load_factory() as mod:
            mod.spawn(mod.db(), "reviewer", goal="G-001", team="g-001-a", name="zz-reviewer",
                      cwd=str(self.env.home_dir / "worktrees" / "w-reviewer"), resume="sess-r")

    def test_escaped_spawn_closed_right_after_start(self):
        self.start_with(HOST)
        self.assertEqual([json.loads(r["detail"])["pid"] for r in self.escaped()], [200])
        self.assertEqual(len(self.alerts()), 1)
        pane = self.env.sql("SELECT pane_id FROM agents WHERE name='zz-reviewer'")[0]["pane_id"]
        self.assertEqual(len(self.closes(pane)), 1)

    def test_sandboxed_ns_after_start_is_left_alone(self):
        self.start_with(SANDBOX)
        self.assertEqual(self.escaped(), [])

    def test_non_claude_command_naming_the_session_is_ignored(self):
        self.ok("config", "sandbox_roles", "reviewer")
        self.env.fake_claude(200, argv=("grep", "claude", "--resume", "sess-r"))
        self.ns(200, HOST)
        with self.env.load_factory() as mod:
            mod.spawn(mod.db(), "reviewer", goal="G-001", team="g-001-a", name="zz-reviewer",
                      cwd=str(self.env.home_dir / "worktrees" / "w-reviewer"), resume="sess-r")
        self.assertEqual(self.escaped(), [])


class StartTimeBeforeCloseTests(EscapeCase):
    """The close kills claude, so /proc/<pid> is gone afterwards: the start time must be read before it."""

    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_task()
        self.ns(1, HOST)

    def escape(self, mod, con, start):
        """One escape of pid 100 (stat shows `start`); the pane close removes /proc/100 like a real close."""
        root = self.env.proc_dir / "100"
        self.ns(100, HOST)
        if start is not None:
            (root / "stat").write_text("100 (claude) S 1 100 100 0 -1 0 " + "0 " * 12 + f"{start}\n")
        closed = []

        def close(*argv, **kw):
            closed.append(argv)
            shutil.rmtree(root)

        mod.herdr = close
        self.assertTrue(mod.sandbox_escape_close(con, "zz-c", "p-c", 100))
        return closed

    def test_reused_pid_alerts_again_when_close_removes_proc(self):
        self.env.seed_agent("zz-c", "coder", goal="G-001", task="T-0001", pane_id="p-c")
        with self.env.load_factory() as mod:
            con = mod.db()
            self.assertEqual(len(self.escape(mod, con, 555)), 1)
            self.assertEqual(len(self.escape(mod, con, 999)), 1)
            keys = sorted(r["key"] for r in con.execute("SELECT key FROM throttle WHERE key LIKE 'sandbox-escaped:%'"))
            con.commit()
        self.assertEqual(keys, ["sandbox-escaped:100:555", "sandbox-escaped:100:999"])
        self.assertEqual(len(self.alerts()), 2)

    def test_same_process_is_alerted_once(self):
        self.env.seed_agent("zz-c", "coder", goal="G-001", task="T-0001", pane_id="p-c")
        with self.env.load_factory() as mod:
            con = mod.db()
            self.escape(mod, con, 555)
            self.escape(mod, con, 555)
            con.commit()
        self.assertEqual(len(self.alerts()), 1)

    def test_unreadable_stat_still_closes_pane_and_alerts(self):
        self.env.seed_agent("zz-c", "coder", goal="G-001", task="T-0001", pane_id="p-c")
        with self.env.load_factory() as mod:
            con = mod.db()
            closed = self.escape(mod, con, None)
            keys = [r["key"] for r in con.execute("SELECT key FROM throttle WHERE key LIKE 'sandbox-escaped:%'")]
            con.commit()
        self.assertEqual(closed, [("pane", "close", "p-c")])
        self.assertEqual(keys, ["sandbox-escaped:100:None"])
        self.assertEqual(len(self.alerts()), 1)
