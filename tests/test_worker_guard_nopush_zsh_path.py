"""T-0788: NO_PUSH_ROLES may not assign the zsh array `path` (tied to PATH); reading it stays allowed."""
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NO_PUSH = ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator")
OTHER = ("coder", "cleaner", "judge")

DENIED = [
    "path=(/evil)", "path=(/evil $path)", "path+=/evil", "path+=(/evil)", "path[1]=/evil",
    "read -A path", "read -a path", "typeset -g path=(/evil)", "typeset path=(/evil)", "declare -g path=(/evil)",
    "f() { local path=(/evil); }", "export path", "readonly path=(/x)", "print -v path /evil", "zparseopts -a path x",
    ": ${path::=/evil}", ": ${path:=/evil}", ": ${path=/evil}", "for path in /evil; do :; done",
]
ALLOWED = [
    "echo $path", "print -l $path", "for p in $path; do echo $p; done", "mypath=/x", "path_x=1", "PATHX=1",
    "filepath=(a b)", "grep path file.txt", "git log -- path",
]


class ZshPathTests(unittest.TestCase):
    def run_hook(self, cmd, role):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": ROOT}
        return hook_run(json.dumps(payload), ["--role", role], env=env)

    def test_denied_plain_chained_and_in_shell(self):
        for role in NO_PUSH:
            for cmd in DENIED:
                q = cmd.replace("'", "'\\''")
                for form in (cmd, "cd /x && " + cmd, "zsh -c '%s'" % q, "bash -c '%s'" % q):
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
            for cmd in ("path=(/evil)", "path+=/evil", "read -A path", "for path in /evil; do :; done"):
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, 0)

    def test_hq_unchanged(self):
        for role in ("boss", "director"):
            with self.subTest(role=role):
                self.assertEqual(self.run_hook("path=(/evil)", role).returncode, 0)


if __name__ == "__main__":
    unittest.main()
