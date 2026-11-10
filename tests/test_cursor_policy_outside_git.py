"""T-1252: Cursor policy lives outside every git work tree (Cursor loads hooks from the git top level)."""
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from harness import REPO, FactoryTestCase
import scout.agent_runtime as ar
import scout.cursor_harness as ch

HOOKS = REPO / "roles" / "hooks"
ADAPTER = HOOKS / "cursor-guard-adapter.py"
SETTINGS = {"permissions": {"deny": [], "allow": []}}


def make_ctx(home, cwd, name="zz-agent", role="lead"):
    return ar.SpawnContext(
        role=role, name=name, cwd=Path(cwd), model="claude-opus-5-5", prompt_text="# p\n", kickoff="k",
        settings_path=Path(home) / "settings.json", settings=SETTINGS, home=Path(home), hooks_dir=HOOKS,
        plugin_dirs=[], add_dirs=[str(home)],
    )


class PlanCursorOutsideGitTests(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory(prefix="zz-cursor-og-")
        self.addCleanup(self._tmp.cleanup)
        self.tmp = Path(self._tmp.name).resolve()
        self.home = self.tmp / "factory"
        self.home.mkdir()
        (self.home / ".git").mkdir()
        self.state = self.tmp / "state"
        env = mock.patch.dict(os.environ, {"XDG_STATE_HOME": str(self.state)})
        env.start()
        self.addCleanup(env.stop)
        fb = mock.patch.object(ch, "find_agent_binary", return_value="/bin/agent")
        fb.start()
        self.addCleanup(fb.stop)

    def _check(self, plan):
        ws = plan.workspace
        self.assertFalse(str(ws).startswith(str(self.home)), ws)
        self.assertFalse(ch.inside_git_worktree(ws))
        hooks = json.loads((ws / ".cursor" / "hooks.json").read_text())["hooks"]
        for ev in ("beforeShellExecution", "preToolUse"):
            self.assertTrue(hooks[ev])
            for item in hooks[ev]:
                self.assertIs(item["failClosed"], True)
        argv = plan.argv
        self.assertEqual(argv[argv.index("--workspace") + 1], str(ws))
        adds = [argv[i + 1] for i, x in enumerate(argv) if x == "--add-dir"]
        self.assertIn(str(self.home), adds)

    def test_hq_cwd_factory_home_with_git_dir(self):
        self._check(ar._plan_cursor(make_ctx(self.home, self.home)))

    def test_task_worktree_with_git_file(self):
        wt = self.home / "worktrees" / "G-001-T-1"
        wt.mkdir(parents=True)
        (wt / ".git").write_text("gitdir: /nowhere\n")
        plan = ar._plan_cursor(make_ctx(self.home, wt, role="coder"))
        self._check(plan)
        self.assertIn(str(wt), [plan.argv[i + 1] for i, x in enumerate(plan.argv) if x == "--add-dir"])
        self.assertFalse((wt / ".cursor").exists())

    def test_xdg_state_inside_factory_home_raises(self):
        with mock.patch.dict(os.environ, {"XDG_STATE_HOME": str(self.home / "run")}):
            with self.assertRaises(ValueError) as c:
                ch.policy_workspace(self.home, "zz-agent", self.home)
        self.assertIn("XDG_STATE_HOME", str(c.exception))

    def test_xdg_state_inside_other_git_tree_raises(self):
        other = self.tmp / "other"
        (other / ".git").mkdir(parents=True)
        with mock.patch.dict(os.environ, {"XDG_STATE_HOME": str(other / "st")}):
            with self.assertRaises(ValueError) as c:
                ch.policy_workspace(self.home, "zz-agent", self.home)
        self.assertIn("XDG_STATE_HOME", str(c.exception))

    def test_policy_dir_mode_0700(self):
        ws = ch.isolated_policy_dir(self.home, "zz-agent")
        self.assertEqual(ws, (self.state / "factory" / "cursor" / "zz-agent").resolve())
        self.assertEqual(ws.stat().st_mode & 0o777, 0o700)

    def test_target_refused_in_git_tree_and_run_cursor(self):
        for bad in (self.home / "run" / "cursor" / "x", self.home / "db"):
            with self.subTest(bad=str(bad)):
                bad.mkdir(parents=True, exist_ok=True)
                with self.assertRaises(ValueError):
                    ch.assert_policy_target_allowed(self.home, bad)
        repo = self.tmp / "repo"
        (repo / ".git").mkdir(parents=True)
        (repo / "sub").mkdir()
        with self.assertRaises(ValueError):
            ch.assert_policy_target_allowed(self.home, repo / "sub")

    def test_target_outside_any_git_tree_accepted(self):
        ok = self.tmp / "plain"
        ok.mkdir()
        ch.assert_policy_target_allowed(self.home, ok)

    def test_inside_git_worktree_detects_file_and_dir(self):
        a = self.tmp / "a"
        (a / "x" / "y").mkdir(parents=True)
        self.assertFalse(ch.inside_git_worktree(a / "x" / "y"))
        (a / ".git").write_text("gitdir: z\n")
        self.assertTrue(ch.inside_git_worktree(a / "x" / "y"))


class HooksOkGitTests(FactoryTestCase):
    def _ws(self, parent, name):
        ws = Path(parent) / name
        self._hooks(ws)
        return ws

    def _hooks(self, ws):
        # written directly: materialize_cursor_policy itself now refuses git work trees
        ch.write_atomic(ws / ".cursor" / "hooks.json", json.dumps(ch.cursor_hooks_json(HOOKS, "coder")))

    def test_nested_under_git_dir_is_not_ok(self):
        base = self.env.user_home / "repo"
        (base / ".git").mkdir(parents=True)
        ws = self._ws(base, "run/cursor/x")
        with self.env.load_factory() as mod:
            self.assertFalse(mod.cursor_hooks_ok(ws))

    def test_non_git_dir_is_ok(self):
        ws = self._ws(self.env.user_home, "plain/ws")
        with self.env.load_factory() as mod:
            self.assertTrue(mod.cursor_hooks_ok(ws))

    def test_workspace_that_is_itself_the_git_root_is_ok(self):
        ws = self.env.user_home / "selfrepo"
        (ws / ".git").mkdir(parents=True)
        self._hooks(ws)
        with self.env.load_factory() as mod:
            self.assertTrue(mod.cursor_hooks_ok(ws))


class AdapterLogTests(unittest.TestCase):
    def _run(self, log, command="sudo id"):
        env = {k: v for k, v in os.environ.items() if k != "FACTORY_CURSOR_HOOK_LOG"}
        env.update({"FACTORY_AGENT": "zz-agent", "FACTORY_ROLE": "coder"})
        if log is not None:
            env["FACTORY_CURSOR_HOOK_LOG"] = str(log)
        return subprocess.run(
            [sys.executable, str(ADAPTER), "--role", "coder", "--event", "beforeShellExecution"],
            input=json.dumps({"command": command, "cwd": "/tmp"}), capture_output=True, text=True, env=env,
            timeout=60,
        )

    def test_deny_logged_without_command_text(self):
        with tempfile.TemporaryDirectory(prefix="zz-cursor-log-") as tmp:
            log = Path(tmp) / "hook-events.jsonl"
            p = self._run(log)
            self.assertEqual(json.loads(p.stdout.strip().splitlines()[-1])["permission"], "deny")
            lines = log.read_text().splitlines()
            self.assertEqual(len(lines), 1)
            rec = json.loads(lines[0])
            self.assertEqual(
                (rec["event"], rec["decision"], rec["agent"], rec["role"]),
                ("beforeShellExecution", "deny", "zz-agent", "coder"))
            self.assertIn("ts", rec)
            self.assertNotIn("sudo", lines[0])

    def test_unwritable_log_keeps_decision_and_exit_code(self):
        with tempfile.TemporaryDirectory(prefix="zz-cursor-log-") as tmp:
            good = self._run(Path(tmp) / "ok.jsonl")
            bad = self._run(Path(tmp) / "no-such-dir" / "hook-events.jsonl")
            none = self._run(None)
        self.assertEqual(bad.returncode, good.returncode)
        self.assertEqual(bad.stdout.strip().splitlines()[-1], good.stdout.strip().splitlines()[-1])
        self.assertEqual(none.returncode, good.returncode)
        self.assertEqual(json.loads(bad.stdout.strip().splitlines()[-1])["permission"], "deny")

    def test_allow_logged_as_allow(self):
        with tempfile.TemporaryDirectory(prefix="zz-cursor-log-") as tmp:
            log = Path(tmp) / "h.jsonl"
            self._run(log, command="echo ok")
            self.assertEqual(json.loads(log.read_text())["decision"], "allow")


if __name__ == "__main__":
    unittest.main()
