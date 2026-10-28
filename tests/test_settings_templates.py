"""G-012 T-B2: roles/settings/<role>.json.in are templates rendered at spawn (docs/settings-templates.md)."""
import contextlib
import importlib.machinery
import io
import importlib.util
import json
import os
import pwd
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from harness import REPO, ROLES, FactoryTestCase, git, rendered_settings_dir

FACTORY = REPO / "bin" / "factory"
HOOK = REPO / "roles" / "hooks" / "worker-guard.py"
SETTINGS = REPO / "roles" / "settings"
PW_DIR = os.path.realpath(pwd.getpwuid(os.getuid()).pw_dir)
LIVE_HOME = os.path.join(PW_DIR, "factory")  # what bin/factory and the hook derive (T-0212)
WORKERS = ("pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "scout", "evaluator")
TOKENS = {"@FACTORY_LIVE_HOME@": LIVE_HOME, "@FACTORY_HOME_DIR@": PW_DIR,
          "@FACTORY_USER_CONFIG@": os.path.join(PW_DIR, ".config")}


def load_factory(home):
    old = os.environ.get("FACTORY_HOME")
    os.environ["FACTORY_HOME"] = str(home)
    try:
        loader = importlib.machinery.SourceFileLoader("factory_settings_templates_test", str(FACTORY))
        mod = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
        loader.exec_module(mod)
        return mod
    finally:
        if old is None:
            os.environ.pop("FACTORY_HOME", None)
        else:
            os.environ["FACTORY_HOME"] = old


def substitute(text):
    for k, v in TOKENS.items():
        text = text.replace(k, v)
    return text


def deny(path):
    return json.loads(Path(path).read_text())["permissions"]["deny"]


class TemplateFileTests(unittest.TestCase):
    def test_every_role_has_a_template_and_nothing_else_is_tracked(self):
        names = sorted(p.name for p in SETTINGS.glob("*.json.in"))
        self.assertEqual(names, sorted(f"{r}.json.in" for r in ROLES))
        tracked = subprocess.run(["git", "ls-files", "roles/settings"], cwd=REPO, capture_output=True, text=True)
        self.assertEqual(tracked.returncode, 0, tracked.stderr)
        self.assertEqual(sorted(tracked.stdout.split()), sorted(f"roles/settings/{r}.json.in" for r in ROLES))

    def test_no_template_names_a_home_directory(self):
        for r in ROLES:
            text = (SETTINGS / f"{r}.json.in").read_text()
            self.assertNotIn("/home/", text, r)
            self.assertIn("@FACTORY_LIVE_HOME@", text, r)

    def test_rendering_with_a_temp_live_home_gives_valid_json(self):
        with tempfile.TemporaryDirectory(prefix="zz-test-render-") as tmp:
            mod = load_factory(REPO)
            tokens = {"@FACTORY_LIVE_HOME@": tmp + "/live", "@FACTORY_HOME_DIR@": tmp, "@FACTORY_USER_CONFIG@": tmp + "/.config"}
            for r in ROLES:
                data = json.loads(mod.settings_text(r, tokens))
                self.assertIn(f"Edit(/{tmp}/live/run/settings/**)", data["permissions"]["deny"], r)
                self.assertNotIn("@FACTORY_", json.dumps(data))
                self.assertNotIn(PW_DIR + "/factory", json.dumps(data), r)

    def test_rendered_rules_equal_the_substituted_template_rule_for_rule(self):
        for r in ROLES:
            template = json.loads(substitute((SETTINGS / f"{r}.json.in").read_text()))
            rendered = json.loads(rendered_settings_dir().joinpath(f"{r}.json").read_text())
            self.assertEqual(rendered, template, r)
            self.assertEqual(rendered["permissions"]["deny"], template["permissions"]["deny"], r)

    def test_render_refuses_a_leftover_token_and_invalid_json(self):
        mod = load_factory(REPO)
        with tempfile.TemporaryDirectory(prefix="zz-test-render-") as tmp:
            for body in ('{"a": "@FACTORY_NOPE@"}', '{"a": '):
                t = Path(tmp) / "x.json.in"
                t.write_text(body)
                with self.assertRaises(SystemExit) as cm:
                    mod.settings_text("coder", template=t)
                self.assertEqual(cm.exception.code, 3)

    def test_every_role_denies_edit_and_write_on_the_rendered_settings(self):
        n = 0
        for r in ROLES:
            d = deny(rendered_settings_dir() / f"{r}.json")
            for tool in ("Edit", "Write"):
                self.assertIn(f"{tool}(/{LIVE_HOME}/run/settings/**)", d, r)
            n += 1
        self.assertEqual(n, len(ROLES))

    def test_worker_roles_deny_the_user_config_dir_and_live_home_file(self):
        n = 0
        for r in WORKERS:
            d = deny(rendered_settings_dir() / f"{r}.json")
            cfg = os.path.join(PW_DIR, ".config", "factory")
            for tool in ("Edit", "Write"):
                for tail in ("", "/**", "/live_home"):
                    self.assertIn(f"{tool}(/{cfg}{tail})", d, f"{r}: {tool} {tail}")
            n += 1
        self.assertEqual(n, 9)

    def test_hook_command_is_an_absolute_path_inside_the_live_checkout(self):
        for r in ROLES:
            data = json.loads(rendered_settings_dir().joinpath(f"{r}.json").read_text())
            groups = data["hooks"].get("PreToolUse", [])
            cmds = [h["command"] for g in groups if g.get("matcher") == "Bash" for h in g["hooks"]]
            guard = [c for c in cmds if "worker-guard.py" in c]
            self.assertEqual(len(guard), 1, r)
            edit = [h["command"] for g in groups if g.get("matcher") != "Bash" for h in g["hooks"]]  # T-0334: workers only
            self.assertEqual(len(edit), 0 if r in ("boss", "director", "lead") else 1, r)
            self.assertTrue(all(f"{LIVE_HOME}/roles/hooks/worker-guard.py" in c for c in edit), r)
            self.assertTrue(guard[0].startswith(f"python3 {LIVE_HOME}/roles/hooks/worker-guard.py"), guard[0])

    def test_hook_entry_point_blocks_and_allows_with_the_rendered_coder_settings(self):
        data = json.loads(rendered_settings_dir().joinpath("coder.json").read_text())
        cmd = data["hooks"]["PreToolUse"][0]["hooks"][0]["command"]
        args = cmd.split("worker-guard.py", 1)[1].split()  # the role flags exactly as the rendered settings pass them
        self.assertEqual(args, ["--role", "coder"])

        def run(command):
            payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": command}})
            return subprocess.run([sys.executable, str(HOOK), *args], input=payload, capture_output=True, text=True, timeout=30)

        self.assertEqual(run("sudo id").returncode, 2)
        self.assertEqual(run("git status").returncode, 0)


class LiveHomePinTests(unittest.TestCase):
    """v4: a live FACTORY_HOME renders only when LIVE_HOME == the running checkout == FACTORY_HOME (fail closed)."""

    def module(self, home, live_home, live_homes=None):
        mod = load_factory(home)
        mod.LIVE_HOME, mod.LIVE_HOMES = str(live_home), set(live_homes or {str(live_home)})
        mod.ROLES_DIR = REPO / "roles"  # the templates are found, so only the pin can stop the render
        return mod

    def render_dies(self, mod, dest):
        err = io.StringIO()
        with contextlib.redirect_stderr(err), self.assertRaises(SystemExit) as cm:
            mod.render_settings("coder", dest)
        self.assertNotEqual(cm.exception.code, 0)
        self.assertIn("refusing to render settings", err.getvalue())

    def test_forged_live_home_dies_and_writes_no_settings_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            forged = Path(tmp, "forged").resolve()
            forged.mkdir()
            mod = self.module(forged, forged)  # live_home names another dir, FACTORY_HOME follows it
            self.render_dies(mod, None)
            self.assertFalse((forged / "run" / "settings").exists())
            self.assertEqual(sorted(p.name for p in forged.rglob("*")), [])

    def test_live_home_equal_to_the_running_checkout_renders(self):
        with tempfile.TemporaryDirectory() as tmp:
            mod = self.module(REPO, REPO)
            dest = mod.render_settings("coder", Path(tmp) / "coder.json")
            hook = json.loads(dest.read_text())["hooks"]["PreToolUse"]
            self.assertIn(f"{REPO}/roles/hooks/worker-guard.py", json.dumps(hook))

    def test_live_home_forged_while_factory_home_is_the_checkout_dies(self):
        with tempfile.TemporaryDirectory() as tmp:
            other = Path(tmp).resolve()
            mod = self.module(REPO, other, {str(REPO), str(other)})
            self.render_dies(mod, other / "coder.json")
            self.assertFalse((other / "coder.json").exists())

    def test_a_temp_factory_home_that_is_not_live_is_not_pinned(self):
        with tempfile.TemporaryDirectory() as tmp:
            other = Path(tmp).resolve()
            mod = self.module(other, REPO, {str(REPO)})  # FACTORY_HOME is not a live home
            self.assertTrue(mod.render_settings("coder", other / "coder.json").is_file())

    def test_factory_home_other_than_the_script_root_dies(self):
        with tempfile.TemporaryDirectory() as tmp:
            other = Path(tmp).resolve()
            mod = self.module(other, REPO, {str(REPO), str(other)})  # FACTORY_HOME is live but is not this checkout
            self.render_dies(mod, other / "coder.json")
            self.assertFalse((other / "coder.json").exists())


class RolesAffectedTests(unittest.TestCase):
    def test_a_changed_template_marks_that_persistent_role_stale_and_others_do_not(self):
        mod = load_factory(REPO)
        self.assertEqual(mod._roles_affected(["roles/settings/boss.json.in"]), {"boss"})
        self.assertEqual(mod._roles_affected(["roles/settings/lead.json.in", "roles/settings/coder.json.in"]), {"lead"})
        self.assertEqual(mod._roles_affected(["roles/settings/coder.json.in"]), set())


class SpawnRenderTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_task(status="todo", pr_url=None)
        self.rendered = self.env.home_dir / "run" / "settings"

    def spawn(self, name):
        return self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", name)

    def start_settings(self, name):
        starts = {c["argv"][2]: c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]}
        argv = starts[name]
        return argv[argv.index("--settings") + 1]

    def test_spawn_passes_the_rendered_file_and_it_is_valid(self):
        self.spawn("zz-test-c1")
        path = self.start_settings("zz-test-c1")
        self.assertEqual(path, str(self.rendered / "coder.json"))
        text = Path(path).read_text()
        self.assertNotIn("@FACTORY_", text)
        self.assertEqual(json.loads(text), json.loads(rendered_settings_dir().joinpath("coder.json").read_text()))
        self.assertEqual(oct(Path(path).stat().st_mode & 0o777), "0o644")

    def test_a_tampered_rendered_file_is_rewritten_at_the_next_spawn(self):
        self.spawn("zz-test-c1")
        target = self.rendered / "coder.json"
        good = target.read_text()
        target.write_text("{}")
        self.env.seed_task("T-0002", status="todo", pr_url=None)
        self.ok("spawn", "coder", "--goal", "G-001", "--task", "T-0002", "--name", "zz-test-c2")
        self.assertEqual(target.read_text(), good)

    def test_a_broken_template_aborts_the_spawn_before_any_pane_is_placed(self):
        roles = self.env.home_dir / "roles"
        roles.unlink()  # a private copy: the symlinked repo roles/ must never be edited
        shutil.copytree(REPO / "roles", roles)
        (roles / "settings" / "coder.json.in").write_text('{"a": "@FACTORY_NOPE@"}')
        r = self.env.run("spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", "zz-test-bad")
        self.assertEqual(r.returncode, 3)
        self.assertIn("unknown @FACTORY_ token", r.stderr)
        placed = [c["argv"] for c in self.env.calls("herdr")
                  if c["argv"][:2] in (["agent", "start"], ["pane", "split"], ["tab", "create"], ["workspace", "create"])]
        self.assertEqual(placed, [])
        self.assertEqual(json.loads(self.env.herdr_state.read_text()).get("panes"), [])
        self.assertEqual(self.env.sql("SELECT count(*) AS n FROM agents WHERE name='zz-test-bad'")[0]["n"], 0)

    def test_init_renders_every_role(self):
        self.assertEqual(sorted(p.name for p in self.rendered.glob("*.json")), sorted(f"{r}.json" for r in ROLES))

    def test_role_detection_from_the_rendered_path_in_a_fake_proc(self):
        self.env.fake_claude(os.getpid(), argv=("claude", "--settings", str(self.rendered / "qa.json"), "--resume", "s"))
        p = self.env.run("tick")
        self.assertEqual(p.returncode, 3, p.stdout + p.stderr)
        self.assertIn("role 'qa'", p.stderr)

    def test_a_claude_without_settings_is_not_a_worker(self):
        self.env.fake_claude(os.getpid(), argv=("claude", "--resume", "s"))
        p = self.env.run("tick")
        self.assertNotIn("may not do this", p.stderr)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_tick_renders_the_legacy_settings_path_rule_for_rule(self):
        legacy = REPO / "roles" / "settings"  # the temp home's roles dir is a symlink to this checkout
        for r in ROLES:
            (legacy / f"{r}.json").unlink(missing_ok=True)
        self.ok("tick")
        for r in ROLES:
            self.assertEqual(json.loads((legacy / f"{r}.json").read_text()),
                             json.loads(rendered_settings_dir().joinpath(f"{r}.json").read_text()), r)
        ignored = subprocess.run(["git", "check-ignore", "roles/settings/coder.json"], cwd=REPO, capture_output=True, text=True)
        self.assertEqual(ignored.stdout.strip(), "roles/settings/coder.json")

    def test_sync_pull_renders_the_legacy_settings_path(self):
        legacy = REPO / "roles" / "settings" / "coder.json"
        legacy.unlink(missing_ok=True)
        with self.env.load_factory() as f:
            origin = self.env.home / "origin.git"
            git("init", "-q", "--bare", "-b", "main", str(origin), cwd=self.env.home)
            git("remote", "add", "origin", str(origin), cwd=self.env.home_dir)
            git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "x", cwd=self.env.home_dir)
            git("push", "-q", "-u", "origin", "main", cwd=self.env.home_dir)
            con = f.db()
            self.assertTrue(f.sync_pull(con, push_ok=False))
        self.assertTrue(legacy.is_file())
        self.assertNotIn("@FACTORY_", legacy.read_text())


