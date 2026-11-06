import json
import os
import shlex
import tempfile
import unittest

from guard_impl import hook_run

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

# T-0995: NO_PUSH roles may not run gh issue, workflow or pr writes; reviewer and qa keep `gh pr comment`.
NP6 = ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator")
COMMENTERS = ("qa", "reviewer")
NON_COMMENTERS = ("pm", "staff-engineer", "scout", "evaluator")

W = (
    "gh issue create", "gh issue edit 1", "gh issue close 1", "gh issue reopen 1", "gh issue comment 1 --body x",
    "gh issue delete 1", "gh issue transfer 1 a/b", "gh issue pin 1", "gh issue unpin 1", "gh issue lock 1",
    "gh issue unlock 1", "gh issue develop 1",
    "gh workflow run x", "gh workflow enable x", "gh workflow disable x",
    "gh pr create", "gh pr edit 1", "gh pr close 1", "gh pr reopen 1", "gh pr ready 1", "gh pr review 1",
    "gh pr merge 1", "gh pr lock 1", "gh pr unlock 1", "gh pr update-branch 1", "gh pr revert 1",
)
COMMENT = ("gh pr comment 1 --body x", "gh pr comment 1 --body-file -")
READS = (
    "gh issue list", "gh issue list --search close", "gh issue view 1", "gh issue status", "gh workflow list",
    "gh workflow view x", "gh pr list", "gh pr view 1", "gh pr view 1 --json title --jq .title", "gh pr diff 1",
    "gh pr checks 1", "gh pr status", "gh api repos/o/r/pulls/1",
)
XARGS = ("echo 1 | xargs gh issue close", "echo 1 | xargs gh pr edit")

# Exit codes of the origin/main hook (1e2dc356b), measured through hook_run on a tree copy, plain forms.
ORIGIN_WRITE = {"coder": 0, "judge": 0, "cleaner": 0, "hq": 0, "researcher": 2}
ORIGIN_READ = {"coder": 0, "judge": 0, "cleaner": 0, "hq": 0}
# researcher reads on origin/main: 2 for the workflow group (not in its read list), 0 for the rest
RESEARCHER_READ_REFUSED = ("gh workflow list", "gh workflow view x")


def wrappers(cmd):
    inner = cmd.replace('"', '\\"')
    return (cmd, cmd.replace("gh ", "gh -R a/b ", 1), cmd.replace("gh ", "gh --repo=a/b ", 1),
            f"bash -c {shlex.quote(cmd)}", f'python3 -c \'import os; os.system("{inner}")\'')


class NoPushGhWriteTests(unittest.TestCase):
    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = os.path.realpath(td.name)
        self.home = os.path.join(self.tmp, "factory")
        os.makedirs(os.path.join(self.home, "roles"))

    def code(self, cmd, role, hq=False):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home, FACTORY_AGENT=f"{role}-x")
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.tmp})
        return hook_run(payload, ["--role", role] + (["--hq"] if hq else []), env=env).returncode

    def test_issue_workflow_pr_writes_refused_in_every_wrapper(self):
        for c in W:
            for w in wrappers(c):
                for role in NP6:
                    self.assertEqual(self.code(w, role), 2, f"{role}: {w}")

    def test_xargs_forms_refused(self):
        for c in XARGS:
            for role in NP6:
                self.assertEqual(self.code(c, role), 2, f"{role}: {c}")

    def test_pr_comment_allowed_for_reviewer_and_qa(self):
        for c in COMMENT + ("gh pr comment 1 --body 'use gh issue create here'",):
            for w in (c, c.replace("gh ", "gh -R a/b ", 1)):
                for role in COMMENTERS:
                    self.assertEqual(self.code(w, role), 0, f"{role}: {w}")

    def test_pr_comment_refused_for_other_no_push_roles(self):
        for c in COMMENT:
            for w in (c, c.replace("gh ", "gh -R a/b ", 1), f"bash -c {shlex.quote(c)}"):
                for role in NON_COMMENTERS:
                    self.assertEqual(self.code(w, role), 2, f"{role}: {w}")

    def test_pr_comment_not_in_inline_scan(self):
        """`gh pr comment` inside inline code stays allowed for reviewer and qa (a Declared Limit for the others)."""
        c = 'python3 -c \'import os; os.system("gh pr comment 1 --body x")\''
        for role in COMMENTERS:
            self.assertEqual(self.code(c, role), 0, role)

    def test_reads_allowed(self):
        for c in READS:
            for role in NP6:
                self.assertEqual(self.code(c, role), 0, f"{role}: {c}")

    def test_other_roles_unchanged(self):
        for c in W + COMMENT:
            for role in ("coder", "judge", "cleaner", "researcher"):
                self.assertEqual(self.code(c, role), ORIGIN_WRITE[role], f"{role}: {c}")
            self.assertEqual(self.code(c, "coder", hq=True), ORIGIN_WRITE["hq"], f"hq: {c}")
        for c in READS:
            for role in ("coder", "judge", "cleaner"):
                self.assertEqual(self.code(c, role), ORIGIN_READ[role], f"{role}: {c}")
            self.assertEqual(self.code(c, "researcher"), 2 if c in RESEARCHER_READ_REFUSED else 0, f"researcher: {c}")
            self.assertEqual(self.code(c, "coder", hq=True), ORIGIN_READ["hq"], f"hq: {c}")
