"""T-1118: a force-stopped agent whose pane name no longer lists leaves no open shell, or an event says why not.
Real `factory stop --now` as a subprocess, temp FACTORY_HOME, host stub."""
import json
import unittest

from test_stop import StopBase

ROW_CWD = "/wt/zz-coder"
FAST = {"FACTORY_STOP_CONFIRM_S": "0.2"}


class LeftoverShell(StopBase):
    def host(self, fg="zsh", cwd=None):
        """p1 is still listed (a bare shell) but the host carries no agent named zz-coder."""
        self.env.sql("UPDATE agents SET cwd=? WHERE name='zz-coder'", (ROW_CWD,))
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "w"}],
            panes=[{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1", "fg": fg,
                    "cwd": cwd or ROW_CWD},
                   {"pane_id": "p2", "tab_id": "t1", "workspace_id": "w1"}],
            agents=[{"name": "zz-coder-b", "agent": "claude", "pane_id": "p2", "agent_status": "working"}])

    def closes(self):
        return [c["argv"][2] for c in self.env.calls("herdr") if c["argv"][:2] == ["pane", "close"]]

    def panes(self):
        return sorted(p["pane_id"] for p in json.loads(self.env.herdr_state.read_text())["panes"])

    def test_force_stop_closes_the_bare_shell_left_under_a_vanished_name(self):
        self.host()
        self.stop("zz-coder", "--now", agent="zz-director", extra_env=FAST)
        self.assertEqual(self.row()["status"], "stopped")
        self.assertEqual(self.closes(), ["p1"])
        self.assertEqual(self.panes(), ["p2"])
        self.assertEqual(len(self.events("leftover-shell-closed")), 1)

    def test_force_stop_keeps_a_pane_whose_foreground_is_not_a_shell_and_says_why(self):
        self.host(fg="claude")
        self.stop("zz-coder", "--now", agent="zz-director", extra_env=FAST)
        self.assertEqual(self.row()["status"], "stopped")
        self.assertEqual(self.closes(), [])
        self.assertEqual(self.panes(), ["p1", "p2"])
        ev = self.events("leftover-shell-kept")
        self.assertEqual(len(ev), 1)
        self.assertIn("not a plain shell", json.loads(ev[0]["detail"])["reason"])

    def test_force_stop_keeps_a_pane_id_held_by_another_live_row(self):
        self.host()
        self.env.sql("UPDATE agents SET pane_id='p1' WHERE name='zz-coder-b'")
        self.stop("zz-coder", "--now", agent="zz-director", extra_env=FAST)
        self.assertEqual(self.closes(), [])
        self.assertIn("another live row", json.loads(self.events("leftover-shell-kept")[0]["detail"])["reason"])

    def test_force_stop_keeps_a_shell_pane_in_another_cwd_and_says_why(self):
        self.host(cwd="/somewhere/else")
        self.stop("zz-coder", "--now", agent="zz-director", extra_env=FAST)
        self.assertEqual(self.closes(), [])
        self.assertIn("cwd", json.loads(self.events("leftover-shell-kept")[0]["detail"])["reason"])


if __name__ == "__main__":
    unittest.main()
