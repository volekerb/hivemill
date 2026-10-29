"""nsbox allow-list mode (T-0160): additive make_cfg kwargs, pid=False, keep_env, rw/ro/masks view, fail closed.

Group A needs no namespaces. Group B runs real stage-1 namespaces on your-box and skips with the reason elsewhere.
Everything lives in temp dirs; the sockets and files hidden from inside are fake ones made here.
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

sys.path.insert(0, str(REPO))
from scout import egress, nsbox  # noqa: E402

UNSHARE = os.environ.get("NSBOX_TEST_UNSHARE", "unshare")
USERNS_OK, USERNS_WHY = nsbox.userns_available(UNSHARE)
SKIP_MSG = f"user namespaces unavailable: {USERNS_WHY}"
PY = sys.executable


def tmpdir(case):
    d = Path(tempfile.mkdtemp(prefix="zz-test-nsbox-allow-")).resolve()
    case.addCleanup(shutil.rmtree, d, True)
    return d


def golden(trial, trials_root, _claude, home, runtime, **over):
    """The dict origin/main's make_cfg returned for these inputs, written out by hand (captured from main)."""
    d = {
        "unshare": shutil.which("unshare"), "setpriv": shutil.which("setpriv"), "python": sys.executable,
        "nsbox": str(Path(nsbox.__file__).resolve()), "egress": str(Path(egress.__file__).resolve()),
        "uid": os.getuid(), "gid": os.getgid(), "net": False, "trial": str(trial), "trials_root": str(trials_root),
        "forward_sock": None,
        "hide_dirs": [f"{home}/.claude", f"{home}/.config", f"{home}/.ssh", f"{home}/.gnupg", f"{home}/factory", runtime],
        "hide_files": [f"{home}/.git-credentials", f"{home}/.netrc"],
        "keep": [str(trial)], "keep_ro": [], "claude": None, "cwd": None,
        "scrub": ["SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS", *nsbox.NET_ENV], "env_set": {},
    }
    d.update(over)
    return d


