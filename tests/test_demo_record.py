import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO / "ci"))

import demo_cdp  # noqa: E402
import demo_record  # noqa: E402
import screenshots  # noqa: E402

RECORD = REPO / "ci" / "demo_record.py"


class FakePipe(unittest.TestCase):
    """demo_cdp.Pipe against an os.pipe pair standing in for the browser's fds 3 and 4."""

    def setUp(self):
        self.to_b_r, self.to_b_w = os.pipe()      # what the client sends
        self.from_b_r, self.from_b_w = os.pipe()  # what the "browser" answers
        self.pipe = demo_cdp.Pipe(self.from_b_r, self.to_b_w, timeout=0.5)
        for fd in (self.to_b_r, self.to_b_w, self.from_b_r, self.from_b_w):
            self.addCleanup(self._close, fd)

    @staticmethod
    def _close(fd):
        try:
            os.close(fd)
        except OSError:
            pass

    def reply(self, *msgs):
        os.write(self.from_b_w, b"".join(json.dumps(m).encode() + b"\0" for m in msgs))

    def test_command_is_nul_terminated_json_with_session(self):
        self.reply({"id": 1, "result": {}})
        self.pipe.send("Page.enable", {"a": 1}, session="S1")
        raw = os.read(self.to_b_r, 4096)
        self.assertTrue(raw.endswith(b"\0"))
        self.assertEqual(json.loads(raw[:-1]), {"id": 1, "method": "Page.enable", "params": {"a": 1}, "sessionId": "S1"})

    def test_two_messages_in_one_read(self):
        self.reply({"method": "Page.loadEventFired", "params": {}}, {"id": 1, "result": {"ok": True}})
        self.assertEqual(self.pipe.send("X"), {"ok": True})
        self.assertEqual(self.pipe.events[0]["method"], "Page.loadEventFired")

    def test_message_split_across_reads(self):
        data = json.dumps({"id": 1, "result": {"v": "x" * 100}}).encode() + b"\0"
        threading.Timer(0.1, os.write, (self.from_b_w, data[:30])).start()
        threading.Timer(0.2, os.write, (self.from_b_w, data[30:])).start()
        self.assertEqual(self.pipe.send("X", timeout=3), {"v": "x" * 100})

    def test_events_list_is_capped(self):
        self.reply(*[{"method": "E", "params": {}} for _ in range(demo_cdp.EVENTS_KEPT + 50)], {"id": 1, "result": {}})
        self.pipe.send("X")
        self.assertEqual(len(self.pipe.events), demo_cdp.EVENTS_KEPT)

    def test_error_reply_raises(self):
        self.reply({"id": 1, "error": {"code": -32601, "message": "no such method"}})
        with self.assertRaisesRegex(demo_cdp.CdpError, "no such method"):
            self.pipe.send("Nope")

    def test_other_id_is_skipped(self):
        self.reply({"id": 99, "result": {"stale": True}}, {"id": 1, "result": {"right": True}})
        self.assertEqual(self.pipe.send("X"), {"right": True})

    def test_timeout_raises(self):
        with self.assertRaises(demo_cdp.CdpTimeout):
            self.pipe.send("X", timeout=0.2)

    def test_closed_pipe_raises(self):
        os.close(self.from_b_w)
        with self.assertRaises(demo_cdp.CdpClosed):
            self.pipe.send("X")


class FakeBrowser:
    def __init__(self):
        self.seeks, self.exprs, self.urls = [], [], []

    def new_page(self, url):
        self.urls.append(url)

    def evaluate(self, expr):
        self.exprs.append(expr)
        if expr.startswith("window.demo.seek("):
            self.seeks.append(float(expr[len("window.demo.seek("):-1]))
        if expr == "document.body.innerText":
            return "hello"
        return True

    def screenshot(self, fmt="png"):
        return b"png%d" % len(self.seeks)


