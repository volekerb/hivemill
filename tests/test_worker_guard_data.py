"""T-0102: worker-guard blocks pkill/killall/kill <pid> for workers (G-008 M1932). The hook is run
as Claude Code runs it: a subprocess fed the tool-call JSON."""
import json
import os
import subprocess
import sys
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")

REPORT_BODY = """## Summary
T-0087 shipped the web design pass (tokens, light/dark). Word (see `factory res`) is what the lead ran; it printed
$(sudo ...) in the transcript, which the old guard refused. Nothing was run with sudo.

## Findings
- the pane list came from `herdr pane list` (read-only) and HERDR_SOCKET_PATH=/proc/nonexistent/x was already set
- export HERDR_SESSION=default appears in the incident log; it is quoted text, not a command
- worker's note: don't "quote" the (unbalanced paren and `backtick
"""

PR_BODY = """T-0102: guard fixes

What changed (see docs/worker-guard.md):
1. `unset HERDR_SESSION` is now judged as a command, not as prose.
2. sudo, su and docker in a body are text: $(sudo id) is not run by a quoted heredoc.
3. factory res | factory agents are broad reads, only when they are commands.

Test evidence: Ran 12 tests (all OK). herdr agent start is only an example here.
"""

NOTE_BODY = """Re-run of the audit (the second one):
`pkill -f unittest` and killall python3 killed other suites; kill 1234 is also refused now.
HERDR_SOCKET_PATH is pinned by the worker settings; HERDR_SESSION is left alone ( see M86 ).
"""

BODIES = (REPORT_BODY, PR_BODY, NOTE_BODY)


def run(cmd, hq=False, role=None):
    argv = [sys.executable, HOOK] + (["--hq"] if hq else []) + (["--role", role] if role else [])
    payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}})
    return subprocess.run(argv, input=payload, capture_output=True, text=True, timeout=30)


class Base(unittest.TestCase):
    def allowed(self, cmd, hq=False):
        p = run(cmd, hq)
        self.assertEqual(p.returncode, 0, f"should allow (hq={hq}): {cmd!r}\n{p.stderr}")

    def blocked(self, cmd, hq=False):
        p = run(cmd, hq)
        self.assertEqual(p.returncode, 2, f"should block (hq={hq}): {cmd!r}")
        self.assertIn("worker-guard", p.stderr)


class KillRuleTests(Base):
    def test_blocked_worker_mode(self):
        for cmd in (
            "pkill -f 'unittest discover'", "killall python3", "killall -9 python3", "nohup pkill -f x",
            "kill 1234", "kill -9 1234", "kill -s KILL 1234", "kill 0", "kill -9 -1", "kill -s KILL -1",
            "kill -- -123", "kill -TERM -123", "kill -1", "kill", "kill $pid", "kill $(pgrep unittest)",
            "timeout 5 kill 1", "env kill 5", "ls | xargs kill", "find . -name x -exec kill {} \\;",
            "bash -c 'kill 5'", "/bin/kill 5", "sh -c 'pkill x'", "echo a; kill 7",
        ):
            with self.subTest(cmd=cmd):
                self.blocked(cmd)

    def test_own_job_and_text_allowed(self):
        for cmd in (
            "kill %1", "kill %%", "kill -TERM $!", "kill -s TERM %1", "kill -9 $!", "kill $!", "kill -l",
            "sleep 60 & kill $!", "grep pkill README.md", "ps aux | grep unittest", 'git commit -m "no pkill"',
            "echo 'killall and kill 1234 are blocked'",
            "gh pr comment 5 --body 'pkill is refused'",
        ):
            with self.subTest(cmd=cmd):
                self.allowed(cmd)

    def test_hq_mode_unaffected(self):
        for cmd in ("pkill -f x", "killall x", "kill 1234", "kill -9 -1"):
            with self.subTest(cmd=cmd):
                self.allowed(cmd, hq=True)


if __name__ == "__main__":
    unittest.main()
