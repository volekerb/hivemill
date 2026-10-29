"""Eval sandbox (T-0118): CONNECT proxy, claude shim, namespace wrapper, contained runner.

Group A needs no namespaces and runs everywhere. Group B (NsboxNamespaceTests) needs unprivileged user namespaces:
it runs on your-box and skips with the reason where they are restricted (e.g. ubuntu-latest CI).
No real network, no real claude, no real token: a fake claude, a fake upstream and a dummy token string.
"""
import json
import os
import shutil
import socket
import stat
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))  # so `python -m unittest tests.test_skilleval_nsbox` works too
from harness import FactoryEnv, REPO  # noqa: E402

sys.path.insert(0, str(REPO))
from scout import egress, nsbox  # noqa: E402

TOKEN = "sk-ant-" + "oat01-DUMMYTOKEN-RANDOM"  # split so the repo secret scan does not flag the dummy
UNSHARE = os.environ.get("NSBOX_TEST_UNSHARE", "unshare")  # seam: a bogus path forces the clean-skip branch
USERNS_OK, USERNS_WHY = nsbox.userns_available(UNSHARE)
SKIP_MSG = f"user namespaces unavailable: {USERNS_WHY}"
IN_GATEBOX = os.environ.get("FACTORY_GATEBOX") == "1"  # set by scout/gatebox.py (T-0652); never set in CI or a normal run
GATEBOX_SKIP = "inside the gate box (FACTORY_GATEBOX=1): the outer box already hides $HOME and /proc, so this nested-userns check cannot observe what it asserts; CI runs it unboxed"


def tmpdir(case):
    d = Path(tempfile.mkdtemp(prefix="zz-test-nsbox-")).resolve()
    case.addCleanup(shutil.rmtree, d, True)
    return d


def wait_for(cond, secs=10):
    end = time.monotonic() + secs
    while time.monotonic() < end:
        if cond():
            return True
        time.sleep(0.05)
    return False


class FakeUpstream:
    """Echo server on a unix socket: replies b'UP:' + what it got. Stands in for api.anthropic.com."""

    def __init__(self, path):
        self.path = str(path)
        self.srv = socket.socket(socket.AF_UNIX)
        self.srv.bind(self.path)
        self.srv.listen(8)
        threading.Thread(target=self._loop, daemon=True).start()

    def _loop(self):
        while True:
            try:
                c, _ = self.srv.accept()
            except OSError:
                return
            threading.Thread(target=self._one, args=(c,), daemon=True).start()

    @staticmethod
    def _one(c):
        with c:
            while True:
                data = c.recv(1024)
                if not data:
                    return
                c.sendall(b"UP:" + data)

    def close(self):
        self.srv.close()


class TcpUpstream:
    """Same echo on 127.0.0.1 (the sandbox's proxy reaches it from outside the namespace)."""

    def __init__(self):
        self.srv = socket.socket()
        self.srv.bind(("127.0.0.1", 0))
        self.srv.listen(8)
        self.port = self.srv.getsockname()[1]
        threading.Thread(target=self._loop, daemon=True).start()

    def _loop(self):
        while True:
            try:
                c, _ = self.srv.accept()
            except OSError:
                return
            threading.Thread(target=FakeUpstream._one, args=(c,), daemon=True).start()

    def close(self):
        self.srv.close()


def connect_via(sock_path, request):
    s = socket.socket(socket.AF_UNIX)
    s.settimeout(5)
    s.connect(str(sock_path))
    s.sendall(request)
    return s


class ProxyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tmpdir(self)
        self.up = FakeUpstream(self.tmp / "up.sock")
        self.addCleanup(self.up.close)
        self.sock = self.tmp / "proxy.sock"
        ready = threading.Event()
        resolver = egress.upstream_resolver({"api.anthropic.com:443": self.up.path})
        t = threading.Thread(target=egress.serve, daemon=True, kwargs=dict(
            sock_path=str(self.sock), resolver=resolver, max_life=30, ready=ready))
        t.start()
        self.assertTrue(ready.wait(5))

    def status(self, request):
        s = connect_via(self.sock, request)
        with s:
            return s.recv(4096).split(b"\r\n")[0]

    def test_allowed_connect_relays_both_ways(self):
        s = connect_via(self.sock, b"CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n")
        with s:
            self.assertEqual(s.recv(4096).split(b"\r\n")[0], b"HTTP/1.1 200 Connection Established")
            s.sendall(b"hello")
            self.assertEqual(s.recv(100), b"UP:hello")
            s.sendall(b"again")
            self.assertEqual(s.recv(100), b"UP:again")

    def test_host_is_case_insensitive(self):
        self.assertIn(b" 200 ", self.status(b"CONNECT API.Anthropic.COM:443 HTTP/1.1\r\n\r\n"))

    def test_denied_targets_get_403(self):
        for req in (b"CONNECT example.com:443 HTTP/1.1\r\n\r\n",
                    b"CONNECT api.anthropic.com:80 HTTP/1.1\r\n\r\n",
                    b"CONNECT api.anthropic.com.evil.test:443 HTTP/1.1\r\n\r\n",
                    b"CONNECT xapi.anthropic.com:443 HTTP/1.1\r\n\r\n",
                    b"GET http://api.anthropic.com/ HTTP/1.1\r\nHost: api.anthropic.com\r\n\r\n",
                    b"GET / HTTP/1.1\r\n\r\n"):
            self.assertEqual(self.status(req), b"HTTP/1.1 403 Forbidden", req)

    def test_denied_target_is_never_resolved(self):
        seen = []
        ready = threading.Event()
        sock = self.tmp / "p2.sock"
        threading.Thread(target=egress.serve, daemon=True, kwargs=dict(
            sock_path=str(sock), max_life=30, ready=ready,
            resolver=lambda h, p: seen.append((h, p)) or self.up.path)).start()
        self.assertTrue(ready.wait(5))
        with connect_via(sock, b"CONNECT example.com:443 HTTP/1.1\r\n\r\n") as s:
            self.assertIn(b" 403 ", s.recv(100))
        self.assertEqual(seen, [])