class RecordSceneTests(unittest.TestCase):
    def test_frame_count_and_seek_times(self):
        got = []
        b = FakeBrowser()
        n = demo_record.record_scene(b, "file:///x.html", 2.5, 30, lambda i, png: got.append((i, png)))
        self.assertEqual(n, 75)
        self.assertEqual([i for i, _ in got], list(range(75)))
        self.assertEqual(b.seeks, [i / 30 for i in range(75)])
        self.assertEqual(got[3][1], b"png4")

    def test_text_dump_every_30th_frame(self):
        with tempfile.TemporaryDirectory() as d:
            demo_record.record_scene(FakeBrowser(), "u", 2.5, 30, lambda i, p: None, text_dir=d, scene_id="hook")
            self.assertEqual(sorted(os.listdir(d)), ["hook-00000.txt", "hook-00030.txt", "hook-00060.txt"])
            self.assertEqual((Path(d) / "hook-00030.txt").read_text(), "hello")

    def test_dir_sink_names(self):
        with tempfile.TemporaryDirectory() as d:
            sink = demo_record.DirSink(Path(d) / "sub")
            sink(0, b"a")
            sink(12, b"b")
            self.assertEqual(sorted(os.listdir(Path(d) / "sub")), ["frame-00000.png", "frame-00012.png"])

    def test_pipe_sink_writes_bytes(self):
        class P:
            stdin = io.BytesIO()
        demo_record.PipeSink(P)(0, b"abc")
        self.assertEqual(P.stdin.getvalue(), b"abc")

    def test_pipe_sink_broken_pipe_is_an_error(self):
        class Dead(io.RawIOBase):
            def write(self, b):
                raise BrokenPipeError
        class P:
            stdin = Dead()
        with self.assertRaises(demo_cdp.CdpError):
            demo_record.PipeSink(P)(0, b"abc")


class CliTests(unittest.TestCase):
    def run_cli(self, *args):
        return subprocess.run([sys.executable, str(RECORD), *args], capture_output=True, text=True, timeout=60)

    def test_tmp_out_refused_exit_3(self):
        r = self.run_cli("--scene", "hook", "--out", "/tmp/x", "--max-frames", "1")
        self.assertEqual(r.returncode, 3, r.stderr)
        self.assertIn("/tmp", r.stderr)

    def test_tmp_work_refused_exit_3(self):
        r = self.run_cli("--scene", "hook", "--out", str(REPO / "docs/demo/out/x"), "--work", "/tmp/w")
        self.assertEqual(r.returncode, 3, r.stderr)

    def test_ui_scene_rejected_exit_2(self):
        r = self.run_cli("--scene", "tour", "--out", str(REPO / "docs/demo/out/x"), "--browser", "/nonexistent")
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("web UI tour", r.stderr)

    def test_unknown_scene_exit_2(self):
        r = self.run_cli("--scene", "nope", "--out", str(REPO / "docs/demo/out/x"))
        self.assertEqual(r.returncode, 2, r.stderr)


class HelperTests(unittest.TestCase):
    def test_screenshots_helpers_exist(self):
        # demo_cdp depends on these names from ci/screenshots.py (T-0402 owns it); fail loudly if they are renamed
        for name in ("fetch_browser", "stop", "spawn", "CHILDREN"):
            self.assertTrue(hasattr(screenshots, name), "ci/screenshots.py lost %s; update ci/demo_cdp.py" % name)

    def test_browser_binary_prefers_given_path(self):
        self.assertEqual(demo_cdp.browser_binary("/nonexistent", "/opt/chrome"), Path("/opt/chrome"))

    def test_gitignore_has_out(self):
        self.assertEqual((REPO / "docs/demo/.gitignore").read_text().split(), ["out/"])


