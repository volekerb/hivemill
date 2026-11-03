"""T-0860 (G-027 C8): after a successful Cursor herdr agent prompt, factory sends Enter; Claude does not."""
import os
import sys
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))  # `-m unittest tests.test_cursor_enter` has no tests/ on the path

from harness import FACTORY, FactoryTestCase


class CursorEnterBase(FactoryTestCase):
    CURSOR_PM = "cursor-pm-g-002-a"
    CLAUDE_PM = "pm-g-001-a"

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal("G-001")
        e.seed_goal("G-002")
        e.sql("UPDATE goals SET org='cursor' WHERE id='G-002'")
        e.seed_team("g-002-a", goal="G-002", lead="cursor-lead-g-002-a")
        e.seed_agent("director", "director")
        fake = e.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho CURSOR_OK\n")
        fake.chmod(0o755)

    def herdr_argv(self):
        return [c["argv"] for c in self.env.calls("herdr")]

    def send_keys_enter(self, name=None):
        out = []
        for a in self.herdr_argv():
            if a[:2] != ["agent", "send-keys"] or len(a) < 4 or a[3] != "Enter":
                continue
            if name is None or (len(a) >= 3 and a[2] == name):
                out.append(a)
        return out

    def assert_prompt_before_enter(self, name):
        calls = self.herdr_argv()
        pi = [i for i, a in enumerate(calls) if a[:2] == ["agent", "prompt"] and len(a) > 2 and a[2] == name]
        ei = [i for i, a in enumerate(calls) if a[:2] == ["agent", "send-keys"] and len(a) > 3
              and a[2] == name and a[3] == "Enter"]
        self.assertTrue(pi, f"expected agent prompt for {name}")
        self.assertTrue(ei, f"expected send-keys Enter for {name}")
        self.assertLess(pi[-1], ei[-1], "Enter must follow prompt in the stub log")

    def spawn_cursor_pm(self):
        return self.ok("spawn", "pm", "--goal", "G-002", "--team", "g-002-a", "--name", "pm-g-002-a", agent="director")

    def spawn_claude_pm(self):
        return self.ok("spawn", "pm", "--goal", "G-001", "--name", "pm-g-001-a", agent="director")


class Kickoff(CursorEnterBase):
    def test_cursor_kickoff_prompt_then_enter(self):
        self.spawn_cursor_pm()
        self.assert_prompt_before_enter(self.CURSOR_PM)

    def test_claude_kickoff_no_enter(self):
        self.spawn_claude_pm()
        self.assertEqual(self.send_keys_enter(), [])

    def test_failed_kickoff_prompt_no_enter(self):
        self.env.calls_file.write_text("")
        with self.env.load_factory() as mod:
            real = mod.herdr

            def herdr(*args, check=True, timeout=mod.HERDR_TIMEOUT_S):
                if len(args) >= 2 and args[:2] == ("agent", "prompt"):
                    real(*args, check=check, timeout=timeout)
                    return None
                return real(*args, check=check, timeout=timeout)

            argv = [str(FACTORY), "spawn", "pm", "--goal", "G-002", "--team", "g-002-a", "--name", "pm-g-002-a"]
            saved_agent = os.environ.get("FACTORY_AGENT")
            os.environ["FACTORY_AGENT"] = "director"
            with mock.patch.object(mod, "herdr", side_effect=herdr):
                old = sys.argv
                sys.argv = argv
                try:
                    mod.main()
                finally:
                    sys.argv = old
                    if saved_agent is None:
                        os.environ.pop("FACTORY_AGENT", None)
                    else:
                        os.environ["FACTORY_AGENT"] = saved_agent
        self.assertEqual(self.send_keys_enter(self.CURSOR_PM), [])


class TickNudge(CursorEnterBase):
    def test_cursor_tick_nudge_prompt_then_enter(self):
        self.spawn_cursor_pm()
        self.env.calls_file.write_text("")  # drop spawn herdr calls
        row = self.env.sql("SELECT pane_id FROM agents WHERE name=?", (self.CURSOR_PM,))[0]
        self.env.set_herdr(
            agents=[{"name": self.CURSOR_PM, "agent": "cursor", "pane_id": row["pane_id"], "agent_status": "done"}])
        self.env.sql("UPDATE agents SET last_nudge=NULL WHERE name=?", (self.CURSOR_PM,))
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,created) VALUES(?,?,?,?,?,?,?)",
                     ("lead", self.CURSOR_PM, "request", "s", "b", "G-002", "2026-01-01T00:00:00+00:00"))
        self.ok("config", "nudge_cooldown_s", "0")
        self.ok("tick")
        self.assert_prompt_before_enter(self.CURSOR_PM)

    def test_claude_tick_nudge_no_enter(self):
        self.spawn_claude_pm()
        self.env.calls_file.write_text("")
        row = self.env.sql("SELECT pane_id FROM agents WHERE name=?", (self.CLAUDE_PM,))[0]
        self.env.set_herdr(
            agents=[{"name": self.CLAUDE_PM, "agent": "claude", "pane_id": row["pane_id"], "agent_status": "idle"}])
        self.env.sql("UPDATE agents SET last_nudge=NULL WHERE name=?", (self.CLAUDE_PM,))
        self.env.sql("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,created) VALUES(?,?,?,?,?,?,?)",
                     ("lead", self.CLAUDE_PM, "request", "s", "b", "G-001", "2026-01-01T00:00:00+00:00"))
        self.ok("config", "nudge_cooldown_s", "0")
        self.ok("tick")
        self.assertEqual(self.send_keys_enter(self.CLAUDE_PM), [])


if __name__ == "__main__":
    unittest.main()
