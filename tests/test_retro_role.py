"""T-1207: retro role spawn ACL and CLI refusal."""
import json
import unittest

from harness import FactoryTestCase, MODELS, ROLES


class RoleTableTests(unittest.TestCase):
    def test_roles_and_models(self):
        self.assertIn("retro", ROLES)
        self.assertEqual(MODELS["retro"], "claude-sonnet-5-5")

    def test_not_persistent(self):
        from harness import REPO
        src = (REPO / "bin" / "factory_cli.py").read_text()
        line = next(x for x in src.splitlines() if x.startswith("PERSISTENT_ROLES"))
        self.assertNotIn('"retro"', line)


class SpawnTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(lead="zz-test-lead")
        for n, r in (("zz-test-director", "director"), ("zz-test-lead", "lead"), ("zz-test-boss", "boss")):
            self.env.seed_agent(n, r, goal="G-001", team="g-001-a")

    def test_retro_spawn_director_and_owner(self):
        for who, name in (("zz-test-director", "cursor-retro-manual"), (None, "cursor-retro-manual2")):
            kw = {"agent": who} if who else {}
            self.assertIn(f"SPAWNED {name}", self.ok("spawn", "retro", "--name", name, **kw).stdout)
        row = dict(self.env.sql("SELECT * FROM agents WHERE name='cursor-retro-manual'")[0])
        self.assertEqual(row["org"], "cursor")
        args = json.loads(row["spawn_args"])
        self.assertEqual(args.get("runtime"), "cursor")

    def test_retro_spawn_refused_for_lead_and_boss(self):
        for who in ("zz-test-lead", "zz-test-boss"):
            self.refused(3, "spawn", "retro", "--name", "cursor-retro-x", agent=who)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM agents WHERE name='cursor-retro-x'")[0]["c"], 0)


class RefusalTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_agent("zz-test-retro", "retro", goal=None, team=None, task=None)

    def test_retro_refuses_sensitive_commands(self):
        for cmd in (("judge", "T-0001", "--merge", "--reason", "x"),
                    ("config", "retro_enabled", "0"),
                    ("project", "list")):
            with self.subTest(cmd=cmd[0]):
                self.refused(3, *cmd, agent="zz-test-retro")

    def test_other_roles_cannot_file_retro_report(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", task="T-0001")
        body = json.dumps({"window_end": "2026-11-09T10:00:00+01:00", "causes": [], "evidence": [], "fixes": []})
        self.refused(3, "retro", "file", "-", agent="zz-test-coder", stdin=body)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM retro_reports")[0]["c"], 0)


if __name__ == "__main__":
    unittest.main()
