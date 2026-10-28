import unittest

from harness import FactoryTestCase


class OpusEffortTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_task("T-0001", role="coder", status="todo", team="g-001-a")
        e.seed_agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")

    def start_argv(self, name):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:3] == ["agent", "start", name]][0]

    def spawn(self, role, name):
        self.ok("spawn", role, "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001", "--name", name,
                "--cwd", str(self.env.home), agent="zz-test-lead")
        return self.start_argv(name)

    def test_opus_role_gets_effort_medium(self):
        argv = self.spawn("reviewer", "zz-test-rev")
        self.assertEqual(argv[argv.index("--model") + 1], "claude-opus-5-5")
        self.assertEqual(argv[argv.index("--effort") + 1], "medium")

    def test_sonnet_role_has_no_effort(self):
        argv = self.spawn("coder", "zz-test-cod")
        self.assertEqual(argv[argv.index("--model") + 1], "claude-sonnet-5-5")
        self.assertNotIn("--effort", argv)

    def test_config_overrides_level(self):
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('opus_effort','high')")
        argv = self.spawn("reviewer", "zz-test-rev")
        self.assertEqual(argv[argv.index("--effort") + 1], "high")

    def test_empty_config_disables(self):
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('opus_effort','')")
        self.assertNotIn("--effort", self.spawn("reviewer", "zz-test-rev"))

    def test_invalid_level_is_refused(self):
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('opus_effort','turbo')")
        p = self.env.run("spawn", "reviewer", "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001",
                         "--name", "zz-test-rev", "--cwd", str(self.env.home), agent="zz-test-lead")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("opus_effort must be one of", p.stderr)
        self.assertEqual([c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]], [])
        self.assertEqual([c for c in self.env.calls("herdr") if c["argv"][0] in ("workspace", "tab", "pane")], [])
        self.assertEqual(self.env.sql("SELECT count(*) FROM agents WHERE name='zz-test-rev'")[0][0], 0)

    def test_invalid_level_refused_before_worktree(self):
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('opus_effort','Medium')")
        p = self.env.run("spawn", "reviewer", "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001",
                         "--name", "zz-test-rev", agent="zz-test-lead")  # no --cwd: would prepare a worktree
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("opus_effort must be one of", p.stderr)
        self.assertEqual([c for c in self.env.calls("herdr") if c["argv"][0] in ("workspace", "tab", "pane")], [])

    def test_config_rejects_invalid_level(self):
        self.env.seed_agent("zz-test-dir", "director")
        bad = self.env.run("config", "opus_effort", "Medium", agent="zz-test-dir")
        self.assertNotEqual(bad.returncode, 0)
        self.assertIn("opus_effort must be one of", bad.stderr)
        self.assertNotEqual(self.env.sql("SELECT value FROM config WHERE key='opus_effort'")[0][0], "Medium")
        good = self.env.run("config", "opus_effort", "high", agent="zz-test-dir")
        self.assertEqual(good.returncode, 0, good.stderr)
        self.assertEqual(self.env.run("config", "opus_effort", "", agent="zz-test-dir").returncode, 0)

    def test_only_opus_model_ids_get_effort(self):
        self.env.run("spawn", "reviewer", "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001",
                     "--name", "zz-test-rev", "--model", "my-opus-clone", "--cwd", str(self.env.home),
                     agent="zz-test-lead")
        self.assertNotIn("--effort", self.start_argv("zz-test-rev"))


if __name__ == "__main__":
    unittest.main()
