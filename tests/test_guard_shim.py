"""T-0353: the sh shim roles/hooks/worker-guard (flag, stale-binary fallback, fail closed, shadow mode) and the two
protected trees it relies on (<live>/run/guard_impl, ~/.local/lib/factory). The shim runs as a subprocess in a temp live
home with a temp HOME and stub hooks; the protected-tree cases go through the real hook (GUARD_IMPL=diff compares Go)."""
import json
import os
import pwd
import shutil
import subprocess
import tempfile
import unittest

import _gitenv
from guard_impl import hook_run

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SHIM = os.path.join(ROOT, "roles", "hooks", "worker-guard")
PW = os.path.realpath(pwd.getpwuid(os.getuid()).pw_dir)
HASH = "a1b2c3d4"
SECRET = "echo SUPERSECRET-command-text-42"
PAYLOAD = json.dumps({"tool_name": "Bash", "tool_input": {"command": SECRET}})
TOOLS = ("cat", "dirname", "sed", "tr", "date", "mkdir", "mktemp", "rm", "cut", "sha256sum", "timeout", "sh", "grep", "sleep", "wc", "kill")


def write(path, text, mode=0o644):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(text)
    os.chmod(path, mode)


class Env:
    """A temp live home (<tmp>/live with roles/hooks/{worker-guard,worker-guard.py}), HOME and a tool-only PATH."""

    def __init__(self, shim_text=None):
        self.tmp = tempfile.mkdtemp(prefix="guard-shim-")
        self.live = os.path.join(self.tmp, "live")
        self.home = os.path.join(self.tmp, "home")
        self.lib = os.path.join(self.home, ".local", "lib", "factory")
        hooks = os.path.join(self.live, "roles", "hooks")
        if shim_text is None:
            with open(SHIM) as f:
                shim_text = f.read()
        write(os.path.join(hooks, "worker-guard"), shim_text, 0o755)
        # the stub "python hook": exit code from STUB_PY_RC, echoes its args and stdin length so passthrough is visible
        write(os.path.join(hooks, "worker-guard.py"),
              "import os, sys\nsys.stdout.write('py-args=' + ' '.join(sys.argv[1:]) + '\\n')\n"
              "sys.stdout.write('py-stdin=' + str(len(sys.stdin.read())) + '\\n')\n"
              "sys.exit(int(os.environ.get('STUB_PY_RC', '0')))\n")
        os.makedirs(self.lib)
        self.bindir = os.path.join(self.tmp, "bin")
        os.makedirs(self.bindir)
        for t in TOOLS:
            p = shutil.which(t)
            if p:
                os.symlink(p, os.path.join(self.bindir, t))
        self.py3 = shutil.which("python3")

    def close(self):
        for dirpath, dirs, _ in os.walk(self.tmp):
            for d in dirs:
                os.chmod(os.path.join(dirpath, d), 0o755)
        shutil.rmtree(self.tmp, ignore_errors=True)

    def flag(self, text):
        write(os.path.join(self.live, "run", "guard_impl"), text)

    def binary(self, body="exit 0", stamp=HASH):
        write(os.path.join(self.lib, "worker-guard"), "#!/bin/sh\n" + body + "\n", 0o755)
        if stamp is not None:
            write(os.path.join(self.lib, "worker-guard.stamp"), stamp + "\n")

    def run(self, args=("--role", "coder"), payload=PAYLOAD, with_python=True, extra=None, shim=None):
        path = self.bindir + (os.pathsep + os.path.dirname(self.py3) if with_python else "")
        env = {"PATH": path, "HOME": self.home, "WORKER_GUARD_SHIM_TRACE": "1", "FACTORY_NOW": "2026-01-02T03:04:05Z"}
        env.update(extra or {})
        return subprocess.run(["sh", shim or os.path.join(self.live, "roles", "hooks", "worker-guard"), *args],
                              input=payload, capture_output=True, text=True, env=env, timeout=30)

    def shadow_log(self):
        p = os.path.join(self.live, "logs", "guard-shadow.log")
        if not os.path.exists(p):
            return ""
        with open(p) as f:
            return f.read()


