"""T-0822: Cursor policy materializes under STATE/factory/cursor/<agent>; shared checkout and --add-dir roots stay clean."""
import importlib
import json
import os
import sys
import time
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from harness import REPO, FactoryTestCase
from test_util_alerts import AlertCase  # noqa: E402

HOOKS = REPO / "roles" / "hooks"


def load_harness():
    return importlib.import_module("scout.cursor_harness")


def hook_command(hooks_path: Path) -> str:
    data = json.loads(hooks_path.read_text())
    return data["hooks"]["beforeShellExecution"][0]["command"]


def fail_closed_ok(hooks_path: Path) -> bool:
    data = json.loads(hooks_path.read_text())
    for val in (data.get("hooks") or {}).values():
        for item in val:
            if item.get("failClosed") is not True:
                return False
    return True


class CursorSpawnWorkspaceTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_agent("zz-test-director", "director", goal="G-001", team="g-001-a")
        self.ok("config", "agent_runtimes", "*=cursor", agent="zz-test-director")
        fake = e.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho ok\n")
        fake.chmod(0o755)

    def test_lead_spawn_argv_workspace_and_add_dir(self):
        before = len(self.env.calls("herdr"))
        name = "zz-cursor-lead-ws"
        self.ok("spawn", "lead", "--name", name, "--goal", "G-001", "--team", "g-001-a",
                agent="zz-test-director")
        starts = [c for c in self.env.calls("herdr")[before:] if c.get("argv", [])[:2] == ["agent", "start"]]
        self.assertEqual(len(starts), 1)
        argv = starts[0]["argv"]
        dash = argv.index("--")
        agent_argv = argv[dash + 1:]
        self.assertIn("--workspace", agent_argv)
        ws = Path(agent_argv[agent_argv.index("--workspace") + 1])
        self.assertTrue(str(ws).endswith(f"/state/factory/cursor/{name}"))
        self.assertIn("--add-dir", agent_argv)
        home = self.env.home_dir
        self.assertFalse((home / ".cursor").exists())
        self.assertFalse((home / "AGENTS.md").exists())
        hooks = ws / ".cursor" / "hooks.json"
        self.assertTrue(hooks.is_file())
        self.assertTrue(fail_closed_ok(hooks))
        self.assertIn("--hq", hook_command(hooks))
        self.assertNotIn("--role pm", hook_command(hooks))

    def test_second_materialize_refreshes_hooks_mtime(self):
        h = load_harness()
        settings = {"permissions": {"deny": [], "allow": []}}
        home = self.env.home_dir
        state = self.env.user_home / ".local" / "state"
        with mock.patch.dict(os.environ, {"XDG_STATE_HOME": str(state)}):
            ws = h.isolated_policy_dir(home, "zz-cursor-remat")
        hooks = ws / ".cursor" / "hooks.json"
        h.materialize_cursor_policy(
            ws, "lead", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# lead\n",
            hq=True, factory_home=home,
        )
        mtime1 = hooks.stat().st_mtime
        time.sleep(0.05)
        h.materialize_cursor_policy(
            ws, "lead", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# lead\n",
            hq=True, factory_home=home,
        )
        self.assertGreaterEqual(hooks.stat().st_mtime, mtime1)
        self.assertEqual(len(list((state / "factory" / "cursor").glob("zz-cursor-remat"))), 1)

    def test_claude_pm_unchanged_when_runtimes_empty(self):
        self.ok("config", "agent_runtimes", "", agent="zz-test-director")
        before = len(self.env.calls("herdr"))
        self.ok("spawn", "pm", "--name", "zz-claude-pm", "--goal", "G-001", "--team", "g-001-a",
                agent="zz-test-director")
        starts = [c for c in self.env.calls("herdr")[before:] if c.get("argv", [])[:2] == ["agent", "start"]]
        self.assertEqual(len(starts), 1)
        argv = starts[0]["argv"]
        self.assertIn("--kind", argv)
        self.assertEqual(argv[argv.index("--kind") + 1], "claude")


class AddDirLeakSpawnTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_agent("zz-test-director", "director", goal="G-001", team="g-001-a")
        self.ok("config", "agent_runtimes", "*=cursor", agent="zz-test-director")
        fake = e.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho ok\n")
        fake.chmod(0o755)

    def _spawn_hq_role(self, role, name):
        return self.refused(
            3, "spawn", role, "--name", name, "--goal", "G-001", "--team", "g-001-a",
            agent="zz-test-director",
        )

    def _retire_seeded_director(self):
        """T-1265: one live director per org. These tests spawn a director to check its add-dir roots, not the twin
        rule, so the fixture director (the caller) stops counting as the org's live director first."""
        self.env.sql("UPDATE agents SET status='exited' WHERE name='zz-test-director'")

    def test_hq_roles_add_dir_roots_clean(self):
        self._retire_seeded_director()
        before = len(self.env.calls("herdr"))
        for role, name in (("director", "zz-c-dir"), ("boss", "zz-c-boss"), ("lead", "zz-c-lead")):
            with self.subTest(role=role):
                self.ok("spawn", role, "--name", name, "--goal", "G-001", "--team", "g-001-a",
                        agent="zz-test-director")
        starts = [c for c in self.env.calls("herdr")[before:] if c.get("argv", [])[:2] == ["agent", "start"]]
        self.assertEqual(len(starts), 3)
        for start in starts:
            argv = start["argv"]
            dash = argv.index("--")
            agent_argv = argv[dash + 1:]
            self.assertIn("--workspace", agent_argv)
            ws = Path(agent_argv[agent_argv.index("--workspace") + 1])
            self.assertTrue(str(ws).startswith(str(self.env.user_home / ".local" / "state" / "factory" / "cursor")))
            add_dirs = [agent_argv[i + 1] for i, x in enumerate(agent_argv) if x == "--add-dir"]
            for d in add_dirs:
                p = Path(d).resolve()
                self.assertFalse((p / ".cursor").exists(), d)
                self.assertFalse((p / "AGENTS.md").exists(), d)

    def test_spawn_refused_when_add_dir_has_cursor_dir(self):
        home = self.env.home_dir
        (home / ".cursor").mkdir()
        before = len(self.env.calls("herdr"))
        p = self._spawn_hq_role("lead", "zz-leak-lead")
        self.assertIn("add-dir", p.stderr.lower())
        starts = [c for c in self.env.calls("herdr")[before:] if c.get("argv", [])[:2] == ["agent", "start"]]
        self.assertEqual(starts, [])

    def test_spawn_refused_when_add_dir_has_agents_md(self):
        agents_md = self.env.home_dir / "AGENTS.md"
        agents_md.write_text("# leak\n")
        before = len(self.env.calls("herdr"))
        self._retire_seeded_director()
        p = self._spawn_hq_role("director", "zz-leak-dir")
        self.assertIn("add-dir", p.stderr.lower())
        starts = [c for c in self.env.calls("herdr")[before:] if c.get("argv", [])[:2] == ["agent", "start"]]
        self.assertEqual(starts, [])


class PolicyTargetTests(unittest.TestCase):
    def test_factory_home_refused(self):
        h = load_harness()
        with __import__("tempfile").TemporaryDirectory(prefix="zz-cursor-home-") as tmp:
            home = Path(tmp) / "factory"
            home.mkdir()
            settings = {"permissions": {"deny": [], "allow": []}}
            with self.assertRaises(ValueError) as ctx:
                h.materialize_cursor_policy(
                    home, "boss", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# b\n",
                    factory_home=home,
                )
            self.assertIn("FACTORY_HOME", str(ctx.exception))

    def test_under_home_outside_run_cursor_refused(self):
        h = load_harness()
        with __import__("tempfile").TemporaryDirectory(prefix="zz-cursor-sub-") as tmp:
            home = Path(tmp) / "factory"
            home.mkdir()
            bad = home / "db"
            bad.mkdir()
            settings = {"permissions": {"deny": [], "allow": []}}
            with self.assertRaises(ValueError):
                h.materialize_cursor_policy(
                    bad, "coder", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# c\n",
                    factory_home=home,
                )

    def test_worktree_still_refused(self):
        h = load_harness()
        with __import__("tempfile").TemporaryDirectory(prefix="zz-cursor-wt-") as tmp:
            home = Path(tmp) / "factory"
            home.mkdir()
            wt = home / "worktrees" / "G-001-T-x"
            wt.mkdir(parents=True)
            settings = {"permissions": {"deny": [], "allow": []}}
            with self.assertRaises(ValueError):
                h.materialize_cursor_policy(
                    wt, "coder", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# c\n",
                    factory_home=home,
                )


