"""G-036 SC3: retro role guard — no push/merge, no role-file edits, no gate or config changes."""
import json
import os
import pwd
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "G-036 SC3 retro_factory and retro role-file edits are Python-only until the Go mirror (#517)"

LIVE = os.path.join(pwd.getpwuid(os.getuid()).pw_dir, "factory")


class RetroGuardTests(unittest.TestCase):
    def assert_rc(self, expect, *, role="retro", tool="Bash", command=None, raw=None):
        if raw is None:
            payload = {"tool_name": tool, "tool_input": {"command": command}}
        elif isinstance(raw, str):
            payload = json.loads(raw)
        else:
            payload = raw
        p = hook_run(json.dumps(payload), ["--role", role])
        self.assertEqual(p.returncode, expect, (command or payload, p.stderr))
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
            f"cp /tmp/zz-retro-src.txt {LIVE}/rol*/coder.md",
            f"echo x > {LIVE}/rol*/coder.md",
            f'echo x > "$(printf %s {LIVE[:-7]}f; printf %s tory/roles)/coder.md"',
            f'echo x > "$(printf %s {LIVE})/roles/coder.md"',
            f'echo x > "$(printf %s {LIVE})/roles/retro.md"',
            f'echo x > "$(printf %s {LIVE}/rol; printf %s es)/coder.md"',
            f'echo x > "$(printf %s {LIVE}/r; printf %s oles)/coder.md"',
            f'echo x > "$(printf %s {LIVE}/; printf %s roles/coder.md)"',
            f"echo x > \"$(printf '%s/%s' {LIVE} roles/retro.md)\"",
            f"echo x > \"$(printf '%s/roles/coder.md' {LIVE})\"",
            'echo x > "$(printf %s /tmp/zz-out); touch /tmp/zz-retro-hook-exec-semi"',
            f'echo x > "$(touch /tmp/zz-retro-hook-exec-block; printf \'%s/%s\' {LIVE} roles/coder.md)"',
            f'echo x > "$(printf %s /tmp/zz-ok.txt)" > $(printf %s {LIVE}/roles/coder.md)',
            f'echo x > $(printf %s {LIVE}/roles/coder.md) > "$(printf %s /tmp/zz-ok.txt)"',
            f"cd {LIVE} && echo x > \"$(printf %s roles/coder.md)\"",
            f"cd {LIVE} && echo x > \"$(printf %s ./roles/coder.md)\"",
            f"ZZ=$(printf '%s/%s' {LIVE} roles/coder.md); echo x > \"$ZZ\"",
            "cd $HOME/factory && echo x > roles/coder.md",
            "cd ${HOME}/factory && echo x > roles/retro.md",
            f"cd $HOME/factory && cp /tmp/zz-retro-src.txt roles/coder.md",
            "cd $HOME/factory && echo x > \"$(printf %s roles/coder.md)\"",
            f"echo x > \"$(printf '%s %s %s' a b c)\"",
            f"X=$(printf %s hi > {LIVE}/run/guard-export.json)",
            f"X=$(mktemp > {LIVE}/run/guard-export.json)",
            "git push origin main  # please merge",
        ):
            with self.subTest(cmd=cmd):
                for marker in (
                    "/tmp/zz-retro-hook-exec-semi",
                    "/tmp/zz-retro-hook-exec-block",
                ):
                    if marker in cmd:
                        if os.path.exists(marker):
                            os.remove(marker)
                self.assert_rc(2, command=cmd)
                for marker in (
                    "/tmp/zz-retro-hook-exec-semi",
                    "/tmp/zz-retro-hook-exec-block",
                ):
                    if marker in cmd:
                        self.assertFalse(os.path.exists(marker), marker)

    def test_blocked_edit_tools(self):
        for tool in ("Write", "Edit", "MultiEdit", "NotebookEdit"):
            with self.subTest(tool=tool):
                self.assert_rc(
                    2,
                    role="retro",
                    raw={"tool_name": tool, "tool_input": {"file_path": f"{LIVE}/roles/coder.md", "content": "x"}},
                )

    def test_allowed_bash(self):
        only_printf_tmp = 'echo x > "$(printf %s /tmp/zz-only-printf)"'
        if os.path.exists("/tmp/zz-only-printf"):
            os.remove("/tmp/zz-only-printf")
        self.assert_rc(0, command=only_printf_tmp)
        self.assertFalse(os.path.exists("/tmp/zz-only-printf"))
        for cmd in (
            'echo x > "$(printf %s /tmp/; printf %s roles/coder.md)"',
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
            raw={"tool_name": "Write", "tool_input": {"file_path": "/tmp/zz-retro-note.txt", "content": "x"}},
        )

    def test_other_roles_unchanged(self):
        w = {"tool_name": "Write", "tool_input": {"file_path": f"{LIVE}/roles/coder.md", "content": "x"}}
        self.assert_rc(0, role="coder", raw=w)
        self.assert_rc(0, role="coder", command="factory config retro_enabled 0")
        self.assert_rc(0, role="coder", command="git log -1")
        self.assert_rc(2, role="coder", command=f"X=$(printf %s hi > {LIVE}/run/guard-export.json)")
        self.assert_rc(2, role="reviewer", command=f"X=$(mktemp > {LIVE}/run/guard-export.json)")


if __name__ == "__main__":
    unittest.main()
