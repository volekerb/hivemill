"""G-020 E: agent panes scene (docs/demo/scenes/panes.html + panes-script.json). Unit tests need no browser;
the render tests run only with DEMO_BROWSER=<path to chrome-headless-shell>."""
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SCENES = ROOT / "docs" / "demo" / "scenes"
HTML = SCENES / "panes.html"
SCRIPT = SCENES / "panes-script.json"
SCRUB = ROOT / "ci" / "scrub_scan.py"
ROLES = ["lead", "pm", "coder", "qa", "reviewer", "judge"]
DURATION = 12


def panes():
    return json.loads(SCRIPT.read_text())


class ScriptSchemaTests(unittest.TestCase):
    def test_six_panes_six_roles(self):
        data = panes()
        self.assertIsInstance(data, list)
        self.assertEqual(sorted(p["role"] for p in data), sorted(ROLES))
        self.assertEqual(len(data), 6)

    def test_pane_fields(self):
        for p in panes():
            self.assertEqual(set(p), {"role", "title", "lines"}, p["role"])
            self.assertTrue(p["title"].strip(), p["role"])
            self.assertTrue(p["lines"], p["role"])
            for line in p["lines"]:
                self.assertEqual(set(line), {"at", "text"}, p["role"])
                self.assertIsInstance(line["at"], (int, float))
                self.assertTrue(line["text"].strip())

    def test_lines_sorted_and_inside_scene(self):
        for p in panes():
            ats = [l["at"] for l in p["lines"]]
            self.assertEqual(ats, sorted(ats), p["role"])
            for at in ats:
                self.assertGreaterEqual(at, 0, p["role"])
                self.assertLess(at, DURATION, p["role"])

    def test_every_pane_has_a_line_before_the_end_screens(self):
        for p in panes():
            self.assertLess(p["lines"][0]["at"], 2.0, p["role"])

    def test_lines_fit_a_pane(self):
        # a pane is ~539 px of text at 24 px mono (~14.4 px per char): no text past the card edge
        for p in panes():
            for l in p["lines"]:
                self.assertLessEqual(len(l["text"]), 34, l["text"])

    def test_story_lines_present(self):
        text = SCRIPT.read_text()
        for needle in ("PR #42", "tests: 42 passed", "verdict: pass", "merged #42", "plan v1 written"):
            self.assertIn(needle, text)


class HtmlContractTests(unittest.TestCase):
    def test_embedded_script_equals_json_file(self):
        m = re.search(r'id="panes-script">(.*?)</script>', HTML.read_text(), re.S)
        self.assertIsNotNone(m, "panes.html must embed panes-script.json (file:// cannot fetch it)")
        self.assertEqual(json.loads(m.group(1)), panes())

    def test_caption_data_matches_storyboard(self):
        board = json.loads((ROOT / "docs" / "demo" / "storyboard.json").read_text())
        scene = [s for s in board["scenes"] if s["id"] == "panes"][0]
        m = re.search(r'id="demo-data">(.*?)</script>', HTML.read_text(), re.S)
        d = json.loads(m.group(1))
        self.assertEqual(d["scene"], "panes")
        self.assertEqual(d["duration"], scene["duration"])
        self.assertEqual(d["captions"], scene["captions"])
        self.assertEqual(scene["source"], "scenes/panes.html")

    def test_uses_seek_hook_and_theme(self):
        text = HTML.read_text()
        self.assertIn("demo.on(", text)
        self.assertIn('href="theme.css"', text)
        self.assertIn('src="demo.js"', text)

    def test_no_wall_clock_timing(self):
        for p in (HTML, SCRIPT):
            self.assertIsNone(re.search(r"Date\.now|new Date|performance\.now|setTimeout|setInterval|requestAnimationFrame",
                                        p.read_text()), p.name)

    def test_no_url(self):
        for p in (HTML, SCRIPT):
            self.assertIsNone(re.search(r"https?://", p.read_text()), p.name)

    def test_no_herdr_commands_or_sockets(self):
        for p in (HTML, SCRIPT):
            text = p.read_text()
            self.assertIsNone(re.search(r"herdr ", text), p.name)
            self.assertNotIn(".sock", text, p.name)
            self.assertNotIn("/run/", text, p.name)

    def test_no_css_animation_started_by_script(self):
        # CSS animations created inside a seek hook would run on the wall clock: glow must be computed from t
        text = HTML.read_text()
        self.assertNotIn("classList", text)
        self.assertNotRegex(text, r"@keyframes|animation:")