class CrossRoleHooksTests(unittest.TestCase):
    def test_materialized_hooks_match_role(self):
        h = load_harness()
        settings = {"permissions": {"deny": [], "allow": []}}
        with __import__("tempfile").TemporaryDirectory(prefix="zz-cursor-xr-") as tmp:
            pm_ws = Path(tmp) / "pm-ws"
            coder_ws = Path(tmp) / "coder-ws"
            lead_ws = Path(tmp) / "lead-ws"
            for d in (pm_ws, coder_ws, lead_ws):
                d.mkdir()
            h.materialize_cursor_policy(
                pm_ws, "pm", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# pm\n",
            )
            h.materialize_cursor_policy(
                coder_ws, "coder", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# coder\n",
            )
            h.materialize_cursor_policy(
                lead_ws, "lead", hooks_dir=HOOKS, claude_settings=settings, system_prompt="# lead\n", hq=True,
            )
            pm_cmd = hook_command(pm_ws / ".cursor" / "hooks.json")
            coder_cmd = hook_command(coder_ws / ".cursor" / "hooks.json")
            lead_cmd = hook_command(lead_ws / ".cursor" / "hooks.json")
            self.assertIn("--role pm", pm_cmd)
            self.assertNotIn("--role coder", pm_cmd)
            self.assertIn("--role coder", coder_cmd)
            self.assertNotIn("--role pm", coder_cmd)
            self.assertIn("--hq", lead_cmd)
            self.assertIn("--role lead", lead_cmd)
            self.assertNotIn("--role pm", lead_cmd)


class WorktreeSpawnRefusalTests(FactoryTestCase):
    INPROC = True

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_agent("zz-test-director", "director", goal="G-001", team="g-001-a")
        self.ok("config", "agent_runtimes", "coder=cursor", agent="zz-test-director")
        fake = e.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho ok\n")
        fake.chmod(0o755)
        self.wt = e.home_dir / "worktrees" / "G-001-T-0822"
        self.wt.mkdir(parents=True)

    def test_spawn_exits_3_when_policy_target_is_worktree(self):
        before = len(self.env.calls("herdr"))

        def worktree_as_policy(home, name, cwd):
            return Path(cwd).resolve()

        with mock.patch("scout.agent_runtime.ch.policy_workspace", side_effect=worktree_as_policy):
            p = self.refused(
                3, "spawn", "coder", "--name", "zz-bad-coder", "--goal", "G-001",
                "--team", "g-001-a", "--cwd", str(self.wt), agent="zz-test-director",
            )
        self.assertIn("worktree", p.stderr.lower())
        starts = [
            c for c in self.env.calls("herdr")[before:]
            if c.get("argv", [])[:2] == ["agent", "start"]
        ]
        self.assertEqual(starts, [])


