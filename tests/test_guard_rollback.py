"""G-016 T-0357 (SC5): rollback of guard_impl to python through the real shim and factory CLI — no relaunch between hook
calls; rendered coder settings entry point; go/shadow fallbacks to python. End-to-end in a temp FACTORY_HOME and temp
HOME with a built Go binary and matching stamp (tests/test_guard_shim.py covers the stub binary)."""
import json
import os
import pwd
import re
import shutil
import subprocess
import sys
import unittest
from pathlib import Path

from harness import FactoryTestCase, git, rendered_settings_dir

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, os.path.join(ROOT, "tests"))
import guard_impl  # noqa: E402

LIVE_HOME = os.path.join(os.path.realpath(pwd.getpwuid(os.getuid()).pw_dir), "factory")


def go_unavailable(err):
    msg = str(err)
    return msg.startswith("go not found on PATH") or "go.mod requires go >=" in msg


class RollbackTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_agent("dir-rollback", "director")
        self.srchash = self._ensure_go_tree_in_home()
        self.live = str(self.env.home_dir)
        self.hook_cmd = f"sh {self.live}/roles/hooks/worker-guard --role coder"
        self.lib = self.env.user_home / ".local" / "lib" / "factory"
        self.flag = self.env.home_dir / "run" / "guard_impl"
        self._install_go_binary()
        p = self.env.run("export")
        self.assertEqual(p.returncode, 0, p.stderr)
        self._assert_rendered_coder_entry()

    def _ensure_go_tree_in_home(self):
        """The harness template home has no go/ tree; link this checkout's go/ so write_guard_impl can set a real srchash."""
        home = self.env.home_dir
        go_dst = home / "go"
        if not go_dst.exists():
            go_dst.symlink_to(ROOT / "go")
            git("add", "go", cwd=home)
            git("commit", "-q", "-m", "test: link go tree for guard_impl", cwd=home)
        return git("rev-parse", "HEAD:go", cwd=home)

    def _assert_rendered_coder_entry(self):
        data = json.loads((rendered_settings_dir() / "coder.json").read_text())
        cmd = data["hooks"]["PreToolUse"][0]["hooks"][0]["command"]
        self.assertEqual(cmd.replace(LIVE_HOME, self.live), self.hook_cmd)

    def _install_go_binary(self):
        try:
            built = guard_impl.go_bin()
        except RuntimeError as e:
            if go_unavailable(e):
                self.skipTest(str(e))
            raise
        self.lib.mkdir(parents=True, exist_ok=True)
        dest = self.lib / "worker-guard"
        shutil.copy2(built, dest)
        os.chmod(dest, 0o755)
        (self.lib / "worker-guard.stamp").write_text(self.srchash + "\n")

    def _run_hook(self, command="ls"):
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": command}})
        env = self.env.env()
        env["WORKER_GUARD_SHIM_TRACE"] = "1"
        return subprocess.run(["sh", "-c", self.hook_cmd], input=payload, capture_output=True, text=True,
                              env=env, timeout=120)

    def _trace_impl(self, command="ls"):
        r = self._run_hook(command)
        m = re.search(r"(?m)^impl=(\w+)", r.stderr)
        self.assertIsNotNone(m, f"no impl= trace: rc={r.returncode} stderr={r.stderr!r}")
        return m.group(1), r

    def _set_guard_impl(self, impl):
        p = self.env.run("config", "guard_impl", impl, agent="dir-rollback")
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_no_relaunch_go_to_python_without_process_restart(self):
        self._set_guard_impl("go")
        self.assertEqual(self._trace_impl()[0], "go")
        p = self.env.run("config", "guard_impl", "python", agent="dir-rollback")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertTrue(self.flag.read_text().startswith("python "), self.flag.read_text())
        self.assertEqual(self._trace_impl()[0], "python")

    def test_no_relaunch_shadow_to_python_without_process_restart(self):
        self._set_guard_impl("shadow")
        self.assertEqual(self._trace_impl()[0], "shadow")
        self.assertEqual(self.env.run("config", "guard_impl", "python", agent="dir-rollback").returncode, 0)
        self.assertEqual(self._trace_impl()[0], "python")

    def test_doc_command_writes_python_flag_line(self):
        """Acceptance 3: the documented `factory config guard_impl python` is real in a temp home."""
        self._set_guard_impl("go")
        p = self.env.run("config", "guard_impl", "python", agent="dir-rollback")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertTrue(self.flag.read_text().startswith("python "), self.flag.read_text())

    def test_stale_stamp_binary_deleted_and_garbage_flag_use_python(self):
        self._set_guard_impl("go")
        (self.lib / "worker-guard.stamp").write_text("deadbeef\n")
        self.assertEqual(self._trace_impl()[0], "python", "stale stamp")
        (self.lib / "worker-guard.stamp").write_text(self.srchash + "\n")
        os.remove(self.lib / "worker-guard")
        self.assertEqual(self._trace_impl()[0], "python", "binary deleted")
        self.flag.write_text("not-valid\n")
        self.assertEqual(self._trace_impl()[0], "python", "garbage flag")

    def test_sudo_ls_blocked_and_ls_allowed_in_python_go_and_shadow(self):
        for impl in ("python", "go", "shadow"):
            with self.subTest(impl=impl):
                self._set_guard_impl(impl)
                _, deny = self._trace_impl("sudo ls")
                self.assertEqual(deny.returncode, 2, deny.stderr)
                _, allow = self._trace_impl("ls")
                self.assertEqual(allow.returncode, 0, allow.stderr)


if __name__ == "__main__":
    unittest.main()
