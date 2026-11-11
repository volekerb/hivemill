"""T-1265: a goal-less `factory spawn boss|director` keeps its bare role name (<org>-boss, not <org>-boss-boss), and a
second boss/director for an org that already has a live one is refused (exit 3, no pane). Subprocess, temp home, herdr stub."""
import unittest

from harness import FactoryTestCase


class SpawnHqName(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_agent("zz-owner-dir", "director")
        self.ok("config", "org_prefix_default", "1")

    def spawn(self, *args):
        return self.env.run("spawn", *args, agent="zz-owner-dir")

    def names(self, role):
        return [r["name"] for r in self.env.sql("SELECT name FROM agents WHERE role=? ORDER BY name", (role,))]

    def pane_ops(self):
        return [c for c in self.env.calls("herdr") if c.get("argv", [None])[:2] in (["tab", "create"], ["pane", "split"])]

    def test_bare_boss_and_director_named_after_org(self):
        for role in ("boss", "director"):
            p = self.spawn(role, "--org", "cursor")
            self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
            self.assertIn(f"cursor-{role}", self.names(role))
            self.assertNotIn(f"cursor-{role}-{role}", self.names(role))

    def test_second_boss_refused_bare_and_named(self):
        self.env.seed_agent("cursor-boss", "boss")
        before = self.pane_ops()
        for extra in ((), ("--name", "cursor-boss2")):
            p = self.spawn("boss", "--org", "cursor", *extra)
            out = p.stdout + p.stderr
            self.assertEqual(p.returncode, 3, out)
            self.assertIn("cursor-boss", out)
            self.assertIn("factory relaunch cursor-boss", out)
        self.assertEqual(self.names("boss"), ["cursor-boss"])
        self.assertEqual(self.pane_ops(), before)

    def test_other_org_boss_not_blocked(self):
        self.env.seed_agent("claude-boss", "boss")
        p = self.spawn("boss", "--org", "cursor")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.names("boss"), ["claude-boss", "cursor-boss"])

    def test_relaunch_same_row_not_refused(self):
        self.env.seed_agent("cursor-boss", "boss")
        self.env.sql("UPDATE agents SET org='cursor', session_id='sid-1' WHERE name='cursor-boss'")
        p = self.env.run("relaunch", "cursor-boss", "--fresh", "--force", agent="zz-owner-dir")
        self.assertNotEqual(p.returncode, 3, p.stdout + p.stderr)
        self.assertNotIn("is already the live", p.stdout + p.stderr)

    def test_worker_default_name_unchanged(self):
        self.env.seed_team("g-001-a", lead="zz-lead")
        self.env.seed_task()
        p = self.spawn("coder", "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.names("coder"), ["claude-coder-t-0001"])


if __name__ == "__main__":
    unittest.main()
