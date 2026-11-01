"""Cursor Agent CLI pilot: adapter, permission translation, materialize (docs/research/cursor-harness.md)."""
import importlib.machinery
import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from harness import REPO, FactoryTestCase

HOOKS = REPO / "roles" / "hooks"
ADAPTER = HOOKS / "cursor-guard-adapter.py"
FACTORY = REPO / "bin" / "factory_cli.py"


def load_harness():
    return importlib.import_module("scout.cursor_harness")


def load_factory(home):
    old = os.environ.get("FACTORY_HOME")
    os.environ["FACTORY_HOME"] = str(home)
    try:
        loader = importlib.machinery.SourceFileLoader("factory_cursor_pilot_test", str(FACTORY))
        mod = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
        loader.exec_module(mod)
        return mod
    finally:
        if old is None:
            os.environ.pop("FACTORY_HOME", None)
        else:
            os.environ["FACTORY_HOME"] = old


def run_adapter(event, payload, role="pm"):
    return subprocess.run(
        [sys.executable, str(ADAPTER), "--role", role, "--event", event],
        input=json.dumps(payload), capture_output=True, text=True, timeout=60,
    )


class TranslateTests(unittest.TestCase):
    def test_bash_and_edit_map(self):
        h = load_harness()
        self.assertEqual(h.translate_permission_token("Bash(sudo:*)"), "Shell(sudo:*)")
        self.assertEqual(h.translate_permission_token("Edit(/x/db/**)"), "Write(/x/db/**)")
        self.assertEqual(h.translate_permission_token("Write"), "Write(**)")
        self.assertIsNone(h.translate_permission_token("Advisor"))
        self.assertIsNone(h.translate_permission_token("Workflow(coder-skills:multi-angle-review)"))

    def test_pm_template_keeps_write_deny(self):
        h = load_harness()
        settings = json.loads((REPO / "roles" / "settings" / "pm.json.in").read_text()
                              .replace("@FACTORY_LIVE_HOME@", "/live")
                              .replace("@FACTORY_HOME_DIR@", "/zz-test-home")
                              .replace("@FACTORY_USER_CONFIG@", "/zz-test-home/.config"))
        perms = h.translate_permissions(settings["permissions"])
        self.assertIn("Write(**)", perms["deny"])
        self.assertTrue(any(t.startswith("Shell(sudo") for t in perms["deny"]))


class AdapterTests(unittest.TestCase):
    def test_allows_harmless_shell(self):
        r = run_adapter("beforeShellExecution", {"command": "echo ok", "cwd": "/tmp"})
        body = json.loads(r.stdout.strip().splitlines()[-1])
        self.assertEqual(body["permission"], "allow")
        self.assertEqual(r.returncode, 0)

    def test_blocks_herdr_via_before_shell(self):
        r = run_adapter("beforeShellExecution", {"command": "herdr server stop", "cwd": "/tmp"})
        body = json.loads(r.stdout.strip().splitlines()[-1])
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 0)  # deny is in JSON for beforeShellExecution
        self.assertIn("herdr", body.get("agent_message", "").lower())

    def test_blocks_shell_tool_pretooluse(self):
        r = run_adapter("preToolUse", {
            "tool_name": "Shell", "tool_input": {"command": "sudo id"}, "cwd": "/tmp",
        })
        body = json.loads(r.stdout.strip().splitlines()[-1])
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 2)

    def test_blocks_write_to_bashrc_with_path_key(self):
        import pwd
        home = pwd.getpwuid(os.getuid()).pw_dir  # guard uses passwd home, not $HOME
        r = run_adapter("preToolUse", {
            "tool_name": "Write", "tool_input": {"path": f"{home}/.bashrc"}, "cwd": "/tmp",
        })
        body = json.loads(r.stdout.strip().splitlines()[-1])
        self.assertEqual(body["permission"], "deny")
        self.assertEqual(r.returncode, 2)