class GoldenDefaultsTests(unittest.TestCase):
    def setUp(self):
        self.root = tmpdir(self)
        self.trial = self.root / "trials" / "t1"
        self.env = mock.patch.dict(os.environ, {"XDG_RUNTIME_DIR": "/run/user/4242"})
        self.env.start()
        self.addCleanup(self.env.stop)

    def test_run_contained_args_reproduce_main(self):
        cwd = self.trial / "w"
        cfg = nsbox.make_cfg(self.trial, None, "/h", self.root / "trials", nsbox.HIDE_DIRS, vendor_dir="/v",
                             keep=[cwd], net=True, unshare="unshare", cwd=cwd)
        want = golden(self.trial, self.root / "trials", None, "/h", "/run/user/4242", net=True, keep_ro=["/v"],
                      keep=[str(self.trial), str(cwd)], cwd=str(cwd))
        self.assertEqual(cfg, want)

    def test_write_shim_args_reproduce_main(self):
        claude = "/opt/c/claude"
        cfg = nsbox.make_cfg(self.trial, claude, "/h", None, nsbox.HIDE_DIRS, vendor_dir=None, forward=True,
                             unshare="unshare")
        keeps = [str(self.trial), claude, os.path.realpath(claude)]
        want = golden(self.trial, self.root / "trials", claude, "/h", "/run/user/4242", keep=keeps, claude=claude,
                      forward_sock=str(self.trial / "proxy.sock"), scrub=["SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS"],
                      env_set={"HTTPS_PROXY": nsbox.PROXY_URL, "https_proxy": nsbox.PROXY_URL, "NO_PROXY": "",
                               "no_proxy": ""})
        self.assertEqual(cfg, want)

    def test_new_keys_only_when_non_default(self):
        base = nsbox.make_cfg(self.trial, None, "/h")
        for k in ("mode", "rw", "ro", "masks", "pid", "tmpfs_size", "tmpfs_sizes"):
            self.assertNotIn(k, base)
        dflt = nsbox.make_cfg(self.trial, None, "/h", mode="allow", tmpfs_size="8m", tmpfs_sizes={})
        self.assertNotIn("tmpfs_size", dflt)
        self.assertNotIn("tmpfs_sizes", dflt)
        allow = nsbox.make_cfg(self.trial, None, "/h", mode="allow", rw=["/a"], ro=["/b"], masks=["/b/m"], pid=False,
                                run_mask=False)
        self.assertEqual((allow["mode"], allow["rw"], allow["ro"], allow["masks"], allow["pid"]),
                         ("allow", ["/a"], ["/b"], ["/b/m"], False))

    def test_tmpfs_size_keys_appear_only_when_set_and_need_allow_mode(self):
        cfg = nsbox.make_cfg(self.trial, None, "/h", mode="allow", tmpfs_size="16m", tmpfs_sizes={"/h/x": "1g"})
        self.assertEqual((cfg["tmpfs_size"], cfg["tmpfs_sizes"]), ("16m", {"/h/x": "1g"}))
        with self.assertRaises(ValueError):
            nsbox.make_cfg(self.trial, None, "/h", tmpfs_size="16m")
        with self.assertRaises(ValueError):
            nsbox.make_cfg(self.trial, None, "/h", tmpfs_sizes={"/h": "16m"})

    def test_bad_tmpfs_size_is_refused(self):
        for bad in ("", "big", "8 m", "8m,mode=0777", "-1",
                    "0", "00", "0k", "0m", "0g", "0%",  # zero is an unlimited tmpfs
                    "17179869184g", "18446744073709551616", "1000000g",  # wrap to 0 in the kernel / absurdly big
                    "17g", "16385m", "101%", "1t"):
            with self.assertRaises(ValueError, msg=bad):
                nsbox.make_cfg(self.trial, None, "/h", mode="allow", tmpfs_size=bad)
            with self.assertRaises(ValueError, msg=bad):
                nsbox.make_cfg(self.trial, None, "/h", mode="allow", tmpfs_sizes={"/h": bad})

    def test_good_tmpfs_sizes_are_accepted(self):
        for ok in ("8m", "512k", "1g", "16g", "16384M", "1%", "100%", "1"):
            cfg = nsbox.make_cfg(self.trial, None, "/h", mode="allow", tmpfs_size=ok)
            self.assertEqual(cfg.get("tmpfs_size", "8m"), ok)

    def test_rw_without_allow_mode_is_refused(self):
        with self.assertRaises(ValueError):
            nsbox.make_cfg(self.trial, None, "/h", rw=["/a"])
        with self.assertRaises(ValueError):
            nsbox.make_cfg(self.trial, None, "/h", mode="bogus")

    def test_pid_false_with_forward_is_refused(self):
        with self.assertRaises(ValueError):
            nsbox.make_cfg(self.trial, None, "/h", pid=False, forward=True)
        with self.assertRaises(ValueError):
            nsbox.make_cfg(self.trial, None, "/h", mode="allow", rw=[str(self.trial)], pid=False, forward=True)
        self.assertEqual(nsbox.make_cfg(self.trial, None, "/h", pid=False, net=True)["pid"], False)
        self.assertTrue(nsbox.make_cfg(self.trial, None, "/h", pid=True, forward=True)["forward_sock"])

    def test_vendor_dir_and_keep_in_allow_mode_are_refused(self):
        with self.assertRaises(ValueError):
            nsbox.make_cfg(self.trial, None, "/h", mode="allow", vendor_dir="/v")
        with self.assertRaises(ValueError):
            nsbox.make_cfg(self.trial, None, "/h", mode="allow", keep=["/k"])
        nsbox.make_cfg(self.trial, None, "/h", mode="deny", vendor_dir="/v", keep=["/k"])

    def test_symlink_leaf_in_rw_or_ro_is_refused(self):
        real = self.root / "real"
        real.mkdir()
        link = self.root / "link"
        link.symlink_to(real)
        for kw in ("rw", "ro"):
            with self.assertRaises(ValueError):
                nsbox.make_cfg(self.trial, None, "/h", mode="allow", **{kw: [str(link)]})
        cfg = nsbox.make_cfg(self.trial, None, "/h", mode="allow", rw=[str(real)])
        self.assertEqual(cfg["rw"], [str(real)])

    def test_symlink_parent_in_rw_or_ro_is_refused(self):
        real = self.root / "realdir"
        (real / "sub").mkdir(parents=True)
        link = self.root / "linkdir"
        link.symlink_to(real)
        for kw in ("rw", "ro"):
            with self.assertRaises(ValueError) as cm:
                nsbox.make_cfg(self.trial, None, "/h", mode="allow", **{kw: [str(link / "sub")]})
            self.assertIn(str(link), str(cm.exception))
        cfg = nsbox.make_cfg(self.trial, None, "/h", mode="allow", rw=[str(real / "sub")])
        self.assertEqual(cfg["rw"], [str(real / "sub")])

    def test_claude_link_leaf_is_still_accepted_but_not_a_symlink_parent(self):
        real = self.root / "versions"
        real.mkdir()
        (real / "claude").write_text("#!/bin/sh\n")
        link = self.root / "bin" / "claude"
        link.parent.mkdir()
        link.symlink_to(real / "claude")
        cfg = nsbox.make_cfg(self.trial, str(link), "/h", mode="allow", run_mask=False)
        self.assertEqual(cfg["ro"], [str(link), str(real / "claude")])
        viadir = self.root / "viadir"
        viadir.symlink_to(self.root / "bin")
        with self.assertRaises(ValueError):
            nsbox.make_cfg(self.trial, str(viadir / "claude"), "/h", mode="allow")

    def test_open_chain_refuses_symlink_parent_and_reports_leaf_link(self):
        real = self.root / "r"
        (real / "d").mkdir(parents=True)
        (self.root / "l").symlink_to(real)
        (real / "leaf").symlink_to(real / "d")
        with self.assertRaises(SystemExit) as cm:
            nsbox._open_chain(str(self.root / "l" / "d"))
        self.assertIn("symlink", str(cm.exception))
        fd, link = nsbox._open_chain(str(real / "leaf"))
        self.assertEqual((fd, link), (None, str(real / "d")))
        fd, link = nsbox._open_chain(str(real / "d"))
        self.addCleanup(os.close, fd)
        self.assertIsNone(link)
        with self.assertRaises(SystemExit) as cm:
            nsbox._open_chain(str(real / "nope"))
        self.assertIn("missing", str(cm.exception))

    def test_stage1_argv_identical_for_pid_unset_and_true(self):
        a = nsbox.compose_stage1(nsbox.make_cfg(self.trial, None, "/h"), ["true"])
        b = nsbox.compose_stage1(nsbox.make_cfg(self.trial, None, "/h", pid=True), ["true"])
        self.assertEqual(a, b)
        self.assertEqual(a[1:8], ["-r", "-n", "-m", "--pid", "--fork", "--kill-child", "--mount-proc"])

    def test_pid_false_drops_the_pid_namespace_flags(self):
        argv = nsbox.compose_stage1(nsbox.make_cfg(self.trial, None, "/h", pid=False), ["true"])
        for flag in ("--pid", "--fork", "--kill-child", "--mount-proc"):
            self.assertNotIn(flag, argv)
        self.assertEqual(argv[1:6], ["-r", "-n", "-m", "--propagation", "private"])

    def test_keep_env_exempts_only_the_named_variable(self):
        scrub = nsbox.make_cfg(self.trial, None, "/h", net=True,
                               keep_env=("CLAUDE_CODE_OAUTH_TOKEN",))["scrub"]
        self.assertNotIn("CLAUDE_CODE_OAUTH_TOKEN", scrub)
        self.assertIn("ANTHROPIC_API_KEY", scrub)
        self.assertIn("CLAUDE_CODE_OAUTH_TOKEN", nsbox.make_cfg(self.trial, None, "/h", net=True)["scrub"])

    def test_allow_mode_masks_run_and_keeps_only_resolve_ro(self):
        cfg = nsbox.make_cfg(self.trial, None, "/h", mode="allow", hide_dirs=["/h"])
        self.assertEqual(cfg["hide_dirs"], ["/h", "/run"])
        want = ["/run/systemd/resolve"] if os.path.isdir("/run/systemd/resolve") else []
        self.assertEqual(cfg["ro"], want)
        off = nsbox.make_cfg(self.trial, None, "/h", mode="allow", hide_dirs=["/h"], run_mask=False)
        self.assertEqual((off["hide_dirs"], off["ro"]), (["/h"], []))
        self.assertNotIn("/run", nsbox.make_cfg(self.trial, None, "/h")["hide_dirs"])  # deny mode unchanged

    def test_allow_mode_does_not_make_trial_or_cwd_writable(self):
        cfg = nsbox.make_cfg(self.trial, None, "/h", mode="allow", cwd="/c", rw=["/w"])
        self.assertEqual((cfg["rw"], cfg["keep"], cfg["keep_ro"]), (["/w"], [], []))


