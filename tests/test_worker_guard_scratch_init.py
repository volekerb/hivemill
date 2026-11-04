"""T-0331: a coder or qa may run `factory init`, `factory project add` and `bash systemd/install.sh --dry-run` only when
HOME and FACTORY_HOME both resolve (realpath) inside the worker's own scratchpad. Every case goes through the real entry
point (guard_impl.hook_run: the hook as a subprocess, and with GUARD_IMPL=diff the Go binary too). The scratchpad is a temp
dir named by WORKER_GUARD_SCRATCH and the live checkout a temp dir named by WORKER_GUARD_LIVE_HOME."""
import json
import os
import shutil
import subprocess
import tempfile
import unittest

from guard_impl import ROOT, hook_run

_tmp = []
INSTALL = os.path.join(ROOT, "systemd", "install.sh")


def setUpModule():
    root = os.path.realpath(tempfile.mkdtemp(prefix="scratch-init-"))
    _tmp.append(root)
    for d in ("scratch/h", "scratch/f", "live/systemd", "wt/systemd", "wt2/systemd", "outside", "other/scratchpad/f"):
        os.makedirs(os.path.join(root, d))
    os.symlink(os.path.join(root, "outside"), os.path.join(root, "scratch", "esc"))
    for d in ("live", "wt"):  # the worktree's install.sh is the live one
        shutil.copy(INSTALL, os.path.join(root, d, "systemd", "install.sh"))
    edited = os.path.join(root, "wt2", "systemd", "install.sh")  # an edited copy
    shutil.copy(INSTALL, edited)
    with open(edited, "ab") as f:
        f.write(b"\necho edited\n")


def tearDownModule():
    for p in _tmp:
        shutil.rmtree(p, ignore_errors=True)


def run(cmd, role="coder", cwd="wt", extra_env=None):
    root = _tmp[0]
    cmd = cmd.replace("@S@", root + "/scratch").replace("@R@", root).replace("@L@", os.path.join(os.path.expanduser("~"), "factory"))
    env = {**os.environ, "WORKER_GUARD_SCRATCH": root + "/scratch", "WORKER_GUARD_LIVE_HOME": root + "/live",
           **(extra_env or {})}
    payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": os.path.join(root, cwd)}
    return hook_run(json.dumps(payload), ["--role", role], env=env, cwd=root)


H = "HOME=@S@/h FACTORY_HOME=@S@/f"
ALLOWED = ("factory init", "factory project add demo @S@/repo", "bash systemd/install.sh --dry-run")


class Allowed(unittest.TestCase):
    def test_three_commands_in_scratch_for_coder_and_qa(self):
        for role in ("coder", "qa"):
            for c in ALLOWED:
                r = run(f"{H} {c}", role)
                self.assertEqual(r.returncode, 0, f"{role}: {c}: {r.stderr}")

    def test_exported_homes_for_coder(self):
        r = run("export HOME=@S@/h FACTORY_HOME=@S@/f; factory init")
        self.assertEqual(r.returncode, 0, r.stderr)
        r = run("export HOME=@S@/h; export FACTORY_HOME=@S@/f; bash systemd/install.sh --dry-run")
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_other_commands_unchanged(self):
        for c in ("factory inbox", "factory whoami", "factory task show T-0331", "ls -l"):
            self.assertEqual(run(c, "qa").returncode, 0, c)


