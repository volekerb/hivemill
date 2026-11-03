"""T-0826: NO_PUSH_ROLES may not use GNU env long-option prefixes (--uns, --ignore-env) to unset/clear GIT_*, nor zsh precommand
modifiers (noglob, nocorrect, repeat N, foreach, vared, integer) to push or to assign PATH/path/GIT_*."""
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NO_PUSH = ("qa", "reviewer")

DENIED = [
    "env --uns=GIT_DIR git status",
    "env --uns GIT_DIR git status",
    "env --unse=GIT_DIR git status",
    "env --ignore-env git status",
    "env --ignore-environment git status",
    "env --xyz git status",
    "noglob git push",
    "nocorrect git push",
    "repeat 1 git push",
    "noglob nocorrect git push",
    "noglob - git push",
    "nocorrect - git push",
    "repeat 1 - git push",
    "noglob - env -i git status",
    "foreach x (a) git push; end",
    "foreach path (a) ls; end",
    "vared PATH",
    "vared -p msg GIT_DIR",
    "integer path",
    "integer GIT_DIR=1",
    "repeat 1 env --uns GIT_DIR git status",
    "noglob env -i git status",
    "noglob - - git push",
    "- noglob git push",
    "- git push",
    "foreach x path (a) ls; end",
    "foreach x GIT_DIR (a) ls; end",
    "for x path in a; do ls; done",
    "for x path (a) ls; done",
    "repeat PATH=1 ls",
    "repeat GIT_DIR=1 git status",
]
ALLOWED = [
    "env FOO=1 ls",
    "env --uns FOO ls",
    "env --null ls",
    "env -- ls",
    "noglob ls *",
    "nocorrect ls",
    "repeat 2 echo hi",
    "git status",
    "noglob git status",
    "noglob - git status",
    "integer x",
    "vared x",
    "foreach x (a b) echo $x; end",
    "foreach x y (a b) echo $x; end",
    "for x y in a b; do ls; done",
    "repeat 1 ls",
    "- ls",
]


class EnvPrefixZshModifierTests(unittest.TestCase):
    def run_hook(self, cmd, role):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": ROOT}
        return hook_run(json.dumps(payload), ["--role", role], env=env)

    def test_denied(self):
        for role in NO_PUSH:
            for cmd in DENIED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 2, p.stderr)

    def test_allowed(self):
        for role in NO_PUSH:
            for cmd in ALLOWED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_scrub_prefix_refused_for_every_role(self):
        for role in ("coder", "qa"):
            for cmd in ("env --uns FACTORY_ROLE ls", "env --uns=HERDR_PANE_ID ls"):
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, 2)


if __name__ == "__main__":
    unittest.main()