def run_cfg(cfg, argv, env=None, timeout=60):
    return subprocess.run(nsbox.compose_stage1(cfg, argv), capture_output=True, text=True, timeout=timeout,
                          env=env or {"PATH": os.environ["PATH"]}, stdin=subprocess.DEVNULL)


def probe(code):
    return [PY, "-c", code]


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class AllowModeNamespaceTests(unittest.TestCase):
    def setUp(self):
        self.root = tmpdir(self)
        self.trial = self.root / "trials" / "t1"
        self.trial.mkdir(parents=True)
        self.home = self.root / "home"
        (self.home / ".ssh").mkdir(parents=True)
        (self.home / "secret").mkdir()
        (self.home / "secret" / "file").write_text("S")
        (self.home / ".ssh" / "key").write_text("K")
        (self.home / ".zshrc").write_text("orig\n")
        self.runtime = self.root / "run"
        self.runtime.mkdir()
        self.socks = []
        for p in (self.home / "agent.sock", self.runtime / "bus"):
            s = socket.socket(socket.AF_UNIX)
            s.bind(str(p))
            s.listen(1)
            self.socks.append(s)
            self.addCleanup(s.close)

    def cfg(self, **kw):
        kw.setdefault("hide_dirs", [str(self.home), str(self.runtime)])
        kw.setdefault("hide_files", ())
        return nsbox.make_cfg(self.trial, None, self.home, self.root / "trials", mode="allow", unshare=UNSHARE, **kw)

    def test_hides_home_runtime_and_sockets_and_isolates_writes(self):
        h, r = self.home, self.runtime
        code = f"""
import os, socket, json
out = {{}}
for name, p in (("secret", "{h}/secret/file"), ("key", "{h}/.ssh/key")):
    try: open(p).read(); out[name] = "readable"
    except OSError: out[name] = "blocked"
for name, p in (("home_sock", "{h}/agent.sock"), ("run_sock", "{r}/bus")):
    s = socket.socket(socket.AF_UNIX)
    try: s.connect(p); out[name] = "connected"
    except OSError: out[name] = "blocked"
open("{h}/.zshrc", "w").write("pwned\\n")
print(json.dumps(out))
"""
        r_ = run_cfg(self.cfg(), probe(code))
        self.assertEqual(r_.returncode, 0, r_.stderr)
        self.assertEqual(json.loads(r_.stdout), {"secret": "blocked", "key": "blocked", "home_sock": "blocked",
                                                 "run_sock": "blocked"})
        self.assertEqual((h / ".zshrc").read_text(), "orig\n")
        self.assertEqual((h / "secret" / "file").read_text(), "S")

    def test_rw_writable_and_visible_outside_ro_is_erofs(self):
        rwd, rod = self.root / "rwd", self.root / "rod"
        rwd.mkdir()
        rod.mkdir()
        (rod / "f").write_text("ro-data")
        code = f"""
import errno
open("{rwd}/new", "w").write("hello")
print(open("{rod}/f").read())
try: open("{rod}/g", "w")
except OSError as e: print(errno.errorcode[e.errno])
"""
        r_ = run_cfg(self.cfg(rw=[rwd], ro=[rod]), probe(code))
        self.assertEqual(r_.returncode, 0, r_.stderr)
        self.assertEqual(r_.stdout.split(), ["ro-data", "EROFS"])
        self.assertEqual((rwd / "new").read_text(), "hello")
        self.assertFalse((rod / "g").exists())

    def test_ordering_ro_parent_rw_child_mask_and_rebound_child(self):
        p = self.root / "P"
        for sub in ("c", "m/own", "m/other"):
            (p / sub).mkdir(parents=True)
        (p / "top").write_text("t")
        code = f"""
import os
P = "{p}"
def w(path):
    try: open(path + "/x", "w").close(); os.unlink(path + "/x"); return "rw"
    except OSError: return "ro"
print(w(P), w(P + "/c"), w(P + "/m/own"), sorted(os.listdir(P + "/m")))
"""
        # listed shortest-last on purpose: the order comes from the path depth, not from the list
        r_ = run_cfg(self.cfg(rw=[p / "m" / "own", p / "c"], ro=[p], masks=[p / "m"]), probe(code))
        self.assertEqual(r_.returncode, 0, r_.stderr)
        self.assertEqual(r_.stdout.strip(), "ro rw rw ['own']")
        self.assertEqual(sorted(os.listdir(p / "m")), ["other", "own"])

    def test_missing_bind_source_fails_closed_naming_the_path(self):
        marker = self.root / "ran"
        missing = self.root / "nope"
        r_ = run_cfg(self.cfg(rw=[missing]), probe(f"open('{marker}', 'w').close()"))
        self.assertNotEqual(r_.returncode, 0)
        self.assertIn(str(missing), r_.stderr)
        self.assertFalse(marker.exists())

    def test_parent_symlink_swapped_in_after_make_cfg_fails_closed(self):
        marker = self.root / "ran"
        parent = self.root / "swap"
        (parent / "sub").mkdir(parents=True)
        decoy = self.root / "decoy"
        (decoy / "sub").mkdir(parents=True)
        cfg = self.cfg(rw=[parent / "sub"])  # accepted: parent is a real dir now
        shutil.rmtree(parent)
        parent.symlink_to(decoy)  # the swap, after the checks
        r_ = run_cfg(cfg, probe(f"open('{marker}', 'w').close()"))
        self.assertNotEqual(r_.returncode, 0)
        self.assertIn(str(parent), r_.stderr)
        self.assertFalse(marker.exists())

    def test_missing_mask_fails_closed(self):
        marker = self.root / "ran"
        r_ = run_cfg(self.cfg(rw=[self.root], masks=[self.root / "nomask"]), probe(f"open('{marker}', 'w').close()"))
        self.assertNotEqual(r_.returncode, 0)
        self.assertIn("nomask", r_.stderr)
        self.assertFalse(marker.exists())

    def test_no_capabilities_and_mount_fails(self):
        code = """
import ctypes, re
caps = re.search(r"CapEff:\\s*(\\w+)", open("/proc/self/status").read()).group(1)
libc = ctypes.CDLL(None, use_errno=True)
rc = libc.mount(b"tmpfs", b"/tmp", b"tmpfs", 0, None)
print(int(caps, 16), rc, ctypes.get_errno() != 0)
"""
        r_ = run_cfg(self.cfg(rw=[self.root]), probe(code))
        self.assertEqual(r_.returncode, 0, r_.stderr)
        self.assertEqual(r_.stdout.split(), ["0", "-1", "True"])

    def test_trial_not_listed_is_read_only_and_hidden_cwd_falls_back_to_first_rw(self):
        rwd = self.root / "rwd"
        rwd.mkdir()
        cfg = self.cfg(rw=[rwd], ro=[self.trial], cwd=self.home)
        code = f"""
import os
try: open("{self.trial}/x", "w"); w = "writable"
except OSError as e: w = "EROFS" if e.errno == 30 else str(e.errno)
print(w, os.getcwd())
"""
        r_ = run_cfg(cfg, probe(code))
        self.assertEqual(r_.returncode, 0, r_.stderr)
        self.assertEqual(r_.stdout.split(), ["EROFS", str(rwd)])

    def test_trial_unlisted_is_not_bound_back_at_all_when_hidden(self):
        hide = [str(self.home), str(self.runtime), str(self.root / "trials")]
        r_ = run_cfg(self.cfg(hide_dirs=hide, rw=[self.root / "home" / "secret"]),
                     probe(f"import os; print(os.path.exists('{self.trial}'))"))
        self.assertEqual(r_.stdout.strip(), "False")

    def _fill(self, path, mib):
        code = f"""
import errno
try:
    with open("{path}/big", "wb") as f:
        f.write(b"x" * ({mib} << 20))
    print("ok")
except OSError as e:
    print(errno.errorcode[e.errno])
"""
        r_ = run_cfg(self._last_cfg, probe(code))
        self.assertEqual(r_.returncode, 0, r_.stderr)
        return r_.stdout.strip()

    def test_default_hide_tmpfs_is_8m(self):
        self._last_cfg = self.cfg()
        self.assertEqual(self._fill(self.home, 12), "ENOSPC")

    def test_tmpfs_size_is_honoured_and_capped(self):
        self._last_cfg = self.cfg(tmpfs_size="16m")
        self.assertEqual(self._fill(self.home, 12), "ok")
        self.assertEqual(self._fill(self.home, 20), "ENOSPC")

    def test_tmp_cap_at_workerbox_512m_is_enforced(self):
        # the workerbox cap on /tmp itself: 512m is the real size, filled in 1 MiB chunks (~512 MB RAM briefly)
        tmp = os.path.realpath("/tmp")
        self._last_cfg = self.cfg(hide_dirs=[tmp, str(self.runtime)], tmpfs_sizes={tmp: "512m"})
        code = f"""
import errno
n = 0
try:
    with open("{tmp}/big", "wb") as f:
        chunk = b"x" * (1 << 20)
        for n in range(1, 700):
            f.write(chunk); f.flush()
    print("unbounded")
except OSError as e:
    print(errno.errorcode[e.errno], n)
"""
        r_ = run_cfg(self._last_cfg, probe(code))
        self.assertEqual(r_.returncode, 0, r_.stderr)
        name, n = r_.stdout.split()
        self.assertEqual(name, "ENOSPC")
        self.assertTrue(500 <= int(n) <= 513, n)  # capped at 512 MiB, well past the 8m default

    def test_tmpfs_sizes_override_one_tree_and_mask(self):
        mask = self.root / "maskdir"
        mask.mkdir()
        self._last_cfg = self.cfg(tmpfs_sizes={str(self.home): "32m", str(mask): "16m"}, masks=[mask])
        self.assertEqual(self._fill(self.home, 20), "ok")
        self.assertEqual(self._fill(self.home, 40), "ENOSPC")
        self.assertEqual(self._fill(self.runtime, 12), "ENOSPC")  # not listed: default size
        self.assertEqual(self._fill(mask, 12), "ok")
        self.assertEqual(self._fill(mask, 20), "ENOSPC")



