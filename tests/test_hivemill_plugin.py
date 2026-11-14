import json
import os
import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

from harness import REPO
from test_skill_authoring import lint_skill

MARKETPLACE = REPO / ".claude-plugin" / "marketplace.json"
PLUGIN = REPO / "plugins" / "hivemill"
MANIFEST = PLUGIN / ".claude-plugin" / "plugin.json"
SKILL = PLUGIN / "skills" / "setup" / "SKILL.md"
SKILL_REL = "plugins/hivemill/skills/setup/SKILL.md"

REQUIRED = [
    "Never ask for, accept or repeat a token, password or API key in this chat.",
    "If the user pastes one, do not use it. Tell them to revoke it and create a new one in their own terminal.",
    "Do not run the setup steps yourself. install.py does them; if it fails, stop and report.",
]
FORBIDDEN = ["git clone", "factory init", "factory up", "systemctl", "curl", "--with-token",
             "GH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"]
SECRET_PREFIXES = ["ghp_", "github_pat_", "sk-ant-"]


def forbidden_hits(text):
    return [f for f in FORBIDDEN if f in text]


class ManifestTests(unittest.TestCase):
    def test_marketplace_fields(self):
        m = json.loads(MARKETPLACE.read_text())
        self.assertEqual(m["name"], "hivemill")
        self.assertTrue(m["owner"]["name"])
        self.assertEqual(len(m["plugins"]), 1)
        e = m["plugins"][0]
        self.assertEqual(e["name"], "hivemill")
        self.assertEqual(e["source"], "./plugins/hivemill")
        self.assertNotIn("version", e)

    def test_plugin_fields(self):
        p = json.loads(MANIFEST.read_text())
        self.assertEqual(p["name"], "hivemill")
        self.assertTrue(p["description"])
        self.assertTrue(p["author"]["name"])
        self.assertNotIn("version", p)  # users track commits
        self.assertEqual(p["name"], json.loads(MARKETPLACE.read_text())["plugins"][0]["name"])

    def test_source_dir_exists_and_has_installer(self):
        self.assertTrue((PLUGIN / "scripts" / "install.py").is_file())

    def test_no_hooks_or_mcp(self):
        self.assertFalse((PLUGIN / "hooks").exists())
        self.assertFalse((PLUGIN / ".mcp.json").exists())
        self.assertFalse((PLUGIN / ".claude-plugin" / "hooks.json").exists())
        self.assertNotIn("hooks", json.loads(MANIFEST.read_text()))
        self.assertNotIn("mcpServers", json.loads(MANIFEST.read_text()))

    def test_no_repo_root_claude_skills(self):
        self.assertFalse((REPO / ".claude" / "skills").exists())


class SkillTests(unittest.TestCase):
    def setUp(self):
        self.text = SKILL.read_text()

    def test_lint(self):
        self.assertEqual(lint_skill(SKILL_REL, self.text), [])

    def test_required_text_once(self):
        for r in REQUIRED:
            self.assertEqual(self.text.count(r), 1, r)

    def test_no_forbidden_text(self):
        self.assertEqual(forbidden_hits(self.text), [])

    def test_forbidden_detector_rejects(self):
        self.assertEqual(forbidden_hits(self.text + "\ncurl https://x\n"), ["curl"])

    def test_flags_exist_in_installer(self):
        installer = (PLUGIN / "scripts" / "install.py").read_text()
        flags = set(re.findall(r"(?<![\w-])(--[a-z][a-z-]+)", self.text))
        self.assertIn("--yes", flags)
        for f in flags:
            self.assertIn('"%s"' % f, installer, f)

    def test_thin(self):
        self.assertLessEqual(len(self.text.splitlines()), 90)

    def test_no_secret_prefixes_in_skill_and_manifests(self):
        # install.py itself holds redaction regexes with these prefixes, so it is not scanned
        files = [SKILL, MANIFEST, MARKETPLACE]
        for p in files:
            t = p.read_text(errors="replace")
            for s in SECRET_PREFIXES:
                self.assertNotIn(s, t, str(p))

    def test_installer_only_allowed_tools(self):
        line = [l for l in self.text.splitlines() if l.startswith("allowed-tools:")]
        self.assertEqual(line, ["allowed-tools: Bash(python3 ${CLAUDE_PLUGIN_ROOT}/scripts/install.py *)"])

    def test_gh_login_not_via_bang(self):
        self.assertIn("separate terminal", self.text)
        self.assertIn("never through `!`", self.text)

    def test_fresh_box_does_not_dead_end(self):
        self.assertIn("`herdr` or `gh` `missing`", self.text)
        self.assertIn("the installer installs them", self.text)

    def test_scout_default_off(self):
        line = [l for l in self.text.splitlines() if "scout, the skills triage helper" in l]
        self.assertEqual(len(line), 1)
        self.assertIn("default off", line[0])
        self.assertIn("pass `--no-scout` unless", line[0])
        self.assertNotIn("default yes", self.text)

    def test_apt_line_not_claimed_in_check(self):
        self.assertIn("not in the `--check` report", self.text)
        self.assertNotIn("the apt line the report printed", self.text)


