"""G-029 T-0847: the researcher always runs in the G-010 worker sandbox (fail closed, read-only gh token).

Group A: sandbox_wanted, the fail-closed spawn and relaunch (herdr stub), prepare_agent's token and cwd binding.
Group B: the real namespace (skipped with userns_available()'s reason where the box cannot).
"""
import json
import os
import sys
import unittest
from pathlib import Path

from harness import REPO, FactoryTestCase
from test_sandbox_spawn import prep
from test_workerbox import NEEDS_USERNS, World, _shim_cfg_literal, put

sys.path.insert(0, str(REPO))
from scout import workerbox  # noqa: E402


class ConstantsTests(unittest.TestCase):
    def test_researcher_is_sandboxable_with_the_read_only_token(self):
        self.assertIn("researcher", workerbox.SANDBOXABLE)
        self.assertEqual(workerbox.ROLE_GH["researcher"], workerbox.GH_RO)
        self.assertEqual(workerbox._GH_KEY["researcher"], "gh_ro")


class ResearcherCase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        prep(self.env)  # stubs, tokens, goal and team
        self.env.seed_task("T-0001", role="researcher", status="todo", pr_url=None, team="g-001-a")
        self.xdg = {"XDG_STATE_HOME": str(Path(self.env.user_home) / "xdg-state")}

    def spawn(self):
        return self.env.run("spawn", "researcher", "--name", "zz-test-researcher", "--goal", "G-001", "--task", "T-0001",
                            extra_env=self.xdg)

    def mux(self, *words):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:len(words)] == list(words)]

    def live(self):
        return self.env.sql("SELECT name FROM agents WHERE role='researcher' AND status='live'")

    def drop_oauth(self):
        (self.env.user_home / ".config" / "factory" / "worker-oauth-token").unlink()


class AlwaysSandboxedTests(ResearcherCase):
    def test_sandbox_wanted_unit(self):
        self.assertEqual(self.env.run("config", "sandbox_roles", "").returncode, 0)
        with self.env.load_factory() as mod:
            con = mod.db()
            self.assertEqual(mod.cfg(con, "sandbox_roles"), "")
            self.assertIs(mod.sandbox_wanted(con, "researcher"), True)
            self.assertIs(mod.sandbox_wanted(con, "researcher", optin=True), True)
            for role in ("pm", "evaluator", "staff-engineer", "coder", "reviewer", "qa"):
                self.assertIs(mod.sandbox_wanted(con, role), False, role)
            con.execute("UPDATE config SET value='coder' WHERE key='sandbox_roles'")
            self.assertIs(mod.sandbox_wanted(con, "researcher"), True)
            self.assertIs(mod.sandbox_wanted(con, "coder"), True)
            self.assertIs(mod.sandbox_wanted(con, "pm"), False)

    def test_sandbox_wanted_true_for_researcher_with_empty_sandbox_roles(self):
        # through the real entry point: a researcher spawn with sandbox_roles '' still goes through the sandbox
        self.assertEqual(self.env.run("config", "sandbox_roles", "").returncode, 0)
        p = self.spawn()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        args = json.loads(self.env.sql("SELECT spawn_args FROM agents WHERE role='researcher'")[0]["spawn_args"])
        self.assertTrue(args["sandbox"])
        agent_dir = self.env.user_home / ".local" / "state" / "factory" / "workers" / "zz-test-researcher"
        self.assertTrue((agent_dir / "bin" / "claude").is_file())
        paths = [x for a in self.mux("tab", "create") + self.mux("pane", "split") for x in a if x.startswith("PATH=")]
        self.assertTrue(paths and all(x.startswith(f"PATH={agent_dir}/bin:") for x in paths), paths)

    def test_other_roles_keep_their_config_driven_behaviour(self):
        # sandbox_roles '' -> a pm, reviewer and staff-engineer spawn is NOT sandboxed
        self.assertEqual(self.env.run("config", "sandbox_roles", "").returncode, 0)
        for role in ("reviewer", "pm"):
            p = self.env.run("spawn", role, "--name", f"zz-x-{role}", "--goal", "G-001", "--team", "g-001-a",
                             *(["--cwd", str(self.env.home_dir / "worktrees" / "w-reviewer")] if role == "reviewer" else []))
            self.assertEqual(p.returncode, 0, (role, p.stderr))
            args = json.loads(self.env.sql("SELECT spawn_args FROM agents WHERE name=?", (f"zz-x-{role}",))[0]["spawn_args"])
            self.assertFalse(args.get("sandbox"), role)

    def test_sandbox_flag_does_not_change_it_and_roles_list_accepts_researcher(self):
        self.assertEqual(self.env.run("config", "sandbox_roles", "reviewer,researcher").returncode, 0)
        self.assertEqual(self.env.run("config", "sandbox_roles", "pm").returncode, 2)