SWAP_SCRIPT = """
import json, os, shutil, sys
sys.path.insert(0, {repo!r})
from scout import nsbox
cfg, parent, decoy = json.loads(sys.argv[1]), sys.argv[2], sys.argv[3]
real_open = nsbox._open_chain
def open_then_swap(path):
    r = real_open(path)  # the sources are open now: swap the parent for a symlink, as a racing process would
    if os.path.isdir(parent) and not os.path.islink(parent):
        shutil.rmtree(parent)
        os.symlink(decoy, parent)
    return r
nsbox._open_chain = open_then_swap
nsbox._mount = lambda *a, **k: print("MOUNT", a[1], flush=True)
try:
    nsbox._allow_view(cfg)
except SystemExit as e:
    print("EXIT", e)
"""


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class NoFollowTargetTests(unittest.TestCase):
    """T-0256: mount targets, makedirs and masks are walked with openat(O_PATH|O_NOFOLLOW) too."""

    def setUp(self):
        self.root = tmpdir(self)
        self.trial = self.root / "trials" / "t1"
        self.trial.mkdir(parents=True)
        self.parent, self.decoy = self.root / "swap", self.root / "decoy"
        (self.parent / "sub").mkdir(parents=True)
        (self.decoy / "sub").mkdir(parents=True)

    def swap_run(self, **kw):
        cfg = nsbox.make_cfg(self.trial, None, self.root / "home", self.root / "trials", mode="allow", hide_dirs=(),
                             hide_files=(), unshare=UNSHARE, run_mask=False, **kw)
        code = SWAP_SCRIPT.format(repo=str(REPO))
        return subprocess.run([UNSHARE, "-r", "-m", PY, "-I", "-c", code, json.dumps(cfg), str(self.parent), str(self.decoy)],
                              capture_output=True, text=True, timeout=60, stdin=subprocess.DEVNULL)

    def test_target_parent_swapped_after_sources_are_open_is_refused(self):
        r_ = self.swap_run(rw=[self.parent / "sub"])
        self.assertEqual(r_.returncode, 0, r_.stderr)
        self.assertIn("EXIT nsbox: ", r_.stdout)
        self.assertIn(str(self.parent), r_.stdout)
        self.assertNotIn("MOUNT", r_.stdout)

    def test_mask_parent_swapped_is_refused(self):
        r_ = self.swap_run(rw=[self.root / "trials"], masks=[self.parent / "sub"])
        self.assertEqual(r_.returncode, 0, r_.stderr)
        self.assertIn("EXIT nsbox: ", r_.stdout)
        self.assertIn(str(self.parent), r_.stdout)
        self.assertEqual(r_.stdout.count("MOUNT"), 1)  # only the rw bind of trials, never the tmpfs mask

    def test_symlinked_mask_leaf_is_refused_before_exec(self):
        marker = self.root / "ran"
        (self.root / "m").symlink_to(self.decoy)
        cfg = nsbox.make_cfg(self.trial, None, self.root / "home", self.root / "trials", mode="allow", unshare=UNSHARE,
                             hide_dirs=(), hide_files=(), rw=[self.root / "trials"], masks=[self.root / "m"])
        r_ = run_cfg(cfg, probe(f"open('{marker}', 'w').close()"))
        self.assertNotEqual(r_.returncode, 0)
        self.assertIn(str(self.root / "m"), r_.stderr)
        self.assertFalse(marker.exists())

    def test_missing_targets_under_a_hidden_tree_are_created_and_bound(self):
        home = self.root / "home"
        (home / "a" / "b").mkdir(parents=True)
        (home / "a" / "b" / "f").write_text("F")
        cfg = nsbox.make_cfg(self.trial, None, home, self.root / "trials", mode="allow", unshare=UNSHARE,
                             hide_dirs=[str(home)], hide_files=(), rw=[home / "a" / "b"], ro=[home / "a" / "b" / "f"])
        r_ = run_cfg(cfg, probe(f"print(open('{home}/a/b/f').read())"))
        self.assertEqual((r_.returncode, r_.stdout.strip()), (0, "F"), r_.stderr)


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class NoFollowHideTests(unittest.TestCase):
    """T-0462: hide-dir tmpfs mounts and hide_files binds are no-follow fd mounts too."""

    def setUp(self):
        self.root = tmpdir(self)
        self.trial = self.root / "trials" / "t1"
        self.trial.mkdir(parents=True)
        self.parent, self.decoy = self.root / "swap", self.root / "decoy"
        for d in (self.parent, self.decoy):
            (d / "sub").mkdir(parents=True)
            (d / "f").write_text("S")
        self.marker = self.root / "ran"

    def run_swapped(self, **kw):
        cfg = nsbox.make_cfg(self.trial, None, self.root / "home", self.root / "trials", mode="allow", unshare=UNSHARE,
                             rw=[self.root / "trials"], **kw)
        shutil.rmtree(self.parent)
        self.parent.symlink_to(self.decoy)  # the swap, after make_cfg
        return run_cfg(cfg, probe(f"open('{self.marker}', 'w').close()"))

    def test_hide_dir_parent_swapped_after_make_cfg_is_refused(self):
        r_ = self.run_swapped(hide_dirs=[str(self.parent / "sub")], hide_files=())
        self.assertNotEqual(r_.returncode, 0)
        self.assertIn(str(self.parent), r_.stderr)
        self.assertFalse(self.marker.exists())

    def test_hide_dir_leaf_symlink_is_refused(self):
        (self.root / "link").symlink_to(self.decoy)
        cfg = nsbox.make_cfg(self.trial, None, self.root / "home", self.root / "trials", mode="allow", unshare=UNSHARE,
                             rw=[self.root / "trials"], hide_dirs=[str(self.root / "link")], hide_files=())
        r_ = run_cfg(cfg, probe(f"open('{self.marker}', 'w').close()"))
        self.assertNotEqual(r_.returncode, 0)
        self.assertIn(str(self.root / "link"), r_.stderr)
        self.assertFalse(self.marker.exists())

    def test_hide_file_parent_swapped_after_make_cfg_is_refused(self):
        r_ = self.run_swapped(hide_dirs=(), hide_files=[str(self.parent / "f")])
        self.assertNotEqual(r_.returncode, 0)
        self.assertIn(str(self.parent), r_.stderr)
        self.assertFalse(self.marker.exists())

    def test_plain_hide_dir_and_file_are_still_hidden(self):
        cfg = nsbox.make_cfg(self.trial, None, self.root / "home", self.root / "trials", mode="allow", unshare=UNSHARE,
                             rw=[self.root / "trials"], hide_dirs=[str(self.parent / "sub")],
                             hide_files=[str(self.decoy / "f")])
        r_ = run_cfg(cfg, probe(f"import os; print(repr(open('{self.decoy}/f').read()), os.listdir('{self.parent}/sub'))"))
        self.assertEqual((r_.returncode, r_.stdout.strip()), (0, "'' []"), r_.stderr)


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class PidAndEnvNamespaceTests(unittest.TestCase):
    def setUp(self):
        self.root = tmpdir(self)

    def cfg(self, **kw):
        return nsbox.make_cfg(self.root, None, self.root, self.root, hide_dirs=[], hide_files=(), unshare=UNSHARE, **kw)

    def test_pid_false_target_keeps_the_stage1_pid(self):
        argv = nsbox.compose_stage1(self.cfg(pid=False), probe("import os; print(os.getpid())"))
        p = subprocess.Popen(argv, stdout=subprocess.PIPE, text=True, env={"PATH": os.environ["PATH"]},
                             stdin=subprocess.DEVNULL)
        out, _ = p.communicate(timeout=60)
        self.assertEqual(p.returncode, 0)
        self.assertEqual(int(out), p.pid)

    def test_pid_true_target_is_pid_in_new_namespace(self):
        r_ = run_cfg(self.cfg(), probe("import os; print(os.getpid())"))
        self.assertNotEqual(int(r_.stdout), 0)
        self.assertLess(int(r_.stdout), 1000)

    def test_keep_env_token_reaches_target_and_is_scrubbed_without_it(self):
        env = {"PATH": os.environ["PATH"], "CLAUDE_CODE_OAUTH_TOKEN": "dummy-token", "ANTHROPIC_API_KEY": "dummy-key"}
        code = "import os; print(os.environ.get('CLAUDE_CODE_OAUTH_TOKEN'), os.environ.get('ANTHROPIC_API_KEY'))"
        kept = run_cfg(self.cfg(net=True, keep_env=("CLAUDE_CODE_OAUTH_TOKEN",)), probe(code), env=env)
        self.assertEqual(kept.stdout.split(), ["dummy-token", "None"])
        scrubbed = run_cfg(self.cfg(net=True), probe(code), env=env)
        self.assertEqual(scrubbed.stdout.split(), ["None", "None"])


