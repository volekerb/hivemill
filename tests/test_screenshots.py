"""ci/screenshots.py: the refusals through the real entry point, the download checks (size and sha256 before unzip),
the missing-library abort, cleanup on success / exception / SIGINT / SIGTERM, and the seed + serve + scrub path for real
against a temp demo home. The download is a fake zip built here and the browser is a stub script written here; the real
Chrome zip is never downloaded or unzipped."""
import contextlib
import hashlib
import io
import os
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import time
import unittest
import unittest.mock
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "ci" / "screenshots.py"
sys.path.insert(0, str(ROOT / "ci"))
import screenshots  # noqa: E402

NOW = "2026-10-25T12:00:00+00:00"
PREFIXES = ("FACTORY_", "HERDR_", "SCRUB_")

PNG_LIB = '''
import struct, zlib


def png_chunk(kind, data):
    return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xffffffff)


def make_png(w, h, row_fn, ctype=2, depth=8, interlace=0, filt=None, pad=0):
    # A PNG built with zlib + struct. row_fn(y) -> the row's w * channels bytes, stored with filter type 0 unless
    # filt(y, row, prev) -> (filter type, filtered bytes) is given. pad adds an ignorable tEXt chunk.
    ch = 3 if ctype == 2 else 4
    raw, prev = b"", bytes(w * ch)
    for y in range(h):
        row = row_fn(y)
        ft, data = filt(y, row, prev) if filt else (0, row)
        raw += bytes([ft]) + data
        prev = row
    out = b"\\x89PNG\\r\\n\\x1a\\n" + png_chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, depth, ctype, 0, 0, interlace))
    if pad:
        out += png_chunk(b"tEXt", b"pad\\x00" + b"x" * pad)
    return out + png_chunk(b"IDAT", zlib.compress(raw)) + png_chunk(b"IEND", b"")


def text_like_row(w, bg, fg, y):
    # every 8th row is a "line of text": a ramp between fg and bg with many distinct colours; the rest is flat
    if y % 8:
        return bytes((bg, bg, bg)) * w
    px = []
    for x in range(w):
        a = bg + (fg - bg) * ((x * 37) % 256) // 255
        px.append(bytes((a, a, (a + (x % 5) * 3) % 256)))
    return b"".join(px)
'''
exec(PNG_LIB)

STUB = '''#!/usr/bin/env python3
''' + PNG_LIB + '''
import sys
from pathlib import Path
here = Path(__file__).resolve().parent
w = h = scheme = dest = None
for a in sys.argv[1:]:
    if a.startswith("--window-size="):
        w, h = map(int, a.split("=", 1)[1].split(","))
    elif a.startswith("--blink-settings=preferredColorScheme="):
        scheme = a.rsplit("=", 1)[1]
    elif a.startswith("--screenshot="):
        dest = a.split("=", 1)[1]
mode = (here / "mode").read_text().strip()
light = scheme == "1"  # Blink: 1 is light, 0 is dark
if mode == "swapped":
    light = not light
bg, fg = (245, 20) if light else (20, 235)
if mode == "same":
    bg, fg = 245, 20
pad = 400000 if mode == "always-big" or mode == "big-at-860" and h == 860 else 0
if mode == "blank":
    fn = lambda y: bytes((bg, bg, bg)) * w
else:
    fn = lambda y: text_like_row(w, bg, fg, y)
Path(dest).write_bytes(make_png(w, h, fn, pad=pad))
'''
LDD_OK = "#!/bin/sh\necho '\tlibc.so.6 => /lib/x86_64-linux-gnu/libc.so.6 (0x00007f)'\n"
LDD_MISSING = "#!/bin/sh\necho '\tlibnss3.so => not found'\necho '\tlibc.so.6 => /lib/libc.so.6 (0x00007f)'\n"


def fake_zip(path, mode="ok"):
    with zipfile.ZipFile(path, "w") as z:
        info = zipfile.ZipInfo("chrome-headless-shell-linux64/chrome-headless-shell")
        info.external_attr = 0o755 << 16
        z.writestr(info, STUB)
        z.writestr("chrome-headless-shell-linux64/mode", mode)
    return Path(path).read_bytes()


REAL_STOP = screenshots.stop
SEEN_TMPS = []  # every temp dir any test's run created; tearDownModule checks no process still references one