class ShimBase(unittest.TestCase):
    def setUp(self):
        self.env = Env()
        self.addCleanup(self.env.close)


class PosixTest(ShimBase):
    def test_sh_and_dash_syntax_clean(self):
        self.assertEqual(subprocess.run(["sh", "-n", SHIM]).returncode, 0)
        dash = shutil.which("dash")
        if dash:
            self.assertEqual(subprocess.run([dash, "-n", SHIM]).returncode, 0)

    def test_committed_mode_is_executable(self):
        out = subprocess.run(["git", "ls-files", "-s", "roles/hooks/worker-guard"], cwd=ROOT, capture_output=True,
                             text=True, check=True, env=_gitenv.scrub(os.environ, isolate_config=True)).stdout
        self.assertTrue(out.startswith("100755 "), out)

    def test_shim_runs_no_git_command(self):
        with open(SHIM) as f:
            self.assertEqual(f.read().count("git "), 0)


class FlagTest(ShimBase):
    def impl(self, **kw):
        r = self.env.run(**kw)
        for line in r.stderr.splitlines():
            if line.startswith("impl="):
                return line[5:], r
        self.fail(f"no impl= trace: rc={r.returncode} stderr={r.stderr!r}")

    def test_falls_back_to_python(self):
        self.env.binary()
        cases = {"missing": None, "garbage": "garbage\n", "empty": "", "go without hash": "go\n",
                 "unknown impl": "rust a1b2c3d4\n", "extra field": "go a1b2c3d4 x\n",
                 "two lines": "go a1b2c3d4\npython x\n", "python": "python\n"}
        for name, text in cases.items():
            with self.subTest(name):
                flag = os.path.join(self.env.live, "run", "guard_impl")
                if os.path.exists(flag):
                    os.remove(flag)
                if text is not None:
                    self.env.flag(text)
                self.assertEqual(self.impl()[0], "python")

    def test_unreadable_flag_is_python(self):
        self.env.binary()
        self.env.flag(f"go {HASH}\n")
        flag = os.path.join(self.env.live, "run", "guard_impl")
        os.chmod(flag, 0)
        if os.access(flag, os.R_OK):
            self.skipTest("running as a user that can read mode 000 files")
        self.assertEqual(self.impl()[0], "python")

    def test_go_and_shadow_when_binary_and_stamp_match(self):
        self.env.binary()
        for mode in ("go", "shadow"):
            with self.subTest(mode):
                self.env.flag(f"{mode} {HASH}\n")
                self.assertEqual(self.impl()[0], mode)

    def test_stale_or_missing_binary_is_python(self):
        self.env.flag(f"go {HASH}\n")
        self.env.binary(stamp="deadbeef")
        self.assertEqual(self.impl()[0], "python", "stamp != srchash")
        self.env.binary(stamp=None)
        os.remove(os.path.join(self.env.lib, "worker-guard.stamp"))
        self.assertEqual(self.impl()[0], "python", "no stamp")
        self.env.binary()
        os.chmod(os.path.join(self.env.lib, "worker-guard"), 0o644)
        self.assertEqual(self.impl()[0], "python", "binary not executable")
        os.remove(os.path.join(self.env.lib, "worker-guard"))
        self.assertEqual(self.impl()[0], "python", "binary absent")

    def test_python_mode_passes_args_and_stdin(self):
        r = self.env.run()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("py-args=--role coder", r.stdout)
        self.assertIn(f"py-stdin={len(PAYLOAD)}", r.stdout)

    def test_go_mode_runs_binary_with_py_hook_and_stdin(self):
        self.env.flag(f"go {HASH}\n")
        self.env.binary('echo "go-args=$*"; wc -c | tr -d " " | sed s/^/go-stdin=/; exit 0')
        r = self.env.run()
        self.assertEqual(r.returncode, 0, r.stderr)
        hook = os.path.join(self.env.live, "roles", "hooks", "worker-guard.py")
        self.assertIn(f"go-args=--py-hook {hook} --role coder", r.stdout)
        self.assertNotIn("py-args", r.stdout, "go mode must not run python")


