"""T-0718: the evaluator's PreToolUse hook must run from a copy inside the trial, because nsbox hides ~/factory.

Group A needs no namespaces: spawn writes <trial>/role/roles/hooks/worker-guard.py and points every hook at it. Group B runs the copy
inside a real allow-list sandbox with the "home" (and the factory checkout in it) hidden, and skips with the reason where
user namespaces are restricted. The old path shows "can't open file" there, which proves the test sees the bug.
"""
GUARD_IMPL_PYTHON_ONLY = "tests the copy of the Python hook that skill-eval spawn writes into a trial; the copy is always Python"
import contextlib
import importlib.util
import io
import json
import os
import shutil
import sqlite3
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import harness  # noqa: E402
from harness import REPO  # noqa: E402
from test_skilleval_sandbox import SandboxBase  # noqa: E402

sys.path.insert(0, str(REPO))
from scout import nsbox  # noqa: E402

UNSHARE = os.environ.get("NSBOX_TEST_UNSHARE", "unshare")
USERNS_OK, USERNS_WHY = nsbox.userns_available(UNSHARE)
SKIP_MSG = f"user namespaces unavailable: {USERNS_WHY}"
GUARD_SRC = REPO / "roles" / "hooks" / "worker-guard.py"
CI_FILES = ("public_text_guard.py", "scrub_scan.py")  # the guard loads the first by path; it loads the second by path


def hook_commands(settings):
    return [h["command"] for groups in settings["hooks"].values() for g in groups for h in g["hooks"]]


