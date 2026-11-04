"""T-1007: every pane close verifies the host pane's name first. A row's pane_id that is now held by a differently named
host pane is never closed (one "pane-close-skipped" event); a pane under the row's exact name with another id is closed
by the host's id; a host that cannot be asked closes nothing. Real tick / spawn / stop as subprocesses, herdr stub."""
import json
import unittest

from harness import FactoryTestCase
from test_stop import StopBase, SID  # noqa: F401  (StopBase has no tests of its own)


def host_state(*pairs):
    """The host lists these (name, pane id) agents and panes."""
    return dict(
        workspaces=[{"workspace_id": "w1", "label": "x"}],
        panes=[{"pane_id": p, "tab_id": "t1", "workspace_id": "w1"} for p in dict.fromkeys(p for _, p in pairs)],
        agents=[{"name": n, "agent": "claude", "pane_id": p, "agent_status": "idle",
                 "agent_session": {"value": "s-" + n}} for n, p in pairs])


class Mixin:
    def closes(self):
        return [c["argv"][2] for c in self.env.calls("herdr") if c["argv"][:2] == ["pane", "close"]]

    def events(self, action):
        return [dict(r) for r in self.env.sql("SELECT entity_id, detail FROM events WHERE action=?", (action,))]

    def panes(self):
        return sorted(p["pane_id"] for p in json.loads(self.env.herdr_state.read_text())["panes"])


class DoneRow(Mixin, FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal("G-9")
        self.env.seed_task("T-9", "G-9", status="todo", pr_url=None)
        self.env.seed_agent("qa-t-9", "qa", "G-9", None, "T-9", status="done", pane_id="p-1")
        self.env.sql("UPDATE agents SET spawn_args='{}' WHERE name='qa-t-9'")

    def row(self):
        return dict(self.env.sql("SELECT status, pane_id FROM agents WHERE name='qa-t-9'")[0])

    def tick(self):
        p = self.env.run("tick")
        self.assertEqual(p.returncode, 0, p.stderr)

    def spawn(self):
        return self.env.run("spawn", "qa", "--name", "qa-t-9", "--goal", "G-9", "--task", "T-9", "--cwd", str(self.env.home))

    # --- tick reap of a done row
    def test_tick_row_id_held_by_other_name_is_not_closed(self):
        self.env.set_herdr(**host_state(("owner-pane", "p-1")))
        self.tick()
        self.assertEqual(self.closes(), [])
        self.assertEqual(self.panes(), ["p-1"])
        ev = self.events("pane-close-skipped")
        self.assertEqual([e["entity_id"] for e in ev], ["qa-t-9"])
        self.assertIn("owner-pane", json.loads(ev[0]["detail"])["reason"])
        self.assertEqual(self.row()["status"], "exited")  # today's no-pane path

    def test_tick_host_pane_under_exact_name_with_other_id_is_closed_by_host_id(self):
        self.env.set_herdr(**host_state(("qa-t-9", "p-2"), ("owner-pane", "p-1")))
        self.tick()
        self.assertEqual(self.closes(), ["p-2"])
        self.assertEqual(self.panes(), ["p-1"])
        self.assertEqual(self.events("pane-close-skipped"), [])
        self.assertEqual(self.row()["status"], "exited")

    def test_tick_same_id_and_name_is_closed_as_today(self):
        self.env.set_herdr(**host_state(("qa-t-9", "p-1")))
        self.tick()
        self.assertEqual(self.closes(), ["p-1"])
        self.assertEqual(self.panes(), [])
        self.assertEqual(self.row()["status"], "exited")

    def test_tick_host_unreachable_closes_nothing_and_does_not_crash(self):
        self.env.set_herdr(fail=True)
        self.tick()
        self.assertEqual(self.closes(), [])
        self.assertEqual(self.row()["status"], "done")

    # --- release_stale_pane (spawn reusing the name)
    def test_release_row_id_held_by_other_name_is_not_closed(self):
        self.env.set_herdr(**host_state(("owner-pane", "p-1")))
        self.spawn()
        self.assertNotIn("p-1", self.closes())
        self.assertIn("p-1", self.panes())
        self.assertEqual(len(self.events("pane-close-skipped")), 1)

    def test_release_host_pane_under_exact_name_with_other_id_is_closed_by_host_id(self):
        self.env.set_herdr(**host_state(("qa-t-9", "p-2"), ("owner-pane", "p-1")))
        self.spawn()
        self.assertEqual(self.closes(), ["p-2"])
        self.assertIn("p-1", self.panes())
        self.assertEqual(self.events("pane-close-skipped"), [])

    def test_release_same_id_and_name_is_closed_as_today(self):
        self.env.set_herdr(**host_state(("qa-t-9", "p-1")))
        p = self.spawn()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.closes(), ["p-1"])
        self.assertEqual([e["entity_id"] for e in self.events("reaped")], ["qa-t-9"])

    def test_release_host_cannot_be_asked_closes_nothing(self):
        self.env.set_herdr(**host_state(("qa-t-9", "p-1")), fail_cmds=["agent list"])
        p = self.spawn()
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(self.closes(), [])
        self.assertEqual(self.panes(), ["p-1"])
        self.assertEqual(len(self.events("stale-pane-close-failed")), 1)


class StopHardFallback(Mixin, StopBase):
    fast = {"FACTORY_STOP_CONFIRM_S": "0.2"}

    def setUp(self):
        super().setUp()
        self.stop("zz-coder")  # plain stop first: --now then goes straight to the hard fallback

    def now(self, code=0):
        return self.stop("zz-coder", "--now", agent="zz-director", code=code, extra_env=self.fast)

    def test_row_id_held_by_other_name_is_not_closed(self):
        self.env.set_herdr(**host_state(("owner-pane", "p1")))
        self.now()
        self.assertEqual(self.closes(), [])
        self.assertEqual(self.panes(), ["p1"])
        self.assertEqual([e["entity_id"] for e in self.events("pane-close-skipped")], ["zz-coder"])

    def test_host_pane_under_exact_name_with_other_id_is_closed_by_host_id(self):
        self.env.set_herdr(**host_state(("zz-coder", "p9"), ("owner-pane", "p1")))
        self.now()
        self.assertEqual(self.closes(), ["p9"])
        self.assertEqual(self.panes(), ["p1"])
        self.assertEqual(self.row()["status"], "stopped")

    def test_same_id_and_name_is_closed_as_today(self):
        self.now()
        self.assertEqual(self.closes(), ["p1"])
        self.assertEqual(self.row()["status"], "stopped")

    def test_host_cannot_be_asked_closes_nothing(self):
        self.env.set_herdr(fail=True)
        self.now(code=1)
        self.assertEqual(self.closes(), [])
        self.assertEqual(self.row()["status"], "live")


if __name__ == "__main__":
    unittest.main()
