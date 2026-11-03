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
    "env --argv0 foo --uns GIT_DIR git status",
    "env -a foo -u GIT_DIR git status",
    "env --argv0 foo --ignore-env git status",
    "env --argv0 foo -i git status",
    "env --argv0 foo --uns PATH ls",
    "env --argv0 foo --xyz ls",
    "env --argv0 foo --unset FACTORY_ROLE git status",
    "env --argv0=foo -u GIT_DIR git status",
    "env --arg foo -u GIT_DIR git status",
    "env -afoo -u GIT_DIR git status",
    "env -C /tmp -u GIT_DIR git status",
    "env --chdir /tmp --uns GIT_DIR git status",
    "env --chdir=/tmp -i git status",
    "env --ch /tmp -i git status",
    "env -C/tmp -i git status",
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
    "env -a foo ls",
    "env -C /tmp ls",
    "env --argv0 foo ls",
    "env --chdir=/tmp ls",
]
SCRUB = [
    "env -a x -u FACTORY_ROLE ls",
    "env --argv0 x -u FACTORY_ROLE ls",
    "env --chdir /tmp --unset FACTORY_ROLE ls",
    "env -C /tmp -u FACTORY_ROLE ls",
    "env -C /tmp --uns FACTORY_ROLE ls",
    "env -afoo -u HERDR_PANE_ID ls",
    "env -C/tmp -u FACTORY_ROLE ls",
    "env --ch /tmp -u FACTORY_ROLE ls",
    "env --arg x --unset=FACTORY_ROLE ls",
]
# T-0826 attempt 4: (role, command) pairs that exit 2 on origin/main and must still exit 2 on head
REGRESSION = [
    ("coder", "env -aC -u FACTORY_ROLE ls"), ("lead", "env -aC -u FACTORY_ROLE ls"),
    ("coder", "env -Ca -u FACTORY_ROLE ls"), ("coder", "env -Cdata -u FACTORY_ROLE ls"),
    ("reviewer", "env -Cdata -u FACTORY_ROLE ls"), ("coder", "env -afooa -u HERDR_PANE_ID ls"),
    ("qa", "env -aC -u FACTORY_ROLE git status"),
    ("coder", "env -ia foo factory task show T-1"), ("coder", "env -iC /tmp factory task show T-1"),
    ("qa", "env -ai git status"), ("qa", "env -Ci git status"), ("qa", "env -a -i git status"),
    ("qa", "env -a -u GIT_DIR git status"), ("qa", "env -Cu GIT_DIR git status"),
]
SAME_PARSER = [
    "env -u FOO -u FACTORY_ROLE ls",
    "env --unset FOO -u FACTORY_ROLE ls",
    "env -vuFACTORY_ROLE ls",
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

    def test_scrub_after_value_option_refused_for_coder(self):
        for cmd in SCRUB:
            with self.subTest(cmd=cmd):
                self.assertEqual(self.run_hook(cmd, "coder").returncode, 2)

    def test_value_option_then_command_allowed_for_coder(self):
        for cmd in ("env -a foo ls", "env -C /tmp ls"):
            with self.subTest(cmd=cmd):
                self.assertEqual(self.run_hook(cmd, "coder").returncode, 0)

    def test_regression_forms_refused_as_on_main(self):
        for role, cmd in REGRESSION:
            with self.subTest(role=role, cmd=cmd):
                self.assertEqual(self.run_hook(cmd, role).returncode, 2)

    def test_scrub_after_other_unset_refused_for_coder(self):
        for cmd in SAME_PARSER:
            with self.subTest(cmd=cmd):
                self.assertEqual(self.run_hook(cmd, "coder").returncode, 2)

    def test_unset_of_other_var_then_command_allowed_for_coder(self):
        for cmd in ("env -u FOO ls", "env -u FOO -u BAR ls", "env -a x -u FOO ls"):
            with self.subTest(cmd=cmd):
                self.assertEqual(self.run_hook(cmd, "coder").returncode, 0)


if __name__ == "__main__":
    unittest.main()
