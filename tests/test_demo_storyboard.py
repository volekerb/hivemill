"""docs/demo foundation: storyboard and brand data, no external assets, demo_stills guards (no browser needed)."""
import json
import os
import re
import struct
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEMO = ROOT / "docs" / "demo"
STILLS = ROOT / "ci" / "demo_stills.py"
BRAND_KEYS = {"name", "wordmark_svg", "tagline", "install", "github_url"}
# G-024 pitch v1 (owner-approved script M15107, owner M15130): 12 shots in 8 scenes, 80 s README cut
SCENE_IDS = ["open", "problem", "request", "orgchart", "work", "mergegate", "result", "close"]
LINKEDIN = DEMO / "storyboard-linkedin.json"
# the captions of the approved script, verbatim, in order (S1-S12)
SCRIPT_CAPTIONS = [
    "This whole software team is AI agents.",
    "One AI coder is fast. It also says \u201cdone\u201d when it isn't.",
    "So you talk to one agent, the boss. Plain English.",
    "The boss briefs a director. The director staffs a team.",
    "pm, coder, QA, reviewer, judge. One job each.",
    "The pm splits the goal into small tasks, each tied to a success criterion.",
    "They work in parallel: own pane, own git worktree, own permissions.",
    "QA tried postcode ABCDE for Germany. It passed. Bug.",
    "Sent back, fixed, checked again, merged.",
    "Nothing merges on an agent's word.",
    "Reviewer and QA bring evidence. The judge holds the gate.",
    "Every merge lands in a changelog you can read.",
    "Merges per day, and where the time goes.",
    "Every rejection is counted, by source, so you see what bounces.",
    "Self-hosted. Open source (MIT). Claude Code underneath.",
]


def board(path=DEMO / "storyboard.json"):
    return json.loads(Path(path).read_text())


def page_seconds(scene):
    return sum(p.get("duration", scene["page_duration"]) if isinstance(p, dict) else scene["page_duration"]
               for p in scene["pages"])


class StoryboardTests(unittest.TestCase):
    def test_top_level_schema(self):
        b = board()
        self.assertEqual((b["fps"], b["width"], b["height"]), (30, 1920, 1080))
        self.assertEqual(set(b), {"fps", "width", "height", "scale", "scenes", "hero"})
        self.assertEqual(b["scale"], 2)  # captured at 3840x2160, exported at 1920x1080

    def test_scenes_in_order_with_beats_1_to_8_once(self):
        scenes = board()["scenes"]
        self.assertEqual([s["id"] for s in scenes], SCENE_IDS)
        self.assertEqual(sorted(s["beat"] for s in scenes), list(range(1, 9)))
        for s in scenes:
            self.assertTrue({"id", "beat", "source", "duration", "captions"} <= set(s), s["id"])
            self.assertTrue(s["source"].startswith(("scenes/%s.html" % s["id"], "ui:/")), s["source"])
            self.assertTrue((DEMO / s["source"]).is_file() or s["source"].startswith("ui:"), s["source"])
        self.assertEqual([s["duration"] for s in scenes], [5, 6, 6, 10, 23, 8, 17, 5])

    def test_total_duration_in_range(self):
        total = sum(s["duration"] for s in board()["scenes"])
        self.assertEqual(total, 80)
        self.assertTrue(60 <= total <= 90)

    def test_real_ui_on_frame_zero(self):
        first = board()["scenes"][0]
        self.assertTrue(first["source"].startswith("ui:"))
        page = first["pages"][0]
        self.assertEqual((page["path"], page["fade_in"]), ("/", False))  # hard open, no fade from black

    def test_captions_are_the_approved_script_verbatim(self):
        texts = [c["text"] for s in board()["scenes"] for c in s["captions"]]
        self.assertEqual(texts, SCRIPT_CAPTIONS)

    def test_ui_shots_use_the_seeded_story(self):
        paths = [p["path"] for s in board()["scenes"] if s["source"].startswith("ui:") for p in s["pages"]]
        self.assertEqual(paths, ["/", "/goal/G-002", "/agents", "/task/T-0008", "/changelog", "/throughput", "/quality"])

    def test_scene_caption_bar_clears_the_player_controls(self):
        css = (DEMO / "scenes" / "theme.css").read_text()
        rule = re.search(r"\.caption-bar \{([^}]*)\}", css).group(1)
        self.assertGreaterEqual(int(re.search(r"bottom: (\d+)px", rule).group(1)), 70)

    def test_hero_inside_its_scene(self):
        b = board()
        hero = b["hero"]
        scene = {s["id"]: s for s in b["scenes"]}[hero["scene"]]
        self.assertEqual(hero["scene"], "orgchart")
        self.assertEqual((hero["start"], hero["end"]), (0.0, 10.0))
        self.assertTrue(10 <= hero["end"] - hero["start"] <= 15)
        self.assertTrue(0 <= hero["start"] < hero["end"] <= scene["duration"])
        self.assertEqual((hero["width"], hero["fps"]), (1600, 20))  # 50 ms frames: a 10 s loop times as 10.000 s (18 fps: 9.999)

    def test_captions_inside_scene_and_long_enough(self):
        for path in (DEMO / "storyboard.json", LINKEDIN):
            for s in board(path)["scenes"]:
                for c in s["captions"]:
                    self.assertTrue(0 <= c["at"] < c["until"] <= s["duration"], (path.name, s["id"], c))
                    self.assertGreaterEqual(c["until"] - c["at"], 2.5, (path.name, s["id"], c))
                    self.assertTrue(c["text"].strip())

    def test_ui_scene_pages_fill_the_scene(self):
        for path in (DEMO / "storyboard.json", LINKEDIN):
            for s in board(path)["scenes"]:
                if s["source"].startswith("ui:"):
                    self.assertEqual(s["source"], "ui:" + s["pages"][0]["path"], s["id"])
                    self.assertEqual(page_seconds(s), s["duration"], (path.name, s["id"]))


