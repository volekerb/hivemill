"""Offline tests for ci/visual_qa.py (G-019 B7): synthetic inputs, no browser, no network."""
import contextlib
import io
import json
import shutil
import signal
import struct
import sys
import tempfile
import unittest
import zlib
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "ci"))
sys.path.insert(0, str(ROOT))
import visual_qa as vq  # noqa: E402

BG = (240, 240, 245)
INK = (10, 10, 10)


def png(path, width, height, ink_rows):
    """RGB PNG, background BG; the first `ink_rows` rows are noisy (never a uniform cell)."""
    raw = bytearray()
    for y in range(height):
        raw.append(0)
        for x in range(width):
            raw += bytes(INK if y < ink_rows and (x + y) % 2 else BG)
    def chunk(k, b):
        return struct.pack(">I", len(b)) + k + b + struct.pack(">I", zlib.crc32(k + b))
    Path(path).write_bytes(b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
                           + chunk(b"IDAT", zlib.compress(bytes(raw))) + chunk(b"IEND", b""))


class Overflow(unittest.TestCase):
    def test_wide_element_outside_scroller_fails(self):
        p = vq.overflow_problems(390, 390, [{"sel": "div.wide", "right": 600, "in_scroll": False}])
        self.assertEqual(len(p), 1)
        self.assertIn("div.wide", p[0])
        self.assertIn("600", p[0])

    def test_same_element_in_scroller_passes(self):
        self.assertEqual(vq.overflow_problems(390, 390, [{"sel": "div.wide", "right": 600, "in_scroll": True}]), [])

    def test_page_scroll_width_fails(self):
        self.assertTrue(vq.overflow_problems(390, 612, []))

    def test_one_pixel_tolerance(self):
        self.assertEqual(vq.overflow_problems(390, 390, [{"sel": "a", "right": 391, "in_scroll": False}]), [])


class Blank(unittest.TestCase):
    def share(self, ink_rows):
        with tempfile.TemporaryDirectory() as d:
            f = Path(d) / "a.png"
            png(f, 264, 80, ink_rows)
            w, ch, rows = vq.demo_cdp.screenshots.decode_png(f, max_rows=1000)
        return vq.blank_share(w, ch, rows, [BG], 8)

    def test_half_blank_fails(self):
        s = self.share(40)
        self.assertAlmostEqual(s, 0.5)
        self.assertTrue(vq.blank_problem(s))

    def test_twenty_percent_blank_passes(self):
        s = self.share(64)
        self.assertAlmostEqual(s, 0.2)
        self.assertEqual(vq.blank_problem(s), [])

    def test_report_shows_one_decimal(self):
        self.assertIn("50.0%", vq.blank_problem(0.5)[0])


class BlankMeasure(unittest.TestCase):
    """Blank = plain page background (--surface-0); card interiors (--surface-1) are content (M13076)."""
    SHOTS = ROOT / "docs" / "design" / "g-019" / "shots"

    @staticmethod
    def tokens(theme):
        from tests import test_web_design as twd  # surface tokens as parsed from web/static/app.css
        return vq.hex_rgb(twd.THEMES[theme]["surface-0"]), vq.hex_rgb(twd.THEMES[theme]["surface-1"])

    def solid_share(self, left, right, bgs):
        """1280x860 capture: x < 640 is colour `left`, x >= 640 colour `right`."""
        w, h = 1280, 860
        row = bytes(left) * 640 + bytes(right) * 640
        return vq.blank_share(w, 3, [row] * h, bgs, vq.SIDEBAR_PX)

    def test_approved_mock_passes_in_both_themes(self):
        for theme in ("light", "dark"):
            s0, _ = self.tokens(theme)
            w, ch, rows = vq.demo_cdp.screenshots.decode_png(self.SHOTS / ("overview-%s-1280.png" % theme), max_rows=100000)
            share = vq.blank_share(w, ch, rows, [s0], vq.SIDEBAR_PX)
            self.assertEqual(vq.blank_problem(share), [], "%s mock blank share %.3f" % (theme, share))

    def test_half_empty_page_fails(self):
        s0, s1 = self.tokens("light")
        share = self.solid_share(s1, s0, [s0])
        problems = vq.blank_problem(share)
        self.assertEqual(len(problems), 1)
        self.assertTrue(problems[0].startswith("blank share"))

    def test_card_interior_is_not_blank(self):
        s0, s1 = self.tokens("light")
        share = self.solid_share(s1, s1, [s0])
        self.assertEqual(share, 0.0)
        self.assertEqual(vq.blank_problem(share), [])