class CursorPromptV2Tests(FactoryTestCase):
    """T-1623: config cursor_prompt=v2 gives a Cursor coder roles/cursor/coder.md and the loop-until-done kickoff;
    v1 (default), Claude agents and HQ roles keep the old prompt and kickoff."""

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_agent("zz-test-director", "director", goal="G-001", team="g-001-a")
        e.seed_task("T-0001", status="todo", pr_url=None, team="g-001-a")
        self.ok("config", "agent_runtimes", "*=cursor", agent="zz-test-director")
        fake = e.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho ok\n")
        fake.chmod(0o755)
        self.roles = e.home_dir / "roles"
        self.cwd = e.home / "zz-coder-cwd"
        self.cwd.mkdir()

    def spawn(self, role, name):
        before = len(self.env.calls("herdr"))
        self.ok("spawn", role, "--name", name, "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001",
                "--cwd", str(self.cwd), agent="zz-test-director")
        prompts = [c["argv"] for c in self.env.calls("herdr")[before:] if c.get("argv", [])[:2] == ["agent", "prompt"]]
        self.assertEqual(len(prompts), 1, prompts)
        brief = (self.env.home_dir / "run" / f"{name}.prompt.md").read_text()
        return prompts[0][3], brief

    def old_brief(self, role):
        return (self.roles / "_common.md").read_text() + "\n\n" + (self.roles / f"{role}.md").read_text()

    def test_v2_coder_gets_short_prompt_and_loop_kickoff(self):
        self.ok("config", "cursor_prompt", "v2", agent="zz-test-director")
        kick, brief = self.spawn("coder", "zz-cursor-coder-v2")
        short = (self.roles / "cursor" / "coder.md").read_text()
        self.assertEqual(brief, short)
        self.assertLess(len(brief), len(self.old_brief("coder")) // 2)
        self.assertIn("Do not end your turn until `factory done` has succeeded", kick)
        self.assertIn("`factory pr create --title 'T-0001: <what the change does>' --body -`", kick)
        self.assertIn("`git push origin HEAD:factory/t-0001`", kick)
        self.assertIn("`factory prcheck`", kick)
        self.assertIn("`factory task update T-0001 --status in_review --pr <PR url>`", kick)
        self.assertIn('`factory done "BLOCKED:', kick)
        self.assertNotIn("\n", kick)
        rule = (self.cwd / ".cursor" / "rules" / "factory-role.mdc").read_text()  # a cwd outside the home holds its own policy
        self.assertIn("## Loop contract", rule)
        self.assertNotIn("# Factory operating rules (every role)", rule)
        inbox = self.env.sql("SELECT body FROM messages WHERE recipient='zz-cursor-coder-v2' AND subject LIKE 'kickoff:%'")
        self.assertEqual([r["body"] for r in inbox], [kick])

    def test_v2_push_target_is_the_guard_pinned_branch(self):
        self.ok("config", "cursor_prompt", "v2", agent="zz-test-director")
        self.env.sql("UPDATE tasks SET branch='factory/zz-other' WHERE id='T-0001'")
        kick, _ = self.spawn("coder", "zz-cursor-coder-br")
        self.assertIn("`git push origin HEAD:factory/t-0001`", kick)
        self.assertNotIn("zz-other", kick)

    def test_v1_default_keeps_old_prompt_and_kickoff(self):
        self.assertEqual(self.ok("config", "cursor_prompt", agent="zz-test-director").stdout.strip(), "v1")
        kick, brief = self.spawn("coder", "zz-cursor-coder-v1")
        self.assertEqual(brief, self.old_brief("coder"))
        self.assertTrue(kick.startswith("You are `zz-cursor-coder-v1`, the factory coder. Run `factory whoami` and "
                                        "`factory inbox` first. Your task is T-0001"), kick)
        self.assertNotIn("Do not end your turn", kick)

    def test_v2_worker_without_cursor_file_keeps_role_prompt_gets_loop_kickoff(self):
        self.ok("config", "cursor_prompt", "v2", agent="zz-test-director")
        kick, brief = self.spawn("pm", "zz-cursor-pm-v2")
        self.assertEqual(brief, self.old_brief("pm"))
        self.assertIn("Do not end your turn until `factory done` has succeeded", kick)
        self.assertIn("Your task is T-0001: read it with `factory task show T-0001`.", kick)
        self.assertNotIn("factory pr create", kick)

    def test_v2_hq_role_keeps_old_kickoff(self):
        self.ok("config", "cursor_prompt", "v2", agent="zz-test-director")
        before = len(self.env.calls("herdr"))
        self.ok("spawn", "lead", "--name", "zz-cursor-lead-v2", "--goal", "G-001", "--team", "g-001-a",
                agent="zz-test-director")
        (kick,) = [c["argv"][3] for c in self.env.calls("herdr")[before:] if c.get("argv", [])[:2] == ["agent", "prompt"]]
        self.assertTrue(kick.startswith("You are `zz-cursor-lead-v2`, the factory lead. Run `factory whoami`"), kick)
        self.assertNotIn("Do not end your turn", kick)
        self.assertEqual((self.env.home_dir / "run" / "zz-cursor-lead-v2.prompt.md").read_text(), self.old_brief("lead"))

    def test_v2_leaves_claude_agents_alone(self):
        self.ok("config", "agent_runtimes", "", agent="zz-test-director")
        self.ok("config", "cursor_prompt", "v2", agent="zz-test-director")
        kick, brief = self.spawn("pm", "zz-claude-pm-v2")
        self.assertEqual(brief, self.old_brief("pm"))
        golden = (GOLDEN_DIR / "pm.txt").read_text().split(GOLDEN_SPLIT, 1)[1]  # golden = this brief + the org section
        self.assertTrue(golden.startswith(brief + "\n## Your org\n"), "spawned Claude pm prompt is not the golden text")
        self.assertEqual(kick, "You are `zz-claude-pm-v2`, the factory pm. Run `factory whoami` and `factory inbox` first. "
                               "Your task is T-0001: read it with `factory task show T-0001`. Goal context: "
                               "`factory goal show G-001`. Your team is g-001-a.")

    def test_config_refuses_unknown_version_accepts_v2(self):
        p = self.refused(2, "config", "cursor_prompt", "v3", agent="zz-test-director")
        self.assertIn("cursor_prompt must be one of v1, v2", p.stderr)
        self.assertIn("factory config cursor_prompt v1", p.stderr)
        self.assertEqual(self.ok("config", "cursor_prompt", agent="zz-test-director").stdout.strip(), "v1")
        self.ok("config", "cursor_prompt", "v2", agent="zz-test-director")
        self.assertEqual(self.ok("config", "cursor_prompt", agent="zz-test-director").stdout.strip(), "v2")

    def test_v2_live_cursor_coder_is_not_flagged_stale(self):
        self.ok("config", "cursor_prompt", "v2", agent="zz-test-director")
        self.spawn("coder", "zz-cursor-coder-fr")
        self.ok("tick")
        stale = self.env.sql("SELECT detail FROM events WHERE action='stale-agent' AND entity_id='zz-cursor-coder-fr'")
        self.assertEqual([r["detail"] for r in stale if "prompt" in r["detail"]], [])
        self.ok("config", "cursor_prompt", "v1", agent="zz-test-director")
        self.ok("tick")
        stale = self.env.sql("SELECT detail FROM events WHERE action='stale-agent' AND entity_id='zz-cursor-coder-fr'")
        self.assertEqual(len([r for r in stale if "prompt" in r["detail"]]), 1, [dict(r) for r in stale])


class ClaudePromptUnchangedTests(AlertCase):
    """T-1623 hard constraint: the Cursor prompt layer never changes what a Claude agent gets. For every role the
    rendered Claude prompt is roles/_common.md + roles/<role>.md (+ the org section), whatever cursor_prompt says."""

    def set_version(self, value):
        self.con.execute("INSERT INTO config(key,value) VALUES('cursor_prompt',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (value,))
        self.con.commit()

    def test_every_role_renders_the_same_under_v1_and_v2(self):
        roles_dir = self.mod.ROLES_DIR
        roles = sorted(p.stem for p in roles_dir.glob("*.md") if not p.stem.startswith("_"))
        self.assertLessEqual({"boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge", "cleaner"}, set(roles))
        common = (roles_dir / "_common.md").read_text()
        for version in ("v1", "v2"):
            self.set_version(version)
            for role in roles:
                for org, multi in ((None, False), ("claude", True)):
                    with self.subTest(version=version, role=role, org=org):
                        self.assertFalse(self.mod.cursor_prompt_v2_on(self.con, "claude"))
                        want = common + "\n\n" + (roles_dir / f"{role}.md").read_text() + self.mod.org_prompt_section(org, multi)
                        got = self.mod.role_prompt_text(role, org, multi,
                                                        cursor_v2=self.mod.cursor_prompt_v2_on(self.con, "claude"))
                        self.assertEqual(got, want)
                        self.assertEqual(self.mod.role_prompt_text(role, org, multi), want)

    def test_shared_role_files_do_not_mention_the_cursor_layer(self):
        for p in sorted(self.mod.ROLES_DIR.glob("*.md")):
            self.assertNotIn("roles/cursor", p.read_text(), p.name)

    def test_claude_coder_kickoff_is_kickoff_text(self):
        self.assertEqual(
            self.mod.kickoff_text("coder", "claude-coder-t-0001", "G-001", "g-001-a", "T-0001", "x"),
            "You are `claude-coder-t-0001`, the factory coder. Run `factory whoami` and `factory inbox` first. "
            "Your task is T-0001: read it with `factory task show T-0001`. Goal context: `factory goal show G-001`. "
            "Your team is g-001-a. x")


GOLDEN_DIR = REPO / "tests" / "testdata" / "claude_prompts"
GOLDEN_REGEN = ("FACTORY_REGEN_GOLDENS=1 python3 -m unittest discover -s tests -p test_cursor_workspace.py "
                "-k ClaudePromptGoldenTests")
GOLDEN_SPLIT = "\n----- role prompt (run/<name>.prompt.md) -----\n"


class ClaudePromptGoldenTests(AlertCase):
    """T-1623 hard constraint (owner M37849): what a Claude agent is given, the kickoff and the role prompt, is pinned
    byte for byte in tests/testdata/claude_prompts/<role>.txt for every role. The files were rendered from origin/main
    before the Cursor prompt layer existed. A wanted role edit regenerates them with GOLDEN_REGEN (docs/cursor-prompt.md)."""

    def roles(self):
        return sorted(p.stem for p in self.mod.ROLES_DIR.glob("*.md") if not p.stem.startswith("_"))

    def render(self, role, **kw):
        kick = self.mod.kickoff_text(role, f"claude-{role}-t-0001", "G-001", "g-001-a", "T-0001", "")
        return kick + GOLDEN_SPLIT + self.mod.role_prompt_text(role, "claude", True, **kw)

    def set_version(self, value):
        self.con.execute("INSERT INTO config(key,value) VALUES('cursor_prompt',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (value,))
        self.con.commit()

    def test_claude_prompt_of_every_role_equals_its_golden_under_v1_and_v2(self):
        roles = self.roles()
        self.assertLessEqual({"boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge", "cleaner"}, set(roles))
        if os.environ.get("FACTORY_REGEN_GOLDENS") == "1":
            GOLDEN_DIR.mkdir(parents=True, exist_ok=True)
            for old in GOLDEN_DIR.glob("*.txt"):
                old.unlink()
            for role in roles:
                (GOLDEN_DIR / f"{role}.txt").write_text(self.render(role))
            self.skipTest(f"regenerated {len(roles)} goldens in {GOLDEN_DIR}")
        hint = f"the Claude prompt changed; if that is wanted, regenerate the goldens: {GOLDEN_REGEN}"
        self.assertEqual(sorted(p.stem for p in GOLDEN_DIR.glob("*.txt")), roles, hint)
        for version in ("v1", "v2"):
            self.set_version(version)
            for role in roles:
                with self.subTest(version=version, role=role):
                    want = (GOLDEN_DIR / f"{role}.txt").read_bytes().decode()
                    v2 = self.mod.cursor_prompt_v2_on(self.con, "claude")
                    self.assertFalse(v2)
                    self.assertEqual(self.render(role), want, hint)
                    self.assertEqual(self.render(role, cursor_v2=v2), want, hint)

    def test_a_changed_claude_prompt_fails_the_golden(self):
        if os.environ.get("FACTORY_REGEN_GOLDENS") == "1":
            self.skipTest("regenerating the goldens")
        want = (GOLDEN_DIR / "coder.txt").read_text()
        self.assertNotEqual(self.render("coder", cursor_v2=True), want)  # the Cursor v2 text is not the Claude text
        real = self.mod.kickoff_text
        with mock.patch.object(self.mod, "kickoff_text", lambda *a: real(*a) + " "):
            self.assertNotEqual(self.render("coder"), want)
        self.assertEqual(self.render("coder"), want)


if __name__ == "__main__":
    unittest.main()
