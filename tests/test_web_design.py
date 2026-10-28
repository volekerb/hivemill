"""Design checks for the web UI static files and layout shell (G-008 T-0087)."""
import re
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
from web import html as wh  # noqa: E402

STATIC = ROOT / "web" / "static"
CSS = (STATIC / "app.css").read_text()
STATUSES = ("ok", "warn", "bad", "info", "muted")


def block(selector):
    """Body of the first top-level-or-nested rule whose selector is exactly `selector`."""
    m = re.search(r"(?:^|[{}\s])" + re.escape(selector) + r"\s*\{([^{}]*)\}", CSS)
    if not m:
        raise AssertionError(f"no rule for {selector!r} in app.css")
    return m.group(1)


def tokens(selector):
    return dict(re.findall(r"--([\w-]+)\s*:\s*(#[0-9a-fA-F]{6})\b", block(selector)))


def lum(hex6):
    def ch(i):
        c = int(hex6[1 + 2 * i:3 + 2 * i], 16) / 255
        return c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4
    return 0.2126 * ch(0) + 0.7152 * ch(1) + 0.0722 * ch(2)


def contrast(a, b):
    hi, lo = sorted((lum(a), lum(b)), reverse=True)
    return (hi + 0.05) / (lo + 0.05)


THEMES = {
    "light": tokens(":root"),
    "dark": tokens(":root[data-theme=dark]"),
}
TONES = ("neutral", "info", "success", "warning", "danger", "review")
SURFACES = ("surface-0", "surface-1", "surface-2", "fill")
# tokens v2 (T-0429 note 2), the 26 colour keys of mock.css
KEYS = ("surface-0", "surface-1", "surface-2", "fill", "text-1", "text-2", "text-3", "hairline", "border", "accent",
        "accent-solid", "track", "data-1", "data-2", *(f"{t}-{k}" for t in TONES for k in ("fg", "bg", "solid")))


class Css(unittest.TestCase):
    def test_every_class_has_a_rule(self):
        for c in sorted(wh.CLASSES):
            with self.subTest(c):
                self.assertRegex(CSS, r"\." + re.escape(c) + r"(?![\w-])")

    def test_dark_blocks(self):
        self.assertIn("prefers-color-scheme: dark", CSS)
        self.assertIn("[data-theme=dark]", CSS)
        self.assertIn("[data-theme=light]", CSS)
        start = CSS.index("prefers-color-scheme: dark")
        media = CSS[start:CSS.index("\n}\n", start)]
        media_tokens = dict(re.findall(r"--([\w-]+)\s*:\s*(#[0-9a-fA-F]{6})\b", media))
        self.assertEqual(media_tokens, THEMES["dark"], "OS dark block and [data-theme=dark] must define the same tokens")
        self.assertIn(":root:not([data-theme=light])", media)

    def test_bar_uses_v(self):
        self.assertIn("var(--v", block(".bar"))
        for n in range(0, 101, 5):
            self.assertIn(f".bar--{n} {{ --v:{n}%; }}", CSS)

    def test_responsive_and_motion(self):
        self.assertIn("(min-width: 640px)", CSS)
        self.assertIn("(min-width: 1024px)", CSS)
        self.assertIn("overflow-x: auto", block(".card"))
        self.assertIn("prefers-reduced-motion: reduce", CSS)

    def test_desktop_air(self):
        """Owner M7206 #3: desktop row 36 -> 40 px and card padding 12 -> 16 px; phone values unchanged."""
        base, desk = CSS[:CSS.index("@media (min-width: 1024px)")], CSS[CSS.index("@media (min-width: 1024px)"):]
        self.assertIn("--row: 44px", base)
        self.assertIn("--card-pad: 12px", base)
        self.assertIn("--row: 40px", desk)
        self.assertIn("--card-pad: 16px", desk)

    def test_fonts_inter_and_jetbrains_first_no_font_files(self):
        self.assertRegex(CSS, r'--font:\s*"Inter", system-ui')
        self.assertRegex(CSS, r'--mono:\s*"JetBrains Mono", ui-monospace')
        self.assertFalse([p.name for p in STATIC.iterdir() if p.suffix.lower() in (".woff", ".woff2", ".ttf", ".otf")])
        self.assertNotIn("@font-face", CSS)

    def test_active_nav_item_keeps_weight(self):
        self.assertNotIn("font-weight", block(".nav-item--active, .nav-item--active:hover"))
        self.assertNotRegex(CSS, r"nav-item--active[^}]*font-weight")

    def test_no_external_assets(self):
        bad = re.compile(r"url\(\s*['\"]?\s*(https?:)?//|@import|fonts\.googleapis|//cdn|https?://", re.I)
        files = [p for p in STATIC.iterdir() if p.is_file()]
        self.assertGreaterEqual(len(files), 3)
        for p in files:
            with self.subTest(p.name):
                self.assertIsNone(bad.search(p.read_text()))

    def test_static_size_budget(self):
        total = sum(p.stat().st_size for p in STATIC.iterdir() if p.is_file())
        self.assertLess(total, 60 * 1024)

    def test_theme_js_small_local_and_progressive(self):
        js = (STATIC / "theme.js").read_text()
        self.assertLessEqual(len(js.splitlines()), 60)
        self.assertIn("localStorage", js)
        self.assertIn("sessionStorage", js)
        self.assertIn("data-theme", js)
        self.assertIn("aria-pressed", js)
        self.assertNotIn("nav.appendChild", js)

    def test_favicon_is_svg(self):
        self.assertIn("<svg", (STATIC / "favicon.svg").read_text())


