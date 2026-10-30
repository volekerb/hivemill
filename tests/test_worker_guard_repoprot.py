"""R-a (T-0638): no agent changes branch protection or repository settings through gh. Run through the hook as invoked."""
import json
import os
import subprocess
import sys
import tempfile
import unittest
from guard_impl import hook_run

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
WORKER_ROLES = ("coder", "qa", "reviewer", "judge", "cleaner", "pm", "scout", "evaluator", "staff-engineer")
HQ_ROLES = ("boss", "director", "lead")
P = "repos/acme/hive/branches/main/protection"

BLOCKED = [
    f"gh api -X PUT {P} -F required_pull_request_reviews=null",
    f"gh api --method PATCH {P}/enforce_admins",
    f"gh api --method=DELETE {P}",
    f"gh api -XPOST {P}/required_signatures",
    f"gh api {P} -X delete",
    f"gh api -H 'Accept: application/vnd.github+json' -X PUT {P}",
    "gh api -X PUT repos/acme/hive/branches/release/x/protection",
    f"gh api {P} -f enforce_admins=false",  # a body without -X makes gh use POST
    "gh api -X POST repos/acme/hive/rulesets --input r.json",
    f"gh api -p luke-cage -X PUT {P}",
    f"gh api --preview luke-cage --method PUT {P}",
    "gh api graphql -f query='mutation{updateBranchProtectionRule(input:{}){clientMutationId}}'",
    "gh api graphql -f query='mutation{updateRepository(input:{}){clientMutationId}}'",
    "gh api graphql -F query=@q.graphql",
    "gh alias set x 'api -X PUT repos/a/b/branches/main/protection'",
    f"gh api -X PUT '{P}#x'",
    f"gh api --field=a=1 {P}",
    f"gh api -X PUT '{P}?x=1'",
    "gh api -X PUT repos/acme/hive/rules/branches/main",
    "gh repo edit acme/hive --default-branch x",
    "gh repo edit --visibility private",
    "gh -R acme/hive repo edit --enable-issues=false",
    "gh repo archive acme/hive --yes",
    "gh api -X PATCH repos/acme/hive -f private=true",
    "gh api --method PATCH /repos/acme/hive",
    "gh api -X DELETE repos/acme/hive",
    "env GH_TOKEN=x gh repo edit acme/hive",
    "sh -c 'gh api -X PUT repos/acme/hive/branches/main/protection'",
    f"gh api -X=DELETE {P}",
    f"gh api --method=PATCH {P}/enforce_admins",
    f"gh api -iX DELETE {P}",
    f"gh api -iXDELETE {P}",
    f"gh api -i -X=PUT {P}",
    "gh api -X DELETE repos/acme/hive/branches/main/%70rotection",
    "gh api -X PUT repos/acme/hive/branches/main/%50ROTECTION",
    "gh api -X PATCH repos/acme/hive%2F",
]
ALLOWED = [
    f"gh api {P}",
    f"gh api -X GET {P}",
    f"gh api --method=GET {P}/enforce_admins",
    f"gh api -X=GET {P}",
    f"gh api -iX GET {P}",
    f"gh api -iXGET {P}",
    "gh api repos/acme/hive/branches/main/%70rotection",
    "gh api repos/acme/hive",
    "gh api repos/acme/hive --jq .private",
    "gh repo view acme/hive --json name",
    "gh api -X POST repos/acme/hive/issues/3/comments -f body=hello",
    "gh api -X PATCH repos/acme/hive/pulls/3 -f title=x",
    "gh api -X PUT repos/acme/hive/topics",
    "gh api graphql -f query='query{viewer{login}}'",
    "gh alias list",
    "gh pr view 3",
    "echo gh repo edit",
    "grep -n 'gh api -X PUT repos/a/b/branches/main/protection' notes.txt",
]


class RepoProtTests(unittest.TestCase):
    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = td.name

    def run_hook(self, cmd, role):
        args = ["--hq"] if role in HQ_ROLES else ["--role", role]
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        env["FACTORY_HOME"] = self.tmp  # no DB there: the public-text rule stays off, so only R-a decides
        return hook_run(json.dumps(
            {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": ROOT}), [*args], env=env)

    def test_every_role_is_refused_each_changing_shape(self):
        for role in WORKER_ROLES + HQ_ROLES:
            for cmd in BLOCKED:
                p = self.run_hook(cmd, role)
                self.assertEqual(p.returncode, 2, f"should block ({role}): {cmd!r}\n{p.stderr}")
                self.assertIn("branch protection and repository settings", p.stderr, cmd)

    def test_reads_and_unrelated_writes_stay_allowed(self):
        for role in ("coder", "judge", "director"):
            for cmd in ALLOWED:
                p = self.run_hook(cmd, role)
                self.assertEqual(p.returncode, 0, f"should allow ({role}): {cmd!r}\n{p.stderr}")


if __name__ == "__main__":
    unittest.main()
