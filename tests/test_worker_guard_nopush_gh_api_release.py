"""T-0881: NO_PUSH_ROLES may not run `gh api` with a body flag (-f -F --field --raw-field --input) unless the method
is GET, nor `gh release create|edit|delete|upload|delete-asset`, directly or in inline interpreter code / bash -c.
Defense in depth only; the boundary is the G-010 sandbox plus the worker PAT."""
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)
from guard_impl import hook_run  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NO_PUSH = ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator")
OTHER = ("coder", "cleaner", "judge")
HQ = ("boss", "director")

DENIED = [
    "gh api repos/o/r/issues -f title=x",
    "gh api repos/o/r/pulls/1/merge -F merge_method=squash",
    "gh api x --raw-field a=b",
    "gh api x --field a=b",
    "gh api x --input body.json",
    "gh release create v1",
    "gh release edit v1",
    "gh release delete v1",
    "gh release upload v1 f",
    "gh release delete-asset v1 f",
    "gh release create -R acme/new v1 --notes x",
    "python3 -c 'import subprocess; subprocess.run([\"gh\",\"release\",\"create\",\"v1\"])'",
    "python3 -c 'import os; os.system(\"gh api repos/o/r/issues -f title=x\")'",
    "python3 -c 'import os; os.system(\"gh api x --input body.json\")'",
    "bash -c 'gh release create v1'",
    "bash -c 'gh api x -F a=b'",
    "python3 -c 'import os; os.system(\"gh api search -X GET; gh api repos/o/r/issues -f title=x\")'",  # GET on another call
]
ALLOWED = [
    "gh api repos/o/r/pulls/1",
    "gh api -X GET search/issues -f q=x",
    "gh api --method GET x -F per_page=5",
    "gh release list",
    "gh release view v1",
    "gh pr view 1",
    "python3 -c 'import os; os.system(\"gh release list\")'",
    "python3 -c 'import os; os.system(\"gh api -X GET search/issues -f q=x\")'",
    "bash -c 'gh release view v1'",
    "gh api --method=GET x -F a=b",
    "gh api -XGET x -f a=b",
    "python3 -c 'import os; os.system(\"gh api x --method GET -F a=b\")'",
]
CODER_CMDS = ["gh api repos/o/r/issues -f title=x", "gh release create v1",
              "gh release create -R acme/new v1 --notes x", "bash -c 'gh release create v1'",
              "python3 -c 'import os; os.system(\"gh release create v1\")'"]


class NoPushGhApiReleaseTests(unittest.TestCase):
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
                    self.assertIn("worker-guard", p.stderr)

    def test_reason_names_the_rule(self):
        self.assertIn("gh api with -f", self.run_hook(DENIED[0], "qa").stderr)
        self.assertIn("gh release create", self.run_hook("gh release create v1", "qa").stderr)

    def test_reads_stay_allowed(self):
        for role in NO_PUSH:
            for cmd in ALLOWED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_coder_cleaner_judge_unchanged(self):
        for role in OTHER:
            for cmd in CODER_CMDS:
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, 0)

    def test_hq_unchanged(self):
        for role in HQ:
            for cmd in CODER_CMDS:
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, 0)


if __name__ == "__main__":
    unittest.main()