if __name__ == "__main__":
    unittest.main()


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class RunMaskTests(unittest.TestCase):
    """T-0673: the host's /run sockets (container engine, ssh-unix-local) are unreachable in allow mode."""
    SOCKS = ("/run/docker.sock", "/var/run/docker.sock", "/run/ssh-unix-local/socket")

    def setUp(self):
        self.root = tmpdir(self)
        self.trial = self.root / "trials" / "t1"
        self.trial.mkdir(parents=True)

    def run_in(self, code, **kw):
        cfg = nsbox.make_cfg(self.trial, None, self.root / "home", self.root / "trials", mode="allow", hide_dirs=(),
                             hide_files=(), unshare=UNSHARE, rw=[self.trial], **kw)
        return run_cfg(cfg, probe(code))

    def test_run_sockets_cannot_be_connected_and_run_is_empty_but_resolve(self):
        code = f"""
import os, socket, json
out = {{}}
for p in {self.SOCKS!r}:
    s = socket.socket(socket.AF_UNIX)
    try: s.connect(p); out[p] = "connected"
    except OSError as e: out[p] = "blocked"
out["ls"] = sorted(os.listdir("/run"))
print(json.dumps(out))
"""
        r = self.run_in(code)
        self.assertEqual(r.returncode, 0, r.stderr)
        out = json.loads(r.stdout)
        for p in self.SOCKS:
            self.assertEqual(out[p], "blocked", p)
        self.assertEqual(out["ls"], ["systemd"] if os.path.isdir("/run/systemd/resolve") else [])

    @unittest.skipUnless(os.path.exists("/run/docker.sock"), "no /run/docker.sock on this host to compare against")
    def test_without_the_mask_the_host_socket_is_reachable(self):
        # control: proves the probe can connect at all, so "blocked" above is the mask and not a broken probe
        r = self.run_in("import socket; s = socket.socket(socket.AF_UNIX); s.connect('/run/docker.sock'); print('ok')",
                        run_mask=False)
        self.assertEqual((r.returncode, r.stdout.strip()), (0, "ok"), r.stderr)

    @unittest.skipUnless(os.path.isfile("/run/systemd/resolve/stub-resolv.conf"), "no systemd-resolved stub file")
    def test_resolv_conf_still_readable_and_read_only(self):
        code = """
import json
out = {"nameserver": "nameserver" in open("/etc/resolv.conf").read()}
try: open("/run/systemd/resolve/stub-resolv.conf", "a"); out["rw"] = True
except OSError: out["rw"] = False
print(json.dumps(out))
"""
        r = self.run_in(code)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(json.loads(r.stdout), {"nameserver": True, "rw": False})

    def test_eval_profile_run_sockets_unreachable(self):
        from scout import sandbox
        socks = []
        for p in self.SOCKS:
            c = socket.socket(socket.AF_UNIX)
            try:
                c.connect(p)  # control: connectable outside, or not asserted
                socks.append(p)
            except OSError:
                pass
            finally:
                c.close()
        if not socks:
            self.skipTest("no connectable host /run socket on this box")
        code = f"""
import socket, json
out = {{}}
for p in {socks!r}:
    s = socket.socket(socket.AF_UNIX)
    try: s.connect(p); out[p] = "connected"
    except OSError as e: out[p] = "blocked"
print(json.dumps(out))
"""
        claude = self.root / "home/.local/bin/claude"  # the eval profile execs claude itself: a probe stands in for it
        claude.parent.mkdir(parents=True)
        claude.write_text(f"#!{PY}\n{code}")
        claude.chmod(0o755)
        (self.trial / "work").mkdir()
        for d in ("bin", "scout", "role"):
            (self.trial / d).mkdir()
        cfg = sandbox.eval_ns_cfg(self.trial, home=self.root / "home", real_claude=claude, unshare=UNSHARE)
        r = run_cfg(cfg, [])
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(set(json.loads(r.stdout).values()), {"blocked"})