class ScrubTests(unittest.TestCase):
    def run_scrub(self, *paths):
        env = dict(os.environ)
        env.pop("SCRUB_TERMS_FILE", None)
        return subprocess.run([sys.executable, str(SCRUB), "--path"] + [str(p) for p in paths],
                              capture_output=True, text=True, timeout=60, cwd=str(ROOT), env=env)

    def test_scenes_are_scrub_clean(self):
        r = self.run_scrub(HTML, SCRIPT)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)

    def test_scrub_rejects_a_home_path_in_a_pane_line(self):
        # the guard must reject a real-looking path and accept the fictional ~/work path used in the script
        with tempfile.TemporaryDirectory() as d:
            bad = Path(d) / "panes-script.json"
            bad.write_text(json.dumps([{"role": "pm", "title": "x", "lines": [{"at": 0, "text": "cd /ho" + "me/alice/src"}]}]))
            self.assertEqual(self.run_scrub(bad).returncode, 1)
            good = Path(d) / "ok.json"
            good.write_text(json.dumps([{"role": "pm", "title": "x", "lines": [{"at": 0, "text": "cd ~/work/acme-web"}]}]))
            self.assertEqual(self.run_scrub(good).returncode, 0)


def read_panes_json(output):
    m = re.search(r'<pre id="out">(.*?)</pre>', output, re.S)
    if not m:
        raise AssertionError("no probe output in dump: " + output[:500])
    return json.loads(m.group(1).replace("&quot;", '"').replace("&amp;", "&").replace("&lt;", "<").replace("&gt;", ">"))


@unittest.skipUnless(os.environ.get("DEMO_BROWSER"), "set DEMO_BROWSER to a chrome-headless-shell to render")
class RenderTests(unittest.TestCase):
    PROBE = ('<script>var o=document.createElement("pre");o.id="out";o.textContent=JSON.stringify('
             'Array.prototype.map.call(document.querySelectorAll(".pane .pb"),function(e){return e.innerText;}));'
             'document.body.appendChild(o);</script>')

    def page(self, tmp, t, probe=False):
        text = HTML.read_text()
        text = text.replace('href="theme.css"', 'href="file://%s/theme.css"' % SCENES)
        text = text.replace('src="demo.js"', 'src="file://%s/demo.js"' % SCENES)
        text = text.replace("demo.seek(0);", "demo.seek(%s);" % t)
        if probe:
            text = text.replace("</body>", self.PROBE + "</body>")
        p = Path(tmp) / ("t%s%s.html" % (str(t).replace(".", "_"), "p" if probe else ""))
        p.write_text(text)
        return p

    def chrome(self, *args):
        r = subprocess.run([os.environ["DEMO_BROWSER"], "--no-sandbox", "--disable-gpu", "--allow-file-access-from-files",
                            "--window-size=1920,1080"] + list(args), capture_output=True, timeout=120)
        self.assertEqual(r.returncode, 0, r.stderr.decode(errors="replace"))
        return r.stdout.decode(errors="replace")

    def shot(self, tmp, t, name):
        out = Path(tmp) / name
        self.chrome("--screenshot=%s" % out, "file://%s" % self.page(tmp, t))
        return out.read_bytes()

    def test_same_t_frames_identical(self):
        with tempfile.TemporaryDirectory() as d:
            a = self.shot(d, 5.3, "a.png")
            b = self.shot(d, 5.3, "b.png")
            other = self.shot(d, 9.1, "c.png")
        self.assertEqual(a, b)
        self.assertNotEqual(a, other)

    def test_every_pane_has_text_at_t11(self):
        with tempfile.TemporaryDirectory() as d:
            out = self.chrome("--dump-dom", "file://%s" % self.page(d, 11, probe=True))
        texts = read_panes_json(out)
        self.assertEqual(len(texts), 6)
        for role, text in zip([p["role"] for p in panes()], texts):
            self.assertTrue(text.strip(), role)


class CauseBeforeEffectTests(unittest.TestCase):
    def test_lead_reports_the_plan_after_the_pm_writes_it(self):
        panes = {p["role"]: p["lines"] for p in json.loads(SCRIPT.read_text())}
        written = [l["at"] for l in panes["pm"] if "plan v1 written" in l["text"]]
        reported = [l["at"] for l in panes["lead"] if l["text"] == "pm: plan v1 written"]
        self.assertEqual((len(written), len(reported)), (1, 1))
        self.assertGreater(reported[0], written[0])


if __name__ == "__main__":
    unittest.main()
