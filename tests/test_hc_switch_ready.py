"""T-1447 (G-031 SC3/SC4): hc pre-switch fixes. One test per BUILD item; machine names are placeholders (m2/t7)."""
import json
import sys
import unittest
from pathlib import Path

from harness import FactoryTestCase

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "bin"))
import factory_cli as f  # noqa: E402

TARGET_VERBS = ("read", "get", "kill", "keys", "send")
LEGACY = "zz-legacy-pm"


class HcSwitchReadyBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team()
        self.ok("config", "max_live_agents", "99", agent="owner")
        self.env.set_herdr(
            workspaces=[{"workspace_id": "w1", "label": "w"}],
            tabs=[{"tab_id": "t1", "workspace_id": "w1", "label": "x", "pane_count": 1}],
            panes=[{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1"}],
            agents=[],
        )
        self.hc_state = self.env.home / "hc.json"
        self.hc_state.write_text("{}")
        self.hc_env = {"FAKE_HC_STATE": str(self.hc_state)}

    def spawn_args(self, name):
        return json.loads(self.env.sql("SELECT spawn_args FROM agents WHERE name=?", (name,))[0][0])

    def spawn_coder(self, task, name, *extra):
        self.env.seed_task(task, goal="G-001", team="g-001-a", role="coder", status="todo", pr_url=None)
        return self.ok("spawn", "coder", "--goal", "G-001", "--team", "g-001-a", "--task", task, "--name", name,
                       *extra, agent="owner", extra_env=self.hc_env)


class LegacyTuiRowTests(HcSwitchReadyBase):
    """BUILD 1: a row with no host_backend and pane_id != name is a TUI row whatever the config."""

    def test_legacy_row_is_reconciled_on_the_tui_under_config_hc(self):
        self.ok("config", "terminal_backend", "hc", agent="owner")
        self.ok("config", "nudge_cooldown_s", "-60", agent="owner")
        self.env.seed_agent(LEGACY, "pm", goal="G-001", team="g-001-a", pane_id="w1:p3")
        self.env.sql(
            "INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x',?,'msg','s','b','2026-01-01')",
            (LEGACY,),
        )
        self.env.set_herdr(agents=[{"name": LEGACY, "agent": "claude", "pane_id": "w1:p3", "agent_status": "idle"}])
        self.ok("tick", agent="owner", extra_env=self.hc_env)
        listed = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "list"]]
        self.assertTrue(listed, "the tick must list the TUI for a live legacy row")
        prompts = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "prompt"] and LEGACY in c["argv"]]
        self.assertEqual(len(prompts), 1)
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name=?", (LEGACY,))[0][0], "live")
        # absent from the TUI list: lost (it was silently skipped before)
        self.env.set_herdr(agents=[])
        self.ok("tick", agent="owner", extra_env=self.hc_env)
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name=?", (LEGACY,))[0][0], "lost")

    def test_row_helpers(self):
        legacy = {"name": "a", "pane_id": "w1:p3", "spawn_args": None}
        hc_named = {"name": "a", "pane_id": "a", "spawn_args": None}
        explicit = {"name": "a", "pane_id": "w1:p3", "spawn_args": json.dumps({"host_backend": "hc"})}

        class R:
            def __init__(self, d):
                self.d = d

            def keys(self):
                return self.d.keys()

            def __getitem__(self, k):
                return self.d[k]
        self.assertTrue(f._row_is_legacy_tui(R(legacy)))
        self.assertFalse(f._row_is_legacy_tui(R(hc_named)))
        self.assertFalse(f._row_is_legacy_tui(R(explicit)))


class ExplicitHostBackendTests(HcSwitchReadyBase):
    """BUILD 2: every new spawn stores the backend it used."""

    def test_spawn_writes_host_backend_under_both_configs(self):
        self.ok("config", "terminal_backend", "herdr", agent="owner")
        p = self.spawn_coder("T-hb1", "zz-coder-hb1")
        self.assertIn("SPAWNED", p.stdout)
        self.assertEqual(self.spawn_args("zz-coder-hb1").get("host_backend"), "herdr")
        self.ok("config", "terminal_backend", "hc", agent="owner")
        p = self.spawn_coder("T-hb2", "zz-coder-hb2")
        self.assertIn("SPAWNED", p.stdout)
        self.assertEqual(self.spawn_args("zz-coder-hb2").get("host_backend"), "hc")


