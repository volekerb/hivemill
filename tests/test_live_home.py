"""T-0212: the live factory home is derived from the passwd entry (bin/factory and roles/hooks/worker-guard.py),
never from $HOME, $XDG_CONFIG_HOME or the checkout the script runs from. <pw_dir>/.config/factory/live_home can only
ADD a live home. The seam is an in-process patch of pwd.getpwuid; there is no env-var seam and no real home is touched."""
import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
import pwd
import runpy
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FACTORY = os.path.join(ROOT, "bin", "factory")
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")

# runs the hook (or any script) as __main__ with pwd.getpwuid patched, the way a test imports it in-process
WRAPPER = """
import os, pwd, runpy, sys
fake = pwd.struct_passwd(("u", "x", os.getuid(), os.getgid(), "", sys.argv[1], "/bin/sh"))
pwd.getpwuid = lambda uid: fake
script = sys.argv[2]
sys.argv = [script] + sys.argv[3:]
runpy.run_path(script, run_name="__main__")
"""


def fake_pw(pw_dir):
    return lambda uid: pwd.struct_passwd(("u", "x", uid, os.getgid(), "", str(pw_dir), "/bin/sh"))


def load_factory(pw_dir, env=None):
    with mock.patch.object(pwd, "getpwuid", fake_pw(pw_dir)), mock.patch.dict(os.environ, env or {}):
        loader = importlib.machinery.SourceFileLoader("factory_live_home_test", FACTORY)
        mod = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
        loader.exec_module(mod)
    return mod


def load_hook(pw_dir):
    with mock.patch.object(pwd, "getpwuid", fake_pw(pw_dir)):
        spec = importlib.util.spec_from_file_location("worker_guard_live_home_test", HOOK)
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
    return mod


class Tmp(unittest.TestCase):
    def setUp(self):
        self.tmp = os.path.realpath(tempfile.mkdtemp(prefix="zz-live-home-"))
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.h = os.path.join(self.tmp, "h")  # the patched passwd home
        self.cfg = os.path.join(self.h, ".config", "factory")
        os.makedirs(self.cfg)
        self.file = os.path.join(self.cfg, "live_home")

    def write_file(self, text, mode=0o644):
        with open(self.file, "w") as f:
            f.write(text)
        os.chmod(self.file, mode)

    def run_cli(self, argv, env):
        """bin/factory through runpy as __main__ with pwd.getpwuid patched; (exit code, stdout, stderr)."""
        out, err = io.StringIO(), io.StringIO()
        full = {"HOME": os.path.join(self.tmp, "userhome"), **env}
        keep = {k: v for k, v in os.environ.items() if k not in ("FACTORY_ROLE", "FACTORY_HOME", "FACTORY_AGENT", "HERDR_PANE_ID", "CLAUDECODE")}
        code = 0
        with mock.patch.object(pwd, "getpwuid", fake_pw(self.h)), mock.patch.dict(os.environ, keep, clear=True), \
                mock.patch.dict(os.environ, full), mock.patch.object(sys, "argv", ["factory", *argv]), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                runpy.run_path(FACTORY, run_name="__main__")
            except SystemExit as e:
                code = e.code or 0
        return code, out.getvalue(), err.getvalue()


