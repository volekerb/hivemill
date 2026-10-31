"""T-0788: NO_PUSH_ROLES may not remove a protected variable (env -u, unset) or clear the environment (env -i)."""
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NO_PUSH = ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator")
OTHER = ("coder", "cleaner", "judge")

DENIED = [
    "env -u GIT_ASKPASS git status", "env -u GIT_CONFIG_GLOBAL git status", "env -u GIT_CONFIG_COUNT -u GIT_CONFIG_KEY_0 git log",
    "env --unset=GIT_CONFIG_PARAMETERS git status", "env --unset GIT_ASKPASS ls", "env -u PATH ls", "unset GIT_ASKPASS",
    "unset -v GIT_CONFIG_GLOBAL", "unset PATH", "unset path", "env -i git status", "env --ignore-environment ls", "env - ls",
    "env -uGIT_ASKPASS ls", "env -u HOME ls", "env -C /x -u PATH ls", "env --chdir /x -u HOME ls",
    "env -S\"-u PATH\" ls", "env --split-string=-i ls", "env -iu FOO ls",
]
ALLOWED = ["env", "env | grep GIT", "env FOO=1 ls", "env -u FOO ls", "unset FOO", "unset -v myvar", "env -C /x ls", "env -C /x -u FOO ls"]


class EnvUnsetTests(unittest.TestCase):
    def run_hook(self, cmd, role):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": ROOT}
        return hook_run(json.dumps(payload), ["--role", role], env=env)

    def test_denied_plain_chained_and_in_shell(self):
        for role in NO_PUSH:
            for cmd in DENIED:
                for form in (cmd, "cd /x && " + cmd, "zsh -c '%s'" % cmd, "bash -c '%s'" % cmd):
                    with self.subTest(role=role, cmd=form):
                        p = self.run_hook(form, role)
                        self.assertEqual(p.returncode, 2, p.stderr)

    def test_allowed(self):
        for role in NO_PUSH:
            for cmd in ALLOWED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_other_roles_unchanged(self):
        for role in OTHER:
            for cmd in ("env -u GIT_ASKPASS ls", "env -i ls", "unset PATH"):
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, 0)

    def test_hq_unchanged(self):
        for role in ("boss", "director"):
            for cmd in ("env -u GIT_ASKPASS ls", "env -i ls", "unset PATH"):
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, 0)


if __name__ == "__main__":
    unittest.main()