class NoParentTests(unittest.TestCase):
    """BUILD 3: hc run never makes the spawned agent the caller terminal's child."""

    def test_hc_run_argv_has_no_parent(self):
        argv = f._hc_translate_args(("agent", "start", "zz", "--kind", "claude"))
        self.assertEqual(argv[:4], ["run", "--bg", "--no-parent", "--name"])
        self.assertEqual(argv.count("--no-parent"), 1)


class HcReaderTests(unittest.TestCase):
    """BUILD 4: hc 0.1.21 kind name and remote rows."""

    def test_claude_code_maps_to_claude_and_remote_row_is_dropped(self):
        payload = {"terminals": [
            {"name": "zz-a", "agent": "claude-code", "address": "t3"},
            {"name": "zz-a", "agent": "claude-code", "address": "m2/t7"},
            {"name": "zz-c", "agent": "cursor", "address": "t4"},
        ]}
        out = f._hc_parse_host_stdout(["ls"], json.dumps(payload))
        self.assertEqual([(a["name"], a["agent"]) for a in out["agents"]], [("zz-a", "claude"), ("zz-c", "cursor")])


class KeyNameTests(unittest.TestCase):
    """BUILD 5: TUI key words become hc key names on the hc argv only."""

    def test_send_keys_enter_and_esc_use_hc_names(self):
        self.assertEqual(f._hc_translate_args(("agent", "send-keys", "zz", "Enter")), ["keys", "zz", "enter"])
        self.assertEqual(f._hc_translate_args(("agent", "send-keys", "zz", "esc")), ["keys", "zz", "esc"])
        self.assertEqual(f._hc_translate_args(("agent", "send-keys", "zz", "ctrl+c")), ["keys", "zz", "ctrl+c"])

    def test_tui_argv_is_unchanged(self):
        self.assertEqual(f._host_ipc_argv("herdr", ("agent", "send-keys", "zz", "Enter")),
                         ["agent", "send-keys", "zz", "Enter"])


class NeedsTargetLeakTests(HcSwitchReadyBase):
    """BUILD 6 / acceptance 11: no target-taking hc verb for a row that is not host_backend=hc, nothing on stdout."""

    def test_spawn_pm_on_herdr_config_does_not_leak_needs_target(self):
        self.ok("config", "terminal_backend", "herdr", agent="owner")
        # a stale same-name row whose pane id equals its name reads as an hc row by inference
        self.env.seed_agent("pm-g-001", "pm", goal="G-001", status="done", pane_id="pm-g-001")
        p = self.ok("spawn", "pm", "--goal", "G-001", agent="owner",
                    extra_env={**self.hc_env, "FAKE_HC_NEEDS_TARGET": "1"})
        self.assertIn("SPAWNED", p.stdout)
        self.assertNotIn("needs_target", p.stdout)
        bad = [c["argv"] for c in self.env.calls("hc") if c["argv"][:1] and c["argv"][0] in TARGET_VERBS]
        self.assertEqual(bad, [])

    def test_inferred_hc_row_on_herdr_config_never_gets_a_target_verb(self):
        self.ok("config", "terminal_backend", "herdr", agent="owner")
        self.ok("config", "nudge_cooldown_s", "-60", agent="owner")
        self.env.seed_agent(LEGACY, "pm", goal="G-001", team="g-001-a", pane_id=LEGACY)  # no host_backend
        self.env.sql(
            "INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('x',?,'msg','s','b','2026-01-01')",
            (LEGACY,),
        )
        self.hc_state.write_text(json.dumps({"agents": [{"name": LEGACY, "agent": "claude", "osc_status": {"state": "idle"}}]}))
        p = self.ok("tick", agent="owner", extra_env={**self.hc_env, "FAKE_HC_NEEDS_TARGET": "1"})
        self.assertNotIn("needs_target", p.stdout)
        bad = [c["argv"] for c in self.env.calls("hc") if c["argv"][:1] and c["argv"][0] in TARGET_VERBS]
        self.assertEqual(bad, [])


if __name__ == "__main__":
    unittest.main()
