"""docs/demo scenes C (hook, request, close): static guards need no browser; the frame test needs DEMO_BROWSER."""
import json
import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEMO = ROOT / "docs" / "demo"
SCENES = DEMO / "scenes"
IDS = ["hook", "request", "close"]
TIMING = re.compile(r"Date\.now|setTimeout|setInterval|requestAnimationFrame")
DATA_BLOCK = re.compile(r'<script type="application/json" id="demo-data">(.*?)</script>', re.S)


def html(scene_id):
    return (SCENES / (scene_id + ".html")).read_text()


def board_scene(scene_id):
    return {s["id"]: s for s in json.loads((DEMO / "storyboard.json").read_text())["scenes"]}[scene_id]


def embedded(scene_id):
    return json.loads(DATA_BLOCK.search(html(scene_id)).group(1))


class SceneStaticTests(unittest.TestCase):
    def test_files_exist_and_use_shared_foundation(self):
        for i in IDS:
            text = html(i)
            self.assertIn('href="theme.css"', text, i)
            self.assertIn('src="demo.js"', text, i)
            self.assertIn('data-scene="%s"' % i, text, i)

    def test_no_wall_clock_timing(self):
        for i in IDS:
            self.assertIsNone(TIMING.search(html(i)), i)

    def test_no_external_url(self):
        for i in IDS:
            self.assertIsNone(re.search(r"https?://", html(i)), i)

    def test_declared_duration_and_captions_match_storyboard(self):
        for i in IDS:
            data, scene = embedded(i), board_scene(i)
            self.assertEqual(data["scene"], i)
            self.assertEqual(data["duration"], scene["duration"], i)
            self.assertEqual(data["captions"], scene["captions"], i)

    def test_embedded_brand_is_brand_json(self):
        brand = json.loads((DEMO / "brand.json").read_text())
        for i in IDS:
            self.assertEqual(embedded(i)["brand"], brand, i)

    def test_no_literal_product_name_outside_embedded_brand(self):
        name = json.loads((DEMO / "brand.json").read_text())["name"].lower()
        self.assertTrue(name)
        for i in IDS:
            body = DATA_BLOCK.sub("", html(i)).lower()
            self.assertNotIn(name, body, i)

    def test_close_gets_brand_values_through_data_brand(self):
        text = html("close")
        for key in ["name", "wordmark_svg", "tagline", "install", "github_url"]:
            self.assertIn('data-brand="%s"' % key, text, key)
        self.assertGreaterEqual(text.count("data-brand="), 4)

    def test_exact_texts(self):
        self.assertIn("Talk to one agent.", html("hook"))
        self.assertIn("A whole team ships your PRs.", html("hook"))
        self.assertIn("Add CSV export to the acme-web reports page", html("request"))
        self.assertEqual(html("request").count("Goal G-004 created. The director is staffing a team."), 1)
        self.assertIn(">boss<", html("request"))

    def test_scenes_fade_in_from_background(self):
        for i in IDS:
            self.assertRegex(html(i), r"\.fade \{ animation: fade-in [^;]*both", i)
        for i in ["hook", "request"]:
            # a later `both` fade-out would hold opacity 0 from t=0 and cancel the fade-in
            self.assertRegex(html(i), r"fade-out [^;,]*forwards", i)
            self.assertNotRegex(html(i), r"fade-out [^;,]*both", i)

    def test_every_animation_has_keyframes(self):
        for i in IDS:
            text = html(i)
            for decl in re.findall(r"animation:([^;}]*)[;}]", text):
                for part in re.split(r",(?![^(]*\))", decl):
                    self.assertIn("@keyframes %s " % part.split()[0], text, i)

def shoot(browser, scene_id, t, tmp):
    page = tmp / ("%s-%s.html" % (scene_id, t))
    src = html(scene_id).replace("<head>", '<head><base href="%s/">' % SCENES.as_uri(), 1)
    assert "demo.seek(0);" in src, "scene must end with demo.seek(0);"
    page.write_text(src.replace("demo.seek(0);", "demo.seek(%s);" % t))
    png = tmp / ("%s-%s-%d.png" % (scene_id, t, len(list(tmp.glob("*.png")))))
    subprocess.run([browser, "--no-sandbox", "--disable-gpu", "--window-size=1920,1080",
                    "--screenshot=%s" % png, page.as_uri()], check=True, capture_output=True, timeout=120)
    return png.read_bytes()


@unittest.skipUnless(os.environ.get("DEMO_BROWSER"), "set DEMO_BROWSER to a chrome-headless-shell to render")
class SceneFrameTests(unittest.TestCase):
    def test_same_seek_time_gives_identical_frames(self):
        with tempfile.TemporaryDirectory() as d:
            tmp = Path(d)
            for i, t in [("hook", "3.0"), ("request", "5.5"), ("close", "2.0")]:
                a = shoot(os.environ["DEMO_BROWSER"], i, t, tmp)
                b = shoot(os.environ["DEMO_BROWSER"], i, t, tmp)
                self.assertTrue(a.startswith(b"\x89PNG"), i)
                self.assertEqual(a, b, "%s at t=%s is not deterministic" % (i, t))

    def test_hook_animates(self):
        with tempfile.TemporaryDirectory() as d:
            tmp = Path(d)
            self.assertNotEqual(shoot(os.environ["DEMO_BROWSER"], "hook", "0.5", tmp),
                                shoot(os.environ["DEMO_BROWSER"], "hook", "3.0", tmp))


class SceneNitTests(unittest.TestCase):
    """T-0486 R5 a/b: request window fits its log and zooms at the boss line; hook reveals one glyph per step."""

    def test_hook_reveals_per_character_not_by_clip_path(self):
        html = (SCENES / "hook.html").read_text()
        l1 = re.search(r'<div class="l1"[^>]*>(.*?)</div>', html, re.S).group(1)
        chars = re.findall(r'<span class="ch" style="animation-delay:([\d.]+)s">(.)</span>', l1)
        self.assertEqual("".join(c for _, c in chars), "Talk to one agent.")
        delays = [float(d) for d, _ in chars]
        self.assertEqual(delays, sorted(delays))
        self.assertEqual(len(set(delays)), len(delays))
        self.assertNotIn("steps(18", html)
        self.assertNotRegex(html, r"\.l1[^{]*\{[^}]*clip-path|\.l1 span \{[^}]*reveal")

    def test_request_window_fits_log_and_zooms_at_boss_line(self):
        html = (SCENES / "request.html").read_text()
        win = re.search(r"\.win \{([^}]*)\}", html, re.S).group(1)
        height = int(re.search(r"height:(\d+)px", win).group(1))
        ox, oy = [int(v) for v in re.search(r"transform-origin:(\d+)px (\d+)px", win).groups()]
        self.assertLessEqual(height, 500, "the log fills about 360 px; the window must not leave a big empty area")
        self.assertTrue(260 <= oy <= 380, "zoom origin must sit on the boss line (~320 px below the window top), got %d" % oy)
        self.assertEqual(ox, 780)


if __name__ == "__main__":
    unittest.main()
