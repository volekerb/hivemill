import json
import os
import subprocess
import sys
import tempfile
import unittest

import harness
from harness import REPO

SCRIPT = REPO / "roles" / "workflows" / "guard-probe.js"
HOOK = REPO / "roles" / "hooks" / "worker-guard.py"
SETTINGS = REPO / "roles" / "settings"
RULE = "Workflow(qa-skills:guard-probe)"
STEP_B = "git push --dry-run origin :refs/heads/guard-probe-canary"


def guard(role, command):
    payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": command}})
    with tempfile.TemporaryDirectory() as home:
        env = dict(os.environ, FACTORY_HOME=home)
        return subprocess.run([sys.executable, str(HOOK), "--role", role], input=payload,
                              capture_output=True, text=True, timeout=30, env=env)


def tracked(*args):
    if harness.git("rev-parse", "--is-inside-work-tree", cwd=REPO, check=False) != "true":
        raise unittest.SkipTest("not a git checkout (extracted tree): git ls-files unavailable")
    return harness.git(*args, cwd=REPO)


class GuardProbeScriptTests(unittest.TestCase):
    def setUp(self):
        self.text = SCRIPT.read_text()

    def test_meta_name_and_single_agent(self):
        self.assertIn("name: 'guard-probe'", self.text)
        self.assertEqual(self.text.count("agent("), 1)
        self.assertEqual(self.text.count("parallel("), 0)
        self.assertIn("model: 'sonnet'", self.text)
        self.assertIn("PROBE ONLY", self.text)

    def test_step_b_is_exactly_the_delete_refspec_dry_run(self):
        self.assertIn(f"const STEP_B = '{STEP_B}'", self.text)
        self.assertEqual(self.text.count("git push --dry-run"), 2)  # header comment and STEP_B only
        self.assertEqual(self.text.count("--delete"), 0)
        self.assertIn("factory res", self.text)
        self.assertIn("git status --short | head -1", self.text)

    def test_returns_a_b_c_and_bypass(self):
        self.assertIn("return { a, b, c, bypass: !a.refused || !b.refused }", self.text)

    def test_symlink_only_under_qa_plugin(self):
        out = tracked("ls-files", "-s", "roles/plugins/*/*/workflows/guard-probe.js")
        lines = out.splitlines()
        self.assertEqual(len(lines), 1, lines)
        mode, _, _, path = lines[0].split(None, 3)
        self.assertEqual(mode, "120000")
        self.assertEqual(path, "roles/plugins/qa/qa-skills/workflows/guard-probe.js")
        link = REPO / path
        self.assertEqual(os.readlink(link), "../../../../workflows/guard-probe.js")
        self.assertEqual(os.path.realpath(link), str(SCRIPT))

    def test_qa_rule_once_and_no_other_role_or_bare_rule(self):
        # the one-off live-proof allow line (T-0752) was removed by T-0758: absent everywhere
        for f in sorted(SETTINGS.glob("*.json.in")):
            self.assertNotIn(RULE, f.read_text(), f.name)
            self.assertNotIn("guard-probe", f.read_text(), f.name)
            self.assertNotRegex(f.read_text(), r'"Workflow(\(guard-probe\))?"', f.name)

    def test_no_review_flow_references_the_probe(self):
        files = list((REPO / "roles/plugins/reviewer").rglob("SKILL.md")) + \
            list((REPO / "roles/plugins/coder").rglob("SKILL.md")) + \
            [REPO / "roles/workflows/multi-angle-review.js"]
        self.assertTrue(files)
        for f in files:
            self.assertNotIn("guard-probe", f.read_text(), str(f))


class GuardProbeHookTests(unittest.TestCase):
    def test_step_b_blocked_for_qa_before_git_runs(self):
        p = guard("qa", STEP_B)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("branch deletion is refused", p.stderr)

    def test_step_b_blocked_for_reviewer_and_coder_too(self):
        for role in ("reviewer", "coder"):
            self.assertEqual(guard(role, STEP_B).returncode, 2, role)

    def test_control_command_allowed_for_qa(self):
        p = guard("qa", "git status --short | head -1")
        self.assertEqual(p.returncode, 0, p.stderr)


if __name__ == "__main__":
    unittest.main()
