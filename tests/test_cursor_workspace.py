"""T-0822: Cursor policy materializes under STATE/factory/cursor/<agent>; shared checkout and --add-dir roots stay clean."""
import importlib
import json
import os
import sys
import time
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from harness import REPO, FactoryTestCase

HOOKS = REPO / "roles" / "hooks"


def load_harness():
    return importlib.import_module("scout.cursor_harness")


def hook_command(hooks_path: Path) -> str:
    data = json.loads(hooks_path.read_text())
    return data["hooks"]["beforeShellExecution"][0]["command"]


def fail_closed_ok(hooks_path: Path) -> bool:
    data = json.loads(hooks_path.read_text())
    for val in (data.get("hooks") or {}).values():
        for item in val:
            if item.get("failClosed") is not True:
                return False
    return True


class CursorSpawnWorkspaceTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_agent("zz-test-director", "director", goal="G-001", team="g-001-a")
        self.ok("config", "agent_runtimes", "*=cursor", agent="zz-test-director")
        fake = e.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho ok\n")
        fake.chmod(0o755)

    def test_lead_spawn_argv_workspace_and_add_dir(self):
        before = len(self.env.calls("herdr"))
        name = "zz-cursor-lead-ws"
        self.ok("spawn", "lead", "--name", name, "--goal", "G-001", "--team", "g-001-a",
                agent="zz-test-director")
        starts = [c for c in self.env.calls("herdr")[before:] if c.get("argv", [])[:2] == ["agent", "start"]]
        self.assertEqual(len(starts), 1)
        argv = starts[0]["argv"]
        dash = argv.index("--")
        agent_argv = argv[dash + 1:]
        self.assertIn("--workspace", agent_argv)
        ws = Path(agent_argv[agent_argv.index("--workspace") + 1])
        self.assertTrue(str(ws).endswith(f"/state/factory/cursor/{name}"))
        self.assertIn("--add-dir", agent_argv)
        home = self.env.home_dir
        self.assertFalse((home / ".cursor").exists())
        self.assertFalse((home / "AGENTS.md").exists())
        hooks = ws / ".cursor" / "hooks.json"
        self.assertTrue(hooks.is_file())
        self.assertTrue(fail_closed_ok(hooks))
        self.assertIn("--hq", hook_command(hooks))
        self.assertNotIn("--role pm", hook_command(hooks))

    def test_second_materialize_refreshes_hooks_mtime(self):
        h = load_harness()
        settings = {"permissions": {"deny": [], "allow": []}}
        home = self.env.home_dir
        state = self.env.user_home / ".local" / "state"
        with mock.patch.dict(os.environ, {"XDG_STATE_HOME": str(state)}):
            ws = h.isolated_policy_dir(home, "zz-cursor-remat")
        hooks = ws / ".cursor" / "hooks.json"
        h.materialize_cursor_policy(
            ws, "lead", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# lead\n",
            hq=True, factory_home=home,
        )
        mtime1 = hooks.stat().st_mtime
        time.sleep(0.05)
        h.materialize_cursor_policy(
            ws, "lead", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# lead\n",
            hq=True, factory_home=home,
        )
        self.assertGreaterEqual(hooks.stat().st_mtime, mtime1)
        self.assertEqual(len(list((state / "factory" / "cursor").glob("zz-cursor-remat"))), 1)

    def test_claude_pm_unchanged_when_runtimes_empty(self):
        self.ok("config", "agent_runtimes", "", agent="zz-test-director")
        before = len(self.env.calls("herdr"))
        self.ok("spawn", "pm", "--name", "zz-claude-pm", "--goal", "G-001", "--team", "g-001-a",
                agent="zz-test-director")
        starts = [c for c in self.env.calls("herdr")[before:] if c.get("argv", [])[:2] == ["agent", "start"]]
        self.assertEqual(len(starts), 1)
        argv = starts[0]["argv"]
        self.assertIn("--kind", argv)
        self.assertEqual(argv[argv.index("--kind") + 1], "claude")


class AddDirLeakSpawnTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_agent("zz-test-director", "director", goal="G-001", team="g-001-a")
        self.ok("config", "agent_runtimes", "*=cursor", agent="zz-test-director")
        fake = e.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho ok\n")
        fake.chmod(0o755)

    def _spawn_hq_role(self, role, name):
        return self.refused(
            3, "spawn", role, "--name", name, "--goal", "G-001", "--team", "g-001-a",
            agent="zz-test-director",
        )

    def _retire_seeded_director(self):
        """T-1265: one live director per org. These tests spawn a director to check its add-dir roots, not the twin
        rule, so the fixture director (the caller) stops counting as the org's live director first."""
        self.env.sql("UPDATE agents SET status='exited' WHERE name='zz-test-director'")

    def test_hq_roles_add_dir_roots_clean(self):
        self._retire_seeded_director()
        before = len(self.env.calls("herdr"))
        for role, name in (("director", "zz-c-dir"), ("boss", "zz-c-boss"), ("lead", "zz-c-lead")):
            with self.subTest(role=role):
                self.ok("spawn", role, "--name", name, "--goal", "G-001", "--team", "g-001-a",
                        agent="zz-test-director")
        starts = [c for c in self.env.calls("herdr")[before:] if c.get("argv", [])[:2] == ["agent", "start"]]
        self.assertEqual(len(starts), 3)
        for start in starts:
            argv = start["argv"]
            dash = argv.index("--")
            agent_argv = argv[dash + 1:]
            self.assertIn("--workspace", agent_argv)
            ws = Path(agent_argv[agent_argv.index("--workspace") + 1])
            self.assertTrue(str(ws).startswith(str(self.env.user_home / ".local" / "state" / "factory" / "cursor")))
            add_dirs = [agent_argv[i + 1] for i, x in enumerate(agent_argv) if x == "--add-dir"]
            for d in add_dirs:
                p = Path(d).resolve()
                self.assertFalse((p / ".cursor").exists(), d)
                self.assertFalse((p / "AGENTS.md").exists(), d)

    def test_spawn_refused_when_add_dir_has_cursor_dir(self):
        home = self.env.home_dir
        (home / ".cursor").mkdir()
        before = len(self.env.calls("herdr"))
        p = self._spawn_hq_role("lead", "zz-leak-lead")
        self.assertIn("add-dir", p.stderr.lower())
        starts = [c for c in self.env.calls("herdr")[before:] if c.get("argv", [])[:2] == ["agent", "start"]]
        self.assertEqual(starts, [])

    def test_spawn_refused_when_add_dir_has_agents_md(self):
        agents_md = self.env.home_dir / "AGENTS.md"
        agents_md.write_text("# leak\n")
        before = len(self.env.calls("herdr"))
        self._retire_seeded_director()
        p = self._spawn_hq_role("director", "zz-leak-dir")
        self.assertIn("add-dir", p.stderr.lower())
        starts = [c for c in self.env.calls("herdr")[before:] if c.get("argv", [])[:2] == ["agent", "start"]]
        self.assertEqual(starts, [])