class DeriveTests(Tmp):
    LOADERS = (("bin/factory", lambda t: load_factory(t.h)), ("hook", lambda t: load_hook(t.h)))

    def derive(self, which, uid=None):
        with mock.patch.object(pwd, "getpwuid", fake_pw(self.h)):
            mod = which(self)
            return mod.derive_live_homes(uid)

    def test_missing_file_only_the_default(self):
        for name, which in self.LOADERS:
            with self.subTest(name):
                mod = which(self)
                self.assertEqual(mod.LIVE_HOMES, {os.path.join(self.h, "factory")})
                self.assertEqual(mod.LIVE_HOME, os.path.join(self.h, "factory"))

    def test_valid_file_only_adds(self):
        custom = os.path.join(self.tmp, "custom")
        self.write_file(custom + "\n")
        for name, which in self.LOADERS:
            with self.subTest(name):
                mod = which(self)
                self.assertEqual(mod.LIVE_HOMES, {os.path.join(self.h, "factory"), custom})
                self.assertEqual(mod.LIVE_HOME, custom)

    def assert_ignored(self, why):
        for name, which in self.LOADERS:
            with self.subTest(f"{name}: {why}"):
                mod = which(self)
                self.assertEqual(mod.LIVE_HOMES, {os.path.join(self.h, "factory")})
                self.assertEqual(mod.LIVE_HOME, os.path.join(self.h, "factory"))

    def test_symlink_file_ignored(self):
        real = os.path.join(self.tmp, "real_live_home")
        with open(real, "w") as f:
            f.write(os.path.join(self.tmp, "custom") + "\n")
        os.symlink(real, self.file)
        self.assert_ignored("symlink")

    def test_symlink_parent_ignored(self):
        other = os.path.join(self.tmp, "otherdir")
        os.makedirs(other)
        with open(os.path.join(other, "live_home"), "w") as f:
            f.write(os.path.join(self.tmp, "custom") + "\n")
        shutil.rmtree(self.cfg)
        os.symlink(other, self.cfg)
        self.assert_ignored("parent symlink")

    def test_group_or_other_writable_ignored(self):
        for mode in (0o664, 0o646):
            self.write_file(os.path.join(self.tmp, "custom") + "\n", mode)
            self.assert_ignored(f"mode {mode:o}")

    def test_foreign_uid_ignored(self):
        self.write_file(os.path.join(self.tmp, "custom") + "\n")
        for name, which in self.LOADERS:
            with self.subTest(name):
                mod = which(self)
                with mock.patch.object(pwd, "getpwuid", fake_pw(self.h)):
                    _, homes = mod.derive_live_homes(uid=os.getuid() + 1)  # the file is owned by us, not by that uid
                self.assertEqual(homes, {os.path.join(self.h, "factory")})

    def test_relative_path_two_lines_empty_ignored(self):
        for text in ("custom\n", "relative/dir", "/abs/one\n/abs/two\n", "", "\n"):
            self.write_file(text)
            self.assert_ignored(repr(text))

    def test_env_never_decides(self):
        """HOME / XDG_CONFIG_HOME pointing at a planted file change nothing: only the passwd home is read."""
        fake = os.path.join(self.tmp, "fake")
        os.makedirs(os.path.join(fake, ".config", "factory"))
        with open(os.path.join(fake, ".config", "factory", "live_home"), "w") as f:
            f.write(os.path.join(fake, "factory") + "\n")
        env = {"HOME": fake, "XDG_CONFIG_HOME": os.path.join(fake, ".config")}
        for name, which in self.LOADERS:
            with self.subTest(name), mock.patch.dict(os.environ, env):
                self.assertEqual(which(self).LIVE_HOMES, {os.path.join(self.h, "factory")})