class LinkedinCutTests(unittest.TestCase):
    """Owner M15130 Q3: the LinkedIn cut drops S10 + S11 and trims S4 (org chart) to 8 s."""

    def test_same_scenes_shorter_cut(self):
        full, cut = board(), board(LINKEDIN)
        self.assertEqual([s["id"] for s in cut["scenes"]], SCENE_IDS)
        self.assertNotIn("hero", cut)
        self.assertEqual({k: v for k, v in cut.items() if k != "scenes"},
                         {k: v for k, v in full.items() if k not in ("scenes", "hero")})
        self.assertEqual([s["duration"] for s in cut["scenes"]], [5, 6, 6, 8, 23, 8, 6, 5])
        self.assertTrue(60 <= sum(s["duration"] for s in cut["scenes"]) <= 90)

    def test_drops_throughput_and_quality_only(self):
        full, cut = board(), board(LINKEDIN)
        texts = [c["text"] for s in cut["scenes"] for c in s["captions"]]
        self.assertEqual(texts, [t for t in SCRIPT_CAPTIONS if t not in SCRIPT_CAPTIONS[12:14]])
        result = {s["id"]: s for s in cut["scenes"]}["result"]
        self.assertEqual([p["path"] for p in result["pages"]], ["/changelog"])
        for a, b in zip(full["scenes"], cut["scenes"]):
            if a["id"] not in ("orgchart", "result"):
                self.assertEqual(a, b, a["id"])


class BrandTests(unittest.TestCase):
    def test_exactly_five_placeholder_keys(self):
        brand = json.loads((DEMO / "brand.json").read_text())
        self.assertEqual(set(brand), BRAND_KEYS)
        for k, v in brand.items():  # T-0453 filled the placeholders (owner M7163); tests/test_demo_theme.py checks the values
            self.assertTrue(v.strip() and "{{" not in v, (k, v))


def scan_texts(root):
    """(path, text) for every text file under root that the asset scans cover. Skipped: the gitignored out/ dir,
    .png, in fonts/ only the .woff2 binaries and OFL-*.txt licence texts (they quote upstream https URLs; checked by
    tests/test_demo_theme.py), and any file that is not valid UTF-8 (binary)."""
    root = Path(root)
    out = []
    for p in sorted(root.rglob("*")):
        rel = p.relative_to(root)
        if not p.is_file() or rel.parts[0] == "out" or p.suffix == ".png":
            continue
        if rel.parts[0] == "fonts" and (p.suffix == ".woff2" or (p.name.startswith("OFL-") and p.suffix == ".txt")):
            continue
        try:
            out.append((p, p.read_text(encoding="utf-8")))
        except UnicodeDecodeError:
            continue
    return out


def external_urls(text):
    return [m.start() for m in re.finditer(r"https?://", text) if not text.startswith("http://127.0.0.1", m.start())]


