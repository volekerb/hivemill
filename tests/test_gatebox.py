"""scout/gatebox.py (T-0632, GS1): the gate sandbox profile over nsbox allow mode, with real unshare.

Group A needs no namespaces. Group B runs real namespaces and skips with the reason only where they are unavailable.
Everything lives in temp dirs; the credentials, sockets and listener hidden from inside are fake ones made here.
The temp dir is under /var/tmp, not /tmp; /var/tmp is hidden by the profile too (T-0652), so the base fixture patches
HIDE_TMP back to /tmp and /dev/shm and the fake HOME is hidden by the profile's own HOME rule. VarTmpTests uses the real list.
"""
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
from harness import REPO  # noqa: E402
import harness

sys.path.insert(0, str(REPO))
from scout import gatebox, nsbox  # noqa: E402

USERNS_OK, USERNS_WHY = nsbox.userns_available()
SKIP_MSG = f"user namespaces unavailable: {USERNS_WHY}"
PY = sys.executable
GIT_ENV = {"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.invalid", "GIT_COMMITTER_NAME": "t",
           "GIT_COMMITTER_EMAIL": "t@example.invalid", "PATH": os.environ["PATH"]}


def git(*args, cwd=None):
    return harness.git(*args, cwd=cwd, env=GIT_ENV)


class Fixture(unittest.TestCase):
    fixture_hide_tmp = ("/tmp", "/dev/shm")  # the fixture root is under /var/tmp, see the module docstring

    def setUp(self):
        base = "/var/tmp" if os.access("/var/tmp", os.W_OK) else None
        self.root = Path(tempfile.mkdtemp(prefix="zz-test-gatebox-", dir=base)).resolve()
        self.addCleanup(shutil.rmtree, self.root, True)
        r = self.root
        self.home, self.runtime, self.main, self.wt = r / "home", r / "run", r / "main", r / "wt"
        for p, body in ((".claude/.credentials.json", "FAKE-CLAUDE"), (".config/gh/hosts.yml", "FAKE-GH"),
                        (".ssh/id_ed25519", "FAKE-SSH")):
            (self.home / p).parent.mkdir(parents=True, exist_ok=True)
            (self.home / p).write_text(body)
        self.runtime.mkdir()
        self.bus = socket.socket(socket.AF_UNIX)
        self.bus.bind(str(self.runtime / "bus"))
        self.bus.listen(1)
        self.addCleanup(self.bus.close)
        self.tcp = socket.socket()
        self.tcp.bind(("127.0.0.1", 0))
        self.tcp.listen(1)
        self.addCleanup(self.tcp.close)
        self.port = self.tcp.getsockname()[1]
        # the git common dir lives OUTSIDE main (like a repo whose worktrees are checked out into FACTORY_MAIN)
        self.repo = r / "repo"
        self.repo.mkdir()
        git("init", "-q", "-b", "main", cwd=self.repo)
        (self.repo / "f.txt").write_text("x\n")
        git("add", ".", cwd=self.repo)
        git("commit", "-qm", "init", cwd=self.repo)
        self.sha = git("rev-parse", "HEAD", cwd=self.repo)
        (self.main / "tests").mkdir(parents=True)
        (self.main / "tests/gate_shards.py").write_text("SHARDS = 1\n")
        for d in gatebox.MASKED:
            (self.main / d).mkdir()
        (self.main / "db/factory.db").write_text("LIVE-DB")
        self.tree = self.main / "worktrees/gate-tree"
        git("worktree", "add", "-q", "--detach", str(self.tree), self.sha, cwd=self.repo)
        self.env = {"PATH": os.environ["PATH"], "LANG": "C.UTF-8", "TERM": "dumb", "GH_TOKEN": "tok", "SSH_AUTH_SOCK": "/x",
                    "FACTORY_AGENT": "coder-x", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/x", "LC_ALL": "C.UTF-8",
                    "GATE_SHARDS": "3"}
        penv = mock.patch.dict(os.environ, {"XDG_RUNTIME_DIR": str(self.runtime)})
        penv.start()
        self.addCleanup(penv.stop)
        if self.fixture_hide_tmp:
            ph = mock.patch.object(gatebox, "HIDE_TMP", self.fixture_hide_tmp)
            ph.start()
            self.addCleanup(ph.stop)

    def build(self, cmd="true", **kw):
        return gatebox.build(cmd, str(self.tree), str(self.main), str(self.wt), self.env, home=str(self.home), **kw)

    def run_in(self, cmd, env=None, **kw):
        saved, self.env = self.env, env or self.env
        try:
            return gatebox.run(cmd, str(self.tree), str(self.main), str(self.wt), self.env, home=str(self.home), **kw)
        finally:
            self.env = saved


