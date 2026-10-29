import http.server
import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
from pathlib import Path
from unittest import mock

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO / "ci"))
sys.path.insert(0, str(Path(__file__).resolve().parent))
import envcheck  # noqa: E402

import demo_cdp  # noqa: E402
import demo_tour  # noqa: E402

TOUR = REPO / "ci" / "demo_tour.py"
BOARD = json.loads((REPO / "docs" / "demo" / "storyboard.json").read_text())


class Maths(unittest.TestCase):
    def test_scroll_starts_at_top_and_ends_at_target(self):
        self.assertEqual(demo_tour.scroll_y(0, 4, 3000), 0)
        self.assertAlmostEqual(demo_tour.scroll_y(4, 4, 3000), 600)

    def test_scroll_target_is_capped_by_page_height(self):
        self.assertAlmostEqual(demo_tour.scroll_y(4, 4, 1380), 300)
        self.assertEqual(demo_tour.scroll_y(4, 4, 900), 0)  # page shorter than the window

    def test_scroll_is_monotonic_and_eased(self):
        ys = [demo_tour.scroll_y(i / 30, 4, 5000) for i in range(121)]
        self.assertEqual(ys, sorted(ys))
        self.assertLess(ys[3], 600 * 3 / 120)  # slower than linear at the start

    def test_fade_is_zero_at_page_edges_and_one_inside(self):
        self.assertEqual(demo_tour.fade(0, 4), 0)
        self.assertEqual(demo_tour.fade(4, 4), 0)
        self.assertEqual(demo_tour.fade(2, 4), 1)
        self.assertAlmostEqual(demo_tour.fade(0.2, 4), 0.5)
        self.assertAlmostEqual(demo_tour.fade(3.8, 4), 0.5)

    def test_caption_lookup(self):
        caps = BOARD["scenes"][5]["captions"]
        self.assertIn("dashboard", demo_tour.caption_at(caps, 3.0))
        self.assertEqual(demo_tour.caption_at(caps, 99), "")


