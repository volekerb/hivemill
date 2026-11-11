"""T-1265 (SpawnHqName class: bare boss/director names, one live per org). T-0872: `factory spawn --name X` never renames silently. With the org prefix on, a bare X is refused with the rule;
an already prefixed X is honoured exactly; without --name the default org-prefixed name is unchanged. Subprocess, temp home."""
import unittest

from harness import FactoryTestCase


class SpawnName(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team("g-001-a", lead="zz-lead")
        self.env.seed_agent("zz-director", "director")
        self.ok("config", "org_prefix_default", "1")

    def spawn(self, *extra):
        return self.env.run("spawn", "pm", "--goal", "G-001", "--team", "g-001-a", *extra, agent="zz-director")

    def names(self):
        return [r["name"] for r in self.env.sql("SELECT name FROM agents WHERE role='pm'")]

    def test_bare_name_refused_with_rule(self):
        p = self.spawn("--name", "pm-x")
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("claude-pm-x", p.stdout + p.stderr)
        self.assertIn("org naming rule", p.stdout + p.stderr)
        self.assertEqual(self.names(), [])

    def test_prefixed_name_honoured_exactly(self):
        p = self.spawn("--name", "claude-pm-x")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.names(), ["claude-pm-x"])

    def test_no_name_keeps_default_prefixed_name(self):
        p = self.spawn()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.names(), ["claude-pm-g-001-a"])

    def test_help_documents_the_rule(self):
        p = self.env.run("spawn", "--help", agent="zz-director")
        txt = " ".join((p.stdout + p.stderr).split())
        self.assertIn("When the org prefix applies, the name must already be <org>-<name>; otherwise it is refused", txt)
        self.assertNotIn("must already follow the org naming rule", txt)

    def test_help_same_with_prefix_off(self):
        self.ok("config", "org_prefix_default", "0")
        p = self.env.run("spawn", "--help", agent="zz-director")
        self.assertIn("When the org prefix applies", " ".join((p.stdout + p.stderr).split()))

    def test_prefix_off_bare_name_accepted_exactly(self):
        self.ok("config", "org_prefix_default", "0")
        p = self.spawn("--name", "pm-x")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.names(), ["pm-x"])

    def test_cursor_goal_bare_name_refused(self):
        self.env.sql("UPDATE goals SET org='cursor' WHERE id='G-001'")
        p = self.spawn("--name", "pm-x")
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("cursor-pm-x", p.stdout + p.stderr)
        self.assertEqual(self.names(), [])

    def test_cursor_goal_prefixed_name_honoured_exactly(self):
        self.env.sql("UPDATE goals SET org='cursor' WHERE id='G-001'")
        fake = self.env.stub_bin / "agent"  # the cursor runtime needs the agent binary on PATH (absent on CI)
        fake.write_text("#!/bin/sh\necho CURSOR_OK\n")
        fake.chmod(0o755)
        p = self.spawn("--name", "cursor-pm-x")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.names(), ["cursor-pm-x"])



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
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertNotIn("is already the live", p.stdout + p.stderr)
        self.assertEqual(self.names("boss"), ["cursor-boss"])

    def test_legacy_unprefixed_live_boss_blocks_second(self):
        self.env.seed_agent("boss", "boss")  # org NULL, no prefix: the default (claude) org
        p = self.spawn("boss", "--org", "claude")
        self.assertEqual(p.returncode, 3, p.stdout + p.stderr)
        self.assertIn("factory relaunch boss", p.stdout + p.stderr)

    def test_worker_default_name_unchanged(self):
        self.env.seed_team("g-001-a", lead="zz-lead")
        self.env.seed_task()
        p = self.spawn("coder", "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.names("coder"), ["claude-coder-t-0001"])



if __name__ == "__main__":
    unittest.main()