class CliTests(Tmp):
    def other_home(self):
        home = os.path.join(self.tmp, "other")
        os.makedirs(home)
        code, _, err = self.run_cli(["init"], {"FACTORY_HOME": home})
        self.assertEqual(code, 0, err)
        return home

    REFUSAL = "may not use the live factory home"

    def test_evaluator_refused_in_the_live_home(self):
        code, _, err = self.run_cli(["goal", "list"], {"FACTORY_ROLE": "evaluator", "FACTORY_HOME": os.path.join(self.h, "factory")})
        self.assertEqual(code, 3)
        self.assertIn(self.REFUSAL, err)

    def test_evaluator_passes_in_a_temp_home(self):
        home = self.other_home()
        _, _, err = self.run_cli(["goal", "list"], {"FACTORY_ROLE": "evaluator", "FACTORY_HOME": home})
        self.assertNotIn(self.REFUSAL, err)

    def test_spoofed_home_and_xdg_do_not_help(self):
        fake = os.path.join(self.tmp, "fake")
        os.makedirs(os.path.join(fake, ".config", "factory"))
        with open(os.path.join(fake, ".config", "factory", "live_home"), "w") as f:
            f.write(os.path.join(fake, "factory") + "\n")
        spoof = {"HOME": fake, "XDG_CONFIG_HOME": os.path.join(fake, ".config")}
        live = os.path.join(self.h, "factory")
        code, _, err = self.run_cli(["goal", "list"], {"FACTORY_ROLE": "evaluator", "FACTORY_HOME": live, **spoof})
        self.assertEqual(code, 3)
        self.assertIn(self.REFUSAL, err)
        with mock.patch.dict(os.environ, spoof):
            mod = load_factory(self.h, {"FACTORY_HOME": live, **spoof})
        self.assertEqual(mod.git_file_protocol(), "never")
        mod.herdr_is_test_stub = lambda: True  # a test-stub herdr, but the home is live: no exemption
        self.assertFalse(mod.worker_stub_exempt())

    def test_file_only_adds(self):
        custom = os.path.join(self.tmp, "custom")
        self.write_file(custom + "\n")
        for home in (os.path.join(self.h, "factory"), custom):
            with self.subTest(home):
                code, _, err = self.run_cli(["goal", "list"], {"FACTORY_ROLE": "evaluator", "FACTORY_HOME": home})
                self.assertEqual(code, 3)
                self.assertIn(self.REFUSAL, err)

    def test_init_live_home_writes_once(self):
        home, custom = self.other_home(), os.path.join(self.tmp, "custom")
        shutil.rmtree(self.cfg)
        env = {"FACTORY_HOME": home}
        code, out, err = self.run_cli(["init", "--live-home", custom], env)
        self.assertEqual(code, 0, err)
        self.assertIn(custom, out)
        with open(self.file) as f:
            self.assertEqual(f.read(), custom + "\n")
        before = os.stat(self.file).st_mtime_ns
        code, out, err = self.run_cli(["init", "--live-home", custom], env)
        self.assertEqual(code, 0, err)
        self.assertIn("nothing changed", out)
        self.assertEqual(os.stat(self.file).st_mtime_ns, before)
        with open(self.file) as f:
            self.assertEqual(f.read(), custom + "\n")
        code, _, err = self.run_cli(["init", "--live-home", os.path.join(self.tmp, "else")], env)
        self.assertEqual(code, 2)
        self.assertIn("refusing to replace", err)
        with open(self.file) as f:
            self.assertEqual(f.read(), custom + "\n")
        self.assertEqual(load_factory(self.h).LIVE_HOMES, {os.path.join(self.h, "factory"), custom})

    def test_init_live_home_refusals(self):
        home = self.other_home()
        shutil.rmtree(self.cfg)
        custom = os.path.join(self.tmp, "custom")
        code, _, err = self.run_cli(["init", "--live-home", custom], {"FACTORY_HOME": home, "FACTORY_ROLE": "coder"})
        self.assertEqual(code, 3)
        self.assertIn("may not be used by an agent", err)
        code, _, err = self.run_cli(["init", "--live-home", "relative/dir"], {"FACTORY_HOME": home})
        self.assertEqual(code, 2)
        self.assertIn("absolute path", err)
        self.assertFalse(os.path.exists(self.file))

    def test_init_without_the_flag_never_writes_the_file(self):
        home = self.other_home()
        shutil.rmtree(self.cfg)
        code, _, err = self.run_cli(["init"], {"FACTORY_HOME": home})
        self.assertEqual(code, 0, err)
        self.assertFalse(os.path.exists(self.file))
        self.assertFalse(os.path.exists(self.cfg))


