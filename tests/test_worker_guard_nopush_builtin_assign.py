"""T-0771: NO_PUSH_ROLES may not assign HOME, XDG_CONFIG_HOME, PATH, PAGER or GIT_* through read, printf -v, mapfile/readarray,
for/select, getopts, let, declare-like builtins or ${VAR:=x} defaults, nor through a dynamic target name."""
import json
import os
import subprocess
import sys
import unittest

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
NO_PUSH = ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator")
OTHER = ("coder", "cleaner", "judge")

DENIED = [
    "read HOME <<< d",
    "printf -v HOME d",
    "mapfile -t HOME < f",
    "readarray HOME < f",
    "for HOME in d; do :; done",
    "getopts HOME x",
    "getopts ab: GIT_DIR",
    "read XDG_CONFIG_HOME <<< d",
    "read -r -a PATH <<< d",
    "printf -v GIT_DIR x",
    "printf -vPAGER x",
    ": ${HOME:=d}",
    ": ${XDG_CONFIG_HOME=d}",
    "local HOME=d",
    "let HOME=1",
    "select PAGER in a; do break; done",
    'printf -v "$v" d',
    'read "$v" <<< d',
    'local "$v"=d',
    ": ${!v:=d}",
    # attached option argument
    "read -aHOME <<< d",
    "read -raHOME <<< d",
    "read -eaHOME <<< d",
    # array subscript
    "printf -v HOME[0] d",
    "read HOME[0] <<< d",
    "local HOME[0]=d",
    # glob in the target
    "read HOM[E] <<< d",
    "read HOM? <<< d",
    "read HOM* <<< d",
    # brace expansion
    "read {HOME,x} <<< d",
    "printf -v {HOME,x} d",
    # nameref
    "declare -n r=HOME",
    "local -n r=HOME",
    "typeset -n r=HOME",
    'declare -n r="$v"',
]
ALLOWED = [
    "read x <<< y",
    "read -r -p prompt x <<< y",
    "printf -v x y",
    "mapfile -t arr < f",
    "for f in a b; do git log -1; done",
    "getopts ab: opt",
    ": ${x:=d}",
    "let n=1+2",
    "local y=1",
    "read -a arr <<< y",
    "read -ra arr <<< y",
    "declare -n r=x",
    "read arr[0] <<< y",
]


class BuiltinAssignTests(unittest.TestCase):
    def run_hook(self, cmd, role):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": ROOT}
        return subprocess.run([sys.executable, HOOK, "--role", role], input=json.dumps(payload), capture_output=True,
                              text=True, timeout=30, env=env)

    def test_denied_for_every_no_push_role(self):
        for role in NO_PUSH:
            for cmd in DENIED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd + "; git log -1", role)  # an allowed git tail: only the new check can refuse
                    self.assertEqual(p.returncode, 2, p.stderr)
                    self.assertIn("XDG_CONFIG_HOME, PATH, PAGER or GIT_", p.stderr)

    def test_allowed_names_outside_protected_set(self):
        for role in NO_PUSH:
            for cmd in ALLOWED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_other_roles_unchanged(self):
        for role in OTHER:
            for cmd in ("read HOME <<< d", "printf -v HOME d", "for HOME in d; do :; done", ": ${HOME:=d}"):
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)


if __name__ == "__main__":
    unittest.main()