class Layout(unittest.TestCase):
    def test_caption_bar_starts_below_the_page_box(self):
        self.assertGreaterEqual(demo_tour.caption_top(1080), demo_tour.page_height(1080))
        self.assertGreater(demo_tour.page_height(1080), 1080 // 2)  # most of the frame stays page

    def test_page_box_css_stops_where_the_caption_band_starts(self):
        self.assertIn("bottom:%dpx" % demo_tour.CAPTION_RESERVE, demo_tour.PAGE_CSS)
        self.assertIn("bottom:%dpx" % demo_tour.CAPTION_BOTTOM, demo_tour.CAPTION_CSS)

    def test_page_is_zoomed_so_the_column_fills_the_frame(self):
        self.assertGreaterEqual(demo_tour.ZOOM, 1.5)  # 14px body text -> 21px at 1080p
        self.assertLessEqual(1170 * demo_tour.ZOOM, 1920)  # the 1170px UI column still fits the width
        self.assertIn("zoom:%s" % demo_tour.ZOOM, demo_tour.ZOOM_CSS)

    def test_label_sits_in_the_caption_band(self):
        top = int(demo_tour.LABEL_CSS.split("top:")[1].split("px")[0])
        self.assertGreaterEqual(top, demo_tour.page_height(1080))
        self.assertLess(top, demo_tour.caption_top(1080))


class Storyboard(unittest.TestCase):
    def test_pages_and_labels_come_from_storyboard(self):
        _, scene = demo_tour.load_tour()
        self.assertEqual(scene["pages"], ["/", "/goals", "/agents", "/quality"])
        self.assertEqual(scene["labels"], ["Overview", "Goals", "Agents", "Quality"])
        self.assertEqual(scene["page_duration"], 4)


class Stub(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        code = 200 if self.path == "/ok" else 404
        self.send_response(code)
        self.end_headers()
        self.wfile.write(b"x")

    def log_message(self, *a):
        pass


class NonOk(unittest.TestCase):
    def setUp(self):
        self.srv = http.server.HTTPServer(("127.0.0.1", 0), Stub)
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        self.addCleanup(self.srv.server_close)
        self.addCleanup(self.srv.shutdown)
        self.base = "http://127.0.0.1:%d" % self.srv.server_port

    def test_ok_page_passes(self):
        demo_tour.check_pages(self.base, ["/ok"])

    def test_non_200_names_the_path(self):
        with self.assertRaises(demo_tour.PageError) as cm:
            demo_tour.check_pages(self.base, ["/ok", "/no-such-page"])
        self.assertIn("/no-such-page", str(cm.exception))
        self.assertIn("404", str(cm.exception))

    def test_record_tour_stops_before_browsing_on_non_200(self):
        browser = mock.Mock()
        with self.assertRaises(demo_tour.PageError):
            demo_tour.record_tour(browser, self.base, ["/ok", "/gone"], 4, 30, lambda i, p: None, None)
        browser.new_page.assert_not_called()

    def test_unreachable_server_is_a_page_error(self):
        with self.assertRaises(demo_tour.PageError):
            demo_tour.check_pages("http://127.0.0.1:1", ["/"])


class ServeFailure(unittest.TestCase):
    def test_server_is_stopped_when_serve_gives_up(self):
        proc = mock.Mock()
        with mock.patch.object(demo_cdp.screenshots, "serve", side_effect=demo_cdp.screenshots.Failed("x")), \
                mock.patch.object(demo_cdp.screenshots, "CHILDREN", [proc]), \
                mock.patch.object(demo_cdp, "stop") as stop:
            with self.assertRaises(demo_cdp.screenshots.Failed):
                demo_cdp.serve_demo("/nowhere/demo")
        stop.assert_called_once_with(proc)


class InterruptedStartup(unittest.TestCase):
    """A SIGTERM/Ctrl-C (KeyboardInterrupt) while seed or serve waits must not leave their child running."""

    def run_interrupted(self, which):
        sleeper = subprocess.Popen(["sleep", "60"], start_new_session=True)
        self.addCleanup(lambda: (sleeper.kill(), sleeper.wait()))

        def spawn_then_interrupt(*_a, **_k):
            demo_cdp.screenshots.CHILDREN.append(sleeper)
            raise KeyboardInterrupt("signal 15")

        with tempfile.TemporaryDirectory(dir=REPO / "docs" / "demo") as d:  # on disk: /tmp is refused
            argv = ["--out", str(Path(d) / "out"), "--work", str(Path(d) / "work"), "--demo-dir", str(Path(d) / "demo")]
            with mock.patch.object(demo_cdp.screenshots, "CHILDREN", []) as children, \
                    mock.patch.object(demo_cdp.screenshots, "serve", side_effect=spawn_then_interrupt), \
                    mock.patch.object(demo_cdp, "seed_demo") as seed_demo, \
                    mock.patch.object(demo_tour.signal, "signal"):
                if which == "seed":
                    seed_demo.side_effect = spawn_then_interrupt
                rc = demo_tour.main(argv)
                self.assertEqual(children, [])
        self.assertEqual(rc, 130)
        self.assertIsNotNone(sleeper.poll(), "child still running after interrupt")

    @envcheck.skip_on_ram()
    def test_interrupt_during_serve_stops_the_server(self):
        self.run_interrupted("serve")

    @envcheck.skip_on_ram()
    def test_interrupt_during_seed_stops_the_seed(self):
        self.run_interrupted("seed")


class Cli(unittest.TestCase):
    def run_tour(self, *args):
        return subprocess.run([sys.executable, str(TOUR), *args], capture_output=True, text=True, timeout=60)

    def test_tmp_out_is_refused(self):
        r = self.run_tour("--out", "/tmp/x", "--max-frames", "1")
        self.assertEqual(r.returncode, 3, r.stderr)
        self.assertIn("/tmp", r.stderr)

    def test_tmp_work_is_refused(self):
        r = self.run_tour("--out", str(REPO / "docs/demo/out/x"), "--work", "/tmp/w")
        self.assertEqual(r.returncode, 3, r.stderr)

    def test_tmp_demo_dir_is_refused(self):
        r = self.run_tour("--out", str(REPO / "docs/demo/out/x"), "--demo-dir", "/tmp/d")
        self.assertEqual(r.returncode, 3, r.stderr)

    @envcheck.skip_on_ram()
    def test_max_frames_below_page_count_is_usage_error(self):
        r = self.run_tour("--out", str(REPO / "docs/demo/out/x"), "--pages", "/a,/b,/c", "--max-frames", "2")
        self.assertEqual(r.returncode, 2)
        self.assertIn("at least the number of pages", r.stderr)

    @envcheck.skip_on_ram()
    def test_bad_pages_is_usage_error(self):
        r = self.run_tour("--out", str(REPO / "docs/demo/out/x"), "--pages", "goals")
        self.assertEqual(r.returncode, 2, r.stderr)


class DemoDir(unittest.TestCase):
    def test_foreign_non_empty_dir_is_refused_and_kept(self):
        with tempfile.TemporaryDirectory(dir=REPO / "docs" / "demo") as d:
            (Path(d) / "keep.txt").write_text("x")
            r = subprocess.run([sys.executable, str(TOUR), "--out", d + "/o", "--demo-dir", d], capture_output=True,
                               text=True, timeout=60)
            self.assertEqual(r.returncode, 3, r.stderr)
            self.assertTrue((Path(d) / "keep.txt").is_file())

    def test_marked_dir_is_rebuilt_and_empty_dir_accepted(self):
        with tempfile.TemporaryDirectory(dir=REPO / "docs" / "demo") as d:
            demo = Path(d) / "demo"
            demo_tour.prepare_demo_dir(demo)
            (demo / "old.db").write_text("x")
            demo_tour.prepare_demo_dir(demo)
            self.assertEqual([p.name for p in demo.iterdir()], [demo_tour.MARKER])


class ServeEnv(unittest.TestCase):
    def test_server_runs_with_demo_home_not_the_real_one(self):
        with tempfile.TemporaryDirectory() as d:
            demo = Path(d) / "demo"
            seen = {}

            class Proc:
                def poll(self):
                    return None

            def spawn(cmd, **kw):
                seen["cmd"], seen["env"] = cmd, kw["env"]
                proc = Proc()
                demo_cdp.screenshots.CHILDREN.append(proc)
                return proc

            class Resp:
                status = 200

                def __enter__(self):
                    return self

                def __exit__(self, *a):
                    return False

            with mock.patch.object(demo_cdp.screenshots, "spawn", spawn), \
                    mock.patch.object(demo_cdp.screenshots.urllib.request, "urlopen", lambda *a, **k: Resp()):
                port, proc = demo_cdp.serve_demo(demo)
            demo_cdp.screenshots.CHILDREN.remove(proc)
        self.assertEqual(seen["env"]["HOME"], str(demo / "home"))
        self.assertEqual(seen["env"]["FACTORY_HOME"], str(demo))
        self.assertNotEqual(seen["env"]["HOME"], os.environ.get("HOME"))
        self.assertIn("serve", seen["cmd"])
        self.assertIn(str(port), seen["cmd"])


@unittest.skipUnless(os.environ.get("DEMO_BROWSER"), "set DEMO_BROWSER to a chrome-headless-shell to run")
class Integration(unittest.TestCase):
    def test_two_pages_three_frames_each(self):
        with tempfile.TemporaryDirectory(dir=REPO / "docs" / "demo") as d:  # on disk, not RAM-backed /tmp
            out, work = Path(d) / "frames", Path(d) / "work"
            r = subprocess.run([sys.executable, str(TOUR), "--out", str(out), "--work", str(work), "--max-frames", "6",
                                "--pages", "/,/goals", "--browser", os.environ["DEMO_BROWSER"]],
                               capture_output=True, text=True, timeout=300)
            self.assertEqual(r.returncode, 0, r.stderr)
            pngs = sorted(out.glob("*.png"))
            self.assertEqual(len(pngs), 6)
            for p in pngs:
                head = p.read_bytes()[:24]
                self.assertEqual(int.from_bytes(head[16:20], "big"), 1920)
                self.assertEqual(int.from_bytes(head[20:24], "big"), 1080)
            text = "".join(f.read_text() for f in (work / "text").glob("*.txt"))
            self.assertIn("Build the checkout flow", text)  # seed data (acme-web goal G-002)
            self.assertNotIn(os.path.expanduser("~"), text)


if __name__ == "__main__":
    unittest.main()
