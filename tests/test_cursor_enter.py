"""T-0860 (G-027 C8): after a successful Cursor herdr agent prompt, factory sends Enter; Claude does not."""
import datetime as dt
import json
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
        return self.ok("spawn", "pm", "--goal", "G-002", "--team", "g-002-a", "--name", self.CURSOR_PM, agent="director")

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

            argv = [str(FACTORY), "spawn", "pm", "--goal", "G-002", "--team", "g-002-a", "--name", self.CURSOR_PM]
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


class KickoffWatch(CursorEnterBase):
    """T-1599: a live cursor agent whose kickoff message is still unread after cursor_kickoff_grace_s (180 s) gets the
    kickoff re-submitted once, one grace later a fresh relaunch, then one escalation. The clock is FACTORY_TEST_NOW."""
    T0 = dt.datetime(2026, 3, 1, 10, 0, 0, tzinfo=dt.timezone.utc)

    def at(self, s):
        return {"FACTORY_TEST_NOW": (self.T0 + dt.timedelta(seconds=s)).isoformat()}

    def spawn(self, cursor=True):
        name = self.CURSOR_PM if cursor else self.CLAUDE_PM
        goal = ("G-002", "--team", "g-002-a") if cursor else ("G-001",)
        self.ok("spawn", "pm", "--goal", *goal, "--name", name, agent="director", extra_env=self.at(0))
        self.env.calls_file.write_text("")  # drop the spawn's own prompt and Enter
        return name

    def tick(self, s):
        return self.ok("tick", extra_env=self.at(s))

    def kick(self, name):
        return self.env.sql("SELECT id, body, read_at FROM messages WHERE recipient=? AND subject LIKE 'kickoff:%' "
                            "ORDER BY id DESC LIMIT 1", (name,))[0]

    def resubmits(self, name):
        """Stub-recorded `agent prompt <name> <kickoff text>` calls (a mail nudge has another text)."""
        body = self.kick(name)["body"]
        return [a for a in self.herdr_argv() if a[:3] == ["agent", "prompt", name] and a[3:4] == [body]]

    def host_calls(self, *cmd):
        return [a for a in self.herdr_argv() if a[:len(cmd)] == list(cmd)]

    def events(self, action, name=None):
        return [json.loads(r["detail"]) for r in self.env.sql(
            "SELECT detail FROM events WHERE action=? AND entity_id=? ORDER BY id", (action, name or self.CURSOR_PM))]

    def live(self):
        return self.env.sql("SELECT count(*) c FROM agents WHERE status='live'")[0]["c"]

    def escalations(self):
        return self.env.sql("SELECT recipient, body FROM messages WHERE kind='escalation' AND subject LIKE ?",
                            (f"{self.CURSOR_PM}: kickoff%",))

    def assert_untouched(self, name):
        self.assertEqual(self.resubmits(name), [])
        self.assertEqual(self.send_keys_enter(name), [])
        self.assertEqual(self.events("kickoff-resent", name), [])

    # acceptance 2
    def test_unread_kickoff_181s_after_spawn_is_resubmitted_once(self):
        name = self.spawn()
        self.tick(181)
        self.assertEqual(len(self.resubmits(name)), 1)
        self.assert_prompt_before_enter(name)
        ev = self.events("kickoff-resent")
        self.assertEqual(len(ev), 1)
        self.assertEqual((ev[0]["kick"], ev[0]["idle_s"], ev[0]["host"]), (self.kick(name)["id"], 181, "herdr"))
        self.tick(300)  # the same grace period: no second re-submit
        self.assertEqual(len(self.resubmits(name)), 1)
        self.assertEqual(len(self.events("kickoff-resent")), 1)
        self.assertEqual(self.host_calls("pane", "close"), [])

    # acceptance 3
    def test_kickoff_read_at_60s_nothing_happens(self):
        name = self.spawn()
        self.ok("inbox", agent=name, extra_env=self.at(60))  # the agent's first command sets the read mark
        self.assertIsNotNone(self.kick(name)["read_at"])
        self.tick(600)
        self.assert_untouched(name)

    def test_unread_at_100s_nothing_happens(self):
        name = self.spawn()
        self.tick(100)
        self.assert_untouched(name)

    def test_claude_agent_unread_at_600s_nothing_happens(self):
        name = self.spawn(cursor=False)
        self.assertIsNone(self.kick(name)["read_at"])
        self.tick(600)
        self.tick(1200)
        self.assert_untouched(name)
        self.assertEqual(self.host_calls("pane", "close"), [])
        self.assertEqual(self.events("kickoff-relaunch", name), [])

    def test_grace_is_configurable_and_zero_turns_the_check_off(self):
        name = self.spawn()
        self.ok("config", "cursor_kickoff_grace_s", "0", agent="director")
        self.tick(600)
        self.assert_untouched(name)
        self.ok("config", "cursor_kickoff_grace_s", "60", agent="director")
        self.tick(660)
        self.assertEqual(len(self.resubmits(name)), 1)

    # acceptance 4
    def test_still_unread_relaunches_once_then_escalates_once(self):
        name = self.spawn()
        self.tick(181)
        self.tick(300)
        self.assertEqual(self.events("kickoff-relaunch"), [])  # 119 s after the resend: not yet
        live, k1 = self.live(), self.kick(name)["id"]
        old = self.env.sql("SELECT session_id, pane_id FROM agents WHERE name=?", (name,))[0]
        self.tick(361)
        self.assertEqual(len(self.host_calls("pane", "close")), 1)
        self.assertEqual(self.host_calls("pane", "close")[0][2], old["pane_id"])
        self.assertEqual(len(self.host_calls("agent", "start")), 1)
        ev = self.events("kickoff-relaunch")
        self.assertEqual(len(ev), 1)
        self.assertEqual((ev[0]["kick"], ev[0]["old_sid"]), (k1, old["session_id"]))
        self.assertEqual(len(self.events("relaunch")), 1)  # the existing relaunch path ran (relaunch_agent's own event)
        row = self.env.sql("SELECT status, session_id, pane_id, started FROM agents WHERE name=?", (name,))[0]
        self.assertEqual(row["status"], "live")
        self.assertTrue(row["session_id"])
        self.assertNotEqual(row["session_id"], old["session_id"])
        self.assertNotEqual(row["pane_id"], old["pane_id"])
        self.assertEqual(self.live(), live)
        k2 = self.kick(name)
        self.assertGreater(k2["id"], k1)  # the fresh pane got its own kickoff message
        self.assertIsNone(k2["read_at"])
        self.assertEqual(self.escalations(), [])
        # the relaunched pane: one resend after its first grace period, one escalation after the second, no relaunch
        self.tick(361 + 181)
        self.assertEqual([e["kick"] for e in self.events("kickoff-resent")], [k1, k2["id"]])
        self.assertEqual(self.escalations(), [])
        self.tick(361 + 181 + 180)
        self.tick(361 + 181 + 600)
        self.tick(361 + 181 + 6000)
        self.assertEqual(len(self.host_calls("pane", "close")), 1)
        self.assertEqual(len(self.host_calls("agent", "start")), 1)
        self.assertEqual(len(self.events("kickoff-relaunch")), 1)
        self.assertEqual(len(self.events("kickoff-resent")), 2)
        esc = self.escalations()
        self.assertEqual(len(esc), 1)
        self.assertEqual(esc[0]["recipient"], "cursor-director")
        pane = self.env.sql("SELECT pane_id FROM agents WHERE name=?", (name,))[0]["pane_id"]
        for part in (f"pane {pane}", "backend herdr", "722 s"):
            self.assertIn(part, esc[0]["body"])
        self.assertEqual(self.live(), live)

    def test_kickoff_read_after_the_resend_stops_the_ladder(self):
        name = self.spawn()
        self.tick(181)
        self.ok("inbox", agent=name, extra_env=self.at(200))
        self.tick(361)
        self.tick(1000)
        self.assertEqual(self.host_calls("pane", "close"), [])
        self.assertEqual(self.events("kickoff-relaunch"), [])
        self.assertEqual(self.escalations(), [])

    def test_closed_spawn_gate_defers_the_relaunch_and_keeps_the_pane(self):
        name = self.spawn()
        self.tick(181)
        self.ok("config", "min_mem_available_mb", "99999999", agent="owner")  # the memory gate is closed
        self.tick(361)
        self.assertEqual(self.host_calls("pane", "close"), [])
        self.assertEqual(self.events("kickoff-relaunch"), [])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='kickoff-relaunch-deferred' AND entity_id=?",
                                          (name,))), 1)
        self.ok("config", "min_mem_available_mb", "1", agent="owner")
        self.tick(421)
        self.assertEqual(len(self.host_calls("pane", "close")), 1)
        self.assertEqual(len(self.events("kickoff-relaunch")), 1)

    # acceptance 5: one owner per pane
    def test_stop_timeout_and_kickoff_relaunch_due_in_the_same_tick_stop_the_pane_once(self):
        name = self.spawn()
        self.tick(181)
        # a `factory stop` whose stop_timeout_s ran out long ago (the stop fallback reads the wall clock)
        self.env.sql("UPDATE agents SET stop_requested_at='2026-01-01T00:00:00+00:00' WHERE name=?", (name,))
        self.tick(361)  # the kickoff relaunch is due in this tick too
        self.assertEqual(len(self.host_calls("pane", "close")), 1)
        self.assertEqual(self.host_calls("agent", "start"), [])
        self.assertEqual(self.events("kickoff-relaunch"), [])
        self.assertEqual(len(self.events("stopped")), 1)
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name=?", (name,))[0]["status"], "stopped")

    def test_agent_being_stopped_is_left_to_the_stop_path(self):
        name = self.spawn()
        self.ok("config", "stop_timeout_s", "999999999", agent="director")  # still inside its timeout: the fallback waits
        self.env.sql("UPDATE agents SET stop_requested_at='2026-01-01T00:00:00+00:00' WHERE name=?", (name,))
        self.tick(181)
        self.tick(361)
        self.tick(900)
        self.assert_untouched(name)
        self.assertEqual(self.host_calls("pane", "close"), [])
        self.assertEqual(self.events("kickoff-relaunch"), [])
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name=?", (name,))[0]["status"], "live")

    def test_queued_relaunch_is_left_to_the_relaunch_path(self):
        name = self.spawn()
        listed = json.loads(self.env.herdr_state.read_text())["agents"]
        self.env.set_herdr(agents=[{**a, "agent_status": "working"} for a in listed])  # not idle: the queue waits
        self.env.sql("UPDATE agents SET relaunch_requested_at='2026-03-01T10:00:00+00:00', relaunch_requested_by='director' "
                     "WHERE name=?", (name,))
        self.tick(181)
        self.tick(361)
        self.assert_untouched(name)
        self.assertEqual(self.events("kickoff-relaunch"), [])

    def test_failed_relaunch_escalates_once_and_does_not_loop(self):
        name = self.spawn()
        self.tick(181)
        self.env.set_herdr(start_fail=True)  # the fresh pane's agent start fails
        self.tick(361)
        self.assertEqual(len(self.events("kickoff-relaunch")), 1)
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='kickoff-relaunch-failed' AND entity_id=?",
                                          (name,))), 1)
        self.assertEqual(len(self.escalations()), 1)
        self.assertIn("relaunch failed", self.escalations()[0]["body"])
        self.tick(600)
        self.tick(1200)
        self.assertEqual(len(self.events("kickoff-relaunch")), 1)
        self.assertEqual(len(self.escalations()), 1)

    def test_hc_hosted_agent_gets_one_send_and_one_enter_without_waiting(self):
        name = self.spawn()
        row = self.env.sql("SELECT spawn_args FROM agents WHERE name=?", (name,))[0]
        self.env.sql("UPDATE agents SET spawn_args=? WHERE name=?",
                     (json.dumps({**json.loads(row["spawn_args"]), "host_backend": "hc"}), name))
        hc_state = self.env.home / "hc.json"
        hc_state.write_text(json.dumps({"agents": [{"name": name, "agent": "cursor", "osc_status": {"state": "idle"}}]}))
        self.ok("tick", extra_env={**self.at(181), "FAKE_HC_STATE": str(hc_state)})
        hc = [c["argv"] for c in self.env.calls("hc")]
        body = self.kick(name)["body"]
        sends = [a for a in hc if a[:1] == ["send"] and name in a and body in a]
        enters = [a for a in hc if a[:1] == ["keys"] and name in a and "enter" in a]
        self.assertEqual((len(sends), len(enters)), (1, 1), hc)
        self.assertLess(hc.index(sends[0]), hc.index(enters[0]))
        self.assertEqual(self.resubmits(name), [])  # nothing went to the TUI host
        ev = self.events("kickoff-resent")
        self.assertEqual([(e["host"], e["prompt_ok"]) for e in ev], [("hc", True)])
        # the spawn-time confirm loop did not run: no wait, no second Enter, no director alert
        self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action LIKE 'cursor-submit-%'"), [])

    # consumer: OWNER_CAP_KEYS (the new key is a timing key, like nudge_cooldown_s)
    def test_grace_key_is_not_an_owner_cap(self):
        with self.env.load_factory() as mod:
            self.assertNotIn("cursor_kickoff_grace_s", mod.OWNER_CAP_KEYS)
            self.assertEqual(mod.DEFAULT_CONFIG["cursor_kickoff_grace_s"], "180")


if __name__ == "__main__":
    unittest.main()
