"""T-0789: NO_PUSH_ROLES may not run interpreter inline code (python -c, perl -e, node -e, ruby -e) that launches
`git push` or a gh write. Defense in depth only (string obfuscation beats it); the boundary is the G-010 sandbox plus
the worker PAT. Other inline code and script files stay allowed."""
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
HQ = ("boss", "director")

PY_PUSH = 'import subprocess; subprocess.run(["git","push"])'
PY_SYS = 'import os; os.system("git push origin main")'
PY_API_POST = 'import os; os.system("gh api -X POST repos/x/y/merges")'

DENIED = [
    "python3 -c '%s'" % PY_PUSH,
    "python3 -c '%s'" % PY_SYS,
    "python -c '%s'" % PY_PUSH,
    "python3.11 -c '%s'" % PY_PUSH,
    "python3 -Ic '%s'" % PY_PUSH,
    "python3 -I -c '%s'" % PY_PUSH,
    "python3 -W ignore -c '%s'" % PY_PUSH,
    "perl -e 'system(\"git\",\"push\")'",
    "perl -E 'system(\"git push\")'",
    "node -e 'require(\"child_process\").execSync(\"git push\")'",
    "node --eval 'require(\"child_process\").execSync(\"git push\")'",
    "node -p 'require(\"child_process\").execSync(\"git push\")'",
    "nodejs -e 'require(\"child_process\").execSync(\"git push\")'",
    "ruby -e 'system(\"git push\")'",
    "python3 -c 'import os; os.system(\"gh pr merge 1\")'",
    "python3 -c 'import os; os.system(\"gh pr create --fill\")'",
    "python3 -c '%s'" % PY_API_POST,
    "python3 -c 'import os; os.system(\"gh api --method=DELETE repos/x/y\")'",
    "python3 -c 'import subprocess; subprocess.run([\"gh\",\"api\",\"-X\",\"PUT\",\"repos/x/y\"])'",
    "python3 -c 'import os; os.system(\"gh repo delete x\")'",
    "python3 -c 'import os; os.system(\"gh repo view x\")'",  # any gh repo subcommand is refused, by spec
    "bash -c \"python3 -c 'import os; os.system(\\\"git push\\\")'\"",
    "sh -c \"perl -e 'system(q(git push))'\"",
    "cd /tmp && python3 -c '%s'" % PY_PUSH,
    "env python3 -c '%s'" % PY_PUSH,
    "perl -e'system(q(git push))'",  # attached code
    "ruby -e\"system(q(git push))\"",
    "node --eval='require(\"child_process\").execSync(\"git push\")'",
    "node --print='require(\"child_process\").execSync(\"git push\")'",
    "python3 -c'import os; os.system(\"git push\")'",
    "python3 -c 'import os; os.system(\"gh -R a/b pr merge 1\")'",  # gh global flag before the subcommand
    "python3 -c 'import os; os.system(\"gh --repo a/b pr create --fill\")'",
    "python3 -c 'import os; os.system(\"gh api x -X GET; gh api y -X POST\")'",
    "python3 -c \"print('git push')\"",  # accepted false positive
]
ALLOWED = [
    'python3 -c "import json"',
    "python3 -c 'import subprocess; subprocess.run([\"git\",\"log\",\"-1\"])'",
    "python3 -c \"print('push')\"",
    "python3 -c \"print('git status')\"",
    "node -e 'console.log(1)'",
    "perl -e 'print 1'",
    "ruby -e 'puts 1'",
    "python3 -c 'import os; os.system(\"gh pr view 1\")'",
    "python3 -c 'import os; os.system(\"gh api repos/x/y/pulls\")'",
    "python3 -c 'import os; os.system(\"gh api -X GET repos/x/y/pulls\")'",
    "python3 -c 'print(\"-X POST\")'",
    "perl -e'print 1'",
    "python3 -c 'import os; os.system(\"gh -R a/b pr view 1\")'",
    "python3 script.py",  # no inline code: out of scope
    "python3 script.py --arg 'git push'",
    "bash -c \"python3 -c 'import json'\"",
]


class InterpInlinePushTests(unittest.TestCase):
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
        p = self.run_hook("python3 -c '%s'" % PY_PUSH, "qa")
        self.assertIn("git push", p.stderr)
        p = self.run_hook("python3 -c '%s'" % PY_API_POST, "qa")
        self.assertIn("gh write", p.stderr)

    def test_allowed_for_no_push_roles(self):
        for role in NO_PUSH:
            for cmd in ALLOWED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_other_roles_unchanged(self):
        for role in OTHER:
            for cmd in ("python3 -c '%s'" % PY_SYS, "perl -e 'system(\"gh pr view 1\")'", "python3 -c 'import json'"):
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, 0)

    def test_hq_unchanged(self):
        for role in HQ:
            for cmd in ("python3 -c '%s'" % PY_PUSH, "perl -e 'system(\"git push\")'", "ruby -e 'system(\"git push\")'", "node -e 'require(\"child_process\").execSync(\"git push\")'",
                        "python3 -c '%s'" % PY_API_POST):
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, 0)


if __name__ == "__main__":
    unittest.main()