class FailClosedTests(ResearcherCase):
    def assert_refused_for_owner_steps(self, p):
        self.assertEqual(p.returncode, 3, p.stdout + p.stderr)
        self.assertIn("worker-oauth-token", p.stderr)
        self.assertIn("docs/worker-sandbox.md", p.stderr)
        self.assertNotIn("sandbox_roles ''", p.stderr)  # there is no "turn it off" for the researcher

    def test_spawn_refused_when_preflight_fails(self):
        self.drop_oauth()
        p = self.spawn()
        self.assert_refused_for_owner_steps(p)
        self.assertEqual(self.live(), [])
        self.assertEqual(self.mux("agent", "start"), [])
        self.assertEqual(self.mux("tab", "create") + self.mux("pane", "split"), [])
        self.assertEqual([c for c in self.env.calls("herdr") if c["argv"][:2] != ["agent", "list"]], [])  # only the read-only list

    def test_spawn_refused_when_userns_is_missing(self):
        (self.env.stub_bin / "unshare").write_text("#!/bin/sh\nexit 1\n")
        p = self.spawn()
        self.assertEqual(p.returncode, 3, p.stdout + p.stderr)
        self.assertIn("docs/worker-sandbox.md", p.stderr)
        self.assertEqual(self.live(), [])
        self.assertEqual(self.mux("agent", "start"), [])

    def test_relaunch_refused_before_the_old_pane_is_closed(self):
        self.assertEqual(self.spawn().returncode, 0)
        self.assertEqual(len(self.live()), 1)
        self.drop_oauth()
        p = self.env.run("relaunch", "zz-test-researcher", "--force", extra_env=self.xdg)
        self.assert_refused_for_owner_steps(p)
        self.assertEqual(self.mux("pane", "close"), [])
        self.assertEqual(len(self.mux("agent", "start")), 1)
        self.assertEqual(len(self.live()), 1)


class PrepareAgentTests(unittest.TestCase):
    def setUp(self):
        self.w = World(self)
        self.research = self.w.home / ".local/state/factory/research"
        self.cwd = self.research / "t-1"
        self.cwd.mkdir(parents=True)

    def prepare(self, **kw):
        return self.w.prepare("researcher-t-1", "researcher", self.cwd, research_root=str(self.research), **kw)

    def test_ro_token_and_cwd_bound_read_write(self):
        cfg = json.loads(_shim_cfg_literal(self.prepare()))
        self.assertEqual(cfg["gh_token"], str(self.w.tokens / "gh-worker-ro-token"))
        self.assertNotIn("gh-worker-rw-token", json.dumps(cfg))
        self.assertIn(str(self.cwd), cfg["ns"]["rw"])
        self.assertNotIn(str(self.cwd), cfg["ns"]["ro"])
        self.assertEqual(cfg["ns"]["cwd"], str(self.cwd))

    def test_token_dir_override_still_picks_the_ro_file(self):
        tdir = self.w.base / "tok"
        for name in ("worker-oauth-token", "gh-worker-ro-token", "gh-worker-rw-token"):
            put(tdir / name, "x\n", 0o600)
        cfg = json.loads(_shim_cfg_literal(self.prepare(token_dir=tdir)))
        self.assertEqual(cfg["gh_token"], str(tdir / "gh-worker-ro-token"))

    def test_cwd_must_be_below_the_research_root(self):
        for bad in (self.research, self.w.home, self.w.home / ".ssh"):
            with self.subTest(str(bad)), self.assertRaises(ValueError):
                self.w.prepare("researcher-t-1", "researcher", bad, research_root=str(self.research))

    def test_research_root_is_ignored_for_other_roles(self):
        with self.assertRaises(ValueError):
            self.w.prepare("coder-t-1", "coder", self.cwd, research_root=str(self.research))

    def test_agent_dir_is_never_a_valid_research_cwd(self):
        agent_dir = self.w.home / ".local/state/factory/workers"
        with self.assertRaises(ValueError):
            self.w.prepare("researcher-t-1", "researcher", agent_dir / "researcher-t-1", research_root=str(agent_dir))


@NEEDS_USERNS
class RealNamespaceTests(unittest.TestCase):
    """Acceptance 5: inside the researcher's sandbox the owner's secrets and the factory code are out of reach."""

    def setUp(self):
        self.w = World(self)
        research = self.w.home / ".local/state/factory/research"
        self.cwd = research / "t-1"
        self.cwd.mkdir(parents=True)
        self.d = self.w.prepare("researcher-t-1", "researcher", self.cwd, research_root=str(research))

    def test_secrets_unreachable_and_only_the_cwd_is_writable(self):
        w = self.w
        ops = [{"op": "open", "path": str(w.tokens / "worker-oauth-token")},
               {"op": "listdir", "path": str(w.home / ".ssh")},
               {"op": "write", "path": str(w.fh / "roles" / "new.txt")},
               {"op": "write", "path": str(w.fh / "roles" / "settings.json")},
               {"op": "write", "path": str(self.cwd / "out.txt"), "data": "ok"},
               {"op": "cwd"}]
        p, res = w.run(self.d, ops, cwd=self.cwd)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIsNotNone(res, p.stdout + p.stderr)
        self.assertFalse(res[0]["ok"], "the worker-oauth-token FILE is readable inside the researcher sandbox")
        self.assertEqual(res[0]["err"], "ENOENT")
        self.assertTrue(not res[1]["ok"] or res[1]["val"] == [], res[1])  # absent or empty
        for r in res[2:4]:
            self.assertFalse(r["ok"], "a write under factory/roles succeeded")
            self.assertIn(r["err"], ("EROFS", "ENOENT"))
        self.assertTrue(res[4]["ok"], res[4])
        self.assertEqual((self.cwd / "out.txt").read_text(), "ok")
        self.assertEqual(res[5]["val"], str(self.cwd))
        self.assertEqual((w.fh / "roles" / "settings.json").read_text(), "{}")  # untouched on the host
        self.assertFalse((w.fh / "roles" / "new.txt").exists())


if __name__ == "__main__":
    unittest.main()