class PolicyTargetTests(unittest.TestCase):
    def test_factory_home_refused(self):
        h = load_harness()
        with __import__("tempfile").TemporaryDirectory(prefix="zz-cursor-home-") as tmp:
            home = Path(tmp) / "factory"
            home.mkdir()
            settings = {"permissions": {"deny": [], "allow": []}}
            with self.assertRaises(ValueError) as ctx:
                h.materialize_cursor_policy(
                    home, "boss", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# b\n",
                    factory_home=home,
                )
            self.assertIn("FACTORY_HOME", str(ctx.exception))

    def test_under_home_outside_run_cursor_refused(self):
        h = load_harness()
        with __import__("tempfile").TemporaryDirectory(prefix="zz-cursor-sub-") as tmp:
            home = Path(tmp) / "factory"
            home.mkdir()
            bad = home / "db"
            bad.mkdir()
            settings = {"permissions": {"deny": [], "allow": []}}
            with self.assertRaises(ValueError):
                h.materialize_cursor_policy(
                    bad, "coder", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# c\n",
                    factory_home=home,
                )

    def test_worktree_still_refused(self):
        h = load_harness()
        with __import__("tempfile").TemporaryDirectory(prefix="zz-cursor-wt-") as tmp:
            home = Path(tmp) / "factory"
            home.mkdir()
            wt = home / "worktrees" / "G-001-T-x"
            wt.mkdir(parents=True)
            settings = {"permissions": {"deny": [], "allow": []}}
            with self.assertRaises(ValueError):
                h.materialize_cursor_policy(
                    wt, "coder", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# c\n",
                    factory_home=home,
                )


class CrossRoleHooksTests(unittest.TestCase):
    def test_materialized_hooks_match_role(self):
        h = load_harness()
        settings = {"permissions": {"deny": [], "allow": []}}
        with __import__("tempfile").TemporaryDirectory(prefix="zz-cursor-xr-") as tmp:
            pm_ws = Path(tmp) / "pm-ws"
            coder_ws = Path(tmp) / "coder-ws"
            lead_ws = Path(tmp) / "lead-ws"
            for d in (pm_ws, coder_ws, lead_ws):
                d.mkdir()
            h.materialize_cursor_policy(
                pm_ws, "pm", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# pm\n",
            )
            h.materialize_cursor_policy(
                coder_ws, "coder", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# coder\n",
            )
            h.materialize_cursor_policy(
                lead_ws, "lead", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# lead\n", hq=True,
            )
            pm_cmd = hook_command(pm_ws / ".cursor" / "hooks.json")
            coder_cmd = hook_command(coder_ws / ".cursor" / "hooks.json")
            lead_cmd = hook_command(lead_ws / ".cursor" / "hooks.json")
            self.assertIn("--role pm", pm_cmd)
            self.assertNotIn("--role coder", pm_cmd)
            self.assertIn("--role coder", coder_cmd)
            self.assertNotIn("--role pm", coder_cmd)
            self.assertIn("--hq", lead_cmd)
            self.assertIn("--role lead", lead_cmd)
            self.assertNotIn("--role pm", lead_cmd)


class WorktreeSpawnRefusalTests(FactoryTestCase):
    INPROC = True

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_agent("zz-test-director", "director", goal="G-001", team="g-001-a")
        self.ok("config", "agent_runtimes", "coder=cursor", agent="zz-test-director")
        fake = e.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho ok\n")
        fake.chmod(0o755)
        self.wt = e.home_dir / "worktrees" / "G-001-T-0822"
        self.wt.mkdir(parents=True)

    def test_spawn_exits_3_when_policy_target_is_worktree(self):
        before = len(self.env.calls("herdr"))

        def worktree_as_policy(home, name, cwd):
            return Path(cwd).resolve()

        with mock.patch("scout.agent_runtime.ch.policy_workspace", side_effect=worktree_as_policy):
            p = self.refused(
                3, "spawn", "coder", "--name", "zz-bad-coder", "--goal", "G-001",
                "--team", "g-001-a", "--cwd", str(self.wt), agent="zz-test-director",
            )
        self.assertIn("worktree", p.stderr.lower())
        starts = [
            c for c in self.env.calls("herdr")[before:]
            if c.get("argv", [])[:2] == ["agent", "start"]
        ]
        self.assertEqual(starts, [])


if __name__ == "__main__":
    unittest.main()
