import json
import os
import tempfile
from pathlib import Path

from harness import REPO, FactoryTestCase


def spawn_cmd(role, goal):
    base = ["spawn", role, "--name", f"zz-test-{role}"]
    if role in ("coder", "qa", "reviewer"):
        base += ["--goal", goal, "--task", "T-0001"]
    elif goal:
        base += ["--goal", goal]
    return base


class GoalPluginDirTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        # never write into the repo: replace the roles symlink with a real dir of symlinks plus our own plugins/
        roles = self.env.home_dir / "roles"
        roles.unlink()
        roles.mkdir()
        for child in (REPO / "roles").iterdir():
            if child.name != "plugins":
                (roles / child.name).symlink_to(child)
        plugins = roles / "plugins"
        plugins.mkdir()
        (plugins / "staff-engineer").symlink_to(REPO / "roles" / "plugins" / "staff-engineer")
        self.goal_dir = plugins / "goals" / "G-001"
        (self.goal_dir / ".claude-plugin").mkdir(parents=True)
        (self.goal_dir / ".claude-plugin" / "plugin.json").write_text(json.dumps({"name": "zz-goal"}))
        self.plugins = plugins
        self.env.seed_goal()
        self.env.seed_goal("G-002")
        self.env.seed_team(lead="zz-test-lead")
        self.env.seed_task("T-0001", role="coder", status="todo", pr_url=None, team="g-001-a")
        self.env.run("config", "max_live_agents", "99")

    def flags(self, role, goal="G-001"):
        self.ok(*spawn_cmd(role, goal))
        starts = [c["argv"] for c in self.env.calls("herdr")
                  if c["argv"][:3] == ["agent", "start", f"zz-test-{role}"]]
        argv = starts[-1]
        return [argv[i + 1] for i, a in enumerate(argv) if a == "--plugin-dir"]

    def test_coder_with_goal_dir(self):
        got = self.flags("coder")
        self.assertEqual([os.path.realpath(g) for g in got], [os.path.realpath(self.goal_dir)])

    def test_coder_without_goal_dir(self):
        self.assertEqual(self.flags("coder", "G-002"), [])

    def test_other_roles_get_none(self):
        for role in ("qa", "reviewer"):
            with self.subTest(role=role):
                self.assertEqual(self.flags(role), [])

    def test_staff_engineer_unchanged(self):
        got = self.flags("staff-engineer", None)
        self.assertEqual([os.path.realpath(g) for g in got],
                         [str(REPO / "roles" / "plugins" / "staff-engineer")])

    def test_malicious_goal_ids(self):
        # the CLI rejects unknown goals first, so call spawn() directly with ids that would escape or alias goals/
        (self.plugins / "x").mkdir()
        (self.goal_dir / "sub").mkdir()
        for i, bad in enumerate(("../x", "G-1/../../etc", "G-001/..", "G-001/sub", "g-001", "G-001\n", "G-")):
            with self.subTest(goal=bad):
                name = f"zz-bad-{i}"
                self.env.sql("INSERT INTO goals(id,title,status,created,updated) VALUES(?,?,?,?,?)",
                             (bad, "zz-test bad", "active", "2026-01-01T00:00:00+00:00", "2026-01-01T00:00:00+00:00"))
                with self.env.load_factory() as mod:
                    mod.spawn(mod.db(), "coder", bad, "g-001-a", "T-0001", name, cwd=str(self.env.home_dir))
                argv = [c["argv"] for c in self.env.calls("herdr") if c["argv"][:3] == ["agent", "start", name]][-1]
                self.assertNotIn("--plugin-dir", argv)

    def test_symlink_escape_ignored(self):
        outside = Path(tempfile.mkdtemp())
        self.addCleanup(lambda: os.rmdir(outside))
        (self.plugins / "goals" / "G-002").symlink_to(outside)
        self.assertEqual(self.flags("coder", "G-002"), [])

    def test_relaunch_path_goes_through_spawn(self):
        import re
        src = (REPO / "bin" / "factory_cli.py").read_text()
        self.assertEqual(len(re.findall(r'"--plugin-dir"', src)), 2)  # only spawn builds the flag