class Contrast(unittest.TestCase):
    def test_low_contrast_text_fails(self):
        self.assertTrue(vq.pair_problems([("t", "#999999", "#FFFFFF")], vq.AA_TEXT, "light"))

    def test_good_text_passes(self):
        self.assertEqual(vq.pair_problems([("t", "#595959", "#FFFFFF")], vq.AA_TEXT, "light"), [])

    def test_non_text_threshold_is_lower(self):
        pair = [("n", "#999999", "#FFFFFF")]  # 2.85
        self.assertTrue(vq.pair_problems(pair, vq.AA_NON_TEXT, "dark"))
        self.assertEqual(vq.pair_problems([("n", "#8a8a8a", "#FFFFFF")], vq.AA_NON_TEXT, "dark"), [])

    def test_real_tokens_are_read_from_app_css(self):
        self.assertEqual(vq.aa_problems(), [])


class Speed(unittest.TestCase):
    def test_median_under_limit_passes(self):
        self.assertEqual(vq.speed_problem("/", [150, 190, 250, 180, 170]), [])

    def test_median_over_limit_fails(self):
        self.assertTrue(vq.speed_problem("/", [210, 220, 190, 230, 240]))


class Constants(unittest.TestCase):
    def test_thresholds_equal_the_goal(self):
        self.assertEqual((vq.BLANK_MAX, vq.AA_TEXT, vq.AA_NON_TEXT, vq.RENDER_MS_MAX), (0.35, 4.5, 3.0, 200))

    def test_no_threshold_option(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out), self.assertRaises(SystemExit):
            vq.main(["--help"])
        for word in ("threshold", "blank-max", "max-ms", "aa"):
            self.assertNotIn("--" + word, out.getvalue())


class Summary(unittest.TestCase):
    rows = [{"route": "/", "theme": "light", "width": 1280, "overflow": [], "blank": 0.5, "png": "x"}]

    def test_fail_last_line_and_exit_code(self):
        text, n = vq.summary(self.rows, [], {"/": 12.0}, ["/ light 1280: blank share 50.0% > 35%"])
        self.assertEqual(text.splitlines()[-1], "visual-qa: FAIL (1 problems)")
        self.assertEqual(vq.exit_code(n), 1)
        self.assertIn("50.0%", text)

    def test_pass_last_line_and_exit_code(self):
        text, n = vq.summary(self.rows, [], {"/": 12.0}, [])
        self.assertEqual(text.splitlines()[-1], "visual-qa: PASS")
        self.assertEqual(vq.exit_code(n), 0)


