"""G-036 SC3: retro role guard — no push/merge, no role-file edits, no gate or config changes."""
import json
import os
import pwd
import subprocess
import unittest

from test_worker_guard import HOOK, run_hook

LIVE = os.path.join(pwd.getpwuid(os.getuid()).pw_dir, "factory")


class RetroGuardTests(unittest.TestCase):
    def assert_rc(self, expect, *, role="retro", tool="Bash", command=None, raw=None):
        if raw is None:
            raw = json.dumps({"tool_name": tool, "tool_input": {"command": command}})
        p = subprocess.run(
            ["python3", HOOK, "--role", role],
            input=raw if isinstance(raw, str) else json.dumps(raw),
            capture_output=True,
            text=True,
        )
        self.assertEqual(p.returncode, expect, (command or raw, p.stderr))
        return p

    def test_blocked_bash(self):
        for cmd in (
            "git push origin HEAD",
            "gh pr merge 1",
            "factory judge T-1 --merge",
            "factory config retro_enabled 0",
            "factory project update factory --gate-cmd true",
            "factory gate run T-1",
            f"cp /tmp/zz-retro-src.txt {LIVE}/roles/retro.md",
            "git push origin main  # please merge",
        ):
            with self.subTest(cmd=cmd):
                self.assert_rc(2, command=cmd)

    def test_blocked_edit_tools(self):
        for tool in ("Write", "Edit", "MultiEdit", "NotebookEdit"):
            with self.subTest(tool=tool):
                self.assert_rc(
                    2,
                    role="retro",
                    tool=tool,
                    raw={"tool_name": tool, "tool_input": {"file_path": f"{LIVE}/roles/coder.md", "content": "x"}},
                )

    def test_allowed_bash(self):
        for cmd in (
            "factory retro packet",
            "factory whoami",
            "factory inbox",
            "factory done x",
            "git log -1",
            "git diff",
            "gh api repos/volekerb/hivemill/pulls/1/comments",
        ):
            with self.subTest(cmd=cmd):
                self.assert_rc(0, command=cmd)

    def test_allowed_write_scratch(self):
        self.assert_rc(
            0,
            role="retro",
            tool="Write",
            raw={"tool_name": "Write", "tool_input": {"file_path": "/tmp/zz-retro-note.txt", "content": "x"}},
        )

    def test_other_roles_unchanged(self):
        w = {"tool_name": "Write", "tool_input": {"file_path": f"{LIVE}/roles/coder.md", "content": "x"}}
        self.assert_rc(0, role="coder", raw=w)
        self.assert_rc(0, role="coder", command="factory config retro_enabled 0")
        self.assert_rc(0, role="coder", command="git log -1")


if __name__ == "__main__":
    unittest.main()
