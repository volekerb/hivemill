"""T-0718: the evaluator's PreToolUse hook must run from a copy inside the trial, because nsbox hides ~/factory.

Group A needs no namespaces: spawn writes <trial>/role/roles/hooks/worker-guard.py and points every hook at it. Group B runs the copy
inside a real allow-list sandbox with the "home" (and the factory checkout in it) hidden, and skips with the reason where
user namespaces are restricted. The old path shows "can't open file" there, which proves the test sees the bug.
"""
import json
import os
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from harness import REPO  # noqa: E402
from test_skilleval_sandbox import SandboxBase  # noqa: E402

sys.path.insert(0, str(REPO))
from scout import nsbox  # noqa: E402

UNSHARE = os.environ.get("NSBOX_TEST_UNSHARE", "unshare")
USERNS_OK, USERNS_WHY = nsbox.userns_available(UNSHARE)
SKIP_MSG = f"user namespaces unavailable: {USERNS_WHY}"
GUARD_SRC = REPO / "roles" / "hooks" / "worker-guard.py"


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
