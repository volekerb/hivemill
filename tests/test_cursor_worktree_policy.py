"""T-0833: Cursor policy lives under STATE/factory/cursor/<agent>, not in shared task worktrees."""
import importlib
import json
import os
import sys
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


class WorktreePolicySpawnTests(FactoryTestCase):
    CODER = "zz-wt-coder"
    REVIEWER = "zz-wt-reviewer"

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
        self.wt = e.home_dir / "worktrees" / "G-001-T-0833"
        self.wt.mkdir(parents=True)
        (self.wt / "src").mkdir()

    def _spawn(self, role, name, cwd=None):
        args = ["spawn", role, "--name", name, "--goal", "G-001", "--team", "g-001-a"]
        if cwd is not None:
            args += ["--cwd", str(cwd)]
        return self.ok(*args, agent="zz-test-director")

    def test_coder_policy_not_in_worktree(self):
        self._spawn("coder", self.CODER, cwd=self.wt)
        self.assertFalse((self.wt / ".cursor").exists())
        self.assertFalse((self.wt / "AGENTS.md").exists())
        coder_hooks = self.env.user_home / ".local" / "state" / "factory" / "cursor" / self.CODER / ".cursor" / "hooks.json"
        self.assertTrue(coder_hooks.is_file())

    def test_reviewer_spawn_does_not_clobber_coder_policy(self):
        self._spawn("coder", self.CODER, cwd=self.wt)
        coder_hooks = self.env.user_home / ".local" / "state" / "factory" / "cursor" / self.CODER / ".cursor" / "hooks.json"
        before = coder_hooks.read_bytes()
        self.assertIn("--role coder", hook_command(coder_hooks))

        self._spawn("reviewer", self.REVIEWER, cwd=self.wt)
        self.assertFalse((self.wt / ".cursor").exists())
        self.assertFalse((self.wt / "AGENTS.md").exists())

        self.assertEqual(coder_hooks.read_bytes(), before)
        self.assertIn("--role coder", hook_command(coder_hooks))

        reviewer_hooks = self.env.user_home / ".local" / "state" / "factory" / "cursor" / self.REVIEWER / ".cursor" / "hooks.json"
        self.assertTrue(reviewer_hooks.is_file())
        self.assertNotEqual(reviewer_hooks, coder_hooks)
        self.assertIn("--role reviewer", hook_command(reviewer_hooks))


class MaterializeRefusalTests(unittest.TestCase):
    def test_worktree_target_raises(self):
        h = load_harness()
        home = Path(tempfile := __import__("tempfile").mkdtemp(prefix="zz-cursor-refuse-"))
        try:
            wt = home / "worktrees" / "G-001-T-x"
            wt.mkdir(parents=True)
            settings = {"permissions": {"deny": [], "allow": []}}
            with self.assertRaises(ValueError) as ctx:
                h.materialize_cursor_policy(
                    wt, "coder", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# c\n",
                    factory_home=home,
                )
            self.assertIn("worktree", str(ctx.exception).lower())
        finally:
            __import__("shutil").rmtree(tempfile, ignore_errors=True)


class SpawnWorktreePolicyRefusalTests(FactoryTestCase):
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
        self.wt = e.home_dir / "worktrees" / "G-001-T-refuse"
        self.wt.mkdir(parents=True)

    def test_spawn_exits_3_when_policy_target_is_worktree(self):
        before = len(self.env.calls("herdr"))

        def worktree_as_policy(home, name, cwd):
            return Path(cwd).resolve()

        with mock.patch("scout.cursor_harness.policy_workspace", side_effect=worktree_as_policy):
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


class HqPolicyIsolationTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_agent("zz-test-director", "director", goal="G-001", team="g-001-a")
        self.ok("config", "agent_runtimes", "pm=cursor", agent="zz-test-director")
        fake = e.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho ok\n")
        fake.chmod(0o755)

    def test_pm_at_factory_home_uses_run_cursor(self):
        name = "zz-hq-pm"
        self.ok("spawn", "pm", "--name", name, "--goal", "G-001", "--team", "g-001-a",
                agent="zz-test-director")
        home = self.env.home_dir
        self.assertFalse((home / ".cursor" / "hooks.json").exists())
        hooks = self.env.user_home / ".local" / "state" / "factory" / "cursor" / name / ".cursor" / "hooks.json"
        self.assertTrue(hooks.is_file())


class PolicyWorkspaceUnitTests(unittest.TestCase):
    def test_worktree_cwd_uses_isolated_dir(self):
        h = load_harness()
        with __import__("tempfile").TemporaryDirectory(prefix="zz-cursor-pw-") as tmp:
            home = Path(tmp) / "factory"
            home.mkdir()
            wt = home / "worktrees" / "G-001-T-1"
            wt.mkdir(parents=True)
            state = Path(tmp) / "state"
            with mock.patch.dict(os.environ, {"XDG_STATE_HOME": str(state)}):
                ws = h.policy_workspace(home, "zz-agent", wt)
            self.assertEqual(ws, (state / "factory" / "cursor" / "zz-agent").resolve())
            self.assertTrue(ws.is_dir())
            self.assertEqual(oct(ws.stat().st_mode & 0o777), oct(0o700))

    def test_temp_cwd_unchanged(self):
        h = load_harness()
        with __import__("tempfile").TemporaryDirectory(prefix="zz-cursor-ext-") as tmp:
            home = Path(tmp) / "factory"
            home.mkdir()
            ext = Path(tmp) / "cursor-pilot-ws"
            ext.mkdir()
            ws = h.policy_workspace(home, "cursor-pilot", ext)
            self.assertEqual(ws.resolve(), ext.resolve())


if __name__ == "__main__":
    unittest.main()
