"""G-031 SC5: worker-guard treats hc and herdr-core like herdr for workers; HQ matches herdr rules."""
import json
import os
import sys
import unittest

GUARD_IMPL_PYTHON_ONLY = "G-031 SC5 hc/herdr-core worker rules are Python-only until the Go mirror (T-0990)"

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402


class WorkerGuardHcParityTests(unittest.TestCase):
    def _rc(self, command, *, hq=False, role="coder"):
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": command}})
        argv = ["--hq"] if hq else ["--role", role]
        return hook_run(payload, argv)

    def test_worker_blocks_hc_and_herdr_core(self):
        for cmd in (
            "hc server stop",
            "herdr-core server stop",
            "HOME=/tmp hc server stop",
            "env -i hc server stop",
            "bash -c 'hc server stop'",
            "hc kill other-agent",
            "HC_SOCKET=/tmp/x hc ls",
            "export HC_SOCKET=/tmp/x",
            "export $(echo HC_SOCKET=/tmp/x)",
            "export `echo HC_SOCKET=/tmp/x`",
            "! export $(echo HC_SOCKET=/tmp/x)",
            "time export $(echo HC_SOCKET=/tmp/x)",
            "if true; then export $(echo HC_SOCKET=/tmp/x); fi",
            "while true; do export $(echo HC_SOCKET=/tmp/x); break; done",
            "! declare -x $(echo HC_SOCKET=/tmp/x)",
            "if true; then declare -x $(echo HC_SOCKET=/tmp/x); fi",
            "! readonly $(echo HC_SOCKET=/tmp/x)",
            "time -p export $(echo HC_SOCKET=/tmp/x)",
            "time -p declare -x $(echo HC_SOCKET=/tmp/x)",
            "time -p typeset -x $(echo HC_SOCKET=/tmp/x)",
            "! time -p export $(echo HC_SOCKET=/tmp/x)",
            "if true; then time -p export $(echo HC_SOCKET=/tmp/x); fi",
            "while true; do time -p export $(echo HC_SOCKET=/tmp/x); break; done",
            "time -p readonly $(echo HC_SOCKET=/tmp/x)",
            "readonly x=$(echo HERDR_SOCKET_PATH=/tmp/x)",
            r"\export $(echo HC_SOCKET=/tmp/x)",
            '"export" $(echo HC_SOCKET=/tmp/x)',
            "ex''port $(echo HC_SOCKET=/tmp/x)",
            r"e\xport $(echo HC_SOCKET=/tmp/x)",
            r"\declare -x $(echo HC_SOCKET=/tmp/x)",
            r"\typeset -x $(echo HC_SOCKET=/tmp/x)",
            r"\readonly $(echo HC_SOCKET=/tmp/x)",
            r"time -p \export $(echo HC_SOCKET=/tmp/x)",
            r"! \export $(echo HC_SOCKET=/tmp/x)",
            r"if true; then \export $(echo HC_SOCKET=/tmp/x); fi",
            r"while true; do \export $(echo HC_SOCKET=/tmp/x); break; done",
            r"{ \export $(echo HC_SOCKET=/tmp/x); }",
            r"\export `echo HC_SOCKET=/tmp/x`",
            "eval export `echo HC_SOCKET=/tmp/x`",
            "unset HC_SOCKET; hc ls",
            "HERDR_SOCKET_PATH=/tmp/core.sock hc ls",
        ):
            with self.subTest(cmd=cmd):
                p = self._rc(cmd)
                self.assertEqual(p.returncode, 2, (cmd, p.stderr))
                self.assertIn("worker-guard", p.stderr)

    def test_worker_allows_hc_mentions_and_data(self):
        for cmd in (
            "echo hc",
            "grep hc README.md",
            "cat tests/stubs/hc",
            "echo HC_SOCKET=/tmp/x",
            "git status",
            "time -p git status",
            "time git status",
        ):
            with self.subTest(cmd=cmd):
                p = self._rc(cmd)
                self.assertEqual(p.returncode, 0, (cmd, p.stderr))

    def test_hq_matches_herdr_for_hc(self):
        for cmd in (
            "herdr server stop",
            "hc server stop",
            "herdr-core server stop",
            "hc kill other-agent",
        ):
            with self.subTest(cmd=cmd):
                p = self._rc(cmd, hq=True)
                self.assertEqual(p.returncode, 0, (cmd, p.stderr))
        for cmd in ("sudo hc server stop", "sudo herdr server stop"):
            with self.subTest(cmd=cmd):
                p = self._rc(cmd, hq=True)
                self.assertEqual(p.returncode, 2, (cmd, p.stderr))
                self.assertIn("worker-guard", p.stderr)


if __name__ == "__main__":
    unittest.main()