class LaunchCleanupTests(unittest.TestCase):
    """Browser.launch / close with a stand-in binary (a shell script that just sleeps)."""

    def setUp(self):
        out = REPO / "docs" / "demo" / "out"
        out.mkdir(parents=True, exist_ok=True)
        self.tmp = Path(tempfile.mkdtemp(prefix="t0486-", dir=out))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.binary = self.tmp / "fake-browser"
        self.binary.write_text("#!/bin/sh\nexec sleep 300\n")
        self.binary.chmod(0o755)
        self.before = list(screenshots.CHILDREN)
        self.addCleanup(self.sweep)

    def sweep(self):
        for proc in list(screenshots.CHILDREN):
            if proc not in self.before:
                demo_cdp.stop(proc)

    def test_work_none_never_uses_tmp(self):
        default = self.tmp / "default-work"
        old = demo_cdp.DEFAULT_WORK
        demo_cdp.DEFAULT_WORK = default
        self.addCleanup(setattr, demo_cdp, "DEFAULT_WORK", old)
        b = demo_cdp.Browser.launch(self.binary, work=None)
        try:
            self.assertEqual(b.profile.parent, default)
            self.assertFalse(demo_record.under_ram(b.profile))
        finally:
            b.close()
        self.assertFalse(old.is_relative_to(Path(tempfile.gettempdir())))
        self.assertEqual(old, REPO / "docs" / "demo" / "out" / "work")

    def test_profile_removed_on_close(self):
        b = demo_cdp.Browser.launch(self.binary, work=self.tmp)
        b.profile.mkdir(parents=True, exist_ok=True)  # the real browser creates it
        (b.profile / "Default").write_text("x")
        b.close()
        self.assertEqual(list(self.tmp.glob("profile-*")), [])

    def test_profile_removed_when_record_raises(self):
        with self.assertRaises(RuntimeError):
            with demo_cdp.Browser.launch(self.binary, work=self.tmp) as b:
                b.profile.mkdir(parents=True, exist_ok=True)
                raise RuntimeError("boom")
        self.assertEqual(list(self.tmp.glob("profile-*")), [])

    def test_signal_between_spawn_and_assignment_stops_child(self):
        def interrupted(*a, **k):
            raise KeyboardInterrupt("signal 15")
        orig = demo_cdp.Pipe
        demo_cdp.Pipe = interrupted
        self.addCleanup(setattr, demo_cdp, "Pipe", orig)
        with self.assertRaises(KeyboardInterrupt):
            demo_cdp.Browser.launch(self.binary, work=self.tmp)
        spawned = [p for p in screenshots.CHILDREN if p not in self.before]
        self.assertEqual(spawned, [])
        self.assertEqual(list(self.tmp.glob("profile-*")), [])

    def test_main_sweeps_children_when_launch_never_returns(self):
        spawned = []

        def launch(*a, **k):
            spawned.append(screenshots.spawn([str(self.binary)]))
            raise KeyboardInterrupt("signal 15")
        orig = demo_cdp.Browser.launch
        demo_cdp.Browser.launch = staticmethod(launch)
        self.addCleanup(setattr, demo_cdp.Browser, "launch", orig)
        rc = demo_record.main(["--scene", "hook", "--out", str(self.tmp / "o"), "--work", str(self.tmp / "w"),
                               "--browser", str(self.binary), "--source", str(REPO / "docs/demo/style/frame-1-hook.html")])
        self.assertEqual(rc, 130)
        self.assertIsNotNone(spawned[0].poll())
        self.assertNotIn(spawned[0], screenshots.CHILDREN)


@unittest.skipUnless(os.environ.get("DEMO_BROWSER"), "set DEMO_BROWSER to a chrome-headless-shell path")
class IntegrationTests(unittest.TestCase):
    def test_records_three_frames(self):
        out = REPO / "docs" / "demo" / "out" / "test-integration"
        shutil.rmtree(out, ignore_errors=True)
        self.addCleanup(shutil.rmtree, out, True)
        r = subprocess.run([sys.executable, str(RECORD), "--scene", "hook", "--out", str(out), "--max-frames", "3",
                            "--browser", os.environ["DEMO_BROWSER"], "--work", str(REPO / "docs/demo/out/work-test"),
                            "--source", str(REPO / "docs/demo/style/frame-1-hook.html")],
                           capture_output=True, text=True, timeout=120)
        self.addCleanup(shutil.rmtree, REPO / "docs/demo/out/work-test", True)
        self.assertEqual(r.returncode, 0, r.stderr[-500:])
        pngs = sorted(out.glob("*.png"))
        self.assertEqual(len(pngs), 3)
        for p in pngs:
            head = p.read_bytes()[:24]
            self.assertEqual(int.from_bytes(head[16:20], "big"), 1920)
            self.assertEqual(int.from_bytes(head[20:24], "big"), 1080)


if __name__ == "__main__":
    unittest.main()
