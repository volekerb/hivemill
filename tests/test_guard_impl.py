"""G-016 T-0352: tests/guard_impl.py picks the hook implementation and, in diff mode, fails on a divergence."""
import json
import os
import stat
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import guard_impl  # noqa: E402
from guard_impl import hook_run  # noqa: E402

BLOCK = json.dumps({"tool_name": "Bash", "tool_input": {"command": "pkill x"}})
ALLOW = json.dumps({"tool_name": "Bash", "tool_input": {"command": "echo hi"}})


def stub(td, body):
    path = os.path.join(td, "worker-guard")
    with open(path, "w") as f:
        f.write("#!/bin/sh\n" + body + "\n")
    os.chmod(path, os.stat(path).st_mode | stat.S_IXUSR)
    return path


class GuardImplTests(unittest.TestCase):
    def env(self, mode, go=None):
        e = {"GUARD_IMPL": mode}
        if go:
            e["GUARD_GO_BIN"] = go
        return mock.patch.dict(os.environ, e)

    def test_default_is_the_python_hook(self):
        with mock.patch.dict(os.environ, clear=False):
            os.environ.pop("GUARD_IMPL", None)
            self.assertEqual(guard_impl.impl(), "python")
            self.assertEqual(hook_run(BLOCK, ["--role", "coder"]).returncode, 2)

    def test_unknown_mode_is_an_error(self):
        with self.env("rust"), self.assertRaises(ValueError):
            hook_run(ALLOW, ["--role", "coder"])

    def test_diff_catches_a_go_binary_that_always_allows(self):
        with tempfile.TemporaryDirectory() as td, self.env("diff", stub(td, "cat >/dev/null; exit 0")):
            with self.assertRaises(AssertionError) as cm:
                hook_run(BLOCK, ["--role", "coder"])
            self.assertIn("pkill x", str(cm.exception))
            self.assertIn("python=2 go=0", str(cm.exception))

    def test_diff_passes_when_both_agree_and_returns_python_result(self):
        with tempfile.TemporaryDirectory() as td, self.env("diff", stub(td, "cat >/dev/null; exit 0")):
            self.assertEqual(hook_run(ALLOW, ["--role", "coder"]).returncode, 0)

    def test_diff_fails_when_only_stderr_differs(self):  # T-0352 B2: same exit code, different message
        with tempfile.TemporaryDirectory() as td, self.env("diff", stub(td, "cat >/dev/null; echo other >&2; exit 2")):
            with self.assertRaises(AssertionError) as cm:
                hook_run(BLOCK, ["--role", "coder"])
            self.assertIn("stderr differs", str(cm.exception))
            self.assertIn("pkill x", str(cm.exception))

    def test_diff_normalizes_only_the_go_prefix(self):
        self.assertEqual(guard_impl.normalize_go_stderr("worker-guard(go): hook error\n"), "worker-guard: hook error\n")
        self.assertEqual(guard_impl.normalize_go_stderr("worker-guard: blocked (go)\n"), "worker-guard: blocked (go)\n")
        with tempfile.TemporaryDirectory() as td, self.env("diff", stub(td, "cat >/dev/null; echo 'worker-guard(go): x' >&2; exit 0")):
            with self.assertRaises(AssertionError):  # prefix normalized, but python's stderr is empty on ALLOW
                hook_run(ALLOW, ["--role", "coder"])

    def test_go_mode_runs_the_go_binary(self):
        with tempfile.TemporaryDirectory() as td, self.env("go", stub(td, "cat >/dev/null; exit 7")):
            self.assertEqual(hook_run(BLOCK, ["--role", "coder"]).returncode, 7)

    def test_go_mode_with_a_missing_binary_errors_instead_of_skipping(self):
        with self.env("go", "/nonexistent/worker-guard"), self.assertRaises(RuntimeError):
            hook_run(BLOCK, ["--role", "coder"])


if __name__ == "__main__":
    unittest.main()