class ProfileTests(Fixture):
    def setUp(self):
        super().setUp()
        # pure argv/env tests must pass on runners without user namespaces; the unavailable case patches it back below
        pns = mock.patch.object(nsbox, "userns_available", return_value=(True, ""))
        pns.start()
        self.addCleanup(pns.stop)

    def test_env_is_an_allow_list(self):
        _, env = self.build()
        for k in ("GH_TOKEN", "SSH_AUTH_SOCK", "FACTORY_AGENT", "DBUS_SESSION_BUS_ADDRESS"):
            self.assertNotIn(k, env)
        self.assertEqual(env["HOME"], str(self.home))
        self.assertEqual(env["TMPDIR"], f"{self.wt}/tmp")
        self.assertEqual(env["PYTHONDONTWRITEBYTECODE"], "1")
        self.assertEqual(env["PYTHONPYCACHEPREFIX"], f"{self.wt}/tmp/pycache")
        self.assertEqual(env["GATE_SHARDS"], "3")
        self.assertEqual(env["LC_ALL"], "C.UTF-8")
        self.assertEqual(env["FACTORY_HOME"], f"{self.wt}/factory-home")
        self.assertEqual(env["GATE_REPORT_DIR"], f"{self.wt}/reports")

    def test_argv_is_the_allow_profile(self):
        argv, _ = self.build()
        cfg = json.loads(argv[argv.index("--setup") + 1])
        self.assertEqual(cfg["mode"], "allow")
        self.assertFalse(cfg["net"])
        self.assertNotEqual(cfg.get("pid"), False)
        self.assertEqual(sorted(cfg["rw"]), sorted([str(self.tree), *(f"{self.wt}/{d}" for d in ("reports", "factory-home", "tmp"))]))
        self.assertEqual(sorted(cfg["ro"]), sorted([str(self.main), str(self.repo / ".git")]))
        self.assertEqual(sorted(cfg["masks"]), sorted(f"{self.main}/{d}" for d in gatebox.MASKED))
        self.assertEqual(sorted(cfg["hide_dirs"]), sorted([str(self.home), str(self.runtime), "/tmp", "/dev/shm"]))  # /var/tmp is patched out here
        self.assertEqual(argv[-3:], ["bash", "-c", "true"])

    def test_unavailable_userns_raises_and_never_runs(self):
        with mock.patch.object(nsbox, "userns_available", return_value=(False, "denied")):
            with self.assertRaisesRegex(gatebox.GateboxError, "unsandboxed"):
                self.build()
            with mock.patch("subprocess.Popen") as popen:
                with self.assertRaises(gatebox.GateboxError):
                    self.run_in("true")
                popen.assert_not_called()

    def test_bad_paths_raise(self):
        with self.assertRaisesRegex(gatebox.GateboxError, "gate tree .* is not a directory"):
            gatebox.build("true", str(self.root / "nope"), str(self.main), str(self.wt), self.env, home=str(self.home))


