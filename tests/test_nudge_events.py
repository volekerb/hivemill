"""T-1542: every tick nudge writes one events row: action nudge-<kind>, detail agent/backend/target/result.
Kinds: unread (agent and boss), idle (director cycle), start-confirm, recycle. Both backends, with the host stubbed.
Python only: go/internal/tick ports pending_nudges, not the nudge, start-confirm or recycle steps."""
import json
import os
import time
import unittest
from unittest import mock

from harness import FactoryTestCase

BACKENDS = ("herdr", "hc")


class NudgeEvents(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()

    def seed(self, name, role, backend, pane="p1", **cols):
        self.env.seed_agent(name, role, pane_id=pane)
        self.env.sql("UPDATE agents SET spawn_args=? WHERE name=?", (json.dumps({"host_backend": backend}), name))
        for k, v in cols.items():
            self.env.sql(f"UPDATE agents SET {k}=? WHERE name=?", (v, name))

    def rows(self, name, kind):
        out = self.env.sql("SELECT actor, entity, detail FROM events WHERE action=? AND entity_id=?", (f"nudge-{kind}", name))
        return [json.loads(r["detail"]) for r in out]

    def run_step(self, m, fn, backend, host_status="idle", prompt_ok=True, **kw):
        listed = [{"name": n, "pane_id": p, "agent_status": host_status}
                  for n, p in self.env.sql("SELECT name, pane_id FROM agents")]
        prompt = (lambda *a, **k: {}) if prompt_ok else (lambda *a, **k: None)
        patches = [mock.patch.object(m, "terminal_backend_effective", return_value=("auto", backend)),
                   mock.patch.object(m, "herdr_agents", return_value=listed),
                   mock.patch.object(m, "_host_agents_list", return_value=listed),
                   mock.patch.object(m, "herdr", side_effect=lambda *a, **k: prompt() if a[:2] == ("agent", "prompt") else None),
                   mock.patch.object(m, "_pt_lap"), mock.patch.object(m, "review_autospawn_tick"),
                   mock.patch.object(m, "coder_ci_resume_tick"), mock.patch.object(m, "judge_autospawn_tick")]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)
        con = m.db()
        fn(con, **kw)
        con.commit()
        mock.patch.stopall()

    def nudge_step(self, m, backend, **kw):
        self.run_step(m, lambda con: m._tick_u_nudge(con, {"t": time.time(), "starting": set()}), backend, **kw)

    def test_unread_nudge_both_backends(self):
        for b in BACKENDS:
            with self.subTest(backend=b), self.env.load_factory() as m:
                name = f"zz-lead-u-{b}"
                self.seed(name, "lead", b, pane=f"pane-{b}")
                self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x',?,'request','s','b',?)",
                             (name, "2026-11-05T12:00:00"))
                self.nudge_step(m, b)
                self.assertEqual(self.rows(name, "unread"), [{"agent": name, "backend": b, "target": f"pane-{b}", "result": "sent"}])

    def test_unread_nudge_skipped_when_prompt_fails(self):
        for b in BACKENDS:
            with self.subTest(backend=b), self.env.load_factory() as m:
                name = f"zz-lead-{b}"
                self.seed(name, "lead", b, pane=f"pane-{b}")
                self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x',?,'request','s','b',?)",
                             (name, "2026-11-05T12:00:00"))
                self.nudge_step(m, b, prompt_ok=False)
                self.assertEqual(self.rows(name, "unread")[0]["result"], "skipped-prompt-failed")

    def test_idle_director_cycle_nudge_both_backends(self):
        for b in BACKENDS:
            with self.subTest(backend=b), self.env.load_factory() as m:
                name = f"zz-director-{b}"
                self.seed(name, "director", b, pane=f"pane-{b}")
                self.nudge_step(m, b)
                self.assertEqual(self.rows(name, "idle"), [{"agent": name, "backend": b, "target": f"pane-{b}", "result": "sent"}])
                self.assertEqual(self.rows(name, "unread"), [])

    def test_no_event_when_nothing_is_sent(self):
        with self.env.load_factory() as m:
            self.seed("zz-lead-q", "lead", "hc")
            self.nudge_step(m, "hc")  # idle lead, no mail: no nudge, no row
            self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action LIKE 'nudge-%'"), [])

    def test_start_confirm_nudge_both_backends(self):
        for b in BACKENDS:
            for ok in (True, False):
                with self.subTest(backend=b, ok=ok), self.env.load_factory() as m:
                    name = f"zz-coder-{b}-{int(ok)}"
                    self.seed(name, "coder", b, pane=f"pane-{b}{int(ok)}", start_pending=time.time(),
                              start_pending_info=json.dumps({"kick": "go"}))
                    self.run_step(m, lambda con: m.start_confirm(con, None), b, prompt_ok=ok)
                    self.assertEqual(self.rows(name, "start-confirm"),
                                     [{"agent": name, "backend": b, "target": f"pane-{b}{int(ok)}",
                                       "result": "sent" if ok else "skipped-prompt-failed"}])

    def test_recycle_nudge_both_backends(self):
        for b in BACKENDS:
            with self.subTest(backend=b), self.env.load_factory() as m:
                name = f"zz-lead-{b}"
                self.seed(name, "lead", b, pane=f"pane-{b}", session_id="sess-1")
                with mock.patch.dict("sys.modules", {"web.tokens": mock.Mock(latest_context=lambda sid: 10**9),
                                                     "web": mock.Mock(tokens=mock.Mock(latest_context=lambda sid: 10**9))}):
                    self.run_step(m, lambda con: m.tick_recycle(con), b)
                self.assertEqual(self.rows(name, "recycle"), [{"agent": name, "backend": b, "target": f"pane-{b}", "result": "sent"}])

    def test_full_tick_writes_unread_nudge_row(self):
        """The real cmd_tick with the stub host: an idle director with unread mail gets one nudge-unread row."""
        e = self.env
        e.seed_agent("zz-director", "director", pane_id="pd")
        e.sql("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x','zz-director','request','s','b','2026-11-05T12:00:00')")
        e.set_herdr(agents=[{"name": "zz-director", "agent": "claude", "pane_id": "pd", "agent_status": "idle",
                             "agent_session": {"value": "s"}}])
        with mock.patch.dict(os.environ, {"FACTORY_NOW": "2026-11-05T12:00:00"}), e.load_factory() as m, \
                mock.patch.object(m, "tick_lock", return_value=None), mock.patch.object(m, "dispatch_tick", lambda con: None):
            m.cmd_tick(mock.Mock(), locked=True)
        rows = e.sql("SELECT action, actor FROM events WHERE action LIKE 'nudge-%' AND entity_id='zz-director'")
        self.assertEqual([(r["action"], r["actor"]) for r in rows], [("nudge-unread", "tick")])
        detail = json.loads(e.sql("SELECT detail FROM events WHERE action='nudge-unread'")[0]["detail"])
        self.assertEqual((detail["agent"], detail["target"], detail["result"]), ("zz-director", "pd", "sent"))
        self.assertIn(detail["backend"], BACKENDS)


if __name__ == "__main__":
    unittest.main()
