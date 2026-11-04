"""T-0972: the tick closes the live pane of a worker agents row that is 'exited' for more than 10 min, per config
exited_pane_reap_mode, and never touches anything else. Real tick as a subprocess, temp FACTORY_HOME, herdr stub."""
import json
import unittest

from harness import FactoryTestCase

OLD = "2026-01-01T00:00:00+00:00"      # long past
FUTURE = "2099-01-01T00:00:00+00:00"   # age is negative: "exited under 10 min ago" without reading the clock


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal("G-9")
        self.env.seed_task("T-9", "G-9", status="todo", pr_url=None)

    def agent(self, name, role="qa", status="exited", pane="p-1", ended=OLD):
        self.env.seed_agent(name, role, "G-9", None, "T-9", status=status, pane_id=pane)
        self.env.sql("UPDATE agents SET spawn_args='{}', ended=? WHERE name=?", (ended, name))

    def host(self, *pairs):
        """The host lists these (name, pane id) agents and panes."""
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "x"}],
            panes=[{"pane_id": p, "tab_id": "t1", "workspace_id": "w1"} for p in dict.fromkeys(p for _, p in pairs)],
            agents=[{"name": n, "agent": "claude", "pane_id": p, "agent_status": "idle",
                     "agent_session": {"value": "s-" + n}} for n, p in pairs])

    def tick(self, mode="on", clear=True):
        if mode:
            self.env.run("config", "exited_pane_reap_mode", mode)
        if clear:
            self.env.sql("DELETE FROM throttle")
        p = self.env.run("tick")
        self.assertEqual(p.returncode, 0, p.stderr)

    def events(self, action):
        return [dict(r) for r in self.env.sql("SELECT entity_id, detail FROM events WHERE action=?", (action,))]

    def closes(self):
        return [c for c in self.env.calls("herdr") if c["argv"][:2] == ["pane", "close"]]

    def panes(self):
        return [p["pane_id"] for p in json.loads(self.env.herdr_state.read_text())["panes"]]


class Reap(Base):
    def test_exited_worker_pane_past_10_min_is_closed_with_one_event(self):
        self.agent("qa-t-9")
        self.host(("qa-t-9", "p-1"))
        self.tick()
        self.assertEqual(self.panes(), [])
        self.assertEqual(len(self.closes()), 1)
        ev = self.events("exited-pane-closed")
        self.assertEqual([e["entity_id"] for e in ev], ["qa-t-9"])
        self.assertEqual(json.loads(ev[0]["detail"])["pane"], "p-1")

    def test_exited_under_10_min_is_not_closed(self):
        self.agent("qa-t-9", ended=FUTURE)
        self.host(("qa-t-9", "p-1"))
        self.tick()
        self.assertEqual(self.panes(), ["p-1"])
        self.assertEqual(self.closes(), [])
        self.assertEqual(self.events("exited-pane-closed"), [])

    def test_exited_without_ended_time_is_not_closed(self):
        self.agent("qa-t-9", ended=None)
        self.host(("qa-t-9", "p-1"))
        self.tick()
        self.assertEqual(self.panes(), ["p-1"])

    def test_dry_run_default_logs_would_close_once_and_closes_nothing(self):
        self.agent("qa-t-9")
        self.host(("qa-t-9", "p-1"))
        self.tick(mode=None)
        self.tick(mode=None)
        self.assertEqual(self.panes(), ["p-1"])
        self.assertEqual(self.closes(), [])
        self.assertEqual(len(self.events("exited-pane-would-close")), 1)

    def test_off_does_nothing(self):
        self.agent("qa-t-9")
        self.host(("qa-t-9", "p-1"))
        self.tick(mode="off")
        self.assertEqual(self.panes(), ["p-1"])
        self.assertEqual(self.events("exited-pane-would-close"), [])

    def test_throttled_second_run_does_nothing(self):
        self.agent("qa-t-9")
        self.host(("qa-t-9", "p-1"))
        self.env.run("config", "exited_pane_reap_mode", "dry-run")
        self.tick(mode=None)
        self.env.sql("DELETE FROM events WHERE action='exited-pane-would-close'")
        self.tick(mode=None, clear=False)
        self.assertEqual(self.events("exited-pane-would-close"), [])


class Never(Base):
    def assertKept(self, *pane_ids):
        self.assertEqual(self.closes(), [])
        self.assertEqual(sorted(self.panes()), sorted(pane_ids))
        self.assertEqual(self.events("exited-pane-closed"), [])

    def test_pane_without_agents_row_is_kept(self):
        self.host(("owner-shell", "p-1"))
        self.tick()
        self.assertKept("p-1")

    def test_exited_row_with_other_pane_id_is_kept(self):
        self.agent("qa-t-9", pane="p-old")
        self.host(("qa-t-9", "p-1"))
        self.tick()
        self.assertKept("p-1")

    def test_hq_rows_are_kept(self):
        for i, role in enumerate(("boss", "director", "lead", "staff-engineer")):
            self.agent(f"claude-{role}", role=role, pane=f"p-{i}")
        self.host(*[(f"claude-{r}", f"p-{i}") for i, r in enumerate(("boss", "director", "lead", "staff-engineer"))])
        self.tick()
        self.assertKept("p-0", "p-1", "p-2", "p-3")

    def test_live_starting_queued_rows_are_kept(self):
        for i, st in enumerate(("live", "starting", "queued", "stopped", "done", "lost")):
            self.agent(f"qa-{st}", status=st, pane=f"p-{i}")
        # a queued row the tick must not try to spawn: claimed far in the future
        self.env.sql("UPDATE agents SET spawn_args=? WHERE name IN ('qa-queued','qa-starting')", (json.dumps({"role": "qa", "claimed_at": 4e9}),))
        names = [(f"qa-{st}", f"p-{i}") for i, st in enumerate(("live", "starting", "queued", "stopped", "done", "lost"))]
        self.host(*names)
        self.tick()
        # 'done' is reaped by the tick's own loop (pre-existing); the rest are untouched
        self.assertEqual(self.events("exited-pane-closed"), [])
        self.assertEqual(sorted(p for p in self.panes()), sorted(p for n, p in names if n != "qa-done"))

    def test_pane_held_by_a_newer_live_row_is_kept(self):
        self.agent("qa-old")
        self.agent("qa-new", status="live", pane="p-1", ended=None)
        self.host(("qa-old", "p-1"), ("qa-new", "p-1"))
        self.tick()
        self.assertKept("p-1")

    def test_reused_name_now_live_is_kept(self):
        self.agent("qa-t-9", status="live", pane="p-2", ended=None)
        self.host(("qa-t-9", "p-1"))
        self.tick()
        self.assertEqual(self.closes(), [])
        self.assertEqual(self.events("exited-pane-closed"), [])


class Failure(Base):
    def test_failed_close_is_one_event_retried_without_spam_and_tick_survives(self):
        self.agent("qa-t-9")
        self.host(("qa-t-9", "p-1"))
        self.env.set_herdr(pane_close_fail=True)
        self.tick()
        self.tick()
        self.assertEqual(len(self.events("exited-pane-close-failed")), 1)
        self.assertEqual(self.events("exited-pane-closed"), [])
        self.assertEqual(len(self.closes()), 2)  # retried on the second run
        self.env.set_herdr(pane_close_fail=False)
        self.tick()
        self.assertEqual(self.panes(), [])
        self.assertEqual(len(self.events("exited-pane-closed")), 1)
        self.assertEqual(len(self.events("exited-pane-close-failed")), 1)


if __name__ == "__main__":
    unittest.main()