class TestTokens(unittest.TestCase):
    def test_theme_keys_match(self):
        self.assertEqual(set(THEMES["light"]), set(THEMES["dark"]))
        for need in KEYS:
            with self.subTest(need):
                self.assertIn(need, THEMES["light"])
                self.assertIn(need, THEMES["dark"])
        self.assertEqual(set(THEMES["light"]), set(KEYS))


class Contrast(unittest.TestCase):
    def text_pairs(self, t):
        """Every text pair of note 2 (>= 4.5)."""
        for tier in ("text-1", "text-2", "text-3"):
            for s in SURFACES:
                yield f"{tier}/{s}", t[tier], t[s]
        for s in SURFACES:
            yield f"accent/{s}", t["accent"], t[s]
        yield "info-fg/info-bg (selected nav, chip)", t["info-fg"], t["info-bg"]
        for tone in TONES:
            yield f"{tone}-fg/{tone}-bg", t[f"{tone}-fg"], t[f"{tone}-bg"]
            yield f"{tone}-fg/surface-1", t[f"{tone}-fg"], t["surface-1"]
        yield "danger-fg/fill", t["danger-fg"], t["fill"]

    def non_text_pairs(self, t):
        """Control borders, icon/dot/bar solids and series colours (>= 3.0), on the surface they sit on."""
        for s in ("surface-0", "surface-1"):
            yield f"border/{s}", t["border"], t[s]
            yield f"accent/{s} (focus ring)", t["accent"], t[s]
            for tone in TONES:
                yield f"{tone}-solid/{s}", t[f"{tone}-solid"], t[s]
            yield f"data-1/{s}", t["data-1"], t[s]
            yield f"data-2/{s}", t["data-2"], t[s]
        yield "data-1/track", t["data-1"], t["track"]
        yield "data-2/track", t["data-2"], t["track"]

    def test_text_pairs_aa(self):
        for theme, t in THEMES.items():
            for name, a, b in self.text_pairs(t):
                with self.subTest(theme=theme, pair=name):
                    self.assertGreaterEqual(contrast(a, b), 4.5, f"{a} on {b}")

    def test_non_text_pairs(self):
        for theme, t in THEMES.items():
            for name, a, b in self.non_text_pairs(t):
                with self.subTest(theme=theme, pair=name):
                    self.assertGreaterEqual(contrast(a, b), 3.0, f"{a} on {b}")

    def test_bar_label_aa(self):
        """The .bar label (text-1) sits over the info-bg fill and the track behind the unfilled part."""
        for theme, t in THEMES.items():
            for bg in ("info-bg", "track"):
                with self.subTest(theme=theme, bg=bg):
                    self.assertGreaterEqual(contrast(t["text-1"], t[bg]), 4.5)
        self.assertIn("var(--info-bg)", block(".bar"))

    def test_summary_keeps_disclosure_marker(self):
        body = block("summary")
        self.assertRegex(body, r"display:\s*list-item")
        self.assertNotRegex(body, r"display:\s*flex")
        self.assertNotRegex(CSS, r"summary[^{}]*\{[^}]*list-style\s*:\s*none")

    def test_contrast_function_known_values(self):
        self.assertAlmostEqual(contrast("#000000", "#ffffff"), 21.0, places=1)
        self.assertLess(contrast("#777777", "#888888"), 1.5)

    def test_low_contrast_would_be_caught(self):
        self.assertLess(contrast("#94a3b8", "#ffffff"), 4.5)


class Shell(unittest.TestCase):
    def setUp(self):
        self.doc = wh.page("T", "<p>x</p>", "/goals")

    def test_head(self):
        self.assertIn('name="viewport" content="width=device-width, initial-scale=1"', self.doc)
        self.assertIn('name="color-scheme" content="light dark"', self.doc)

    def test_scripts_only_local_deferred(self):
        scripts = re.findall(r"<script\b[^>]*>", self.doc)
        self.assertEqual(scripts, ['<script src="/static/theme.js" defer>'])
        self.assertNotIn("<script>", self.doc)
        self.assertNotRegex(self.doc, r"\son\w+=")
        self.assertNotRegex(self.doc, r"""(src|href)=["']https?:""")
        self.assertNotIn("style=", self.doc)


if __name__ == "__main__":
    unittest.main()