def referencing(markers):
    """pids of our own processes whose cmdline or environment mentions one of the marker paths. Unreadable
    /proc entries (other uids, exited, zombies) are skipped."""
    found = []
    uid = os.getuid()
    for entry in Path("/proc").iterdir():
        if not entry.name.isdigit() or int(entry.name) == os.getpid():
            continue
        try:
            if entry.stat().st_uid != uid:
                continue
            blob = (entry / "cmdline").read_bytes() + (entry / "environ").read_bytes()
        except (OSError, ProcessLookupError):
            continue
        text = blob.decode("utf-8", "replace")
        if any(m in text for m in markers):
            found.append(int(entry.name))
    return found


TEST_PGIDS = set()  # process groups of helpers this module started


def kill_group(pgid):
    try:
        os.killpg(pgid, signal.SIGKILL)
    except (ProcessLookupError, PermissionError):
        pass


def tearDownModule():
    for pgid in TEST_PGIDS:
        kill_group(pgid)
    leaked = referencing([str(d) for d in SEEN_TMPS])
    assert not leaked, "processes still reference this run's temp demo homes: %s" % leaked


def group_gone(pgid):
    try:
        os.killpg(pgid, 0)
    except ProcessLookupError:
        return True
    return False


def alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


class ScreenshotsBase(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory()
        self.addCleanup(self.scratch.cleanup)
        self.dir = Path(self.scratch.name)
        self.images = self.dir / "images"
        self.tmps, self.pids, self.spawned, self.serve_pids = [], [], [], []
        cwd = os.getcwd()
        os.chdir(ROOT)
        self.addCleanup(os.chdir, cwd)
        env = {k: v for k, v in os.environ.items() if not k.startswith(PREFIXES)}
        for p in (unittest.mock.patch.dict(os.environ, env, clear=True),
                  unittest.mock.patch.object(screenshots, "IMAGES", self.images)):
            p.start()
            self.addCleanup(p.stop)
        self.fonts()
        real_mkdtemp, real_spawn = tempfile.mkdtemp, screenshots.spawn

        def mkdtemp(*a, **kw):
            d = real_mkdtemp(*a, **kw)
            self.tmps.append(Path(d))
            SEEN_TMPS.append(Path(d))
            return d

        def spawn(cmd, **kw):
            proc = real_spawn(cmd, **kw)
            self.pids.append(proc.pid)
            self.spawned.append((cmd, kw.get("env")))
            if "serve" in cmd:
                self.serve_pids.append(proc.pid)
                self.addCleanup(REAL_STOP, proc)  # a failing test must not leave its server running
            return proc

        for p in (unittest.mock.patch.object(tempfile, "mkdtemp", mkdtemp),
                  unittest.mock.patch.object(screenshots, "spawn", spawn)):
            p.start()
            self.addCleanup(p.stop)

    def fonts(self, listing="DejaVu Sans\n", match=None):
        """Install fake fc-list / fc-match scripts; match defaults to an existing (fake) font file."""
        font = self.dir / "DejaVuSans.ttf"
        font.write_bytes(b"font")
        for name, text in (("fc-list", listing), ("fc-match", str(font) if match is None else match)):
            path = self.dir / name
            path.write_text("#!/bin/sh\ncat <<'EOF'\n%s\nEOF\n" % text.rstrip("\n"))
            path.chmod(0o755)
        for attr, name in (("FC_LIST", "fc-list"), ("FC_MATCH", "fc-match")):
            p = unittest.mock.patch.object(screenshots, attr, str(self.dir / name))
            p.start()
            self.addCleanup(p.stop)

    def use_zip(self, mode="ok", ldd=LDD_OK, size_delta=0, sha=None):
        """Patch download to write a fake zip and the pins to match it (size_delta / sha break them on purpose)."""
        zpath = self.dir / "fake.zip"
        data = fake_zip(zpath, mode)
        calls = []

        def download(url, dest):
            calls.append(url)
            Path(dest).write_bytes(data)

        ldd_path = self.dir / "ldd"
        ldd_path.write_text(ldd)
        ldd_path.chmod(0o755)
        for p in (unittest.mock.patch.object(screenshots, "download", download),
                  unittest.mock.patch.object(screenshots, "SIZE", len(data) + size_delta),
                  unittest.mock.patch.object(screenshots, "SHA256", sha or hashlib.sha256(data).hexdigest()),
                  unittest.mock.patch.object(screenshots, "LDD", str(ldd_path))):
            p.start()
            self.addCleanup(p.stop)
        return calls

    def run_main(self, *args, env=None):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err), \
                unittest.mock.patch.dict(os.environ, env or {}):
            code = screenshots.main(["--now", NOW] + list(args))
        return code, out.getvalue(), err.getvalue()

    def assert_clean(self):
        self.assertTrue(self.tmps, "no temp dir was created")
        for d in self.tmps:
            self.assertFalse(d.exists(), "temp dir left behind: %s" % d)
        for pid in self.pids:
            self.assertFalse(alive(pid), "child process %d still running" % pid)
            self.assertTrue(group_gone(pid), "process group %d still has members" % pid)
        self.assertEqual(referencing([str(d) for d in self.tmps]), [], "a process still references the temp demo home")


