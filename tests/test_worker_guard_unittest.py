"""T-0642: worker-guard refuses a full local unittest run for coder, qa and reviewer (CI is the full gate)."""
import json
import os
import subprocess
import sys
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
ROLES = ("coder", "qa", "reviewer")

BLOCKED = (
    "python3 -m unittest discover",
    "python3 -m unittest discover -s tests",
    "python3 -m unittest discover -s tests -v",
    "python3 -m unittest discover -s tests -p 'test_*.py'",
    "python3 -m unittest",
    "python3 -m unittest -v",
    "python -m unittest discover",
    "python3.12 -m unittest discover -s tests",
    "cd tests && python3 -m unittest discover",
    "FOO=1 python3 -m unittest discover -s tests",
    "timeout 600 python3 -m unittest discover -s tests",
    "bash -c 'python3 -m unittest discover -s tests'",
    "python3 -X dev -m unittest discover",
)
ALLOWED = (
    "python3 -m unittest tests.test_x",
    "python3 -m unittest tests.test_x tests.test_y -v",
    "python3 -m unittest -v tests.test_x.Case.test_one",
    "python3 -m unittest discover -s tests -p test_x.py",
    "python3 -m unittest discover -s tests -p test_gate_run.py -v",
    "python3 ci/affected.py --base origin/main --quick --run",
    "python3 -m py_compile bin/factory",
    "grep -rn 'unittest discover' docs",
    "echo python3 -m unittest discover",
)


def run_hook(command, role):
    argv = [sys.executable, HOOK] + (["--role", role] if role else [])
    payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": command}})
    return subprocess.run(argv, input=payload, capture_output=True, text=True, timeout=30)


class UnittestFullRunGuard(unittest.TestCase):
    def test_full_runs_are_refused_with_advice(self):
        for role in ROLES:
            for cmd in BLOCKED:
                with self.subTest(role=role, cmd=cmd):
                    p = run_hook(cmd, role)
                    self.assertEqual(p.returncode, 2, p.stderr)
                    self.assertIn("ci/affected.py", p.stderr)
                    self.assertIn("--quick", p.stderr)

    def test_single_module_runs_are_allowed(self):
        for role in ROLES:
            for cmd in ALLOWED:
                with self.subTest(role=role, cmd=cmd):
                    p = run_hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_other_roles_are_not_restricted(self):
        for role in ("judge", "cleaner", "pm", None):
            with self.subTest(role=role):
                self.assertEqual(run_hook("python3 -m unittest discover -s tests", role).returncode, 0)


if __name__ == "__main__":
    unittest.main()