class MaterializeTests(unittest.TestCase):
    def test_writes_hooks_cli_and_rule(self):
        h = load_harness()
        settings = {"permissions": {"deny": ["Bash(sudo:*)", "Edit", "Advisor"], "allow": ["Bash(git status:*)"]}}
        with tempfile.TemporaryDirectory(prefix="zz-cursor-mat-") as tmp:
            info = h.materialize_cursor_policy(
                tmp, "pm", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# pm\n",
            )
            hooks = json.loads(Path(info["hooks"]).read_text())
            self.assertTrue(hooks["hooks"]["beforeShellExecution"][0]["failClosed"])
            self.assertIn("cursor-guard-adapter.py", hooks["hooks"]["beforeShellExecution"][0]["command"])
            cli = json.loads(Path(info["cli"]).read_text())
            self.assertIn("Shell(sudo:*)", cli["permissions"]["deny"])
            self.assertIn("Write(**)", cli["permissions"]["deny"])
            self.assertNotIn("Advisor", json.dumps(cli))
            self.assertIn("alwaysApply: true", Path(info["rule"]).read_text())
            self.assertTrue((Path(tmp) / "AGENTS.md").is_file())


class ConfigAndCliTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_agent("zz-test-director", "director", goal="G-001", team="g-001-a")
        e.seed_agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")

    def test_config_rejects_evaluator(self):
        p = self.refused(2, "config", "cursor_pilot_roles", "evaluator", agent="zz-test-director")
        self.assertIn("allow-list", p.stderr)

    def test_config_accepts_pm(self):
        self.ok("config", "cursor_pilot_roles", "pm", agent="zz-test-director")
        self.assertEqual(self.ok("config", "cursor_pilot_roles", agent="zz-test-director").stdout.strip(), "pm")

    def test_config_accepts_coder(self):
        self.ok("config", "cursor_pilot_roles", "coder", agent="zz-test-director")
        self.assertEqual(self.ok("config", "cursor_pilot_roles", agent="zz-test-director").stdout.strip(), "coder")

    def test_status_lists_matrix(self):
        p = self.ok("cursor-pilot", "status", agent="zz-test-director")
        self.assertIn("WORKS", p.stdout)
        self.assertIn("NEEDS_WORK", p.stdout)

    def test_probe_passes(self):
        p = self.ok("cursor-pilot", "probe", "--role", "pm", agent="zz-test-director")
        self.assertIn("PROBE ok", p.stdout)

    def test_prepare_materializes(self):
        with tempfile.TemporaryDirectory(prefix="zz-cursor-prep-") as tmp:
            p = self.ok("cursor-pilot", "prepare", "--role", "pm", "--cwd", tmp, agent="zz-test-director")
            self.assertIn("PREPARED", p.stdout)
            self.assertTrue((Path(tmp) / ".cursor" / "hooks.json").is_file())
            self.assertTrue((Path(tmp) / ".cursor" / "cli.json").is_file())

    def test_agent_runtimes_config(self):
        self.ok("config", "agent_runtimes", "pm=cursor", agent="zz-test-director")
        self.assertEqual(self.ok("config", "agent_runtimes", agent="zz-test-director").stdout.strip(),
                         "pm=cursor")
        self.ok("config", "agent_runtimes", "coder=cursor", agent="zz-test-director")
        self.refused(2, "config", "agent_runtimes", "evaluator=cursor", agent="zz-test-director")

    def test_agent_runtimes_star_cursor(self):
        self.ok("config", "agent_runtimes", "*=cursor", agent="zz-test-director")
        val = self.ok("config", "agent_runtimes", agent="zz-test-director").stdout.strip()
        self.assertIn("boss=cursor", val)
        self.assertIn("pm=cursor", val)
        self.assertIn("coder=cursor", val)
        self.assertNotIn("evaluator=cursor", val)

    def test_spawn_cursor_pm_herdr_kind(self):
        """With agent_runtimes=pm=cursor, spawn uses herdr --kind cursor (same place/prompt as Claude)."""
        self.ok("config", "agent_runtimes", "pm=cursor", agent="zz-test-director")
        fake = self.env.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho CURSOR_PILOT_OK\n")
        fake.chmod(0o755)
        before = len(self.env.calls("herdr"))
        p = self.ok("spawn", "pm", "--name", "zz-cursor-pm", "--goal", "G-001",
                    "--team", "g-001-a", agent="zz-test-director")
        self.assertIn("runtime=cursor", p.stdout)
        self.assertIn("kind=cursor", p.stdout)
        self.assertIn("SPAWNED zz-cursor-pm", p.stdout)
        starts = [c for c in self.env.calls("herdr")[before:] if c["argv"][:2] == ["agent", "start"]]
        self.assertEqual(len(starts), 1, f"expected one herdr agent start, got {starts}")
        argv = starts[0]["argv"]
        self.assertIn("--kind", argv)
        self.assertEqual(argv[argv.index("--kind") + 1], "cursor")
        self.assertNotIn("claude", argv[: argv.index("--")] if "--" in argv else argv)
        row = self.env.sql("SELECT spawn_args, pane_id, status FROM agents WHERE name='zz-cursor-pm'")[0]
        sa = json.loads(row["spawn_args"])
        self.assertEqual(sa.get("runtime"), "cursor")
        self.assertEqual(sa.get("launch"), "herdr-kind")
        self.assertEqual(sa.get("herdr_kind"), "cursor")
        self.assertIsNotNone(row["pane_id"])
        self.assertEqual(row["status"], "live")
        prompts = [c for c in self.env.calls("herdr")[before:] if c["argv"][:2] == ["agent", "prompt"]]
        self.assertTrue(prompts, "kickoff must go via herdr agent prompt")

    def test_spawn_star_cursor_hq_and_worker(self):
        """*=cursor: HQ lead + worker roles both herdr --kind cursor; HQ policy under run/cursor/."""
        self.ok("config", "agent_runtimes", "*=cursor", agent="zz-test-director")
        fake = self.env.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho CURSOR_OK\n")
        fake.chmod(0o755)
        before = len(self.env.calls("herdr"))
        self.ok("spawn", "lead", "--name", "zz-cursor-lead", "--goal", "G-001",
                "--team", "g-001-a", agent="zz-test-director")
        self.ok("spawn", "coder", "--name", "zz-cursor-coder", "--goal", "G-001",
                "--team", "g-001-a", agent="zz-test-director")
        starts = [c for c in self.env.calls("herdr")[before:] if c["argv"][:2] == ["agent", "start"]]
        kinds = [c["argv"][c["argv"].index("--kind") + 1] for c in starts]
        self.assertEqual(kinds, ["cursor", "cursor"])
        lead = self.env.sql("SELECT spawn_args,cwd FROM agents WHERE name='zz-cursor-lead'")[0]
        sa = json.loads(lead["spawn_args"])
        self.assertEqual(sa.get("herdr_kind"), "cursor")
        ws = Path(sa.get("cursor_workspace") or "")
        self.assertTrue(str(ws).endswith("/run/cursor/zz-cursor-lead"), ws)
        self.assertTrue((ws / ".cursor" / "hooks.json").is_file())
        # HQ cwd is FACTORY_HOME; policy must not land in the shared checkout root's .cursor/
        self.assertFalse((Path(lead["cwd"]) / ".cursor" / "hooks.json").is_file())


class SpawnArgvTests(unittest.TestCase):
    def test_build_argv_print_force(self):
        h = load_harness()
        with mock.patch.object(h, "find_agent_binary", return_value="/bin/agent"):
            argv = h.build_cursor_argv(cwd="/tmp/ws", model="claude-opus-5-5", prompt="hi")
        self.assertEqual(argv[0], "/bin/agent")
        self.assertIn("--print", argv)
        self.assertIn("--force", argv)
        self.assertIn("--trust", argv)
        self.assertIn("hi", argv)


if __name__ == "__main__":
    unittest.main()