class TestRefusals(unittest.TestCase):
    def cli(self, cwd, *args):
        return subprocess.run([sys.executable, str(SCRIPT)] + list(args), cwd=str(cwd), capture_output=True, text=True)

    def test_refuses_outside_a_checkout(self):
        with tempfile.TemporaryDirectory() as d:
            r = self.cli(d)
        self.assertEqual(r.returncode, 3, r.stderr)
        self.assertIn("run this from the factory checkout", r.stderr)

    def test_refuses_out_outside_docs_images(self):
        with tempfile.TemporaryDirectory() as d:
            r = self.cli(ROOT, "--out", d)
        self.assertEqual(r.returncode, 3, r.stderr)
        self.assertIn("outside", r.stderr)

    def test_refuses_out_that_escapes_with_dotdot(self):
        r = self.cli(ROOT, "--out", str(ROOT / "docs" / "images" / ".." / "x"))
        self.assertEqual(r.returncode, 3, r.stderr)

    def test_accepts_checkout_and_default_out(self):
        # both guards pass, so the run reaches argument validation (exit 2) without creating a temp dir
        r = self.cli(ROOT, "--pages", "bogus")
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("unknown page", r.stderr)
        r = self.cli(ROOT / "ci", "--out", str(ROOT / "docs" / "images" / "sub"), "--pages", "bogus")
        self.assertEqual(r.returncode, 2, r.stderr)

    def test_source_has_no_shell_true_and_no_shell_draft(self):
        self.assertNotIn("shell=True", SCRIPT.read_text(encoding="utf-8"))
        self.assertFalse((ROOT / "ci" / "screenshots.sh").exists())


class TestDownloadChecks(ScreenshotsBase):
    def never_unzip(self):
        p = unittest.mock.patch.object(screenshots, "unzip", side_effect=AssertionError("unzip was called"))
        p.start()
        self.addCleanup(p.stop)

    def test_size_mismatch_aborts_before_unzip(self):
        calls = self.use_zip(size_delta=1)
        self.never_unzip()
        code, out, err = self.run_main("--out", str(self.images))
        self.assertEqual(code, 3, err)
        self.assertIn("size mismatch", err)
        self.assertEqual(calls, [screenshots.URL])
        self.assert_clean()

    def test_sha256_mismatch_aborts_before_unzip(self):
        self.use_zip(sha="0" * 64)
        self.never_unzip()
        code, out, err = self.run_main("--out", str(self.images))
        self.assertEqual(code, 3, err)
        self.assertIn("sha256 mismatch", err)
        self.assert_clean()

    def test_only_the_pinned_url_is_fetched(self):
        self.assertEqual(screenshots.URL, "https://storage.googleapis.com/chrome-for-testing-public/131.0.6778.204/"
                                          "linux64/chrome-headless-shell-linux64.zip")
        self.assertEqual(screenshots.SIZE, 113084320)
        self.assertEqual(screenshots.SHA256, "afaac86e302c4874245991a3d509d529c50ecdd447affff0cdc2b08520906c32")

    def test_missing_library_aborts_with_the_not_found_lines(self):
        self.use_zip(ldd=LDD_MISSING)
        p = unittest.mock.patch.object(screenshots, "seed", side_effect=AssertionError("seed ran after ldd failed"))
        p.start()
        self.addCleanup(p.stop)
        code, out, err = self.run_main("--out", str(self.images))
        self.assertEqual(code, 1, err)
        self.assertIn("libnss3.so => not found", err)
        self.assertNotIn("libc.so.6", err)
        self.assert_clean()

    def test_zip_member_escaping_the_target_is_rejected(self):
        evil = self.dir / "evil.zip"
        with zipfile.ZipFile(evil, "w") as z:
            z.writestr("../escape.txt", "x")
        with self.assertRaises(screenshots.Failed):
            screenshots.unzip(evil, self.dir / "target")
        self.assertFalse((self.dir / "escape.txt").exists())