class GuardCopyTests(SandboxBase):
    def spawn_eval(self, f, info):
        return f.spawn(f.db(), "evaluator", **self.spawn_kwargs(info))

    def test_spawn_copies_the_guard_and_points_every_hook_at_it(self):
        with self.env.load_factory() as f:
            before = {r: f.settings_text(r) for r in ("coder", "pm", "scout")}
            info = self.build(f.db())
            self.spawn_eval(f, info)
            after = {r: f.settings_text(r) for r in before}
            live_roles = str(f.ROLES_DIR)
        self.assertEqual(before, after)  # non-evaluator settings untouched
        rdir = Path(info["dir"]).resolve() / "role"
        guard = rdir / "roles" / "hooks" / "worker-guard.py"
        st = guard.lstat()
        self.assertTrue(stat.S_ISREG(st.st_mode))  # a regular file, not a symlink
        self.assertFalse(st.st_mode & (stat.S_IWGRP | stat.S_IWOTH))
        self.assertEqual(guard.read_bytes(), GUARD_SRC.read_bytes())
        cmds = hook_commands(json.loads((rdir / "settings.json").read_text()))
        self.assertGreaterEqual(len(cmds), 2)
        for c in cmds:
            self.assertEqual(c, f"python3 {guard} --role evaluator")
            self.assertNotIn(live_roles, c)

    def test_relaunch_recopies_a_tampered_guard(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
            rdir = Path(info["dir"]).resolve() / "role"
            f.evaluator_settings(rdir)
            guard = rdir / "roles" / "hooks" / "worker-guard.py"
            os.chmod(guard, 0o755)
            guard.write_text("import sys; sys.exit(0)\n")
            f.evaluator_settings(rdir)
        self.assertEqual(guard.read_bytes(), GUARD_SRC.read_bytes())
        self.assertEqual(stat.S_IMODE(guard.stat().st_mode), 0o555)

    def test_spawn_copies_the_ci_files_the_public_text_check_loads(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
            self.spawn_eval(f, info)
        rdir = Path(info["dir"]).resolve() / "role"
        for name in CI_FILES:
            dst = rdir / "ci" / name
            st = dst.lstat()
            self.assertTrue(stat.S_ISREG(st.st_mode))  # regular file, not a symlink
            self.assertEqual(stat.S_IMODE(st.st_mode), 0o555)
            self.assertEqual(dst.read_bytes(), (REPO / "ci" / name).read_bytes())

    def test_relaunch_recopies_tampered_ci_files(self):
        with self.env.load_factory() as f:
            rdir = Path(self.build(f.db())["dir"]).resolve() / "role"
            f.evaluator_settings(rdir)
            for name in CI_FILES:
                os.chmod(rdir / "ci" / name, 0o755)
                (rdir / "ci" / name).write_text("raise SystemExit(0)\n")
            f.evaluator_settings(rdir)
        for name in CI_FILES:
            self.assertEqual((rdir / "ci" / name).read_bytes(), (REPO / "ci" / name).read_bytes())
            self.assertEqual(stat.S_IMODE((rdir / "ci" / name).stat().st_mode), 0o555)

    def test_missing_ci_source_dies_naming_the_file(self):
        for missing in CI_FILES:
            with self.subTest(missing=missing):
                fake = Path(tempfile.mkdtemp(prefix="zz-test-fakerepo-")).resolve()
                self.addCleanup(shutil.rmtree, fake, True)
                shutil.copytree(REPO / "roles", fake / "roles", symlinks=True)
                (fake / "ci").mkdir()
                for name in CI_FILES:
                    if name != missing:
                        shutil.copy(REPO / "ci" / name, fake / "ci" / name)
                rdir = fake / "trial" / "role"
                with self.env.load_factory() as f:
                    f.ROLES_DIR = fake / "roles"
                    err = io.StringIO()
                    with contextlib.redirect_stderr(err), self.assertRaises(SystemExit) as cm:
                        f.evaluator_settings(rdir)
                self.assertEqual(cm.exception.code, 3)
                self.assertIn(str(fake / "ci" / missing), err.getvalue())

    def public_text_hook(self, rdir, tmp):
        """Run the copied guard as a qa hook on a git push in a repo whose origin is configured as public."""
        home = tmp / "home"
        (home / "db").mkdir(parents=True)
        con = sqlite3.connect(home / "db" / "factory.db")
        con.execute("CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
        con.execute("INSERT INTO config VALUES ('public_text_repos', 'acme/pub')")
        con.commit()
        con.close()
        repo = tmp / "repo"
        harness.git("init", "-q", "-b", "main", str(repo), cwd=tmp)
        harness.git("remote", "add", "origin", "https://github.com/acme/pub.git", cwd=repo, inert_remote=True)
        env = {k: v for k, v in os.environ.items() if not k.startswith("SCRUB_") and k != "FACTORY_AGENT"}
        env["FACTORY_HOME"] = str(home)
        payload = {"tool_name": "Bash", "tool_input": {"command": "gh pr create --title t --body b"}, "cwd": str(repo)}
        return subprocess.run([sys.executable, str(rdir / "roles" / "hooks" / "worker-guard.py"), "--role", "qa"],
                              input=json.dumps(payload), capture_output=True, text=True, timeout=60, env=env)

    def test_copied_guard_loads_the_public_text_checker(self):
        with self.env.load_factory() as f:
            rdir = Path(self.build(f.db())["dir"]).resolve() / "role"
            f.evaluator_settings(rdir)
        tmp = Path(tempfile.mkdtemp(prefix="zz-test-pubcopy-")).resolve()
        self.addCleanup(shutil.rmtree, tmp, True)
        (tmp / "with").mkdir()
        (tmp / "without").mkdir()
        p = self.public_text_hook(rdir, tmp / "with")
        self.assertNotIn("cannot be loaded", p.stderr)
        self.assertIn("no owner terms loaded", p.stderr)  # the loaded checker ran and refused
        shutil.rmtree(rdir / "ci")  # control: without the ci copy the same payload fails closed
        p = self.public_text_hook(rdir, tmp / "without")
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("public-text-guard cannot be loaded", p.stderr)

    def test_live_homes_of_the_copy_is_the_trial_role_dir_not_trials(self):
        with self.env.load_factory() as f:
            rdir = Path(self.build(f.db())["dir"]).resolve() / "role"
            f.evaluator_settings(rdir)
            doc = f.evaluator_settings.__doc__
        spec = importlib.util.spec_from_file_location("guard_copy_mod", rdir / "roles" / "hooks" / "worker-guard.py")
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        homes = mod.Analyzer(str(rdir), "evaluator").live_homes()
        self.assertIn(str(rdir), homes)
        self.assertNotIn(str(rdir.parent), homes)  # the trial dir
        self.assertNotIn(str(rdir.parent.parent), homes)  # the trials dir
        self.assertIn("mirrors the repo", doc)


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class GuardInSandboxTests(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp(prefix="zz-test-guardcopy-")).resolve()
        self.addCleanup(shutil.rmtree, self.root, True)
        self.home = self.root / "home"
        self.old = self.home / "factory" / "roles" / "hooks" / "worker-guard.py"  # where the hook used to point
        self.old.parent.mkdir(parents=True)
        shutil.copy(GUARD_SRC, self.old)
        self.trial = self.root / "trials" / "t1"
        self.new = self.trial / "role" / "roles" / "hooks" / "worker-guard.py"
        self.new.parent.mkdir(parents=True)
        shutil.copy(GUARD_SRC, self.new)
        os.chmod(self.new, 0o555)
        (self.trial / "fh").mkdir()
        (self.trial / "work").mkdir()

    def hook(self, script, command):
        cfg = nsbox.make_cfg(self.trial, None, self.home, self.root / "trials", mode="allow", unshare=UNSHARE,
                             hide_dirs=[str(self.home)], hide_files=(), cwd=self.trial / "work")
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": command}, "cwd": str(self.trial / "work")})
        env = {"PATH": os.environ["PATH"], "FACTORY_ROLE": "evaluator", "FACTORY_AGENT": "zz-ev",
               "FACTORY_HOME": str(self.trial / "fh"), "HOME": str(self.trial / "home")}
        argv = [sys.executable, str(script), "--role", "evaluator"]
        return subprocess.run(nsbox.compose_stage1(cfg, argv), input=payload, capture_output=True, text=True,
                              env=env, timeout=60)

    def test_old_path_is_hidden_so_the_hook_cannot_start(self):
        p = self.hook(self.old, "factory whoami")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("can't open file", p.stderr)

    def test_copy_allows_an_allowed_command(self):
        for cmd in ("factory whoami", "factory task show T-0001", "git status", "python3 -m unittest discover -s tests"):
            p = self.hook(self.new, cmd)
            self.assertEqual(p.returncode, 0, f"{cmd}: {p.stderr}")

    def test_copy_still_refuses(self):
        for cmd in ("factory config git_push 1","factory spawn coder", "herdr pane list"):
            p = self.hook(self.new, cmd)
            self.assertEqual(p.returncode, 2, f"{cmd}: rc={p.returncode} {p.stderr}")
            self.assertIn("blocked", p.stderr)


if __name__ == "__main__":
    unittest.main()