class FailClosedTest(ShimBase):
    def test_python_missing_blocks(self):
        r = self.env.run(with_python=False)
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("worker-guard shim:", r.stderr)

    def test_python_nonzero_statuses_block(self):
        for rc in (1, 3, 126, 127):
            with self.subTest(rc):
                r = self.env.run(extra={"STUB_PY_RC": str(rc)})
                self.assertEqual(r.returncode, 2, r.stderr)
                self.assertIn(f"python failed ({rc}); blocking", r.stderr)

    def test_python_deny_and_allow_pass_through(self):
        self.assertEqual(self.env.run(extra={"STUB_PY_RC": "2"}).returncode, 2)
        self.assertEqual(self.env.run(extra={"STUB_PY_RC": "0"}).returncode, 0)

    def test_go_binary_exit_1_blocks(self):
        self.env.flag(f"go {HASH}\n")
        self.env.binary("exit 1")
        r = self.env.run()
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("worker-guard shim: go failed (1); blocking", r.stderr)

    def test_go_binary_killed_by_sigkill_blocks(self):
        self.env.flag(f"go {HASH}\n")
        self.env.binary("kill -9 $$")
        r = self.env.run()
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("failed (137); blocking", r.stderr)

    def test_go_binary_exit_0_and_2_pass_through(self):
        self.env.flag(f"go {HASH}\n")
        for rc in (0, 2):
            self.env.binary(f"exit {rc}")
            self.assertEqual(self.env.run().returncode, rc)

    def test_c4_without_the_mapping_the_check_would_fail(self):
        """C4: a shim whose `finish` returns the raw status (what exec gives) leaks exit 1: the checks above fail on it."""
        with open(SHIM) as f:
            text = f.read()
        mapping = "    case $2 in\n        0 | 2) exit \"$2\" ;;\n    esac\n"
        self.assertIn(mapping, text)
        bad = Env(text.replace(mapping, "    exit \"$2\"\n"))
        self.addCleanup(bad.close)
        bad.flag(f"go {HASH}\n")
        bad.binary("exit 1")
        self.assertEqual(bad.run().returncode, 1, "the unmapped shim fails open; test_go_binary_exit_1_blocks catches it")