class HookTests(Tmp):
    def run_hook(self, cmd, role="coder", pw_dir=None):
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.tmp})
        env = {k: v for k, v in os.environ.items() if k not in ("FACTORY_HOME", "FACTORY_ROLE")}
        return subprocess.run([sys.executable, "-c", WRAPPER, pw_dir or self.h, HOOK, "--role", role], input=payload,
                              capture_output=True, text=True, timeout=30, env=env)

    def blocked(self, cmd, role="coder"):
        p = self.run_hook(cmd, role)
        self.assertEqual(p.returncode, 2, f"should block ({role}): {cmd!r}\n{p.stderr}")
        return p

    def allowed(self, cmd, role="coder"):
        p = self.run_hook(cmd, role)
        self.assertEqual(p.returncode, 0, f"should allow ({role}): {cmd!r}\n{p.stderr}")

    def test_live_home_blocked_temp_home_allowed(self):
        live = os.path.join(self.h, "factory")
        self.assertIn("broad factory reads", self.blocked(f"FACTORY_HOME={live} factory res").stderr)
        self.allowed(f"FACTORY_HOME={os.path.join(self.tmp, 'other')} factory res")

    def test_file_added_home_is_live_too(self):
        custom = os.path.join(self.tmp, "custom")
        self.write_file(custom + "\n")
        self.blocked(f"FACTORY_HOME={custom} factory res")
        self.allowed(f"FACTORY_HOME={os.path.join(self.tmp, 'other')} factory res")

    def test_hook_checkout_root_stays_live(self):
        self.blocked(f"FACTORY_HOME={ROOT} factory res")

    def test_bash_writes_to_the_config_dir_blocked(self):
        c = self.cfg
        for role in ("coder", "qa"):
            for cmd in (f"echo /tmp/x > {c}/live_home", f"echo /tmp/x > ~/.config/factory/live_home",
                        f"cp /tmp/f {c}/live_home", "cp /tmp/f ~/.config/factory/live_home",
                        f"rm {c}/live_home", "rm ~/.config/factory/live_home",
                        f"sed -i s/a/b/ {c}/live_home", "sed -i s/a/b/ ~/.config/factory/live_home",
                        f"python3 -c \"open('{c}/live_home','w')\"",
                        f"touch {c}/live_home", "touch ~/.config/factory/live_home",
                        "mkdir -p ~/.config/factory", f"mkdir -p {c}",
                        "cd ~/.config/factory && echo x > live_home", f"cd {c} && echo x > live_home",
                        f"cd {c}; touch live_home",
                        "rm -r ~/.config/factory", "rm -r ~/.config",
                        "mv ~/.config/factory /tmp/y", f"mv {c} /tmp/y",
                        "ln -sfn /tmp/x ~/.config/factory", f"ln -s /tmp/x {c}/live_home",
                        "echo x | tee ~/.config/factory/live_home", "echo x >> $HOME/.config/factory/live_home",
                        "bash -c 'echo x > ~/.config/factory/live_home'", f"truncate -s 0 {c}/live_home",
                        "factory init --live-home /tmp/x", "factory init --live /tmp/x", "factory init --live-home=/tmp/x",
                        "python3 bin/factory init --live-home /tmp/x",
                        f"echo x &> {c}/live_home", "echo x &> ~/.config/factory/live_home",
                        "echo x >& ~/.config/factory/live_home", "echo x &>> ~/.config/factory/live_home",
                        "perl -e 'unlink q(/home/u/.config/factory/live_home)'",
                        "perl -e 'unlink glob(\"~/.config/factory/live_home\")'",
                        "perl -e 'open(F, \">\", \"/home/u/.config/factory/live_home\")'",
                        f"perl -e 'open(F,\">{c}/live_home\")'"):
                with self.subTest(f"{role}: {cmd}"):
                    self.assertIn("worker-guard", self.blocked(cmd, role).stderr)

    def test_reads_and_unrelated_writes_allowed(self):
        c = self.cfg
        for role in ("coder", "qa"):
            for cmd in (f"cat {c}/live_home", "cat ~/.config/factory/live_home", "grep . ~/.config/factory/live_home",
                        f"cd {c} && cat live_home", f"ls {c}", "echo hi > /tmp/x", "touch ~/notes.txt", "mv /tmp/a ~/",
                        "rm ~/old.txt", "cp ~/.config/factory/live_home /tmp/copy", "factory init",
                        "sed -i s/a/b/ /tmp/f", "mkdir -p ~/.config/other",
                        "echo hi > /tmp/x 2>&1", "echo hi >&2", "echo hi 2>&-", "echo hi &> /tmp/x",
                        "perl -e 'print 1'", "perl -e 'unlink q(/tmp/x)'"):
                with self.subTest(f"{role}: {cmd}"):
                    self.allowed(cmd, role)


if __name__ == "__main__":
    unittest.main()
