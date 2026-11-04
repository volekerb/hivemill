"""T-1037: env --split-string prefixes and -S quoting/escapes (GNU env) must reach check_scrub and the no-push env parser."""
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

DENY = [
    ("coder", "env --split-s '-u FACTORY_ROLE' ls"), ("coder", "env --split='-u FACTORY_ROLE' ls"),
    ("coder", "env --sp='-u FACTORY_ROLE' ls"), ("coder", "env -S '\"-u\" FACTORY_ROLE' ls"),
    ("coder", "env -S'-u\\_FACTORY_ROLE' ls"), ("coder", "env -S\"-u 'FACTORY_ROLE'\" ls"),
    ("qa", "env -S '\"-u\" GIT_DIR' git status"), ("qa", "env -S'-u\\_GIT_DIR' git status"),
    ("qa", "env --split-s '-u GIT_DIR' git status"), ("coder", "env -S '\"-i\"' factory task show T-1"),
]
ALLOW = [
    ("qa", "git status"), ("coder", "env -S 'ls -l'"), ("qa", "env -S 'git status'"),
    ("coder", "env -S '\"ls\" -l'"),
]


def run_hook(cmd, role):
    env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
    payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": ROOT}
    return hook_run(json.dumps(payload), ["--role", role], env=env)


class EnvSplitStringTests(unittest.TestCase):
    def test_deny_forms(self):
        for role, cmd in DENY:
            with self.subTest(role=role, cmd=cmd):
                p = run_hook(cmd, role)
                self.assertEqual(p.returncode, 2, p.stderr)

    def test_allow_forms(self):
        for role, cmd in ALLOW:
            with self.subTest(role=role, cmd=cmd):
                p = run_hook(cmd, role)
                self.assertEqual(p.returncode, 0, p.stderr)


if __name__ == "__main__":
    unittest.main()