class ShadowTest(ShimBase):
    def setUp(self):
        super().setUp()
        self.env.flag(f"shadow {HASH}\n")

    def test_disagreement_returns_python_and_logs_hash_only(self):
        self.env.binary("exit 2")  # python says allow (0), go says block (2)
        r = self.env.run(extra={"STUB_PY_RC": "0"})
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("py-args=--role coder", r.stdout)
        log = self.env.shadow_log()
        self.assertEqual(len(log.splitlines()), 1, log)
        row = json.loads(log)
        self.assertEqual(sorted(row), ["go", "py", "role_args", "sha256", "ts"])
        self.assertEqual((row["py"], row["go"]), (0, 2))
        self.assertEqual(row["role_args"], "--role coder")
        self.assertEqual(row["ts"], "2026-01-02T03:04:05Z")
        self.assertRegex(row["sha256"], r"^[0-9a-f]{64}$")
        self.assertNotIn(SECRET, log)
        self.assertNotIn("SUPERSECRET", log)

    def test_python_block_is_authoritative_when_go_allows(self):
        self.env.binary("exit 0")
        r = self.env.run(extra={"STUB_PY_RC": "2"})
        self.assertEqual(r.returncode, 2)
        self.assertEqual(json.loads(self.env.shadow_log())["go"], 0)

    def test_agreement_logs_nothing(self):
        self.env.binary("exit 0")
        self.assertEqual(self.env.run().returncode, 0)
        self.assertEqual(self.env.shadow_log(), "")

    def test_go_reads_the_same_payload(self):
        self.env.binary("grep -q SUPERSECRET && exit 0; exit 2")
        self.env.run()
        self.assertEqual(self.env.shadow_log(), "", "go saw the payload, so it agreed with python")

    def test_go_hang_is_bounded_and_logged(self):
        self.env.binary("sleep 30")
        r = self.env.run()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(json.loads(self.env.shadow_log())["go"], 124)

    def test_factory_home_picks_the_log_dir(self):
        self.env.binary("exit 2")
        other = os.path.join(self.env.tmp, "fh")
        self.env.run(extra={"FACTORY_HOME": other})
        self.assertTrue(os.path.exists(os.path.join(other, "logs", "guard-shadow.log")))

    def test_python_failure_in_shadow_still_blocks(self):
        self.env.binary("exit 0")
        r = self.env.run(extra={"STUB_PY_RC": "1"})
        self.assertEqual(r.returncode, 2)
        self.assertIn("failed (1); blocking", r.stderr)

    def test_temp_payload_file_is_removed(self):
        self.env.binary("exit 2")
        tdir = os.path.join(self.env.tmp, "t")
        os.makedirs(tdir)
        self.env.run(extra={"TMPDIR": tdir})
        self.assertEqual(os.listdir(tdir), [])


def hook(cmd, role="coder"):
    return hook_run(json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": ROOT}), ["--role", role])


LIB = f"{PW}/.local/lib/factory"
IMPL = f"{PW}/factory/run/guard_impl"
BLOCKED = [
    f"cp /tmp/x {LIB}/worker-guard",
    f"cp /tmp/x {LIB}/worker-guard.stamp",
    f"echo go > {LIB}/worker-guard.stamp",
    f"echo x >> {LIB}/worker-guard",
    f"mv /tmp/x {LIB}/worker-guard",
    f"ln -sf /tmp/x {LIB}/worker-guard",
    f"rm {LIB}/worker-guard.stamp",
    f"rm -r {LIB}",
    f"echo x | tee {LIB}/worker-guard.stamp",
    f"cd {LIB} && echo x > worker-guard.stamp",
    f"echo go > {IMPL}",
    f"cp /tmp/x {IMPL}",
    f"rm {IMPL}",
    f"mv /tmp/x {IMPL}",
    f"sed -i s/python/go/ {IMPL}",
    f"python3 -c \"open('{IMPL}','w').write('go x')\"",
    f"python3 -c \"open('{LIB}/worker-guard','w')\"",
    f"cd {PW}/factory/run && echo go > guard_impl",
    f"echo go > {PW}/.config/factory/guard_impl",
]
ALLOWED = [f"ls {LIB}", f"cat {LIB}/worker-guard.stamp", f"cat {IMPL}", f"cp {LIB}/worker-guard /tmp/copy"]


class ProtectedTreesTest(unittest.TestCase):
    def test_writes_into_the_flag_and_the_binary_tree_are_blocked(self):
        for cmd in BLOCKED:
            r = hook(cmd)
            self.assertEqual(r.returncode, 2, f"{cmd}: {r.stderr}")

    def test_reads_stay_allowed(self):
        for cmd in ALLOWED:
            r = hook(cmd)
            self.assertEqual(r.returncode, 0, f"{cmd}: {r.stderr}")

    def test_spec_acceptance_6_cases(self):
        for cmd, rc in (("cp /tmp/x ~/.local/lib/factory/worker-guard", 2), (f"echo go > {IMPL}", 2),
                        ("echo go > ~/.config/factory/guard_impl", 2), ("ls ~/.local/lib/factory", 0)):
            self.assertEqual(hook(cmd).returncode, rc, cmd)


if __name__ == "__main__":
    unittest.main()