class IosReleaseNodeTests(unittest.TestCase):
    """Runs the node --test suite of plugins/hivemill/scripts/ios-release (Linux, no Apple access)."""

    def test_ios_release_node_tests(self):
        node = shutil.which("node")
        if node is None:
            if os.environ.get("CI"):
                self.fail("node is not installed but CI is set: the ios-release tests must run in CI")
            self.skipTest("node is not installed")
        major = subprocess.run([node, "-p", "process.versions.node.split('.')[0]"],
                               capture_output=True, text=True, timeout=30).stdout.strip()
        if not major.isdigit() or int(major) < 20:
            if os.environ.get("CI"):
                self.fail(f"node >= 20 is required in CI, found {major or 'unknown'}")
            self.skipTest("node >= 20 is required")
        tests = sorted((REPO / "plugins/hivemill/scripts/ios-release/test").glob("*.test.mjs"))
        self.assertTrue(tests, "no *.test.mjs files under plugins/hivemill/scripts/ios-release/test")
        r = subprocess.run([node, "--test", "--test-reporter=tap", *map(str, tests)], cwd=REPO, capture_output=True,
                           text=True, timeout=300)
        out = r.stdout + r.stderr
        self.assertEqual(r.returncode, 0, out[-3000:])
        self.assertIn("# fail 0", out)


class IosReleaseSkillTests(unittest.TestCase):
    """The ios-testflight-release skill and the ios-release README."""

    DIR = PLUGIN / "scripts" / "ios-release"
    SKILL_PATH = PLUGIN / "skills" / "ios-testflight-release" / "SKILL.md"
    SKILL_REL = "plugins/hivemill/skills/ios-testflight-release/SKILL.md"
    RULES = [
        "Never ask for, accept or repeat an App Store Connect key, key id or issuer id in this chat.",
        "Never read, print or cat a credential file or a .p8 key.",
        "Always run --dry-run first and show its result before a real upload.",
        "Run a real upload only when the user asked for one in this conversation.",
        "Do not run xcodebuild or call the App Store Connect API yourself. release.sh does them; "
        "if it fails, stop and report the exit code.",
        "Report the final JSON summary line and the exit code; do not paste the full log.",
    ]
    ALLOWED = "allowed-tools: Bash(bash ${CLAUDE_PLUGIN_ROOT}/scripts/ios-release/release.sh *)"

    @classmethod
    def setUpClass(cls):
        cls.text = cls.SKILL_PATH.read_text()
        cls.readme = (cls.DIR / "README.md").read_text()

    def test_lint_clean(self):
        self.assertEqual(lint_skill(self.SKILL_REL, self.text), [])

    def test_rules_lines_exactly_once(self):
        lines = self.text.splitlines()
        for rule in self.RULES:
            self.assertEqual(lines.count("- " + rule), 1, rule)

    def test_allowed_tools_line(self):
        lines = [l for l in self.text.splitlines() if l.startswith("allowed-tools:")]
        self.assertEqual(lines, [self.ALLOWED])

    def test_thin_and_model_invocable(self):
        self.assertLessEqual(len(self.text.splitlines()), 80)
        self.assertNotIn("disable-model-invocation", self.text)
        self.assertIn("beta app review", self.text.lower())

    def test_no_secret_prefixes(self):
        for name, text in (("SKILL.md", self.text), ("README.md", self.readme)):
            for bad in SECRET_PREFIXES + ["-----BEGIN"]:
                self.assertNotIn(bad, text, f"{name} contains {bad}")

    def test_readme_exit_codes(self):
        rows = re.findall(r"^\| (\d) \|", self.readme, re.M)
        self.assertEqual(sorted(set(rows)), [str(i) for i in range(10)])

    def test_readme_has_every_help_flag(self):
        bash = shutil.which("bash")
        if bash is None:
            if os.environ.get("CI"):
                self.fail("bash is not installed but CI is set")
            self.skipTest("bash is not installed")
        r = subprocess.run([bash, str(self.DIR / "release.sh"), "--help"], capture_output=True, text=True,
                           timeout=60)
        self.assertEqual(r.returncode, 0, r.stderr)
        flags = set(re.findall(r"--[a-z][a-z-]*", r.stdout))
        self.assertIn("--signing", flags)
        self.assertIn("--dry-run", flags)
        for flag in sorted(flags):
            self.assertIn(flag, self.readme, flag)

    def test_readme_required_text(self):
        for needle in ["xcodeAccount", "apiKey", "Admin", "App Manager", "Cloud signing permission error",
                       "/usr/bin:/bin:/usr/sbin:/sbin", "rsync", "forbiddenWorktrees", "heavySlotCommand",
                       "{build}", "submitExternal", "hasAccessToAllBuilds", "beta app review",
                       "An App Manager key cannot cloud-sign", "Settings > Accounts", "chmod 600",
                       "security add-generic-password", "MISSING_EXPORT_COMPLIANCE", "--resume-build"]:
            self.assertIn(needle, self.readme, needle)
        self.assertNotIn('"heavySlot"', self.readme)

    def test_readme_has_every_example_key(self):
        example = json.loads((self.DIR / "ios-release.example.json").read_text())
        for key in example:
            self.assertIn(key, self.readme, key)


if __name__ == "__main__":
    unittest.main()
