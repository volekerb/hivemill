import json
import unittest

import test_plugins
import test_resume
from harness import REPO, ROLES, rendered_settings

spawn_cmd, CLI_SPAWNABLE = test_plugins.spawn_cmd, test_plugins.CLI_SPAWNABLE


def flag_value(argv, flag):
    return argv[argv.index(flag) + 1]


def assert_strict(tc, argv, role):
    tc.assertIn("--strict-mcp-config", argv, f"{role}: {argv}")
    tc.assertEqual(json.loads(flag_value(argv, "--mcp-config")), {"mcpServers": {}}, role)


class StrictMcpSpawnTests(test_plugins.PluginDirSpawnTests):
    test_plugin_dir_iff_role_dir_exists = None  # inherited setUp only; that test lives in test_plugins

    def starts(self):
        return {c["argv"][2]: c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]}

    def test_strict_mcp_flags_for_every_role_but_boss(self):
        for role in CLI_SPAWNABLE:
            with self.subTest(role=role):
                self.ok(*spawn_cmd(role))
        starts = self.starts()
        for role in CLI_SPAWNABLE:
            argv = starts[f"zz-test-{role}"]
            if role == "boss":
                self.assertNotIn("--strict-mcp-config", argv)
                self.assertNotIn("--mcp-config", argv)
            else:
                assert_strict(self, argv, role)

    def test_existing_flags_survive(self):
        self.ok(*spawn_cmd("coder"))
        argv = self.starts()["zz-test-coder"]
        self.assertEqual(argv.count("--plugin-dir"), 1)  # no goal plugin dir exists for G-001 in the temp home
        for flag in ("--model", "--settings", "--append-system-prompt-file", "--session-id"):
            self.assertIn(flag, argv)


class StrictMcpResumeTests(test_resume.ResumeTestCase):
    def test_relaunch_resume_carries_strict_flags_and_resume_flags(self):
        self.okr("relaunch", "zz-test-lead", agent="zz-test-director")
        argv = self.starts()["zz-test-lead"]
        assert_strict(self, argv, "lead")
        self.assertEqual(argv[argv.index("--system-prompt-snapshot") + 1], "off")

    def test_resume_of_boss_has_no_strict_flags(self):
        self.okr("relaunch", "zz-test-boss", agent="zz-test-director")
        argv = self.starts()["zz-test-boss"]
        self.assertNotIn("--strict-mcp-config", argv)
        self.assertNotIn("--mcp-config", argv)
        self.assertEqual(argv[argv.index("--system-prompt-snapshot") + 1], "off")


class SettingsKeysTests(unittest.TestCase):
    def load(self, role):
        return json.loads(rendered_settings(role).read_text())

    def test_non_boss_settings_disable_connectors_only(self):
        for role in ROLES:
            if role == "boss":
                continue
            with self.subTest(role=role):
                d = self.load(role)
                self.assertEqual(d["env"]["ENABLE_CLAUDEAI_MCP_SERVERS"], "false")
                self.assertNotIn("enabledPlugins", d)  # plugins are T-0305

    def test_boss_settings_do_not_disable_connectors(self):
        d = self.load("boss")
        self.assertNotIn("enabledPlugins", d)
        self.assertNotIn("ENABLE_CLAUDEAI_MCP_SERVERS", d["env"])


if __name__ == "__main__":
    unittest.main()