class TestCleanup(ScreenshotsBase):
    def seed_that(self, action):
        def seed(demo, now):
            proc = screenshots.spawn(["sleep", "60"])
            self.assertTrue(alive(proc.pid))
            action()
            time.sleep(2)  # a delivered signal raises here
        p = unittest.mock.patch.object(screenshots, "seed", seed)
        p.start()
        self.addCleanup(p.stop)

    def test_cleanup_on_exception(self):
        self.use_zip()

        def boom():
            raise RuntimeError("boom")
        self.seed_that(boom)
        with self.assertRaises(RuntimeError), contextlib.redirect_stdout(io.StringIO()):
            screenshots.main(["--now", NOW, "--out", str(self.images)])
        self.assert_clean()

    def test_cleanup_on_sigint(self):
        self.use_zip()
        self.seed_that(lambda: os.kill(os.getpid(), signal.SIGINT))
        code, out, err = self.run_main("--out", str(self.images))
        self.assertEqual(code, 130, err)
        self.assertIn("cleanup: removed", out)
        self.assert_clean()

    def test_cleanup_on_sigterm_and_handlers_restored(self):
        self.use_zip()
        before = signal.getsignal(signal.SIGTERM)
        self.seed_that(lambda: os.kill(os.getpid(), signal.SIGTERM))
        code, out, err = self.run_main("--out", str(self.images))
        self.assertEqual(code, 130, err)
        self.assert_clean()
        self.assertEqual(signal.getsignal(signal.SIGTERM), before)


def snapshot(root):
    """{relative path: (kind, mtime_ns, content)} for everything under root, root itself included."""
    snap = {}
    for p in [root] + sorted(root.rglob("*")):
        st = p.lstat()
        snap[str(p.relative_to(root))] = ("d" if p.is_dir() else "f", st.st_mtime_ns,
                                          None if p.is_dir() else p.read_bytes())
    return snap


class TestIsolation(ScreenshotsBase):
    """The capture flow (browser stubbed) must never read, create or modify the caller's HOME or FACTORY_HOME."""

    def make_sentinel(self, name):
        root = self.dir / name
        (root / "sub").mkdir(parents=True)
        (root / "sub" / "secret.txt").write_text("do not read " + name)
        (root / ".claude").mkdir()
        (root / ".claude" / "settings.json").write_text("{}")
        return root

    def test_capture_flow_leaves_sentinel_home_and_factory_home_untouched(self):
        self.use_zip()
        home, fhome = self.make_sentinel("sentinel-home"), self.make_sentinel("sentinel-factory")
        before = (snapshot(home), snapshot(fhome))
        env = {"HOME": str(home), "FACTORY_HOME": str(fhome), "FACTORY_AGENT": "owner", "FACTORY_DB": str(fhome / "db"),
               "HERDR_PANE": "p1"}
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home", env=env)
        self.assertEqual(code, 0, out + err)
        self.assertEqual((snapshot(home), snapshot(fhome)), before)
        self.assertTrue((self.images / "home-light.png").is_file())
        self.assertTrue(self.spawned)
        for cmd, child_env in self.spawned:
            self.assertNotIn(str(home), " ".join(cmd))
            self.assertNotIn(str(fhome), " ".join(cmd))
            for k, v in (child_env or {}).items():
                self.assertNotIn(str(home), v, k)
                self.assertNotIn(str(fhome), v, k)
        self.assert_clean()

    def test_server_and_seed_get_a_clean_env(self):
        self.use_zip()
        env = {"HOME": "/nonexistent-caller-home", "FACTORY_HOME": "/nonexistent-caller-factory",
               "FACTORY_AGENT": "owner", "FACTORY_SERVE_RELOAD_INTERVAL": "1", "HERDR_PANE": "p1",
               "ANTHROPIC_API_KEY": "sk-test", "SCRUB_TERMS_FILE": ""}
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home", env=env)
        self.assertEqual(code, 0, out + err)
        seed_env = next(e for c, e in self.spawned if any(str(a).endswith("demo_seed.py") for a in c))
        serve_env = next(e for c, e in self.spawned if "serve" in c)
        self.assertEqual(set(seed_env), {"HOME", "PATH", "LANG"})
        self.assertEqual(set(serve_env), {"HOME", "PATH", "LANG", "FACTORY_HOME"})
        self.assertEqual(serve_env["HOME"], serve_env["FACTORY_HOME"] + "/home")
        self.assertNotIn("nonexistent", " ".join(serve_env.values()) + " ".join(seed_env.values()))

    def test_clean_env_drops_every_inherited_variable(self):
        with unittest.mock.patch.dict(os.environ, {"FACTORY_X": "1", "HERDR_Y": "2", "PATH": "/p"}):
            env = screenshots.clean_env("/h")
            self.assertEqual(env, {"HOME": "/h", "PATH": "/p", "LANG": "C.UTF-8"})
            self.assertEqual(screenshots.clean_env("/h", "/f")["FACTORY_HOME"], "/f")