class VarTmpProfileTests(Fixture):
    fixture_hide_tmp = None  # the real HIDE_TMP

    def test_var_tmp_is_hidden_with_the_other_tmp_dirs(self):
        with mock.patch.object(nsbox, "userns_available", return_value=(True, "")):
            argv, _ = self.build()
        cfg = json.loads(argv[argv.index("--setup") + 1])
        self.assertIn("/var/tmp", cfg["hide_dirs"])
        self.assertEqual(cfg["tmpfs_sizes"].get("/var/tmp"), "16m")


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class VarTmpTests(Fixture):
    fixture_hide_tmp = None  # the real HIDE_TMP: the fixture root (under /var/tmp) is itself hidden, rw dirs are bound back

    def test_var_tmp_writes_do_not_persist(self):
        n = self.root.name
        r = self.run_in(f"echo x > /var/tmp/{n}-leak && cat /var/tmp/{n}-leak && echo ok > {self.wt}/reports/x")
        self.assertEqual(r.returncode, 0, r.output)
        self.assertFalse(Path("/var/tmp", f"{n}-leak").exists())
        self.assertEqual((self.wt / "reports/x").read_text(), "ok\n")  # the rw dirs under /var/tmp are bound back

    def test_var_tmp_is_not_readable_from_the_gate(self):
        (self.root / "secret.txt").write_text("SECRET")
        r = self.run_in(f"cat {self.root}/secret.txt")
        self.assertNotEqual(r.returncode, 0)
        self.assertNotIn("SECRET", r.output)


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class HomeLayoutTests(Fixture):
    """The production layout (T-0652 c): FACTORY_MAIN, the gate trees and wt_root all live UNDER the hidden $HOME
    (~/factory, ~/.cache/factory-gate/<id>). Fake HOME, never the live one."""

    def setUp(self):
        super().setUp()
        self.main = self.home / "factory"
        shutil.move(str(self.root / "main"), str(self.main))
        self.wt = self.home / ".cache/factory-gate/7"
        self.tree = self.wt / "head"
        self.tree.parent.mkdir(parents=True)
        git("worktree", "add", "-q", "--detach", str(self.tree), self.sha, cwd=self.repo)

    def test_gate_runs_with_everything_under_home(self):
        r = self.run_in(f"cat {self.main}/tests/gate_shards.py; git rev-parse HEAD; echo hi > {self.wt}/reports/x; "
                        f"touch {self.tree}/y; echo z > {self.main}/z; echo cred=[$(cat {self.home}/.config/gh/hosts.yml 2>&1)]")
        self.assertIn("SHARDS = 1", r.output)
        self.assertIn(self.sha, r.output)
        self.assertIn("Read-only file system", r.output)
        self.assertIn("No such file", r.output)
        self.assertNotIn("FAKE-GH", r.output)
        self.assertEqual((self.wt / "reports/x").read_text(), "hi\n")
        self.assertEqual((self.tree / "y").read_text(), "")  # the gate tree is rw
        self.assertFalse((self.main / "z").exists())

    def test_home_tmpfs_writes_stay_in_the_box(self):
        r = self.run_in(f"echo x > {self.home}/stray; mkdir -p {self.home}/.cache/pip && echo y > {self.home}/.cache/pip/z")
        self.assertEqual(r.returncode, 0, r.output)
        self.assertFalse((self.home / "stray").exists())
        self.assertFalse((self.home / ".cache/pip").exists())


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class RealNamespaceTests(Fixture):
    def test_fake_credentials_unreadable(self):
        for rel in (".claude/.credentials.json", ".config/gh/hosts.yml", ".ssh/id_ed25519"):
            with self.subTest(rel=rel):
                r = self.run_in(f"cat {self.home}/{rel}")
                self.assertNotEqual(r.returncode, 0, r.output)
                self.assertNotIn("FAKE-", r.output)

    def test_user_bus_socket_unreachable(self):
        code = f"import socket; s=socket.socket(socket.AF_UNIX); s.connect('{self.runtime}/bus')"
        r = self.run_in(f"{PY} -c \"{code}\"")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("Error", r.output)
        self.bus.settimeout(0.2)
        with self.assertRaises(socket.timeout):
            self.bus.accept()  # nothing ever reached the real listener

    def test_outside_loopback_listener_unreachable(self):
        code = f"import socket; socket.create_connection(('127.0.0.1', {self.port}), 2)"
        r = self.run_in(f"{PY} -c \"{code}\"")
        self.assertNotEqual(r.returncode, 0)
        self.assertRegex(r.output, "refused|unreachable|Errno")

    def test_main_and_git_common_dir_read_only_tree_writable(self):
        for p in (self.main / "x", self.repo / ".git/hooks/x", self.repo / ".git/config"):
            with self.subTest(path=str(p)):
                before = p.read_bytes() if p.exists() else None
                r = self.run_in(f"echo x >> {p}")
                self.assertNotEqual(r.returncode, 0)
                self.assertIn("Read-only file system", r.output)
                self.assertEqual(p.read_bytes() if p.exists() else None, before)
        r = self.run_in(f"echo ok > {self.tree}/x")
        self.assertEqual(r.returncode, 0, r.output)
        self.assertEqual((self.tree / "x").read_text(), "ok\n")

    def test_hidden_trees_do_not_leak_writes_and_rw_dirs_persist(self):
        n = self.root.name  # unique, so a leftover file of another run cannot confuse the outside check
        r = self.run_in(f"echo hi > {self.home}/x && echo ok > {self.wt}/reports/x && echo t > {self.wt}/tmp/x "
                        f"&& echo f > {self.wt}/factory-home/x && echo s > /tmp/{n} && echo d > /dev/shm/{n}")
        self.assertEqual(r.returncode, 0, r.output)
        self.assertFalse((self.home / "x").exists())
        self.assertEqual((self.wt / "reports/x").read_text(), "ok\n")
        self.assertEqual((self.wt / "tmp/x").read_text(), "t\n")
        self.assertEqual((self.wt / "factory-home/x").read_text(), "f\n")
        self.assertFalse(Path("/tmp", n).exists())
        self.assertFalse(Path("/dev/shm", n).exists())

    def test_main_readable_state_masked_and_git_works(self):
        r = self.run_in(f"cat {self.main}/tests/gate_shards.py; echo db=[$(ls -A {self.main}/db)]; "
                        f"git -C {self.tree} rev-parse HEAD; echo home=[$(ls -A {self.home})]")
        self.assertEqual(r.returncode, 0, r.output)
        self.assertIn("SHARDS = 1", r.output)
        self.assertIn("db=[]", r.output)
        self.assertNotIn("LIVE-DB", r.output)
        self.assertIn(self.sha, r.output)
        self.assertIn("home=[]", r.output)
        self.assertEqual((self.main / "db/factory.db").read_text(), "LIVE-DB")

    def test_py_compile_works_in_the_read_only_tree(self):
        (self.tree / "m.py").write_text("X = 1\n")  # a tracked-style file: the tree is checked out, then bound ro
        r = self.run_in(f"{PY} -m py_compile m.py && ls {self.wt}/tmp/pycache >/dev/null && echo compiled")
        self.assertEqual(r.returncode, 0, r.output)
        self.assertIn("compiled", r.output)
        self.assertFalse((self.tree / "__pycache__").exists())

    def test_caller_secrets_unset_inside(self):
        r = self.run_in("env")
        self.assertEqual(r.returncode, 0, r.output)
        for k in ("GH_TOKEN", "SSH_AUTH_SOCK", "FACTORY_AGENT", "DBUS_SESSION_BUS_ADDRESS"):
            self.assertNotIn(k + "=", r.output)
        self.assertIn(f"HOME={self.home}", r.output)
        self.assertIn(f"TMPDIR={self.wt}/tmp", r.output)
        self.assertIn(f"FACTORY_MAIN={self.main}", r.output)

    def test_caps_dropped_and_own_pid_namespace(self):
        r = self.run_in("grep -E '^(CapEff|CapBnd|NoNewPrivs)' /proc/self/status; echo pid=$$; ls /proc | grep -c '^[0-9]'")
        self.assertEqual(r.returncode, 0, r.output)
        self.assertIn("CapEff:\t0000000000000000", r.output)
        self.assertIn("CapBnd:\t0000000000000000", r.output)
        self.assertIn("NoNewPrivs:\t1", r.output)
        self.assertRegex(r.output, r"pid=\d\b")  # a fresh pid namespace: low pids only
        self.assertLess(int(r.output.split()[-1]), 10)  # only this namespace's processes are visible

    def test_timeout_kills_the_tree(self):
        r = self.run_in(f"sleep 300 & echo $! > {self.wt}/tmp/child; wait", timeout=2)
        self.assertTrue(r.timed_out)
        inner = int((self.wt / "tmp/child").read_text())  # pid inside the namespace: gone with its init
        self.assertEqual(inner, 2)
        sleepers = subprocess.run(["pgrep", "-f", "^sleep 300$", "-u", str(os.getuid())], capture_output=True, text=True)
        self.assertEqual(sleepers.stdout.split(), [], "a gate child outlived the timeout")


if __name__ == "__main__":
    unittest.main()
