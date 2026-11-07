import json
import re
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


if __name__ == "__main__":
    unittest.main()
