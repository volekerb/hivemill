"""docs/demo theme polish: brand filled, fonts vendored with licence, no external URL, deterministic, shared names kept."""
import hashlib
import json
import re
import struct
import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEMO = ROOT / "docs" / "demo"
THEME = DEMO / "scenes" / "theme.css"
FONTS = DEMO / "fonts"
STYLE = DEMO / "style"

# Every selector and variable theme.css defined before the polish (T-0431). Other scenes build on them: never rename.
OLD_VARS = ["--accent", "--bg", "--bg-2", "--boss", "--coder", "--director", "--fs-body", "--fs-caption", "--fs-hero",
            "--fs-small", "--fs-title", "--judge", "--lead", "--line", "--mono", "--muted", "--ok", "--panel", "--pm",
            "--qa", "--reviewer", "--sans", "--text"]
OLD_SELECTORS = ["*", ".caption-bar", ".caption-bar span", ".caption-bar.on", ".chip", ".dot",
                 ".hero-caption .caption-bar", ".hero-caption .caption-bar span", ".mono", ".role-boss", ".role-coder",
                 ".role-director", ".role-judge", ".role-lead", ".role-pm", ".role-qa", ".role-reviewer", ".stage",
                 ".wordmark", '.wordmark [data-brand="name"]', ":root", "body", "html"]


def strip_comments(css):
    return re.sub(r"/\*.*?\*/", "", css, flags=re.S)


def selectors(css):
    out = set()
    for m in re.finditer(r"([^{}]+)\{", strip_comments(css)):
        for sel in m.group(1).split(","):
            sel = sel.strip()
            if sel and not sel.startswith("@"):
                out.add(sel)
    return out


def demo_text_files():
    files = [p for p in (DEMO / "scenes").rglob("*") if p.is_file()] + sorted(STYLE.glob("*.html"))
    return files


class BrandTests(unittest.TestCase):
    def setUp(self):
        self.brand = json.loads((DEMO / "brand.json").read_text())

    def test_no_placeholder_left(self):
        self.assertEqual([k for k, v in self.brand.items() if "{{" in str(v)], [])

    def test_values(self):
        self.assertEqual(self.brand["name"], "Hivemill")
        self.assertEqual(self.brand["github_url"], "github.com/volekerb/hivemill")
        self.assertEqual(self.brand["tagline"], "Talk to one agent. A whole team ships your PRs.")
        self.assertTrue(self.brand["install"].strip())

    def test_wordmark_path_resolves_from_docs_demo(self):
        target = (DEMO / self.brand["wordmark_svg"]).resolve()
        self.assertTrue(target.is_file(), target)
        self.assertEqual(target, (ROOT / "docs" / "images" / "wordmark-dark.svg").resolve())

    def test_no_domain_or_scheme_in_brand(self):
        for k, v in self.brand.items():
            self.assertNotRegex(str(v), r"https?://", k)
        self.assertNotRegex(self.brand["github_url"], r"^(https?://|www\.)")

    def test_frames_embed_the_same_brand(self):
        for page in STYLE.glob("*.html"):
            m = re.search(r'id="demo-data">(.*?)</script>', page.read_text(), flags=re.S)
            self.assertEqual(json.loads(m.group(1))["brand"], self.brand, page.name)


class FontTests(unittest.TestCase):
    def woff2(self):
        return sorted(FONTS.glob("*.woff2"))

    def test_files_and_sizes(self):
        names = [p.name for p in self.woff2()]
        self.assertTrue(1 <= len(names) <= 4, names)
        self.assertTrue(any(n.startswith("Inter-") for n in names), names)
        self.assertTrue(any(n.startswith("JetBrainsMono-") for n in names), names)
        for p in self.woff2():
            self.assertLessEqual(p.stat().st_size, 400 * 1024, p.name)
            self.assertEqual(p.read_bytes()[:4], b"wOF2", p.name)

    def test_licence_texts(self):
        for name, holder in (("OFL-Inter.txt", "Inter Project"), ("OFL-JetBrainsMono.txt", "JetBrains Mono Project")):
            text = (FONTS / name).read_text()
            self.assertIn("SIL OPEN FONT LICENSE Version 1.1", text, name)
            self.assertIn(holder, text, name)

    def test_third_party_lists_every_file_with_matching_sha256(self):
        third = (ROOT / "THIRD_PARTY.md").read_text()
        for p in self.woff2():
            digest = hashlib.sha256(p.read_bytes()).hexdigest()
            self.assertRegex(third, r"`%s` sha256 `%s`" % (re.escape(p.name), digest))
        for needle in ("github.com/rsms/inter", "github.com/JetBrains/JetBrainsMono", "v4.1", "v2.304"):
            self.assertIn(needle, third)

    def test_font_faces_use_relative_vendored_files(self):
        css = strip_comments(THEME.read_text())
        urls = re.findall(r'@font-face[^}]*?url\("([^"]+)"\)', css)
        self.assertEqual(len(urls), len(self.woff2()))
        for u in urls:
            self.assertNotRegex(u, r"^(https?:|//|/)")
            self.assertTrue((THEME.parent / u).resolve().is_file(), u)
        self.assertIn('--sans: "Inter"', css)
        self.assertIn('--mono: "JetBrains Mono"', css)


class ThemeTests(unittest.TestCase):
    def test_no_external_url_or_dejavu(self):
        for p in demo_text_files():
            text = p.read_text()
            self.assertNotRegex(text, r"https?://|fonts\.googleapis|DejaVu", p.name)

    def test_deterministic_no_wall_clock(self):
        for p in [THEME] + sorted(STYLE.glob("*.html")):
            self.assertNotRegex(p.read_text(), r"Date\.now|setTimeout|setInterval|requestAnimationFrame", p.name)

    def test_old_variables_and_selectors_still_defined(self):
        css = THEME.read_text()
        defined_vars = set(re.findall(r"(--[\w-]+)\s*:", strip_comments(css)))
        self.assertEqual(sorted(set(OLD_VARS) - defined_vars), [])
        self.assertEqual(sorted(set(OLD_SELECTORS) - selectors(css)), [])

    def test_depth_classes_exist(self):
        css = strip_comments(THEME.read_text())
        self.assertIn("radial-gradient", css)
        self.assertRegex(css, r"\.active\s*\{[^}]*animation:")
        self.assertIn("@keyframes", css)
        self.assertRegex(css, r"\.card\s*\{[^}]*box-shadow")

    def test_scrub_scan_clean(self):
        r = subprocess.run([sys.executable, str(ROOT / "ci" / "scrub_scan.py"), "--path"]
                           + [str(p) for p in DEMO.rglob("*") if p.is_file() and p.suffix in (".html", ".css", ".json", ".js", ".txt")],
                           capture_output=True, text=True, cwd=ROOT)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)


class FrameTests(unittest.TestCase):
    def test_five_pngs_1080p_and_small(self):
        pngs = sorted(STYLE.glob("frame-*.png"))
        self.assertEqual(len(pngs), 5)
        for p in pngs:
            head = p.read_bytes()[:24]
            self.assertEqual(head[:8], b"\x89PNG\r\n\x1a\n", p.name)
            self.assertEqual(struct.unpack(">II", head[16:24]), (1920, 1080), p.name)
            self.assertLessEqual(p.stat().st_size, 1536 * 1024, p.name)


if __name__ == "__main__":
    unittest.main()