class Blocked(unittest.TestCase):
    def blocked(self, cmds, roles=("coder", "qa")):
        for role in roles:
            for c in cmds:
                r = run(c, role)
                self.assertEqual(r.returncode, 2, f"{role}: expected a block for {c!r}, got {r.returncode}")

    def test_homes_not_both_in_scratch(self):
        for c in ("factory init", "factory project add demo /tmp/x"):
            self.blocked([
                c,  # nothing set
                f"HOME=@S@/h {c}",  # FACTORY_HOME unset
                f"FACTORY_HOME=@S@/f {c}",  # HOME is not set in the command
                f"HOME=@S@/h FACTORY_HOME=@L@ {c}",  # live FACTORY_HOME
                f"HOME=@L@ FACTORY_HOME=@S@/f {c}",  # live HOME
                f"HOME=@S@/h FACTORY_HOME=f {c}",  # relative
                f"HOME=@S@/h FACTORY_HOME=./f {c}",
                f"HOME=@S@/h FACTORY_HOME=@S@/../outside {c}",  # `..` escape
                f"HOME=@S@/h FACTORY_HOME=@S@/esc/f {c}",  # symlink out of the scratchpad
                f"HOME=@S@/esc FACTORY_HOME=@S@/f {c}",
                f"HOME=@S@/h FACTORY_HOME=@R@/other/scratchpad/f {c}",  # another agent's scratchpad
                f"HOME=@S@/h FACTORY_HOME=$X {c}",  # dynamic
                f"HOME=@S@/h FACTORY_HOME=~/f {c}",
            ])

    def test_init_live_home_and_unlisted_commands(self):
        self.blocked([f"{H} factory init --live-home @S@/x", f"{H} factory init --live",
                      "HOME=@S@/h factory up", "HOME=@S@/h factory spawn pm", "factory tick"])

    def test_install_sh_forms(self):
        self.blocked([f"{H} bash systemd/install.sh", f"{H} bash systemd/install.sh --dry-run --with-serve",
                      f"HOME=@S@/h bash systemd/install.sh --dry-run",
                      f"HOME=@S@/h FACTORY_HOME=@L@ bash systemd/install.sh --dry-run",
                      f"HOME=@S@/h FACTORY_HOME=@S@/esc bash systemd/install.sh --dry-run"])

    def test_edited_install_sh_is_still_inspected(self):
        r = run(f"{H} bash systemd/install.sh --dry-run", "coder", cwd="wt2")
        self.assertEqual(r.returncode, 2, r.stderr)

    def test_install_sh_without_a_live_copy_is_inspected(self):
        r = run(f"{H} bash systemd/install.sh --dry-run", "coder", extra_env={"WORKER_GUARD_LIVE_HOME": _tmp[0] + "/outside"})
        self.assertEqual(r.returncode, 2, r.stderr)

    def test_evaluator_and_other_roles_stay_blocked(self):
        for c in ALLOWED:
            r = run(f"{H} {c}", "evaluator")
            self.assertEqual(r.returncode, 2, f"evaluator: {c}")
        for c in ("export HOME=@S@/h; factory init",):  # qa may not export HOME (the git-config rule)
            self.assertEqual(run(c, "qa").returncode, 2, c)


class InstallDryRunWritesNothing(unittest.TestCase):
    """The allowed `bash systemd/install.sh --dry-run` run for real (not through the hook), HOME and FACTORY_HOME in a temp
    dir: the file listing of both is the same before and after, and the units are only echoed."""

    def listing(self, *roots):
        out = []
        for r in roots:
            for dp, dn, fn in os.walk(r):
                out += [os.path.join(dp, x) for x in dn + fn]
        return sorted(out)

    def test_listing_unchanged(self):
        home, fh = os.path.join(_tmp[0], "scratch", "h"), os.path.join(_tmp[0], "scratch", "f")
        before = self.listing(home, fh)
        env = {"PATH": os.environ["PATH"], "HOME": home, "FACTORY_HOME": fh, "USER": "tester"}
        p = subprocess.run(["bash", INSTALL, "--dry-run"], env=env, capture_output=True, text=True, timeout=60, cwd=_tmp[0])
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.listing(home, fh), before)
        self.assertIn("[dry-run] mkdir -p " + os.path.join(home, ".config", "systemd", "user"), p.stdout)
        self.assertIn("[dry-run] systemctl --user enable factory-tick.timer", p.stdout)
        self.assertFalse(os.path.exists(os.path.join(home, ".config")))


if __name__ == "__main__":
    unittest.main()
