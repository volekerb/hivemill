"""T-0642: worker-guard refuses a full local unittest run for coder, qa and reviewer (CI is the full gate)."""
import json
import os
import subprocess
import sys
import unittest
from guard_impl import hook_run

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
    "python3 -munittest discover",
    "python3 -munittest",
    "python3 -m unittest discover -p test_x.py -p 'test_*.py'",
    "python3 -m unittest discover -p test_x.py --pattern='test_*'",
    "python3 -m unittest.__main__ discover",
    "pytest",
    "pytest -q",
    "pytest tests",
    "pytest tests/",
    "pytest ./tests/ -x",
    "pytest .",
    "python3 -m pytest",
    "python3 -m pytest -q tests/",
    "python3 -m pytest --maxfail 1",
    "py.test",
    "timeout 600 pytest tests",
    "pytest --keep-duplicates",
    "pytest --keep-duplicates tests",
    "pytest -r A tests",
    "pytest -r a",
    "pytest --lfnf all",
    "pytest --timeout 60 tests",
    "pytest --junit-xml out/x.xml tests",
    "pytest --log-file logs/x.log",
    "pytest --log-file x.py",
    "pytest --cov-config cfg/.coveragerc tests",
    "pytest --cache-dir /tmp/c tests",
    "pytest --junit-prefix a/b",
    "pytest --unknown-plugin a/b tests",
    "pytest tests/*",
    "pytest tests/.",
    "pytest tests/../tests",
    "pytest $PWD/tests",
    "pytest ../tests",
    "pytest tests/sub",
    "pytest tests/test_*.py",
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
    "python3 -m unittest -k name",
    "python3 -m unittest discover -s tests -k name",
    "pytest tests/test_x.py",
    "pytest tests/test_x.py::Case::test_one -q",
    "pytest -k name",
    "pytest tests -k name",
    "python3 -m pytest tests/test_x.py tests/test_y.py",
    "python3 -m pytest -k name tests",
    "python3 -m pytest --maxfail 1 tests/test_x.py",
    "FACTORY_HOME=$(mktemp -d) python3 bin/factory --help",
    "git log --grep='run pytest tests then python3 -m unittest discover'",
    "cat <<'EOF'\npytest tests\nEOF",
    "pytest --lf tests/test_x.py",
    "pytest --unknown tests/test_x.py",
    "pytest tests/test_x.py::t[1]",
)


def run_hook(command, role):
    argv = (["--role", role] if role else [])
    payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": command}})
    return hook_run(payload, argv)


class UnittestFullRunGuard(unittest.TestCase):
    def test_full_runs_are_refused_with_advice(self):
        for role in ROLES:
            for cmd in BLOCKED:
                with self.subTest(role=role, cmd=cmd):
                    p = run_hook(cmd, role)
                    self.assertEqual(p.returncode, 2, p.stderr)
                    self.assertIn("ci/affected.py", p.stderr)
                    self.assertIn("--quick", p.stderr)
                    self.assertIn("python3 -m unittest tests.test_X", p.stderr)
                    self.assertIn("asks its lead", p.stderr)
                    self.assertIn("sharded", p.stderr)

    def test_single_module_runs_are_allowed(self):
        for role in ROLES:
            for cmd in ALLOWED:
                with self.subTest(role=role, cmd=cmd):
                    p = run_hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_other_roles_are_not_restricted(self):
        for role in ("judge", "cleaner", "pm", None):
            for cmd in ("python3 -m unittest discover -s tests", "pytest tests", "python3 -m pytest"):
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(run_hook(cmd, role).returncode, 0)

    def test_hq_mode_is_not_restricted(self):
        for cmd in ("python3 -m unittest discover -s tests", "pytest tests"):
            with self.subTest(cmd=cmd):
                payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}})
                self.assertEqual(hook_run(payload, ["--hq"]).returncode, 0)


if __name__ == "__main__":
    unittest.main()
