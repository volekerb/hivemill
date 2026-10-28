"""G-020 D: org chart (loopable hero) and merge gate scenes. Static checks need no browser; the render checks need
DEMO_BROWSER (path to an already-downloaded chrome-headless-shell) and are skipped without it."""
import json
import os
import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEMO = ROOT / "docs" / "demo"
SCENES = DEMO / "scenes"
BOARD = json.loads((DEMO / "storyboard.json").read_text())
SCENE_IDS = ["orgchart", "mergegate"]
ROLES = ["boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge"]
BROWSER = os.environ.get("DEMO_BROWSER")
# Same-t renders must be byte-identical. The loop seam t=13.0 vs t=0.0 is compared byte for byte as well: every visual
# is a pure function of t and is zero at both ends, so no anti-aliasing tolerance is needed.


def read(scene):
    return (SCENES / (scene + ".html")).read_text()


def storyboard_scene(scene):
    return [s for s in BOARD["scenes"] if s["id"] == scene][0]


class StaticTests(unittest.TestCase):
    def test_files_exist_and_use_shared_assets(self):
        for scene in SCENE_IDS:
            text = read(scene)
            self.assertIn('href="theme.css"', text, scene)
            self.assertIn('src="demo.js"', text, scene)
            self.assertIn('data-scene="%s"' % scene, text)

    def test_no_external_url(self):
        for scene in SCENE_IDS:
            self.assertEqual(re.findall(r"https?://", read(scene)), [], scene)

    def test_no_wall_clock_timing(self):
        for scene in SCENE_IDS:
            self.assertEqual(re.findall(r"Date\.now|setTimeout|setInterval|requestAnimationFrame|new Date|performance\.now",
                                        read(scene)), [], scene)

    def test_embedded_data_matches_storyboard(self):
        for scene in SCENE_IDS:
            m = re.search(r'<script type="application/json" id="demo-data">(.*?)</script>', read(scene), re.S)
            data = json.loads(m.group(1))
            sb = storyboard_scene(scene)
            self.assertEqual(data["duration"], sb["duration"], scene)
            self.assertEqual(data["captions"], sb["captions"], scene)
            self.assertEqual(data["scene"], scene)

    def test_durations_equal_storyboard(self):
        self.assertEqual(storyboard_scene("orgchart")["duration"], 14)
        self.assertEqual(storyboard_scene("mergegate")["duration"], 12)
        self.assertEqual(BOARD["hero"]["scene"], "orgchart")
        self.assertEqual(BOARD["hero"]["end"], 13.0)

    def test_orgchart_shows_all_roles(self):
        text = read("orgchart")
        for role in ROLES:
            self.assertIn('id="n-%s"' % role, text)
            self.assertRegex(text, r">%s</text>" % role)

    def test_orgchart_loop_returns_to_start_state(self):
        # the seam contract is in the source: the envelope is 0 from t=13 on and nothing is lit before the first spawn
        text = read("orgchart")
        self.assertIn("var LOOP = 13;", text)
        self.assertIn("t >= LOOP ? 0", text)

    def test_mergegate_content(self):
        text = read("mergegate")
        for needle in ["#42 Add CSV export", "reviewer", "qa", "PASS", "judge", "merged", "main",
                       "tests: 42 passed", "acceptance 1-5 checked"]:
            self.assertIn(needle, text)

    def test_merged_stamp_right_of_judge_gate(self):
        text = read("mergegate")
        gate = int(re.search(r"\.gate \{[^}]*left:(\d+)px;[^}]*width:(\d+)px", text).group(1))
        gate_w = int(re.search(r"\.gate \{[^}]*width:(\d+)px", text).group(1))
        stamp = int(re.search(r"\.stamp \{[^}]*left:(\d+)px", text).group(1))
        self.assertGreaterEqual(stamp, gate + gate_w)

    def test_captions_toggle_is_additive_helper(self):
        js = (SCENES / "demo.js").read_text()
        self.assertIn("captions: function (on)", js)
        self.assertIn("nocaptions=1", js)




@unittest.skipUnless(BROWSER, "set DEMO_BROWSER to a chrome-headless-shell path to run the render tests")
class RenderTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = Path(tempfile.mkdtemp(prefix="demo-d-"))
        shutil.copytree(DEMO, cls.tmp / "demo", ignore=shutil.ignore_patterns("style", "out"))
        (cls.tmp / "images").mkdir()
        for f in (ROOT / "docs" / "images").glob("wordmark*.svg"):
            shutil.copy(f, cls.tmp / "images" / f.name)

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmp, ignore_errors=True)

    def shot(self, scene, t, captions, tag):
        scenes = self.tmp / "demo" / "scenes"
        page = scenes / ("%s-%s.html" % (scene, tag))
        hide = "" if captions else "demo.captions(false);"
        page.write_text(read(scene).replace("</body>", "<script>%sdemo.seek(%s);</script></body>" % (hide, t)))
        out = self.tmp / (tag + ".png")
        cmd = [BROWSER, "--headless", "--disable-gpu", "--no-sandbox", "--hide-scrollbars", "--force-device-scale-factor=1",
               "--window-size=1920,1080", "--virtual-time-budget=2000", "--user-data-dir=" + str(self.tmp / ("p-" + tag)),
               "--allow-file-access-from-files", "--screenshot=" + str(out), page.resolve().as_uri()]
        subprocess.run(cmd, check=True, capture_output=True, timeout=120)
        return out.read_bytes()

    def test_loop_seam_frames_identical_without_captions(self):
        a = self.shot("orgchart", 0.0, False, "seam0")
        b = self.shot("orgchart", 13.0, False, "seam13")
        self.assertEqual(a, b)

    def test_same_t_frames_identical(self):
        self.assertEqual(self.shot("orgchart", 8.0, False, "same-a"), self.shot("orgchart", 8.0, False, "same-b"))
        self.assertEqual(self.shot("mergegate", 6.0, True, "mg-a"), self.shot("mergegate", 6.0, True, "mg-b"))

    def test_scene_changes_over_time(self):
        self.assertNotEqual(self.shot("orgchart", 0.0, False, "t0"), self.shot("orgchart", 8.0, False, "t8"))
        self.assertNotEqual(self.shot("mergegate", 2.0, True, "m2"), self.shot("mergegate", 11.0, True, "m11"))

    def test_captions_toggle_hides_bar(self):
        self.assertNotEqual(self.shot("orgchart", 3.0, True, "cap-on"), self.shot("orgchart", 3.0, False, "cap-off"))


class GateDoorTests(unittest.TestCase):
    def test_door_seam_has_no_bar_that_crosses_the_judge_label(self):
        # T-0486 R5 d: shut doors meet at the gate centre, where the label is; a border there draws a bar through it
        html = (SCENES / "mergegate.html").read_text()
        for rule in re.findall(r"\.door\.[lr] \{([^}]*)\}", html):
            self.assertNotIn("border", rule)
        self.assertEqual(len(re.findall(r"\.door\.[lr] \{", html)), 2)


if __name__ == "__main__":
    unittest.main()
