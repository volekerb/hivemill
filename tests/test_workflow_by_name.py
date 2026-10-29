import json
import os
import re
import subprocess
import unittest

from harness import REPO

SETTINGS = REPO / "roles" / "settings"
RULE = "Workflow(multi-angle-review)"
ROLES = ("reviewer", "qa", "coder")
LINKS = {
    "reviewer": REPO / "roles/plugins/reviewer/reviewer-skills/workflows/multi-angle-review.js",
    "qa": REPO / "roles/plugins/qa/qa-skills/workflows/multi-angle-review.js",
    "coder": REPO / "roles/plugins/coder/coder-skills/workflows/multi-angle-review.js",
}
SCRIPT = REPO / "roles" / "workflows" / "multi-angle-review.js"


class WorkflowAllowRuleTests(unittest.TestCase):
    def allow(self, role):
        return json.loads((SETTINGS / f"{role}.json.in").read_text())["permissions"]["allow"]

    def test_exact_rule_for_three_roles_only(self):
        for role in ROLES:
            self.assertEqual(self.allow(role).count(RULE), 1, role)
        for f in sorted(SETTINGS.glob("*.json.in")):
            role = f.name.split(".")[0]
            if role not in ROLES:
                self.assertNotIn(RULE, f.read_text(), role)

    def test_no_bare_or_wildcard_workflow_rule(self):
        for f in sorted(SETTINGS.glob("*.json.in")):
            rules = json.loads(f.read_text())["permissions"].get("allow", [])
            for r in rules:
                if r.startswith("Workflow"):
                    self.assertEqual((f.name, r), (f.name, RULE))
            self.assertNotRegex(f.read_text(), r'"Workflow"')


class WorkflowByNameTests(unittest.TestCase):
    def test_one_script_and_links_resolve_to_it(self):
        listed = subprocess.run(["git", "ls-files"], cwd=REPO, capture_output=True, text=True, check=True).stdout
        paths = [l for l in listed.splitlines() if l.endswith("multi-angle-review.js")]
        self.assertEqual(len(paths), 4, paths)  # the script + 3 plugin symlinks
        for role, link in LINKS.items():
            self.assertTrue(link.is_symlink(), role)
            self.assertEqual(os.path.realpath(link), str(SCRIPT), role)

    def test_plugin_dirs_come_from_the_main_checkout(self):
        src = (REPO / "bin" / "factory").read_text()
        self.assertIn('pdir = ROLES_DIR / "plugins" / role', src)
        self.assertRegex(src, r"(?m)^ROLES_DIR = HOME / \"roles\"")
        self.assertIn('claude_args += ["--plugin-dir", plugin]', src)

    def test_callers_use_the_name_not_a_path(self):
        files = [
            "roles/plugins/reviewer/reviewer-skills/skills/multi-angle-review/SKILL.md",
            "roles/plugins/coder/coder-skills/skills/self-review/SKILL.md",
            "docs/review-workflow.md", "docs/self-review.md",
        ]
        for f in files:
            text = (REPO / f).read_text()
            self.assertNotIn("scriptPath", text, f)
            self.assertNotRegex(text, r"/ho" r"me/\w+", f)
        for f in files[:2]:
            self.assertIn('Workflow(name="multi-angle-review"', (REPO / f).read_text(), f)


if __name__ == "__main__":
    unittest.main()