class ProxyLifetimeTests(unittest.TestCase):
    pids = []

    @classmethod
    def tearDownClass(cls):
        for pid in cls.pids:
            if egress.pid_alive(pid):
                os.kill(pid, 9)

    def start(self, **kw):
        tmp = tmpdir(self)
        cfg = nsbox.make_cfg(tmp / "trials" / "t1", forward=True)
        os.makedirs(tmp / "trials" / "t1")
        pid = nsbox.start_proxy(str(tmp / "trials" / "t1"), cfg, **kw)
        self.pids.append(pid)
        self.addCleanup(nsbox.stop_proxy, str(tmp / "trials" / "t1"))
        return tmp / "trials" / "t1", pid

    def test_exits_when_watched_pid_dies(self):
        watched = subprocess.Popen(["sleep", "60"])
        self.addCleanup(watched.kill)
        _, pid = self.start(watch_pid=watched.pid)
        self.assertTrue(egress.pid_alive(pid))
        watched.kill()
        watched.wait()
        self.assertTrue(wait_for(lambda: not egress.pid_alive(pid), 5), "proxy outlived its watched pid")

    def test_exits_after_max_lifetime(self):
        _, pid = self.start(max_life=1)
        self.assertTrue(wait_for(lambda: not egress.pid_alive(pid), 6), "proxy outlived max_life")

    def test_stop_proxy_kills_and_is_idempotent(self):
        trial, pid = self.start()
        self.assertTrue(egress.pid_alive(pid))
        nsbox.stop_proxy(str(trial))
        self.assertTrue(wait_for(lambda: not egress.pid_alive(pid), 5))
        self.assertFalse((trial / "proxy.pid").exists())
        nsbox.stop_proxy(str(trial))  # second call: no error

    def test_stop_proxy_ignores_a_recycled_pid(self):
        tmp = tmpdir(self)
        other = subprocess.Popen(["sleep", "60"])
        self.addCleanup(other.kill)
        (tmp / "proxy.pid").write_text(f"{other.pid}\n")
        nsbox.stop_proxy(str(tmp))
        self.assertIsNone(other.poll(), "stop_proxy killed a process that is not our proxy")


class ShimTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tmpdir(self)
        self.home = self.tmp / "home"
        (self.home / ".config" / "factory").mkdir(parents=True)
        self.trial = self.tmp / "trials" / "t1"
        self.claude = self.tmp / "bin" / "claude"
        self.claude.parent.mkdir()
        self.claude.write_text("#!/bin/sh\nexit 0\n")
        self.claude.chmod(0o755)
        self.token_file = self.home / ".config" / "factory" / "eval-oauth-token"
        self.shim = nsbox.write_shim(self.trial, self.claude, home=self.home, unshare="/nonexistent/unshare")

    def run_shim(self):
        env = {"PATH": os.environ["PATH"], "HOME": str(self.trial / "home")}
        r = subprocess.run([str(self.shim)], env=env, capture_output=True, text=True, timeout=30)
        nsbox.stop_proxy(str(self.trial))
        return r

    def test_shim_file_mode_and_content(self):
        self.assertEqual(self.shim, self.trial.resolve() / "bin" / "claude")
        self.assertEqual(stat.S_IMODE(self.shim.stat().st_mode), 0o755)
        text = self.shim.read_text()
        self.assertTrue(text.startswith("#!/"))
        self.assertIn(str(self.token_file), text)  # the default token path is baked in, absolute
        self.assertIn(str(self.claude), text)
        for banned in ("$HOME", "expanduser", "Path.home", "environ.get(\"HOME"):
            self.assertNotIn(banned, text)
        self.assertNotIn(TOKEN, text)

    def test_factory_cli_snapshot_is_written(self):
        self.assertTrue(os.access(self.trial / "bin" / "factory", os.X_OK))
        self.assertTrue((self.trial / "scout" / "db.py").is_file())

    def test_real_claude_must_be_named_claude(self):
        with self.assertRaises(ValueError):
            nsbox.write_shim(self.trial, self.tmp / "bin" / "claude-2.1.310", home=self.home)

    def test_token_missing_exits_3(self):
        r = self.run_shim()
        self.assertEqual(r.returncode, 3)
        self.assertIn("owner decision D3 pending: no eval token", r.stderr)

    def test_token_mode_0644_exits_3(self):
        self.token_file.write_text(TOKEN)
        self.token_file.chmod(0o644)
        r = self.run_shim()
        self.assertEqual(r.returncode, 3)
        self.assertIn("owner decision D3 pending: no eval token", r.stderr)
        self.assertNotIn(TOKEN, r.stderr + r.stdout)

    def test_token_wrong_owner_exits_3(self):
        self.token_file.write_text(TOKEN)
        self.token_file.chmod(0o600)
        with mock.patch("os.getuid", return_value=os.getuid() + 1), self.assertRaises(SystemExit) as cm:
            nsbox.read_token(str(self.token_file))
        self.assertEqual(cm.exception.code, 3)

    def test_token_symlink_is_refused(self):
        real = self.tmp / "real-token"
        real.write_text(TOKEN)
        real.chmod(0o600)
        self.token_file.symlink_to(real)
        with self.assertRaises(SystemExit) as cm:
            nsbox.read_token(str(self.token_file))
        self.assertEqual(cm.exception.code, 3)

    def test_token_mode_0600_proceeds_past_the_check(self):
        self.token_file.write_text(TOKEN + "\n")
        self.token_file.chmod(0o600)
        self.assertEqual(nsbox.read_token(str(self.token_file)), TOKEN)
        r = self.run_shim()  # unshare is bogus: it gets as far as exec'ing it, i.e. past the token check
        self.assertNotEqual(r.returncode, 3)
        self.assertNotIn("D3 pending", r.stderr)
        self.assertNotIn(TOKEN, r.stderr + r.stdout)


class ComposedArgvTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tmpdir(self)
        self.claude = self.tmp / "bin" / "claude"
        self.cfg = nsbox.make_cfg(self.tmp / "trials" / "t1", self.claude, home=self.tmp / "home", forward=True)
        self.args = ["--model", "sonnet", "-p", "hi there"]

    def test_stage1(self):
        s1 = nsbox.compose_stage1(self.cfg, self.args)
        i = s1.index("--propagation")
        self.assertEqual(s1[1:4], ["-r", "-n", "-m"])
        self.assertEqual(s1[i:i + 2], ["--propagation", "private"])
        self.assertIn("--pid", s1)
        self.assertIn("--mount-proc", s1)
        self.assertEqual(s1[-len(self.args):], self.args)

    def test_stage2(self):
        s2 = nsbox.compose_stage2(self.cfg, self.args)
        self.assertEqual(s2[1:3], [f"--map-user={os.getuid()}", f"--map-group={os.getgid()}"])
        self.assertEqual(s2[-len(self.args) - 1:], [str(self.claude), *self.args])
        self.assertTrue(os.path.isabs(str(self.claude)) and os.path.basename(s2[-len(self.args) - 1]) == "claude")
        for cap in ("--no-new-privs", "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all"):
            self.assertIn(cap, s2)

    def test_token_is_in_no_argv_element(self):
        with mock.patch.dict(os.environ, {"CLAUDE_CODE_OAUTH_TOKEN": TOKEN}):
            cfg = nsbox.make_cfg(self.tmp / "trials" / "t1", self.claude, home=self.tmp / "home", forward=True)
            argvs = nsbox.compose_stage1(cfg, self.args) + nsbox.compose_stage2(cfg, self.args)
        self.assertFalse([a for a in argvs if TOKEN in a])

    def test_contained_config_has_no_proxy_and_scrubs_secrets(self):
        cfg = nsbox.make_cfg(self.tmp / "trials" / "t1", None, home=self.tmp / "home")
        self.assertIsNone(cfg["forward_sock"])
        self.assertEqual(cfg["env_set"], {})
        for var in ("CLAUDE_CODE_OAUTH_TOKEN", "HTTPS_PROXY", "SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS"):
            self.assertIn(var, cfg["scrub"])

    def test_hide_dirs_resolve_from_the_given_home(self):
        home = self.tmp / "home"
        self.assertIn(f"{home}/.claude", self.cfg["hide_dirs"])
        self.assertIn(f"{home}/factory", self.cfg["hide_dirs"])


