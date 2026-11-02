"""CI wiring for cursor runtime tests and GUARD_IMPL=diff (T-0861)."""
import json
import os
import re
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "tests"))
sys.path.insert(0, str(ROOT / "ci"))

import _gitenv  # noqa: F401,E402

import ci_tier  # noqa: E402
import guard_impl_unittest  # noqa: E402
import shard  # noqa: E402

from test_ci_tier import Repo  # noqa: E402

WF = ROOT / ".github" / "workflows" / "tests.yml"
CURSOR_TESTS = (
    "test_cursor_pilot.py",
    "test_cursor_harness.py",
    "test_cursor_guard_adapter.py",
)
BLOCK = json.dumps({"tool_name": "Bash", "tool_input": {"command": "pkill x"}})


def stub_go(td, body):
    path = os.path.join(td, "worker-guard")
    with open(path, "w") as f:
        f.write("#!/bin/sh\n" + body + "\n")
    os.chmod(path, os.stat(path).st_mode | stat.S_IXUSR)
    return path


def workflow_guard_diff_job_block(text):
    m = re.search(r"(?ms)^  guard-diff:\n(.*?)(?=^  [a-z].*:|\Z)", text)
    if not m:
        raise AssertionError("tests.yml has no guard-diff job")
    return m.group(1)


def assert_workflow_guard_diff(text):
    if "GUARD_IMPL=diff" not in text:
        raise AssertionError("tests.yml must set GUARD_IMPL=diff for the guard-diff job")
    block = workflow_guard_diff_job_block(text)
    if re.search(r"(?m)^\s+continue-on-error:\s*true", block):
        raise AssertionError("guard-diff job must not use continue-on-error: true")


class WorkflowGuardDiffTest(unittest.TestCase):
    def test_guard_impl_unittest_matches_docs_guard_diff_module_set(self):
        self.assertEqual(
            guard_impl_unittest.diff_module_names(),
            [
                "test_worker_guard.py",
                "test_priv_guard.py",
                "test_guard_diff.py",
            ],
        )

    def test_workflow_has_guard_diff_job_without_continue_on_error(self):
        assert_workflow_guard_diff(WF.read_text())

    def test_removing_guard_impl_diff_from_workflow_fails_this_check(self):
        wf = WF.read_text()
        assert_workflow_guard_diff(wf)
        stripped = wf.replace("GUARD_IMPL=diff", "")
        with self.assertRaises(AssertionError):
            assert_workflow_guard_diff(stripped)

    def test_guard_diff_run_exits_nonzero_on_python_go_disagreement(self):
        """The CI run line is GUARD_IMPL=diff python3 ci/guard_impl_unittest.py run; hook_run must not pass silently."""
        from guard_impl import hook_run  # noqa: E402
        with tempfile.TemporaryDirectory() as td:
            go = stub_go(td, "cat >/dev/null; exit 0")
            with mock.patch.dict(os.environ, {"GUARD_IMPL": "diff", "GUARD_GO_BIN": go}, clear=False):
                with self.assertRaises(AssertionError) as cm:
                    hook_run(BLOCK, ["--role", "coder"])
                self.assertIn("python=2 go=0", str(cm.exception))
        script = (
            "import json, os, stat, sys, tempfile\n"
            "from unittest import mock\n"
            "sys.path.insert(0, %r)\n"
            "from guard_impl import hook_run\n"
            "BLOCK = json.dumps({'tool_name': 'Bash', 'tool_input': {'command': 'pkill x'}})\n"
            "td = tempfile.mkdtemp()\n"
            "path = os.path.join(td, 'worker-guard')\n"
            "open(path, 'w').write('#!/bin/sh\\ncat >/dev/null; exit 0\\n')\n"
            "os.chmod(path, os.stat(path).st_mode | stat.S_IXUSR)\n"
            "try:\n"
            "    with mock.patch.dict(os.environ, {'GUARD_IMPL': 'diff', 'GUARD_GO_BIN': path}, clear=False):\n"
            "        hook_run(BLOCK, ['--role', 'coder'])\n"
            "except AssertionError:\n"
            "    sys.exit(1)\n"
            "sys.exit(0)\n"
        ) % str(ROOT / "tests")
        r = subprocess.run([sys.executable, "-c", script], cwd=ROOT, capture_output=True, text=True)
        self.assertEqual(r.returncode, 1, r.stdout + r.stderr)


class CursorSuiteCollectorTest(unittest.TestCase):
    def test_cursor_modules_are_not_excluded_from_shards_or_workflow(self):
        wf = WF.read_text()
        exclude_lines = [ln for ln in wf.splitlines() if re.search(r"exclude", ln, re.I)]
        all_mods = shard.all_modules()
        for name in CURSOR_TESTS:
            path = ROOT / "tests" / name
            if not path.is_file():
                print("absent: %s" % name)
                continue
            self.assertIn(name, all_mods, "%s must be collected by ci/shard.py" % name)
            for ln in exclude_lines:
                self.assertNotIn(name, ln, "%s must not appear on a workflow exclude line" % name)


class TierCursorTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.repo = Repo(self._tmp.name)
        self.repo.write("tests/test_cursor_pilot.py",
                        "import unittest\n\nclass T(unittest.TestCase):\n    def test_x(self): pass\n")
        from test_ci_tier import git  # noqa: E402
        git(self.repo.root, "add", "-A")
        git(self.repo.root, "commit", "-q", "-m", "cursor pilot")
        self.repo.base = self.repo.rev()

    def test_only_test_cursor_pilot_is_not_docs_tier(self):
        self.repo.write("tests/test_cursor_pilot.py",
                          "import unittest\n\nclass T(unittest.TestCase):\n    def test_y(self): pass\n")
        from test_ci_tier import git  # noqa: E402
        git(self.repo.root, "add", "-A")
        git(self.repo.root, "commit", "-q", "-m", "head")
        head = self.repo.rev()
        plan = ci_tier.plan(str(self.repo.root), self.repo.base, head)
        self.assertNotEqual(plan["mode"], "docs", plan)


if __name__ == "__main__":
    unittest.main()