class TestStop(unittest.TestCase):
    def setUp(self):
        p = unittest.mock.patch.object(screenshots, "GRACE", 0.5)
        p.start()
        self.addCleanup(p.stop)

    def wait_gone(self, pid):
        end = time.monotonic() + 5
        while alive(pid) and time.monotonic() < end:
            time.sleep(0.05)
        return not alive(pid)

    def leader_with_helper(self, helper_body):
        """A group leader that starts a helper in its group and exits at once (so the leader is reaped while the helper
        keeps the group alive). The helper has no pipes (stdio is /dev/null) and ignores SIGPIPE, so nothing it does can
        kill it early; it announces readiness by writing its pid to a file in a temp dir. Returns (leader, helper pid)."""
        ready = Path(tempfile.mkdtemp(prefix="shots-helper-"))
        self.addCleanup(lambda: shutil.rmtree(ready, ignore_errors=True))
        flag = ready / "ready"
        helper_code = ("import os, signal, time\nsignal.signal(signal.SIGPIPE, signal.SIG_IGN)\n%s\n"
                       "open(%r + '.tmp', 'w').write(str(os.getpid()))\nos.rename(%r + '.tmp', %r)\ntime.sleep(60)\n"
                       % (helper_body, str(flag), str(flag), str(flag)))
        code = ("import subprocess, sys\n"
                "subprocess.Popen([sys.executable, '-c', %r], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,"
                " stderr=subprocess.DEVNULL)\n" % helper_code)
        proc = subprocess.Popen([sys.executable, "-c", code], start_new_session=True,
                                stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        TEST_PGIDS.add(proc.pid)
        self.addCleanup(kill_group, proc.pid)  # runs even when a mutated stop() crashes the test
        proc.wait()
        end = time.monotonic() + 10
        while not flag.exists() and time.monotonic() < end:
            time.sleep(0.02)
        self.assertTrue(flag.exists(), "helper never became ready")
        return proc, int(flag.read_text())

    def test_stop_terminates_a_running_group(self):
        proc = subprocess.Popen(["sleep", "60"], start_new_session=True)
        self.addCleanup(proc.wait)
        screenshots.stop(proc)
        self.assertIsNotNone(proc.poll())
        self.assertFalse(alive(proc.pid))

    def test_stop_sweeps_a_group_whose_leader_was_already_reaped(self):
        proc, helper = self.leader_with_helper("pass")
        self.assertIsNotNone(proc.poll())
        self.assertTrue(alive(helper))
        screenshots.stop(proc)
        self.assertTrue(self.wait_gone(helper), "helper %d survived stop()" % helper)

    def test_stop_with_nothing_left_is_harmless(self):
        proc = subprocess.Popen(["true"], start_new_session=True)
        proc.wait()
        screenshots.stop(proc)  # no member in the group: no error

    def test_stop_escalates_to_sigkill_for_a_group_that_ignores_sigterm(self):
        proc, helper = self.leader_with_helper(
            "signal.signal(signal.SIGTERM, signal.SIG_IGN)")
        screenshots.stop(proc)
        self.assertTrue(self.wait_gone(helper), "SIGTERM-proof helper %d survived stop()" % helper)


class TestFullRun(ScreenshotsBase):
    """Real ci/demo_seed.py, real `factory serve` on 127.0.0.1 and the real scrub scan; only the browser is a stub."""

    def test_success_writes_light_and_dark_pngs_and_cleans_up(self):
        self.use_zip()
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home,goals")
        self.assertEqual(code, 0, out + err)
        self.assertEqual(sorted(p.name for p in self.images.iterdir()),
                         ["goals-dark.png", "goals-light.png", "home-dark.png", "home-light.png"])
        for p in self.images.iterdir():
            data = p.read_bytes()
            self.assertEqual(struct.unpack(">I", data[16:20])[0], 1280)
        self.assertNotEqual((self.images / "home-light.png").read_bytes(), (self.images / "home-dark.png").read_bytes())
        self.assertIn("scrub home: exit 0", out)
        self.assertIn("scrub goals: exit 0", out)
        self.assertIn("--now " + NOW, out)
        self.assertIn("terminated 1 child process(es)", out)  # the server; the browser stub had already exited
        self.assert_clean()

    def serve_group_gone(self):
        self.assertEqual(len(self.serve_pids), 1, "expected exactly one serve to have been started")
        pid = self.serve_pids[0]
        self.assertFalse(alive(pid))
        self.assertTrue(group_gone(pid), "serve process group %d still has members" % pid)

    def test_serve_group_is_gone_after_success(self):
        self.use_zip()
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home")
        self.assertEqual(code, 0, out + err)
        self.serve_group_gone()
        self.assert_clean()

    def test_serve_group_is_gone_after_an_exit_3_abort_following_serve_start(self):
        self.use_zip(mode="blank")  # the text check runs after the server is up and the pages are captured
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home")
        self.assertEqual(code, 3, out + err)
        self.assertIn("no rendered text", err)
        self.serve_group_gone()
        self.assert_clean()

    def test_serve_group_is_gone_after_a_scrub_abort(self):
        self.use_zip()
        terms = self.dir / "terms.txt"
        terms.write_text("<nav\n", encoding="utf-8")
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home", env={"SCRUB_TERMS_FILE": str(terms)})
        self.assertEqual(code, 1, out + err)
        self.serve_group_gone()
        self.assert_clean()

    def test_serve_group_is_gone_after_an_exception(self):
        self.use_zip()
        p = unittest.mock.patch.object(screenshots, "capture_all", side_effect=RuntimeError("boom"))
        p.start()
        self.addCleanup(p.stop)
        with self.assertRaises(RuntimeError), contextlib.redirect_stdout(io.StringIO()):
            screenshots.main(["--now", NOW, "--out", str(self.images), "--pages", "home"])
        self.serve_group_gone()
        self.assert_clean()

    def test_serve_group_is_gone_after_sigint(self):
        self.use_zip()

        def interrupted(*a, **kw):
            os.kill(os.getpid(), signal.SIGINT)
            time.sleep(2)  # the delivered signal raises here
        p = unittest.mock.patch.object(screenshots, "capture_all", interrupted)
        p.start()
        self.addCleanup(p.stop)
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home")
        self.assertEqual(code, 130, out + err)
        self.serve_group_gone()
        self.assert_clean()

    def test_serve_group_is_gone_after_sigterm_even_with_a_second_signal_during_cleanup(self):
        self.use_zip()

        def terminated(*a, **kw):
            os.kill(os.getpid(), signal.SIGTERM)
            time.sleep(2)
        p = unittest.mock.patch.object(screenshots, "capture_all", terminated)
        p.start()
        self.addCleanup(p.stop)
        real_stop = screenshots.stop

        def stop_after_second_signal(proc):
            os.kill(os.getpid(), signal.SIGINT)  # ignored while cleaning up
            real_stop(proc)
        q = unittest.mock.patch.object(screenshots, "stop", stop_after_second_signal)
        q.start()
        self.addCleanup(q.stop)
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home")
        self.assertEqual(code, 130, out + err)
        self.serve_group_gone()
        self.assert_clean()

    def test_scrub_hit_aborts_before_capture(self):
        self.use_zip()
        terms = self.dir / "terms.txt"
        terms.write_text("<nav\n", encoding="utf-8")
        p = unittest.mock.patch.object(screenshots, "capture_all", side_effect=AssertionError("captured after a hit"))
        p.start()
        self.addCleanup(p.stop)
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home", env={"SCRUB_TERMS_FILE": str(terms)})
        self.assertEqual(code, 1, out + err)
        self.assertIn("scrub scan hit on page home", err)
        self.assertFalse(self.images.exists())
        self.assert_clean()

    def test_oversize_png_retries_once_with_a_shorter_window(self):
        self.use_zip(mode="big-at-860")
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home")
        self.assertEqual(code, 0, out + err)
        self.assertIn("retrying once with a 640 px window", out)
        self.assertLessEqual((self.images / "home-light.png").stat().st_size, 307200)
        self.assert_clean()

    def test_oversize_png_after_retry_aborts_without_writing(self):
        self.use_zip(mode="always-big")
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home")
        self.assertEqual(code, 1, out + err)
        self.assertIn("over 307200 bytes", err)
        self.assertFalse(self.images.exists())
        self.assert_clean()

    def test_identical_light_and_dark_aborts(self):
        self.use_zip(mode="same")
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home")
        self.assertEqual(code, 1, out + err)
        self.assertIn("light and dark images are identical", err)
        self.assert_clean()


class TestFontPreflight(ScreenshotsBase):
    """check_fonts runs right after the checkout/--out refusals: exit 3 before any download, nothing written."""

    def downloads(self):
        calls = []
        p = unittest.mock.patch.object(screenshots, "download", lambda url, dest: calls.append(url))
        p.start()
        self.addCleanup(p.stop)
        return calls

    def assert_refused_without_fonts(self):
        calls = self.downloads()
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home")
        self.assertEqual(code, 3, out + err)
        self.assertIn("fontconfig", err)
        self.assertIn("fonts-dejavu-core", err)
        self.assertEqual(calls, [], "the preflight must fail before any download")
        self.assertFalse(self.images.exists())
        self.assertEqual(self.tmps, [])

    def test_fc_list_missing(self):
        p = unittest.mock.patch.object(screenshots, "FC_LIST", str(self.dir / "no-such-fc-list"))
        p.start()
        self.addCleanup(p.stop)
        self.assert_refused_without_fonts()

    def test_fc_list_prints_no_fonts(self):
        self.fonts(listing="")
        self.assert_refused_without_fonts()

    def test_fc_match_names_a_missing_file(self):
        self.fonts(match=str(self.dir / "gone" / "DejaVuSans.ttf"))
        self.assert_refused_without_fonts()

    def test_fc_match_names_nothing(self):
        self.fonts(match="")
        self.assert_refused_without_fonts()

    def test_passes_with_fake_font_output(self):
        screenshots.check_fonts()  # setUp installed a working fake fc-list / fc-match


class TestThemeForcing(ScreenshotsBase):
    def args(self, theme):
        return screenshots.browser_args("/b", theme, "/d.png", "http://127.0.0.1:1/", 860, "/p")

    def test_light_and_dark_force_the_right_blink_value(self):
        # Blink's PreferredColorScheme enum is {kDark, kLight}: 0 is dark, 1 is light
        self.assertIn("--blink-settings=preferredColorScheme=1", self.args("light"))
        self.assertIn("--blink-settings=preferredColorScheme=0", self.args("dark"))
        self.assertEqual(screenshots.THEMES, {"light": "1", "dark": "0"})

    def test_args_keep_width_profile_and_destination(self):
        a = self.args("dark")
        self.assertIn("--window-size=1280,860", a)
        self.assertIn("--user-data-dir=/p", a)
        self.assertIn("--screenshot=/d.png", a)
        self.assertEqual(a[-1], "http://127.0.0.1:1/")


class TestRenderedChecks(ScreenshotsBase):
    W, H = 1280, 420

    def write(self, name, bg, fg, flat=False):
        fn = (lambda y: bytes((bg, bg, bg)) * self.W) if flat else (lambda y: text_like_row(self.W, bg, fg, y))
        path = self.dir / name
        path.write_bytes(make_png(self.W, self.H, fn))
        return path

    def pair(self, light, dark):
        return [self.write("home-light.png", *light), self.write("home-dark.png", *dark)]

    def test_uniform_png_is_rejected(self):
        files = [self.write("home-light.png", 245, 245, flat=True), self.write("home-dark.png", 20, 20, flat=True)]
        with self.assertRaises(screenshots.Refused) as cm, contextlib.redirect_stdout(io.StringIO()):
            screenshots.check_rendered(files, ["home"])
        self.assertIn("Captured images have no rendered text; check fonts.", str(cm.exception))

    def test_text_like_png_is_accepted(self):
        files = self.pair((245, 20), (20, 235))
        colours, luma = screenshots.image_stats(files[0])
        self.assertGreaterEqual(colours, screenshots.MIN_COLOURS)
        with contextlib.redirect_stdout(io.StringIO()):
            screenshots.check_rendered(files, ["home"])

    def test_swapped_pair_is_rejected(self):
        files = self.pair((20, 235), (245, 20))  # the "light" file is the dark image
        with self.assertRaises(screenshots.Refused) as cm, contextlib.redirect_stdout(io.StringIO()):
            screenshots.check_rendered(files, ["home"])
        self.assertIn("Theme mismatch: home light/dark images do not match their names.", str(cm.exception))

    def test_nearly_equal_brightness_is_rejected(self):
        files = self.pair((130, 20), (120, 20))
        with self.assertRaises(screenshots.Refused) as cm, contextlib.redirect_stdout(io.StringIO()):
            screenshots.check_rendered(files, ["home"])
        self.assertIn("Theme mismatch", str(cm.exception))

    def test_luminance_is_the_weighted_mean(self):
        path = self.dir / "flat.png"
        path.write_bytes(make_png(8, 4, lambda y: bytes((200, 100, 50)) * 8))
        colours, luma = screenshots.image_stats(path)
        self.assertEqual(colours, 1)
        self.assertAlmostEqual(luma, 0.299 * 200 + 0.587 * 100 + 0.114 * 50, places=6)

    def test_rgba_is_decoded(self):
        path = self.dir / "rgba.png"
        path.write_bytes(make_png(4, 2, lambda y: bytes((10, 20, 30, 255)) * 4, ctype=6))
        self.assertEqual(screenshots.image_stats(path)[0], 1)

    def test_other_formats_are_refused(self):
        for kw in ({"ctype": 0, "depth": 8}, {"depth": 16}, {"interlace": 1}):
            path = self.dir / "odd.png"
            path.write_bytes(make_png(4, 2, lambda y: bytes(12), **kw))
            with self.assertRaises(screenshots.Refused, msg=str(kw)):
                screenshots.image_stats(path)
        path.write_bytes(b"not a png")
        with self.assertRaises(screenshots.Refused):
            screenshots.image_stats(path)

    def test_all_png_filter_types_decode_to_the_original_pixels(self):
        w, h, ch = 16, 6, 3
        img = [bytes((x * 13 + y * 29 + c * 7) % 256 for x in range(w) for c in range(ch)) for y in range(h)]

        def filt(y, row, prev):
            ft = y % 5  # rows use filter types 0, 1, 2, 3, 4, 0
            out = bytearray()
            for i in range(len(row)):
                a = row[i - ch] if i >= ch else 0
                b = prev[i]
                c = prev[i - ch] if i >= ch else 0
                if ft == 0:
                    pred = 0
                elif ft == 1:
                    pred = a
                elif ft == 2:
                    pred = b
                elif ft == 3:
                    pred = (a + b) // 2
                else:
                    pa, pb, pc = abs(b - c), abs(a - c), abs(a + b - 2 * c)
                    pred = a if pa <= pb and pa <= pc else b if pb <= pc else c
                out.append((row[i] - pred) & 255)
            return ft, bytes(out)

        path = self.dir / "filtered.png"
        path.write_bytes(make_png(w, h, lambda y: img[y], filt=filt))
        _w, _ch, rows = screenshots.decode_png(path)
        self.assertEqual([bytes(r) for r in rows], img)


class TestRenderedChecksThroughMain(ScreenshotsBase):
    def test_blank_images_abort_with_exit_3_and_write_nothing(self):
        self.use_zip(mode="blank")
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home")
        self.assertEqual(code, 3, out + err)
        self.assertIn("Captured images have no rendered text; check fonts.", err)
        self.assertFalse(self.images.exists())
        self.assert_clean()

    def test_swapped_themes_abort_with_exit_3_and_write_nothing(self):
        self.use_zip(mode="swapped")
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home")
        self.assertEqual(code, 3, out + err)
        self.assertIn("Theme mismatch: home light/dark images do not match their names.", err)
        self.assertFalse(self.images.exists())
        self.assert_clean()

    def test_rejected_run_does_not_touch_existing_images(self):
        self.images.mkdir()
        old = self.images / "home-light.png"
        old.write_bytes(b"old image")
        self.use_zip(mode="blank")
        code, out, err = self.run_main("--out", str(self.images), "--pages", "home")
        self.assertEqual(code, 3, out + err)
        self.assertEqual(old.read_bytes(), b"old image")
        self.assertEqual([p.name for p in self.images.iterdir()], ["home-light.png"])


if __name__ == "__main__":
    unittest.main()