FAKE_CLAUDE = r'''#!{python}
import json, os, pwd, socket, subprocess, sys, time, urllib.request
cfg = json.load(open(sys.argv[1]))
out = {{"argv": sys.argv, "uid": os.getuid()}}

def lst(p):
    try:
        return sorted(os.listdir(p))
    except OSError as e:
        return "ERR %s" % e.errno

def status():
    return dict(l.split(":\t", 1) for l in open("/proc/self/status").read().splitlines() if ":\t" in l)

try:
    urllib.request.urlopen("http://example.com", timeout=3)
    out["fetch"] = "ok"
except Exception:
    out["fetch"] = "fail"
try:
    socket.create_connection(("1.1.1.1", 80), timeout=3)
    out["direct"] = "ok"
except OSError:
    out["direct"] = "fail"

def via_proxy(target):
    s = socket.create_connection(("127.0.0.1", 3128), timeout=5)
    s.sendall(("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n" % (target, target)).encode())
    head = s.recv(4096).split(b"\r\n")[0].decode()
    reply = None
    if " 200 " in head:
        s.sendall(b"ping")
        reply = s.recv(100).decode()
    return [head, reply]

out["allowed"] = via_proxy("api.anthropic.com:443")
out["denied"] = via_proxy("example.com:443")
out["proxy_env"] = [os.environ.get("HTTPS_PROXY"), os.environ.get("https_proxy")]
out["ls"] = {{k: lst(p) for k, p in cfg["hidden"].items()}}
out["visible"] = {{k: os.path.exists(p) for k, p in cfg["visible"].items()}}
out["umount_rc"] = {{k: subprocess.run(["umount", p], capture_output=True).returncode for k, p in cfg["hidden"].items()}}
out["token_set"] = os.environ.get("CLAUDE_CODE_OAUTH_TOKEN")
out["environ"] = dict(os.environ)
out["env_unset"] = [os.environ.get("SSH_AUTH_SOCK"), os.environ.get("DBUS_SESSION_BUS_ADDRESS")]
out["cmdline"] = open("/proc/self/cmdline").read().split("\0")[:-1]
st = status()
out["caps"] = {{k: st[k].strip() for k in ("CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb")}}
out["nnp"] = st["NoNewPrivs"].strip()
out["proc_pids"] = sorted(int(p) for p in os.listdir("/proc") if p.isdigit())
out["proc_caps"] = {{p: dict(l.split(":\t", 1) for l in open("/proc/%s/status" % p).read().splitlines() if ":\t" in l)["CapEff"].strip()
                    for p in os.listdir("/proc") if p.isdigit()}}
for k, path in (("environ", "/proc/%d/environ" % cfg["sentinel"]), ("root", "/proc/%d/root/%s" % (cfg["sentinel"], cfg["sentinel_dir"]))):
    try:
        os.listdir(path) if k == "root" else open(path, "rb").read()
        out["sentinel_" + k] = "readable"
    except OSError:
        out["sentinel_" + k] = "blocked"
out["outer_pid_visible"] = os.path.exists("/proc/%d" % cfg["outer_pid"])
out["sockets"] = {{}}
for k, path in cfg["sockets"].items():
    s = socket.socket(socket.AF_UNIX)
    try:
        s.connect(path)
        out["sockets"][k] = "connected"
    except OSError:
        out["sockets"][k] = "failed"
r = subprocess.run([cfg["factory"], "--help"], capture_output=True, text=True, env=dict(os.environ, FACTORY_HOME=cfg["factory_home"]))
out["factory_help_rc"] = r.returncode
r = subprocess.run([cfg["factory"], "task", "show", "T-0001"], capture_output=True, text=True,
                   env=dict(os.environ, FACTORY_HOME=cfg["factory_home"], FACTORY_AGENT="boss"))
out["factory_task"] = [r.returncode, r.stdout[:200], r.stderr[:200]]
json.dump(out, open(cfg["obs"] + ".tmp", "w"))
os.rename(cfg["obs"] + ".tmp", cfg["obs"])
open(cfg["ready"], "w").close()
end = time.time() + 30
while time.time() < end and not os.path.exists(cfg["go"]):
    time.sleep(0.1)
'''


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class NsboxNamespaceTests(unittest.TestCase):
    """Group B: the real namespaces, with a fake claude started through the generated shim."""

    @classmethod
    def setUpClass(cls):
        cls.tmp = Path(tempfile.mkdtemp(prefix="zz-test-nsbox-")).resolve()
        cls.up = TcpUpstream()
        cls.fenv = FactoryEnv()
        cls.fenv.seed_project()
        cls.fenv.seed_goal()
        cls.fenv.seed_task()
        cls.uid = os.getuid()
        # a fake "real home" with secrets in it, a live-looking factory dir and a unix socket
        cls.home = cls.tmp / "realhome"
        for d in (".claude", ".ssh", ".config/factory", ".config/herdr", "factory"):
            (cls.home / d).mkdir(parents=True)
        (cls.home / ".claude" / "secret.json").write_text("{}")
        (cls.home / "factory" / "factory.db").write_text("live")
        cls.token_file = cls.home / ".config" / "factory" / "eval-oauth-token"
        cls.token_file.write_text(TOKEN + "\n")
        cls.token_file.chmod(0o600)
        cls.herdr_sock = cls.home / ".config" / "herdr" / "herdr.sock"
        cls.herdr_srv = socket.socket(socket.AF_UNIX)
        cls.herdr_srv.bind(str(cls.herdr_sock))
        cls.herdr_srv.listen(1)
        cls.trials = cls.tmp / "trials"
        cls.trial = cls.trials / "t1"
        (cls.trials / "t2").mkdir(parents=True)
        (cls.trials / "t2" / "other-trial-secret").write_text("x")
        cls.trial.mkdir()
        (cls.trial / "home").mkdir()
        cls.claude = cls.tmp / "bin" / "claude"
        cls.claude.parent.mkdir()
        cls.claude.write_text(FAKE_CLAUDE.format(python=sys.executable))
        cls.claude.chmod(0o755)
        runtime = Path(os.environ.get("XDG_RUNTIME_DIR") or f"/run/user/{cls.uid}")
        cls.sentinel = subprocess.Popen(["sleep", "120"], env={"SENTINEL_MARK": "zz-sentinel-marker"})
        cfg = {
            "hidden": {"claude": str(cls.home / ".claude"), "factory": str(cls.home / "factory"),
                       "ssh": str(cls.home / ".ssh"), "config": str(cls.home / ".config"),
                       "sibling": str(cls.trials / "t2")},
            "visible": {"trial": str(cls.trial), "claude": str(cls.claude)},
            "sentinel": cls.sentinel.pid, "sentinel_dir": str(cls.home / ".claude").lstrip("/"),
            "outer_pid": os.getpid(), "factory": str(cls.trial / "bin" / "factory"),
            "factory_home": str(cls.fenv.home_dir),
            "sockets": {"user_bus": str(runtime / "bus"), "herdr": str(cls.herdr_sock)},
            "obs": str(cls.trial / "obs.json"), "ready": str(cls.trial / "ready"), "go": str(cls.trial / "go"),
        }
        if runtime.is_dir():
            cfg["hidden"]["runtime"] = str(runtime)
        cls.cfg = cfg
        (cls.trial / "cfg.json").write_text(json.dumps(cfg))
        shim = nsbox.write_shim(cls.trial, cls.claude, home=cls.home, trials_root=cls.trials,
                                upstream={"api.anthropic.com:443": ["127.0.0.1", cls.up.port]}, unshare=UNSHARE)
        env = {"PATH": f"{cls.trial / 'bin'}:/usr/bin:/bin", "HOME": str(cls.trial / "home"),
               "SSH_AUTH_SOCK": "/tmp/fake-agent.sock", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/tmp/fake-bus",
               "CLAUDE_CODE_MESSAGING_TOKEN": "msg-secret", "FOO_SECRET": "foo-secret", "LC_ALL": "C.UTF-8",
               "CLAUDE_CODE_ENTRYPOINT": "cli"}
        cls.proc = subprocess.Popen([str(shim), str(cls.trial / "cfg.json")], env=env, cwd=cls.trial,
                                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        try:
            if not wait_for(lambda: (cls.trial / "ready").exists() or cls.proc.poll() is not None, 60):
                raise RuntimeError("fake claude never became ready")
            if not (cls.trial / "ready").exists():
                raise RuntimeError(f"shim exited {cls.proc.returncode}: {cls.proc.stdout.read()}")
            cls.obs = json.loads((cls.trial / "obs.json").read_text())
            # (e) while claude runs: scan every cmdline on the box for the token
            cls.cmdline_hits = []
            for p in Path("/proc").iterdir():
                if p.name.isdigit():
                    try:
                        if TOKEN.encode() in (p / "cmdline").read_bytes():
                            cls.cmdline_hits.append(p.name)
                    except OSError:
                        pass
        except BaseException:
            cls.tearDownClass()
            raise
        (cls.trial / "go").touch()
        cls.proc.wait(timeout=30)

    @classmethod
    def tearDownClass(cls):
        (cls.trial / "go").touch()
        proc = getattr(cls, "proc", None)
        if proc and proc.poll() is None:
            proc.kill()
            proc.wait()
        if proc and proc.stdout:
            proc.stdout.close()
        nsbox.stop_proxy(str(cls.trial))
        cls.sentinel.kill()
        cls.sentinel.wait()
        cls.up.close()
        cls.herdr_srv.close()
        cls.fenv.cleanup()
        shutil.rmtree(cls.tmp, ignore_errors=True)

    def test_a_no_network_inside(self):
        self.assertEqual(self.obs["fetch"], "fail")
        self.assertEqual(self.obs["direct"], "fail")

    def test_b_proxy_allows_only_the_api_host(self):
        self.assertEqual(self.obs["allowed"], ["HTTP/1.1 200 Connection Established", "UP:ping"])
        self.assertEqual(self.obs["denied"][0], "HTTP/1.1 403 Forbidden")
        self.assertEqual(self.obs["proxy_env"], ["http://127.0.0.1:3128"] * 2)

    def test_c_secret_dirs_are_empty_and_own_paths_visible(self):
        for k, v in self.obs["ls"].items():
            self.assertEqual(v, [], k)
        self.assertEqual(self.obs["visible"], {"trial": True, "claude": True})

    def test_d_real_uid_and_no_umount(self):
        self.assertEqual(self.obs["uid"], self.uid)
        for k, rc in self.obs["umount_rc"].items():  # item 16 too: every tmpfs overlay
            self.assertNotEqual(rc, 0, f"umount of {k} succeeded inside")

    def test_e_token_in_env_only(self):
        self.assertEqual(self.obs["token_set"], TOKEN)
        self.assertEqual(self.cmdline_hits, [])
        self.assertFalse([a for a in self.obs["argv"] + self.obs["cmdline"] if TOKEN in a])

    def test_shim_passes_only_allow_listed_env_to_claude(self):
        env = self.obs["environ"]
        for k in ("CLAUDE_CODE_MESSAGING_TOKEN", "FOO_SECRET", "SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS"):
            self.assertNotIn(k, env)
        self.assertNotIn("msg-secret", json.dumps(env))
        self.assertNotIn("foo-secret", json.dumps(env))
        self.assertEqual(env["CLAUDE_CODE_OAUTH_TOKEN"], TOKEN)  # still loaded from the token file
        self.assertEqual(env["HOME"], str(self.trial))
        self.assertEqual(env["LC_ALL"], "C.UTF-8")
        self.assertEqual(env["CLAUDE_CODE_ENTRYPOINT"], "cli")
        self.assertIn("PATH", env)
        allowed = set(nsbox.ENV_PASS) | set(nsbox.CLAUDE_ENV_PASS) | {"HOME", "CLAUDE_CODE_OAUTH_TOKEN", "LC_ALL",
                                                                      "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"}
        self.assertLessEqual(set(env), allowed | {"LC_CTYPE", "PWD", "SHLVL", "_", "OLDPWD"})

    def test_f_outer_processes_unreachable(self):
        self.assertEqual(self.obs["sentinel_environ"], "blocked")
        self.assertEqual(self.obs["sentinel_root"], "blocked")

    def test_g_cmdline_has_claude_basename_in_first_three(self):
        self.assertIn("claude", [os.path.basename(a) for a in self.obs["cmdline"][:3]])

    def test_h_factory_cli_works_and_live_db_is_not_reachable(self):
        self.assertEqual(self.obs["factory_help_rc"], 0)
        rc, out, err = self.obs["factory_task"]
        self.assertEqual(rc, 0, err)
        self.assertIn("T-0001", out)
        self.assertEqual(self.obs["ls"]["factory"], [])  # the live-looking factory.db in the fake home is hidden

    def test_real_home_resolution_ignores_HOME(self):
        # HOME was <trial>/home in the shim env; the passwd-style home (the seam) is what got hidden and read
        self.assertEqual((self.obs["ls"]["claude"], self.obs["ls"]["factory"], self.obs["ls"]["config"]), ([], [], []))
        self.assertEqual(self.obs["token_set"], TOKEN)

    def test_capabilities_all_zero_and_no_new_privs(self):
        self.assertEqual(set(self.obs["caps"].values()), {"0000000000000000"}, self.obs["caps"])
        self.assertEqual(self.obs["nnp"], "1")

    def test_runtime_dir_is_hidden_and_socket_env_unset(self):
        if "runtime" in self.obs["ls"]:
            self.assertEqual(self.obs["ls"]["runtime"], [])
        self.assertEqual(self.obs["env_unset"], [None, None])

    def test_unix_sockets_unreachable(self):
        self.assertEqual(self.obs["sockets"], {"user_bus": "failed", "herdr": "failed"})

    @unittest.skipIf(IN_GATEBOX, GATEBOX_SKIP)
    def test_own_pid_namespace_with_fresh_proc(self):
        self.assertFalse(self.obs["outer_pid_visible"])
        self.assertNotIn(self.sentinel.pid, self.obs["proc_pids"])
        self.assertLess(max(self.obs["proc_pids"]), 100)

    def test_no_privileged_helper_survives(self):
        self.assertGreaterEqual(len(self.obs["proc_caps"]), 2)  # claude plus the forwarder
        self.assertEqual(set(self.obs["proc_caps"].values()), {"0000000000000000"}, self.obs["proc_caps"])

    def test_proxy_is_gone_after_claude_exits(self):
        pid = None
        self.assertTrue(wait_for(lambda: not (self.trial / "proxy.sock").exists(), 5), "proxy still listening")
        pidfile = self.trial / "proxy.pid"
        if pidfile.exists():
            pid = int(pidfile.read_text())
        self.assertTrue(pid is None or wait_for(lambda: not egress.pid_alive(pid), 5))


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class RunContainedTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tmpdir(self)
        self.trials = self.tmp / "trials"
        self.trial = self.trials / "t1"
        self.trial.mkdir(parents=True)
        (self.trials / "t2").mkdir()
        (self.trials / "t2" / "secret").write_text("x")

    def script(self, body):
        p = self.trial / "check.py"
        p.write_text(body)
        return [sys.executable, str(p)]

    @unittest.skipIf(IN_GATEBOX, GATEBOX_SKIP)
    def test_hiding_no_network_no_secrets_and_output_returned(self):
        real = Path(nsbox.real_home())
        body = f"""
import json, os, socket, urllib.request
def ls(p):
    try: return sorted(os.listdir(p))
    except OSError as e: return 'ERR'
try:
    urllib.request.urlopen('http://example.com', timeout=3); fetch = 'ok'
except Exception: fetch = 'fail'
print(json.dumps(dict(claude=ls({str(real / '.claude')!r}), fac=ls({str(real / 'factory')!r}),
    db=os.path.exists({str(real / 'factory' / 'factory.db')!r}), sib=ls({str(self.trials / 't2')!r}),
    fetch=fetch, uid=os.getuid(), env=[os.environ.get(k) for k in ('CLAUDE_CODE_OAUTH_TOKEN', 'HTTPS_PROXY', 'https_proxy')],
    cwd=os.getcwd())))
raise SystemExit(7)
"""
        env = dict(os.environ, CLAUDE_CODE_OAUTH_TOKEN=TOKEN, HTTPS_PROXY="http://127.0.0.1:3128")
        r = nsbox.run_contained(self.trial, self.script(body), cwd=self.trial, env=env, timeout=30,
                                trials_root=self.trials, unshare=UNSHARE)
        self.assertEqual(r.returncode, 7, r.output)
        self.assertFalse(r.timed_out)
        obs = json.loads(r.output.strip().splitlines()[-1])
        self.assertEqual(obs["claude"], [])
        self.assertEqual(obs["fac"], [])
        self.assertFalse(obs["db"])
        self.assertEqual(obs["sib"], [])
        self.assertEqual(obs["fetch"], "fail")
        self.assertEqual(obs["uid"], os.getuid())
        self.assertEqual(obs["env"], [None, None, None])
        self.assertEqual(obs["cwd"], str(self.trial))
        self.assertNotIn(TOKEN, r.output)

    def test_timeout_kills_the_whole_tree(self):
        marker = "zz-nsbox-sleeper-4242"
        body = f"""
import subprocess, time
subprocess.Popen(['sleep', '300'], env={{'MARK': {marker!r}}})
subprocess.Popen(['sh', '-c', 'sleep 301 ; echo {marker}'])
time.sleep(300)
"""
        t0 = time.monotonic()
        r = nsbox.run_contained(self.trial, self.script(body), cwd=self.trial, timeout=2, trials_root=self.trials,
                                unshare=UNSHARE)
        self.assertTrue(r.timed_out)
        self.assertLess(time.monotonic() - t0, 20)

        def stragglers():
            hits = []
            for p in Path("/proc").iterdir():
                if p.name.isdigit():
                    try:
                        c = (p / "cmdline").read_bytes()
                        if c.startswith((b"sleep\0300", b"sleep\0301")) or marker.encode() in c:
                            hits.append(p.name)
                    except OSError:
                        pass
            return hits
        self.assertTrue(wait_for(lambda: not stragglers(), 5), f"left behind: {stragglers()}")

PROBE = """
import json, os, sys
def ls(p):
    try: return sorted(os.listdir(p))
    except OSError: return 'ERR'
def rl(p):
    try: return os.readlink(p)
    except OSError: return 'ERR'
def rd(p):
    try: return open(p).read()
    except OSError: return 'ERR'
out = dict(cwd=os.getcwd(), up=ls('..'), sib=ls('../t2'), sibfile=rd('../t2/sib-secret'), p1=rl('/proc/1/cwd'),
           p1ls=ls('/proc/1/cwd'), p1up=ls('/proc/1/cwd/..'), p1sib=rd('/proc/1/cwd/../t2/sib-secret'))
for pid in os.listdir('/proc'):
    if pid.isdigit():
        try: cmd = open('/proc/%s/cmdline' % pid).read()
        except OSError: continue
        if 'egress.py' in cmd:
            out['fwd'] = dict(cwd=rl('/proc/%s/cwd' % pid), ls=ls('/proc/%s/cwd' % pid), up=ls('/proc/%s/cwd/..' % pid),
                              sib=rd('/proc/%s/cwd/../t2/sib-secret' % pid), root=ls('/proc/%s/root/%s' % (pid, HOMEFAC)))
open(sys.argv[1], 'w').write(json.dumps(out))
"""


class CwdEscapeFixture:
    """A fake real home whose factory dir holds the trials root, a sibling trial and a worktree: all hidden."""

    def build(self, case):
        self.tmp = tmpdir(case)
        self.home = self.tmp / "realhome"
        self.fac = self.home / "factory"
        self.trials = self.fac / "trials"
        self.trial = self.trials / "t1"
        self.wt = self.fac / "worktrees" / "w1"
        self.vis = self.tmp / "vis"  # visible, outside every hide dir and not above one
        for d in (self.trial, self.trials / "t2", self.wt, self.vis):
            d.mkdir(parents=True)
        (self.trials / "t2" / "sib-secret").write_text("SIBLING-SECRET")
        (self.wt / "wt-secret").write_text("WORKTREE-SECRET")
        self.probe = self.tmp / "probe.py"
        self.probe.write_text(PROBE.replace("HOMEFAC", repr(str(self.fac).lstrip("/"))))

    def assert_contained(self, case, obs, where):
        blob = json.dumps(obs)
        for leak in ("SIBLING-SECRET", "WORKTREE-SECRET", "worktrees", '"t2"', "wt-secret", "sib-secret"):
            case.assertNotIn(leak, blob, f"{where}: {leak} reachable: {obs}")
        case.assertEqual(obs["sibfile"], "ERR", where)
        case.assertEqual(obs["p1sib"], "ERR", where)
        case.assertEqual(obs["cwd"], str(self.trial), where)
        case.assertEqual(obs["p1"], str(self.trial), where)


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class CwdEscapeTests(unittest.TestCase):
    """Group B (B1): the inherited cwd must not reach the real tree through `..` or /proc/<pid>/cwd."""

    def setUp(self):
        self.fx = CwdEscapeFixture()
        self.fx.build(self)

    def contained(self, cwd, **kw):
        out = self.fx.tmp / "obs.json"
        r = nsbox.run_contained(self.fx.trial, [sys.executable, str(self.fx.probe), str(out)], cwd=cwd, timeout=30,
                                home=self.fx.home, trials_root=self.fx.trials, hide_dirs=(str(self.fx.fac),),
                                unshare=UNSHARE, **kw)
        self.assertEqual(r.returncode, 0, r.output)
        return json.loads(out.read_text())

    def test_checker_with_cwd_in_trial_cannot_reach_sibling_via_dotdot_or_proc(self):
        obs = self.contained(self.fx.trial)
        self.fx.assert_contained(self, obs, "check.py cwd=trial")

    def test_cwd_equal_to_above_or_inside_a_hide_dir_is_refused(self):
        fx = self.fx
        for cwd in (fx.fac, fx.home, fx.wt, fx.trials, fx.trials / "t2"):
            with self.subTest(cwd=str(cwd)), self.assertRaises(ValueError):
                self.contained(cwd)

    def test_cwd_under_the_trial_dir_is_accepted_even_inside_a_hide_dir(self):
        sub = self.fx.trial / "work"
        sub.mkdir()
        obs = self.contained(sub)
        self.assertEqual(obs["cwd"], str(sub))
        self.assertEqual(obs["sibfile"], "ERR")

    def test_checker_with_no_cwd_from_a_hidden_dir_lands_in_the_trial(self):
        with mock.patch.dict(os.environ, {}):
            cwd = os.getcwd()
            os.chdir(self.fx.wt)
            self.addCleanup(os.chdir, cwd)
            obs = self.contained(None)
        self.fx.assert_contained(self, obs, "check.py cwd=None")

    def test_visible_cwd_outside_the_hidden_dirs_is_kept(self):
        obs = self.contained(self.fx.vis)
        self.assertEqual(obs["cwd"], str(self.fx.vis))
        self.assertEqual(obs["sibfile"], "ERR")

    def test_shim_claude_and_forwarder_cannot_reach_hidden_content(self):
        fx = self.fx
        out = fx.tmp / "claude-obs.json"
        claude = fx.tmp / "bin" / "claude"
        claude.parent.mkdir()
        claude.write_text(f"#!{sys.executable}\nimport runpy, sys\nsys.argv = ['probe', {str(out)!r}]\n"
                          f"runpy.run_path({str(fx.probe)!r}, run_name='__main__')\n")
        claude.chmod(0o755)
        token = fx.tmp / "token"
        token.write_text(TOKEN + "\n")
        token.chmod(0o600)
        shim = nsbox.write_shim(fx.trial, claude, token_file=token, home=fx.home, trials_root=fx.trials,
                                hide_dirs=(str(fx.fac),), unshare=UNSHARE)
        self.addCleanup(nsbox.stop_proxy, str(fx.trial))
        r = subprocess.run([str(shim)], cwd=fx.trial, env={"PATH": "/usr/bin:/bin"}, capture_output=True, text=True,
                           timeout=60)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        obs = json.loads(out.read_text())
        fx.assert_contained(self, obs, "claude")
        self.assertIn("fwd", obs, "forwarder not found inside the namespace")
        fwd = obs["fwd"]
        self.assertNotIn("t2", json.dumps(fwd))
        self.assertNotIn("SIBLING-SECRET", json.dumps(fwd))
        self.assertEqual(fwd["sib"], "ERR")
        self.assertEqual(fwd["root"], "ERR")

    def test_symlinked_hide_dir_is_hidden(self):
        link = self.fx.tmp / "linkhome"
        link.symlink_to(self.fx.fac)
        out = self.fx.tmp / "obs2.json"
        body = self.fx.tmp / "ls.py"
        body.write_text("import os, sys, json\nopen(sys.argv[1], 'w').write(json.dumps("
                        f"[os.listdir({str(self.fx.fac)!r}), os.listdir({str(link)!r})]))\n")
        r = nsbox.run_contained(self.fx.trial, [sys.executable, str(body), str(out)], cwd=self.fx.vis, timeout=30,
                                home=self.fx.home, trials_root=self.fx.trials, hide_dirs=(str(link),),
                                unshare=UNSHARE)
        self.assertEqual(r.returncode, 0, r.output)
        real, via_link = json.loads(out.read_text())
        self.assertEqual(real, ["trials"], "the real dir behind the symlink must be hidden (only the kept trial path)")
        self.assertEqual(via_link, ["trials"])
        self.assertNotIn("worktrees", real)

    def test_run_contained_default_env_is_an_allow_list(self):
        out = self.fx.tmp / "env.json"
        body = self.fx.tmp / "env.py"
        body.write_text("import os, sys, json\nopen(sys.argv[1], 'w').write(json.dumps(dict(os.environ)))\n")
        leaks = {"CLAUDE_CODE_MESSAGING_TOKEN": "msg-secret", "SOME_UNKNOWN_VAR": "x", "LC_ALL": "C.UTF-8"}
        with mock.patch.dict(os.environ, leaks):
            r = nsbox.run_contained(self.fx.trial, [sys.executable, str(body), str(out)], cwd=self.fx.vis, timeout=30,
                                    home=self.fx.home, trials_root=self.fx.trials, hide_dirs=(str(self.fx.fac),),
                                    unshare=UNSHARE, env_extra={"EXTRA_OK": "1"})
        self.assertEqual(r.returncode, 0, r.output)
        env = json.loads(out.read_text())
        self.assertNotIn("CLAUDE_CODE_MESSAGING_TOKEN", env)
        self.assertNotIn("SOME_UNKNOWN_VAR", env)
        self.assertNotIn("msg-secret", json.dumps(env))
        self.assertEqual(env["HOME"], str(self.fx.trial))
        self.assertEqual(env["LC_ALL"], "C.UTF-8")
        self.assertEqual(env["EXTRA_OK"], "1")
        self.assertIn("PATH", env)


class EnvAllowListComposeTests(unittest.TestCase):
    def test_env_pass_is_small_and_has_no_secret_names(self):
        self.assertEqual(set(nsbox.ENV_PASS), {"PATH", "LANG", "TERM", "TMPDIR"})


class CleanSkipTests(unittest.TestCase):
    def test_userns_available_reports_the_reason(self):
        ok, why = nsbox.userns_available("/nonexistent/unshare")
        self.assertFalse(ok)
        self.assertIn("/nonexistent/unshare", why)

    def test_group_b_skips_with_reason_when_namespaces_are_unavailable(self):
        env = dict(os.environ, NSBOX_TEST_UNSHARE="/nonexistent/unshare")
        r = subprocess.run([sys.executable, "-m", "unittest", "-v", "test_skilleval_nsbox.NsboxNamespaceTests",
                            "test_skilleval_nsbox.RunContainedTests"], cwd=Path(__file__).parent, env=env,
                           capture_output=True, text=True, timeout=120)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertNotIn("FAIL", r.stderr)
        self.assertNotIn("ERROR", r.stderr)
        self.assertIn("skipped \"user namespaces unavailable: cannot run /nonexistent/unshare", r.stderr)
        self.assertRegex(r.stderr, r"Ran \d+ tests?")
        self.assertRegex(r.stderr, r"OK \(skipped=\d+\)")


if __name__ == "__main__":
    unittest.main()
