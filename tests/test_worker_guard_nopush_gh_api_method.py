"""T-0987: NO_PUSH_ROLES may not run `gh api` with a non-GET method (-X/--method POST|PUT|PATCH|DELETE) even without a
body flag. GET stays allowed; coder, cleaner and judge are unchanged. Defense in depth only; the boundary is the G-010
sandbox plus the worker PAT."""
import json
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)
from guard_impl import hook_run  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NO_PUSH = ("qa", "reviewer", "pm")
OTHER = ("coder", "judge")

DENIED = [
    "gh api -X DELETE repos/acme/x/issues/1",
    "gh api -X PUT repos/acme/x/issues/1/lock",
    "gh api -X POST repos/acme/x/issues/1/comments",
    "gh api -X PATCH repos/acme/x/issues/1",
    "gh api --method DELETE repos/acme/x/issues/1",
    "gh api --method PUT repos/acme/x/issues/1/lock",
    "gh api --method POST repos/acme/x/issues/1/comments",
    "gh api --method=DELETE repos/acme/x/issues/1",
    "gh api -XDELETE repos/acme/x/issues/1",
    "gh api repos/acme/x/issues/1 -X delete",
    "gh -R acme/x api -X POST repos/acme/x/issues",
]
ALLOWED = [
    "gh api repos/acme/x",
    "gh api -X GET repos/acme/x",
    "gh api --method GET repos/acme/x",
    "gh api --method=get repos/acme/x",
    "gh api -XGET repos/acme/x -f per_page=5",
]


class NoPushGhApiMethodTests(unittest.TestCase):
    def run_hook(self, cmd, role):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": ROOT}
        return hook_run(json.dumps(payload), ["--role", role], env=env)

    def test_non_get_method_without_body_denied(self):
        for role in NO_PUSH:
            for cmd in DENIED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 2, p.stderr)
                    self.assertIn("gh api -X/--method", p.stderr)

    def test_get_stays_allowed(self):
        for role in NO_PUSH:
            for cmd in ALLOWED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_coder_and_judge_unchanged(self):
        for role in OTHER:
            for cmd in DENIED[:3] + ALLOWED[:2]:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertNotIn("no-push roles never write", p.stderr)


if __name__ == "__main__":
    unittest.main()