def css_wordmark_file(css_path, css_text):
    """The file the theme.css .wordmark rule shows as its background (url() resolves against the CSS file)."""
    m = re.search(r'\[data-brand="wordmark_svg"\][^}]*url\("([^"]+)"\)', css_text)
    assert m, "theme.css has no wordmark_svg background url()"
    return (Path(css_path).parent / m.group(1)).resolve()


class WordmarkSingleSourceTests(unittest.TestCase):
    """brand.json wordmark_svg (relative to docs/demo/) and the theme.css background url() must be the same file."""

    def brand_file(self, brand_path=DEMO / "brand.json"):
        return (Path(brand_path).parent / json.loads(Path(brand_path).read_text())["wordmark_svg"]).resolve()

    def test_css_url_and_brand_json_resolve_to_the_same_file(self):
        css = DEMO / "scenes" / "theme.css"
        self.assertEqual(css_wordmark_file(css, css.read_text()), self.brand_file())
        self.assertTrue(self.brand_file().is_file())

    def test_divergence_is_detected(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d) / "demo"
            (root / "scenes").mkdir(parents=True)
            (root.parent / "images").mkdir()
            (root / "scenes" / "theme.css").write_text((DEMO / "scenes" / "theme.css").read_text())
            brand = json.loads((DEMO / "brand.json").read_text())
            brand["wordmark_svg"] = "../images/wordmark-light.svg"  # mutated copy: brand moved, CSS did not
            (root / "brand.json").write_text(json.dumps(brand))
            css = root / "scenes" / "theme.css"
            self.assertNotEqual(css_wordmark_file(css, css.read_text()), self.brand_file(root / "brand.json"))


