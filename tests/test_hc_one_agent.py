"""T-1217 (G-031 SC6): one hc-hosted agent while terminal_backend stays herdr."""
import json
import sys
import time
import unittest
from pathlib import Path
from unittest import mock

from harness import FactoryTestCase

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "bin"))
import factory_cli as f  # noqa: E402

TUI = "zz-tui-pm"
HC = "zz-hc-coder"
HC_ENV = {}
PERM = (
    "Bash command\n  cat /etc/hosts\n\nDo you want to proceed?\n"
    "❯ 1. Yes\n  2. Yes, and don't ask again\n  3. No\n"
)


class HcOneAgentMixedTickTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.ok("config", "terminal_backend", "herdr", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.hc_state = self.env.home / "hc.json"
        self.hc_state.write_text("{}")
        HC_ENV["FAKE_HC_STATE"] = str(self.hc_state)

    def _hc_env(self):
        return {**HC_ENV}

    def _set_spawn_args_hc(self, name):
        self.env.sql(
            "UPDATE agents SET spawn_args=? WHERE name=?",
            (json.dumps({"host_backend": "hc"}), name),
        )

    def test_tui_live_hc_lost_when_hosts_split(self):
        self.env.seed_agent(TUI, "pm", goal="G-001", team="g-001-a", pane_id="p-tui")
        self.env.seed_agent(HC, "coder", goal="G-001", team="g-001-a", pane_id=HC)
        self._set_spawn_args_hc(HC)
        self.env.set_herdr(agents=[{"name": TUI, "agent": "claude", "pane_id": "p-tui", "agent_status": "idle"}])
        self.hc_state.write_text(json.dumps({"agents": []}))
        self.ok("tick", agent="owner", extra_env=self._hc_env())
        rows = {r["name"]: r["status"] for r in self.env.sql("SELECT name, status FROM agents")}
        self.assertEqual(rows[TUI], "live")
        self.assertEqual(rows[HC], "lost")

    def test_hc_live_tui_lost_when_hosts_split(self):
        self.env.seed_agent(TUI, "pm", goal="G-001", team="g-001-a", pane_id="p-tui")
        self.env.seed_agent(HC, "coder", goal="G-001", team="g-001-a", pane_id=HC)
        self._set_spawn_args_hc(HC)
        self.env.set_herdr(agents=[])
        self.hc_state.write_text(json.dumps({"agents": [{"name": HC, "agent": "claude", "osc_status": {"state": "idle"}}]}))
        self.ok("tick", agent="owner", extra_env=self._hc_env())
        rows = {r["name"]: r["status"] for r in self.env.sql("SELECT name, status FROM agents")}
        self.assertEqual(rows[HC], "live")
        self.assertEqual(rows[TUI], "lost")


class HcOneAgentAgentsColumnTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.ok("config", "terminal_backend", "herdr", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_agent("zz-a", "pm", goal="G-001", team="g-001-a", pane_id="p1")
        self.env.seed_agent("zz-b", "coder", goal="G-001", team="g-001-a", pane_id="zz-b")
        self.env.sql(
            "UPDATE agents SET spawn_args=? WHERE name='zz-b'",
            (json.dumps({"host_backend": "hc"}),),
        )

    def test_agents_backend_column_last(self):
        p = self.ok("agents", agent="owner")
        lines = [ln for ln in p.stdout.splitlines() if ln.strip()]
        header = lines[0]
        self.assertTrue(header.endswith("BACKEND"), header)
        self.assertIn("hc", p.stdout)
        self.assertIn("herdr", p.stdout)


class HcOneAgentSpawnTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.ok("config", "terminal_backend", "herdr", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.env.seed_task("T-hc1", goal="G-001", team="g-001-a", role="coder", status="todo", pr_url=None)
        self.ok("config", "max_live_agents", "99", agent="owner")
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "w"}],
            tabs=[{"tab_id": "t1", "workspace_id": "w1", "label": "x", "pane_count": 1}],
            panes=[{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1"}],
            agents=[],
        )
        self.hc_state = self.env.home / "hc.json"
        self.hc_state.write_text("{}")
        self.hc_extra = {"FAKE_HC_STATE": str(self.hc_state)}

    def test_spawn_host_backend_hc_on_herdr_config(self):
        name = "zz-coder-t-hc1"
        p = self.ok(
            "spawn",
            "coder",
            "--goal",
            "G-001",
            "--team",
            "g-001-a",
            "--task",
            "T-hc1",
            "--name",
            name,
            "--host-backend",
            "hc",
            agent="owner",
            extra_env=self.hc_extra,
        )
        self.assertIn("SPAWNED", p.stdout)
        sa = json.loads(self.env.sql("SELECT spawn_args FROM agents WHERE name=?", (name,))[0][0])
        self.assertEqual(sa.get("host_backend"), "hc")
        tb = self.env.sql("SELECT value FROM config WHERE key='terminal_backend'")[0][0]
        self.assertEqual(tb, "herdr")
        hc_run = [c for c in self.env.calls("hc") if c["argv"][:2] == ["run", "--bg"] and name in c["argv"]]
        self.assertEqual(len(hc_run), 1)
        herdr_starts = [c for c in self.env.calls("herdr") if c["argv"][:3] == ["agent", "start", name]]
        self.assertEqual(herdr_starts, [])
        herdr_layout = [
            c
            for c in self.env.calls("herdr")
            if c["argv"][:1] in (["tab"], ["pane"], ["workspace"]) or c["argv"][:2] in (("pane", "split"),)
        ]
        self.assertEqual(herdr_layout, [])
        for c in hc_run:
            self.assertIsNone(c.get("sock"))

    def test_spawn_host_backend_herdr_on_hc_config_creates_tui_pane(self):
        self.ok("config", "terminal_backend", "hc", agent="owner")
        self.env.seed_task("T-tui1", goal="G-001", team="g-001-a", role="coder", status="todo", pr_url=None)
        self.ok("config", "max_live_agents", "99", agent="owner")
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "w"}],
            tabs=[{"tab_id": "t1", "workspace_id": "w1", "label": "x", "pane_count": 1}],
            panes=[{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1"}],
            agents=[],
        )
        self.hc_state = self.env.home / "hc.json"
        self.hc_state.write_text("{}")
        name = "zz-coder-t-tui1"
        p = self.ok(
            "spawn",
            "coder",
            "--goal",
            "G-001",
            "--team",
            "g-001-a",
            "--task",
            "T-tui1",
            "--name",
            name,
            "--host-backend",
            "herdr",
            agent="owner",
            extra_env={"FAKE_HC_STATE": str(self.hc_state)},
        )
        self.assertIn("SPAWNED", p.stdout)
        pane_id = self.env.sql("SELECT pane_id FROM agents WHERE name=?", (name,))[0][0]
        self.assertTrue(str(pane_id).startswith("p"))
        self.assertNotEqual(pane_id, name)
        hc_run = [c for c in self.env.calls("hc") if c["argv"][:2] == ["run", "--bg"]]
        self.assertEqual(hc_run, [])
        herdr_starts = [c for c in self.env.calls("herdr") if c["argv"][:3] == ["agent", "start", name]]
        self.assertEqual(len(herdr_starts), 1)


class HcOneAgentDialogTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.ok("config", "terminal_backend", "herdr", agent="owner")
        self.ok("config", "blocked_alert_s", "0", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.hc_state = self.env.home / "hc.json"
        self.hc_extra = {"FAKE_HC_STATE": str(self.hc_state)}

    def test_hc_row_permission_dialog_esc_via_hc(self):
        self.env.seed_agent(HC, "coder", goal="G-001", team="g-001-a", pane_id=HC)
        self.env.sql(
            "UPDATE agents SET spawn_args=? WHERE name=?",
            (json.dumps({"host_backend": "hc"}), HC),
        )
        self.env.sql("UPDATE agents SET blocked_since=? WHERE name=?", (time.time() - 1000, HC))
        self.env.set_herdr(agents=[])
        self.hc_state.write_text(
            json.dumps(
                {
                    "agents": [{"name": HC, "agent": "claude", "osc_status": {"state": "blocked"}}],
                    "read_text": {HC: PERM},
                }
            )
        )
        self.ok("tick", agent="owner", extra_env=self.hc_extra)
        keys = [c["argv"] for c in self.env.calls("hc") if c["argv"][:1] == ["keys"]]
        self.assertEqual(keys, [["keys", HC, "esc"]])

    def test_tui_row_permission_dialog_still_esc_via_herdr(self):
        tui = "zz-tui-dialog"
        self.env.seed_agent(tui, "pm", goal="G-001", team="g-001-a", pane_id="p-dialog")
        self.env.sql("UPDATE agents SET blocked_since=? WHERE name=?", (time.time() - 1000, tui))
        self.env.set_herdr(
            agents=[{"name": tui, "agent": "claude", "pane_id": "p-dialog", "agent_status": "blocked"}],
            read_text={tui: PERM},
        )
        self.hc_state.write_text("{}")
        self.ok("tick", agent="owner", extra_env=self.hc_extra)
        keys = [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "send-keys"]]
        self.assertEqual(keys, [["agent", "send-keys", tui, "esc"]])


class HcOneAgentRelaunchTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.ok("config", "terminal_backend", "herdr", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.hc_state = self.env.home / "hc.json"
        self.hc_extra = {"FAKE_HC_STATE": str(self.hc_state)}

    def test_hc_relaunch_when_idle_kills_then_respawns(self):
        self.env.seed_agent(HC, "coder", goal="G-001", team="g-001-a", pane_id=HC, status="live")
        self.env.sql(
            "UPDATE agents SET spawn_args=?, session_id=? WHERE name=?",
            (json.dumps({"host_backend": "hc"}), "sess-hc-relaunch", HC),
        )
        self.hc_state.write_text(
            json.dumps(
                {
                    "agents": [
                        {
                            "name": HC,
                            "agent": "claude",
                            "osc_status": {"state": "idle"},
                            "agent_session": {"value": "sess-hc-relaunch"},
                        }
                    ]
                }
            )
        )
        self.env.set_herdr(agents=[])
        self.ok("relaunch", HC, "--when-idle", agent="owner", extra_env=self.hc_extra)
        pending = self.env.sql("SELECT relaunch_requested_at FROM agents WHERE name=?", (HC,))[0][0]
        self.assertIsNotNone(pending)
        self.ok("tick", agent="owner", extra_env=self.hc_extra)
        cleared = self.env.sql("SELECT relaunch_requested_at FROM agents WHERE name=?", (HC,))[0][0]
        self.assertIsNone(cleared)
        kills = [c["argv"] for c in self.env.calls("hc") if c["argv"][:1] == ["kill"]]
        self.assertEqual(kills, [["kill", HC]])
        runs = [c for c in self.env.calls("hc") if c["argv"][:2] == ["run", "--bg"] and HC in c["argv"]]
        self.assertGreaterEqual(len(runs), 1)


class HcOneAgentHcLsFailTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.ok("config", "terminal_backend", "herdr", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.hc_state = self.env.home / "hc.json"
        self.hc_extra = {"FAKE_HC_STATE": str(self.hc_state)}

    def test_failed_hc_ls_does_not_mark_hc_row_lost(self):
        self.env.seed_agent(HC, "coder", goal="G-001", team="g-001-a", pane_id=HC, status="live")
        self._set_spawn_args_hc(HC)
        self.env.set_herdr(agents=[{"name": TUI, "agent": "claude", "pane_id": "p-tui", "agent_status": "idle"}])
        self.hc_state.write_text(json.dumps({"fail": True}))
        self.ok("tick", agent="owner", extra_env=self.hc_extra)
        status = self.env.sql("SELECT status FROM agents WHERE name=?", (HC,))[0][0]
        self.assertEqual(status, "live")

    def _set_spawn_args_hc(self, name):
        self.env.sql(
            "UPDATE agents SET spawn_args=? WHERE name=?",
            (json.dumps({"host_backend": "hc"}), name),
        )


class HcOneAgentNudgeTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.ok("config", "terminal_backend", "herdr", agent="owner")
        self.ok("config", "nudge_cooldown_s", "-60", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.hc_state = self.env.home / "hc.json"
        self.hc_state.write_text("{}")
        self.hc_extra = {"FAKE_HC_STATE": str(self.hc_state)}

    def test_hc_row_null_status_skips_unread_nudge(self):
        self.env.seed_agent(HC, "coder", goal="G-001", team="g-001-a", pane_id=HC)
        self.env.sql(
            "UPDATE agents SET spawn_args=? WHERE name=?",
            (json.dumps({"host_backend": "hc"}), HC),
        )
        self.env.sql(
            "INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x',?,'msg','s','b','2026-01-01')",
            (HC,),
        )
        self.env.set_herdr(agents=[])
        self.hc_state.write_text(json.dumps({"agents": [{"name": HC, "agent": "claude", "osc_status": None}]}))
        self.ok("tick", agent="owner", extra_env=self.hc_extra)
        sends = [c for c in self.env.calls("hc") if c["argv"][:1] == ["send"]]
        self.assertEqual(sends, [])

    def test_tui_idle_still_gets_unread_nudge(self):
        tui = "zz-tui-nudge"
        self.env.seed_agent(tui, "pm", goal="G-001", team="g-001-a", pane_id="p-nudge")
        self.env.sql(
            "INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x',?,'msg','s','b','2026-01-01')",
            (tui,),
        )
        self.env.set_herdr(agents=[{"name": tui, "agent": "claude", "pane_id": "p-nudge", "agent_status": "idle"}])
        self.hc_state.write_text(json.dumps({"agents": []}))
        self.ok("tick", agent="owner", extra_env=self.hc_extra)
        prompts = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "prompt"] and tui in c["argv"]]
        self.assertEqual(len(prompts), 1)


class HcConfigHerdrRowTests(FactoryTestCase):
    """terminal_backend hc with spawn_args.host_backend herdr (TUI pilot on hc factory)."""

    INPROC = True
    TUI = "zz-pilot-tui"

    def setUp(self):
        super().setUp()
        self.ok("config", "terminal_backend", "hc", agent="owner")
        self.ok("config", "blocked_alert_s", "0", agent="owner")
        self.env.seed_goal()
        self.env.seed_team()
        self.hc_state = self.env.home / "hc.json"
        self.hc_state.write_text(json.dumps({"agents": [{"name": "zz-hc-other", "agent": "claude", "osc_status": {"state": "idle"}}]}))
        self.hc_extra = {"FAKE_HC_STATE": str(self.hc_state)}

    def _herdr_row(self, name, pane, status="live", session="sess-tui"):
        self.env.seed_agent(name, "coder", goal="G-001", team="g-001-a", pane_id=pane, status=status)
        self.env.sql(
            "UPDATE agents SET spawn_args=?, session_id=? WHERE name=?",
            (json.dumps({"host_backend": "herdr"}), session, name),
        )

    def test_spawn_omitted_host_backend_no_spawn_args_key(self):
        self.ok("config", "max_live_agents", "99", agent="owner")
        self.env.seed_task("T-omit", goal="G-001", team="g-001-a", role="coder", status="todo", pr_url=None)
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "w"}],
            tabs=[{"tab_id": "t1", "workspace_id": "w1", "label": "x", "pane_count": 1}],
            panes=[{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1"}],
            agents=[],
        )
        name = "zz-coder-t-omit"
        p = self.ok(
            "spawn",
            "coder",
            "--goal",
            "G-001",
            "--team",
            "g-001-a",
            "--task",
            "T-omit",
            "--name",
            name,
            agent="owner",
            extra_env=self.hc_extra,
        )
        self.assertIn("SPAWNED", p.stdout)
        sa = json.loads(self.env.sql("SELECT spawn_args FROM agents WHERE name=?", (name,))[0][0])
        self.assertNotIn("host_backend", sa)

    def test_tick_reaps_done_herdr_row_on_hc_config(self):
        self._herdr_row(self.TUI, "p-pilot", status="done")
        self.env.set_herdr(
            agents=[{"name": self.TUI, "agent": "claude", "pane_id": "p-pilot", "agent_status": "idle"}]
        )
        self.ok("tick", agent="owner", extra_env=self.hc_extra)
        row = self.env.sql("SELECT status, pane_id FROM agents WHERE name=?", (self.TUI,))[0]
        self.assertEqual(row["status"], "exited")
        closes = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["pane", "close"]]
        self.assertEqual(len(closes), 1)
        self.assertEqual(closes[0]["argv"][2], "p-pilot")

    def test_tui_dialog_esc_when_hc_config(self):
        self._herdr_row(self.TUI, "p-dialog")
        self.env.sql("UPDATE agents SET blocked_since=? WHERE name=?", (time.time() - 1000, self.TUI))
        self.env.set_herdr(
            agents=[{"name": self.TUI, "agent": "claude", "pane_id": "p-dialog", "agent_status": "blocked"}],
            read_text={self.TUI: PERM},
        )
        self.ok("tick", agent="owner", extra_env=self.hc_extra)
        keys = [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "send-keys"]]
        self.assertEqual(keys, [["agent", "send-keys", self.TUI, "esc"]])

    def test_release_stale_closes_herdr_pane_on_hc_config(self):
        self.env.seed_task("T-re", goal="G-001", team="g-001-a", role="coder", status="todo", pr_url=None)
        self.ok("config", "max_live_agents", "99", agent="owner")
        self._herdr_row(self.TUI, "p-old", status="done")
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "w"}],
            tabs=[{"tab_id": "t1", "workspace_id": "w1", "label": "x", "pane_count": 1}],
            panes=[{"pane_id": "p-old", "tab_id": "t1", "workspace_id": "w1"}, {"pane_id": "p-new", "tab_id": "t1", "workspace_id": "w1"}],
            agents=[{"name": self.TUI, "agent": "claude", "pane_id": "p-old", "agent_status": "idle"}],
        )
        p = self.ok(
            "spawn",
            "coder",
            "--goal",
            "G-001",
            "--team",
            "g-001-a",
            "--task",
            "T-re",
            "--name",
            self.TUI,
            agent="owner",
            extra_env=self.hc_extra,
        )
        self.assertIn("SPAWNED", p.stdout)
        closes = [c["argv"][2] for c in self.env.calls("herdr") if c["argv"][:2] == ["pane", "close"]]
        self.assertIn("p-old", closes)

    def test_herdr_relaunch_when_idle_on_hc_config(self):
        self._herdr_row(self.TUI, "p-re", session="sess-re")
        self.env.set_herdr(
            agents=[
                {
                    "name": self.TUI,
                    "agent": "claude",
                    "pane_id": "p-re",
                    "agent_status": "idle",
                    "agent_session": {"value": "sess-re"},
                }
            ]
        )
        self.ok("relaunch", self.TUI, "--when-idle", agent="owner", extra_env=self.hc_extra)
        self.ok("tick", agent="owner", extra_env=self.hc_extra)
        cleared = self.env.sql("SELECT relaunch_requested_at FROM agents WHERE name=?", (self.TUI,))[0][0]
        self.assertIsNone(cleared)
        closes = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["pane", "close"]]
        self.assertGreaterEqual(len(closes), 1)

    def test_stop_closes_herdr_pane_on_hc_config(self):
        self._herdr_row(self.TUI, "p-stop", session="sess-stop")
        self.env.set_herdr(
            agents=[
                {
                    "name": self.TUI,
                    "agent": "claude",
                    "pane_id": "p-stop",
                    "agent_status": "idle",
                    "agent_session": {"value": "sess-stop"},
                }
            ]
        )
        self.env.sql("UPDATE agents SET stop_requested_at=? WHERE name=?", (time.time(), self.TUI))
        with mock.patch.object(f, "_scan_claude", return_value={}), mock.patch.object(f, "claude_procs", return_value=[]):
            self.ok("stop", self.TUI, "--now", agent="owner", extra_env={**self.hc_extra, "FACTORY_STOP_CONFIRM_S": "0.1"})
        closes = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["pane", "close"]]
        self.assertEqual(len(closes), 1)
        self.assertEqual(closes[0]["argv"][2], "p-stop")

    def test_stop_hc_row_on_herdr_config_hc_kills(self):
        name = "zz-hc-stop-now"
        self.ok("config", "terminal_backend", "herdr", agent="owner")
        self.env.seed_agent(name, "coder", goal="G-001", team="g-001-a", pane_id=name, status="live")
        self.env.sql(
            "UPDATE agents SET spawn_args=?, session_id=?, stop_requested_at=? WHERE name=?",
            (json.dumps({"host_backend": "hc"}), "sess-hc-stop", time.time(), name),
        )
        self.env.set_herdr(agents=[])
        self.hc_state.write_text(
            json.dumps(
                {
                    "agents": [
                        {
                            "name": name,
                            "agent": "claude",
                            "osc_status": {"state": "idle"},
                            "agent_session": {"value": "sess-hc-stop"},
                        }
                    ]
                }
            )
        )
        with mock.patch.object(f, "_scan_claude", return_value={}), mock.patch.object(f, "claude_procs", return_value=[]):
            self.ok("stop", name, "--now", agent="owner", extra_env={**self.hc_extra, "FACTORY_STOP_CONFIRM_S": "0.1"})
        kills = [c["argv"] for c in self.env.calls("hc") if c["argv"][:1] == ["kill"]]
        self.assertEqual(kills, [["kill", name]])
        status = self.env.sql("SELECT status FROM agents WHERE name=?", (name,))[0][0]
        self.assertEqual(status, "stopped")

    def test_herdr_director_unread_nudge_on_hc_config(self):
        lead = "zz-tui-director-nudge"
        self.ok("config", "nudge_cooldown_s", "-60", agent="owner")
        self.env.seed_agent(lead, "director", goal="G-001", pane_id="p-dir", status="live")
        self.env.sql(
            "UPDATE agents SET spawn_args=? WHERE name=?",
            (json.dumps({"host_backend": "herdr"}), lead),
        )
        self.env.sql(
            "INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x',?,'msg','s','b','2026-01-01')",
            (lead,),
        )
        self.env.set_herdr(
            agents=[{"name": lead, "agent": "claude", "pane_id": "p-dir", "agent_status": "idle"}]
        )
        self.ok("tick", agent="owner", extra_env=self.hc_extra)
        prompts = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "prompt"] and lead in c["argv"]]
        self.assertEqual(len(prompts), 1)

    def test_herdr_director_recycle_on_hc_config(self):
        lead = "zz-tui-director-recycle"
        self.ok("config", "recycle_context_tokens", "100000", agent="owner")
        self.env.seed_agent(lead, "director", goal="G-001", pane_id="p-rc", status="live")
        self.env.sql(
            "UPDATE agents SET spawn_args=?, session_id=? WHERE name=?",
            (json.dumps({"host_backend": "herdr"}), "sess-rc", lead),
        )
        d = self.env.user_home / ".claude" / "projects" / "-zz-proj"
        d.mkdir(parents=True, exist_ok=True)
        lines = [
            {"type": "assistant", "message": {"id": "m0", "usage": {"input_tokens": 5, "cache_read_input_tokens": 1}}},
            {
                "type": "assistant",
                "message": {
                    "id": "m1",
                    "usage": {"input_tokens": 10, "cache_read_input_tokens": 150000 - 30, "cache_creation_input_tokens": 20},
                },
            },
        ]
        (d / "sess-rc.jsonl").write_text("\n".join(json.dumps(x) for x in lines) + "\n")
        self.env.set_herdr(
            agents=[
                {
                    "name": lead,
                    "agent": "claude",
                    "pane_id": "p-rc",
                    "agent_status": "idle",
                    "agent_session": {"value": "sess-rc"},
                }
            ]
        )
        self.ok("tick", agent="owner", extra_env={**self.hc_extra, "FACTORY_NOW": "2026-06-01T12:00:00+00:00"})
        queued = self.env.sql("SELECT 1 FROM events WHERE entity_id=? AND action='recycle-queued'", (lead,))
        self.assertEqual(len(queued), 1)


if __name__ == "__main__":
    unittest.main()
