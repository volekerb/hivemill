"""T-0882: NO_PUSH_ROLES may not feed an interpreter (python/perl/node/ruby) program on stdin (heredoc, here-string,
echo/printf piped in) that launches `git push` or a gh write, or (python) extracts an archive. Same scan as T-0789.
Out of scope by design: a piped non-literal source (`cat file | python3`). Defense in depth only; sandbox + PAT are the boundary."""
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)
from guard_impl import hook_run  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NO_PUSH = ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator", "researcher")
OTHER = ("coder", "cleaner", "judge")
HQ = ("boss", "director")

PY = 'import subprocess; subprocess.run(["git","push"])'
PERL = 'system("git push");'
NODE = 'require("child_process").execSync("gh pr merge 1")'
RUBY = 'system("git push")'
EXTRACT = 'import tarfile; tarfile.open("a.tar").extractall("x")'


def heredoc(interp, body):
    return "%s <<'EOF'\n%s\nEOF" % (interp, body)


DENIED = [
    heredoc("python3 -", PY),
    heredoc("python3", PY),
    "python3 <<< '%s'" % PY,
    "echo '%s' | python3" % PY,
    "printf '%s' '%s' | python3 -" % ("%s\\n", PY),
    "cat <<'EOF' | python3\n%s\nEOF" % PY,
    heredoc("perl -", PERL),
    heredoc("node", NODE),
    heredoc("ruby", RUBY),
    "perl <<< '%s'" % PERL,
    "echo '%s' | node" % NODE,
    "echo '%s' | ruby" % RUBY,
    heredoc("python3 -", PY.replace("git", "gh").replace('"push"', '"pr","merge"')),
    heredoc("python3 -", EXTRACT),  # folded T-0808 F4: stdin extraction
    "python3 <<< '%s'" % EXTRACT,
    heredoc("env python3 -", PY),
    heredoc("python3 - >/dev/null", PY),
    heredoc("python3 -W ignore -", PY),
]
# each of the above also nested in bash -c and after cd /x &&
DENIED += ["cd /x && " + c for c in DENIED[:10]]
def dq(c):
    return '"' + c.replace("\\", "\\\\").replace('"', '\\"') + '"'  # bash double quotes, newlines kept


DENIED += ["bash -c %s" % dq(c) for c in (
    "python3 <<< '%s'" % PY, "echo '%s' | python3" % PY, heredoc("python3 -", PY), heredoc("ruby", RUBY))]

ALLOWED = [
    heredoc("python3 -", "print(1)"),
    "echo 'print(1)' | python3",
    "python3 script.py <<< input",
    "python3 script.py <<< 'git push'",  # a script file: stdin is data
    "cat file | python3",  # out of scope: non-literal source
    "python3 <<< 'print(1)'",
    heredoc("perl -", "print 1"),
    heredoc("node", "console.log(1)"),
    heredoc("cat", "git push"),  # not an interpreter
    "echo 'git push' | cat",
    "echo 'git push'",
    "python3 -c 'print(1)' <<< 'git push'",  # -c code: stdin is data
    heredoc("python3 -", "import tarfile; tarfile.open('a.tar').getnames()"),
]


class InterpStdinTests(unittest.TestCase):
    def run_hook(self, cmd, role):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": ROOT}
        return hook_run(json.dumps(payload), ["--role", role], env=env)

    def test_denied_for_every_no_push_role(self):
        for role in NO_PUSH:
            for cmd in DENIED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 2, p.stderr)

    def test_denied_reason_names_the_rule(self):
        self.assertIn("git push", self.run_hook(heredoc("python3 -", PY), "qa").stderr)
        self.assertIn("gh write", self.run_hook(heredoc("node", NODE), "qa").stderr)
        self.assertIn("extracts an archive", self.run_hook(heredoc("python3 -", EXTRACT), "qa").stderr)

    def test_allowed_for_no_push_roles(self):
        for role in NO_PUSH:
            for cmd in ALLOWED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_other_roles_unchanged(self):
        for role in OTHER:
            for cmd in (heredoc("perl -", PERL.replace("git push", "gh pr view 1")), "echo 'print(1)' | python3",
                        "python3 <<< 'print(1)'"):
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, 0)

    def test_hq_unchanged(self):
        for role in HQ:
            for cmd in (heredoc("python3 -", PY), "python3 <<< '%s'" % PY, "echo '%s' | python3" % PY, heredoc("node", NODE)):
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, 0)


if __name__ == "__main__":
    unittest.main()