class NoExternalAssetsTests(unittest.TestCase):
    def files(self):
        out = scan_texts(DEMO)
        self.assertTrue(out)
        return out

    def test_binary_and_out_files_do_not_crash_the_scan(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            (root / "out").mkdir()
            (root / "out" / "frame.png").write_bytes(b"\x89PNG\r\n\x1a\n\xff\xfe\x00")
            (root / "out" / "dump.txt").write_text("see http://example.com")  # gitignored: skipped
            (root / "scenes").mkdir()
            (root / "scenes" / "blob.bin").write_bytes(b"\xff\xfe\x80\x81")
            (root / "scenes" / "a.css").write_text("a{}")
            self.assertEqual([p.name for p, _ in scan_texts(root)], ["a.css"])

    def test_fonts_dir_scans_stray_text_but_not_font_assets(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            (root / "fonts").mkdir()
            (root / "fonts" / "Inter.woff2").write_bytes(b"wOF2\x00")
            (root / "fonts" / "OFL-Inter.txt").write_text("https://scripts.sil.org/OFL")
            (root / "fonts" / "stray.css").write_text("@import url(http://evil.example/x.css);")
            (root / "fonts" / "stray.html").write_text("<link href='https://cdn.example/x'>")
            (root / "fonts" / "notes.txt").write_text("hello")
            scanned = {p.name: t for p, t in scan_texts(root)}
            self.assertEqual(sorted(scanned), ["notes.txt", "stray.css", "stray.html"])
            self.assertTrue(external_urls(scanned["stray.css"]))
            self.assertTrue(external_urls(scanned["stray.html"]))

    def test_no_urls(self):
        for p, text in self.files() + [(STILLS, STILLS.read_text())]:
            self.assertEqual(external_urls(text), [], "%s has an external URL" % p)

    def test_no_web_fonts_or_imports(self):
        for p, text in self.files():
            self.assertNotIn("@import", text, p)
            self.assertNotRegex(text, r"@font-face[^}]*url\((?!\"\.\./fonts/[\w-]+\.woff2\")", str(p))
            self.assertNotRegex(text, r"@import\s+url\(", str(p))

    def test_frames_use_embedded_data_matching_their_scene(self):
        # style frames are the G-020 look reference; they match the scene file they were drawn from, which the
        # storyboard may since have retimed or retired (hook, panes)
        frames = sorted((DEMO / "style").glob("*.html"))
        self.assertEqual(len(frames), 5)
        for f in frames:
            m = re.search(r'id="demo-data">(.*?)</script>', f.read_text(), re.S)
            d = json.loads(m.group(1))
            self.assertTrue((DEMO / "scenes" / (d["scene"] + ".html")).is_file(), f.name)
            self.assertTrue(isinstance(d["captions"], list) and all(c["text"].strip() for c in d["captions"]), f.name)
            self.assertEqual(set(d["brand"]), BRAND_KEYS)

    def test_png_cap(self):
        pngs = sorted((DEMO / "style").glob("*.png"))
        self.assertEqual(len(pngs), 5)
        self.assertLessEqual(sum(p.stat().st_size for p in pngs), 2097152)
        for p in pngs:
            self.assertEqual(struct.unpack(">II", p.read_bytes()[16:24]), (1920, 1080), p.name)


class FictionalIdTests(unittest.TestCase):
    REAL_ID = re.compile(r"\b(t-0[0-9]{3}|T-0[0-9]{3}|M[0-9]{4})\b")

    def test_style_files_have_only_fictional_ids(self):
        files = [p for p in (DEMO / "style").iterdir() if p.suffix != ".png"]
        self.assertGreaterEqual(len(files), 5)
        for p in files:
            for m in self.REAL_ID.finditer(p.read_text()):
                self.assertIn(m.group(0), ("T-0042", "t-0042"), "%s has a real-looking id %s" % (p.name, m.group(0)))

    def test_regex_rejects_a_real_id(self):
        self.assertTrue(self.REAL_ID.search("pm-t-0412"))
        self.assertTrue(self.REAL_ID.search("M6795"))
        self.assertFalse(self.REAL_ID.search("G-004 PR #42"))


class CapBeforeWriteTests(unittest.TestCase):
    def load(self):
        sys.path.insert(0, str(ROOT / "ci"))
        import demo_stills
        return demo_stills

    def fake_render(self, tmp, sizes):
        paths = []
        for i, n in enumerate(sizes):
            p = Path(tmp) / ("new-%d.png" % i)
            p.write_bytes(b"x" * n)
            paths.append(p)
        return paths

    def test_over_cap_fails_and_leaves_out_untouched(self):
        ds = self.load()
        with tempfile.TemporaryDirectory() as t, tempfile.TemporaryDirectory() as out:
            (Path(out) / "new-0.png").write_bytes(b"old")
            rendered = self.fake_render(t, [600, 600])
            with self.assertRaises(ds.shots.Failed):
                ds.install(rendered, Path(out), 1000)
            self.assertEqual([p.name for p in Path(out).iterdir()], ["new-0.png"])
            self.assertEqual((Path(out) / "new-0.png").read_bytes(), b"old")

    def test_under_cap_installs_all(self):
        ds = self.load()
        with tempfile.TemporaryDirectory() as t, tempfile.TemporaryDirectory() as out:
            ds.install(self.fake_render(t, [400, 500]), Path(out), 1000)
            self.assertEqual(sorted(p.name for p in Path(out).iterdir()), ["new-0.png", "new-1.png"])
            self.assertEqual((Path(out) / "new-1.png").stat().st_size, 500)


class StillsGuardTests(unittest.TestCase):
    def run_stills(self, *args):
        return subprocess.run([sys.executable, str(STILLS)] + list(args), capture_output=True, text=True, timeout=60,
                              cwd=str(ROOT))

    def test_out_outside_style_is_refused(self):
        with tempfile.TemporaryDirectory() as d:
            r = self.run_stills("--out", d)
        self.assertEqual(r.returncode, 3, r.stderr)
        self.assertIn("outside", r.stderr)

    def test_out_dotdot_escape_is_refused(self):
        r = self.run_stills("--out", str(DEMO / "style" / ".." / ".."))
        self.assertEqual(r.returncode, 3, r.stderr)

    def test_missing_browser_is_refused(self):
        r = self.run_stills("--browser", "/nonexistent/chrome-headless-shell")
        self.assertEqual(r.returncode, 3, r.stderr)

    def test_unknown_option_is_usage_error(self):
        self.assertEqual(self.run_stills("--bogus").returncode, 2)


@unittest.skipUnless(os.environ.get("DEMO_BROWSER"), "set DEMO_BROWSER to a chrome-headless-shell to render")
class RenderTests(unittest.TestCase):
    def test_frame_1_renders_1920x1080(self):
        # --out must be under docs/demo/style, so render into a temp subdirectory there and remove it again
        with tempfile.TemporaryDirectory(dir=str(DEMO / "style")) as d:
            r = subprocess.run([sys.executable, str(STILLS), "--browser", os.environ["DEMO_BROWSER"], "--out", d],
                               capture_output=True, text=True, timeout=300, cwd=str(ROOT))
            self.assertEqual(r.returncode, 0, r.stderr)
            png = Path(d) / "frame-1-hook.png"
            self.assertEqual(struct.unpack(">II", png.read_bytes()[16:24]), (1920, 1080))


if __name__ == "__main__":
    unittest.main()