class FakeSession(unittest.TestCase):
    """main() with the seed, server and browser replaced; the temp dir and child must be gone afterwards."""

    def setUp(self):
        self.made = []
        self.stopped = []
        real = tempfile.mkdtemp

        def mk(*a, **k):
            d = real(*a, **k)
            self.made.append(Path(d))
            return d
        patches = [mock.patch.object(vq.tempfile, "mkdtemp", mk),
                   mock.patch.object(vq.demo_cdp, "stop", lambda p: self.stopped.append(p)),
                   mock.patch.object(vq.demo_cdp.screenshots, "CHILDREN", ["server"])]
        for p in patches:
            p.start()
            self.addCleanup(p.stop)

    def test_cleanup_after_normal_run(self):
        row = {"route": "/", "theme": "light", "width": 1280, "overflow": [], "blank": None, "png": "x"}
        with mock.patch.object(vq, "run_checks", lambda o, t: ([row], {"/": 1.0}, [], [])), \
                contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(vq.main([]), 0)
        self.assertFalse(self.made[0].exists())
        self.assertEqual(self.stopped, ["server"])

    def test_cleanup_after_exception(self):
        def boom(o, t):
            raise RuntimeError("browser exploded")
        with mock.patch.object(vq, "run_checks", boom), self.assertRaises(RuntimeError):
            vq.main([])
        self.assertFalse(self.made[0].exists())
        self.assertEqual(self.stopped, ["server"])

    def test_setup_error_exits_2(self):
        def no_browser(o, t):
            raise vq.SetupError("no usable browser")
        err = io.StringIO()
        with mock.patch.object(vq, "run_checks", no_browser), contextlib.redirect_stderr(err):
            self.assertEqual(vq.main([]), 2)
        self.assertIn("no usable browser", err.getvalue())
        self.assertFalse(self.made[0].exists())

    def test_unreadable_screenshot_exits_2(self):
        def bad_png(o, t):
            raise vq.demo_cdp.screenshots.Refused("x.png is not a PNG.")
        err = io.StringIO()
        with mock.patch.object(vq, "run_checks", bad_png), contextlib.redirect_stderr(err):
            self.assertEqual(vq.main([]), 2)
        self.assertIn("not a PNG", err.getvalue())

    def _handlers(self):
        return signal.getsignal(signal.SIGINT), signal.getsignal(signal.SIGTERM)

    def test_signal_handlers_restored_on_every_path(self):
        row = {"route": "/", "theme": "light", "width": 1280, "overflow": [], "blank": None, "png": "x"}

        def no_browser(o, t):
            raise vq.SetupError("no usable browser")

        def boom(o, t):
            raise RuntimeError("browser exploded")
        # pin a known non-ignore handler so the test does not depend on test order
        self.addCleanup(signal.signal, signal.SIGTERM, signal.getsignal(signal.SIGTERM))
        signal.signal(signal.SIGTERM, signal.SIG_DFL)
        before = self._handlers()
        with mock.patch.object(vq, "run_checks", lambda o, t: ([row], {"/": 1.0}, [], [])), \
                contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(vq.main([]), 0)
        self.assertEqual(self._handlers(), before)
        with mock.patch.object(vq, "run_checks", no_browser), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(vq.main([]), 2)
        self.assertEqual(self._handlers(), before)
        with mock.patch.object(vq, "run_checks", boom), self.assertRaises(RuntimeError):
            vq.main([])
        self.assertEqual(self._handlers(), before)

    def test_empty_routes_is_a_setup_error(self):
        with self.assertRaises(vq.SetupError):
            vq.run_checks(mock.Mock(routes=",", out=None, browser=None), Path("/nonexistent"))

    def test_failing_run_exits_1_and_writes_json(self):
        row = {"route": "/", "theme": "dark", "width": 1280, "overflow": [], "blank": 0.9, "png": "x"}
        with tempfile.TemporaryDirectory() as d, \
                mock.patch.object(vq, "run_checks", lambda o, t: ([row], {"/": 1.0}, ["/ dark 1280: blank"], [])), \
                contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(vq.main(["--json", str(Path(d) / "r.json")]), 1)
            self.assertEqual(json.loads((Path(d) / "r.json").read_text())["result"], "FAIL")
        self.assertIn("visual-qa: FAIL (1 problems)", out.getvalue())

    def test_refuses_live_home(self):
        live = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, live, True)
        err = io.StringIO()
        with mock.patch.object(vq.tempfile, "mkdtemp", lambda *a, **k: live), \
                mock.patch.object(vq, "live_homes", lambda: [Path(live).resolve()]), \
                mock.patch.object(vq, "run_checks", side_effect=AssertionError("must not run")), \
                contextlib.redirect_stderr(err):
            self.assertEqual(vq.main([]), 2)
        self.assertIn("live home", err.getvalue())

    def test_check_home_accepts_a_temp_dir(self):
        with tempfile.TemporaryDirectory() as d:
            self.assertEqual(vq.check_home(d, [Path("/nonexistent-live")]), Path(d).resolve())


if __name__ == "__main__":
    unittest.main()