class GuardDiffSettingsTests(unittest.TestCase):
    def test_a_removed_template_deny_rule_is_reported_as_loosened(self):
        with tempfile.TemporaryDirectory(prefix="zz-test-gd-") as tmp:
            repo = Path(tmp)
            git("init", "-q", "-b", "main", cwd=repo)
            (repo / "roles" / "hooks").mkdir(parents=True)
            (repo / "roles" / "settings").mkdir()
            (repo / "roles" / "hooks" / "worker-guard.py").write_text((HOOK).read_text())
            tpl = json.loads((SETTINGS / "coder.json.in").read_text())
            (repo / "roles" / "settings" / "coder.json.in").write_text(json.dumps(tpl))
            git("add", "-A", cwd=repo)
            git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base", cwd=repo)
            tpl["permissions"]["deny"].remove("Bash(sqlite3:*)")
            (repo / "roles" / "settings" / "coder.json.in").write_text(json.dumps(tpl))
            corpus = repo / "corpus.json"
            corpus.write_text(json.dumps({"must_block": ["sudo id"], "must_allow": ["git status"]}))
            run = lambda: subprocess.run([sys.executable, str(REPO / "ci" / "guard_diff.py"), "--base", "HEAD", "--repo", str(repo),
                                          "--corpus", str(corpus), "--hook", str(repo / "roles" / "hooks" / "worker-guard.py")],
                                         capture_output=True, text=True, cwd=repo)
            p = run()
            self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
            self.assertIn("SETTINGS-LOOSENED coder deny removed: Bash(sqlite3:*)", p.stdout)
            tpl["permissions"]["deny"].append("Bash(sqlite3:*)")
            (repo / "roles" / "settings" / "coder.json.in").write_text(json.dumps(tpl))
            p = run()
            self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
            self.assertNotIn("SETTINGS-LOOSENED", p.stdout)


if __name__ == "__main__":
    unittest.main()
