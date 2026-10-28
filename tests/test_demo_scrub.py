"""ci/demo_scrub.py: the committed demo tree is clean (always on), and planted leaks in a temp copy are caught.

Leak fixtures are assembled at runtime so this file holds no hit itself."""
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
TOOL = ROOT / "ci" / "demo_scrub.py"
sys.path.insert(0, str(ROOT / "ci"))
import demo_scrub  # noqa: E402

HOME_LEAK = "cfg=/ho" + "me/alice/work\n"
LAN_LEAK = "host 192." + "168.1.20\n"
FONT_URL = '<link href="https://' + 'fonts.example.net/x.css">\n'


def run_cli(*args, cwd=ROOT):
    env = {k: v for k, v in os.environ.items() if k != "SCRUB_TERMS_FILE"}
    return subprocess.run([sys.executable, str(TOOL), *args], cwd=cwd, env=env, capture_output=True, text=True)


class CommittedTree(unittest.TestCase):
    def test_committed_demo_tree_is_clean(self):
        r = run_cli()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)

    def test_usage_error_exits_2(self):
        self.assertEqual(run_cli("--text-dir", "/nonexistent-dir-for-test").returncode, 2)
        self.assertEqual(run_cli("--bogus").returncode, 2)


class Planted(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        (self.root / "docs" / "demo" / "scenes").mkdir(parents=True)
        (self.root / "ci").mkdir()
        self.put("docs/demo/scenes/ok.html", '<p>hello http://127.0.0.1:8080/x</p><script src="demo.js"></script>\n')

    def put(self, rel, text, mode="w"):
        p = self.root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text) if mode == "w" else p.write_bytes(text)

    def scan(self, **kw):
        env = os.environ.pop("SCRUB_TERMS_FILE", None)
        try:
            return demo_scrub.scan(root=self.root, **kw)
        finally:
            if env is not None:
                os.environ["SCRUB_TERMS_FILE"] = env

    def test_clean_copy_passes(self):
        self.assertEqual(self.scan(), 0)

    def test_home_path_in_source_fails(self):
        self.put("docs/demo/scenes/bad.html", HOME_LEAK)
        self.assertEqual(self.scan(), 1)

    def test_lan_address_in_demo_script_fails(self):
        self.put("ci/demo_x.py", "# " + LAN_LEAK)
        self.assertEqual(self.scan(), 1)

    def test_https_font_url_fails(self):
        self.put("docs/demo/scenes/bad.html", FONT_URL)
        self.assertEqual(self.scan(), 1)

    def test_remote_font_face_and_script_src_fail(self):
        self.put("docs/demo/scenes/a.css", "@font-face { src: url(../../../x.woff2); }\n")
        self.assertEqual(self.scan(), 1)
        (self.root / "docs/demo/scenes/a.css").unlink()
        self.put("docs/demo/scenes/b.html", '<script src="../../outside.js"></script>\n')
        self.assertEqual(self.scan(), 1)

    def test_local_font_face_and_script_pass(self):
        self.put("docs/demo/fonts/f.woff2", b"\0wOF2", mode="b")
        self.put("docs/demo/scenes/a.css", "@font-face { src: url(../fonts/f.woff2); }\n")
        self.assertEqual(self.scan(), 0)

    def test_text_dump_leak_fails(self):
        txt = self.root / "text"
        txt.mkdir()
        (txt / "hook-0030.txt").write_text("title\n" + HOME_LEAK)
        self.assertEqual(self.scan(text_dir=txt), 1)
        (txt / "hook-0030.txt").write_text("title\nall fine\n")
        self.assertEqual(self.scan(text_dir=txt), 0)

    def test_text_dump_leak_fails_via_cli(self):
        txt = self.root / "text"
        txt.mkdir()
        (txt / "a.txt").write_text(LAN_LEAK)
        self.assertEqual(run_cli("--text-dir", str(txt)).returncode, 1)

    def test_binary_files_are_skipped(self):
        self.put("docs/demo/scenes/shot.png", b"\x89PNG\0" + HOME_LEAK.encode(), mode="b")
        self.put("docs/demo/scenes/clip.mp4", HOME_LEAK.encode() + b"\0", mode="b")
        self.assertEqual(self.scan(), 0)

    def test_out_dir_is_skipped(self):
        self.put("docs/demo/out/work/text.txt", HOME_LEAK + FONT_URL)
        self.assertEqual(self.scan(), 0)

    def test_userinfo_url_is_external(self):
        # T-0486 R6 a: a loopback prefix followed by userinfo punctuation must not pass as the local server
        for url in ("http://127.0.0.1" + "@" + "cdn.example.net/x.js", "http://127.0.0.1:80" + "@" + "cdn.example.net/x.js"):
            self.assertIsNone(demo_scrub.LOCAL_RE.match(url), url)
            self.put("docs/demo/scenes/bad.html", '<img src="%s">\n' % url)
            hits = demo_scrub.static_hits(self.root / "docs/demo/scenes/bad.html", self.root)
            self.assertTrue(any("external URL" in h for h in hits), hits)
        for url in ("http://127.0.0.1", "http://127.0.0.1:8080/a?b=c", "http://127.0.0.1/"):
            self.assertIsNotNone(demo_scrub.LOCAL_RE.match(url), url)

    def test_protocol_relative_reference_fails(self):
        # T-0486 R6 b
        for rel, text in (("a.html", '<img src="//cdn.example.net/x.png">\n'),
                          ("b.html", "<a href='//cdn.example.net/'>x</a>\n"),
                          ("c.css", "a { background: url(//cdn.example.net/x.png); }\n"),
                          ("d.css", '@import "//cdn.example.net/x.css";\n')):
            self.put("docs/demo/scenes/" + rel, text)
            self.assertEqual(self.scan(), 1, rel)
            (self.root / "docs/demo/scenes" / rel).unlink()
        self.put("docs/demo/scenes/ok.css", "a { background: url(../fonts/x.png); }\n// not a url\n")
        self.assertEqual(self.scan(), 0)

    def test_text_file_with_a_stray_nul_is_reported(self):
        # T-0486 R6 c: scrub_scan.py skips a file with a NUL as binary; a source file must not vanish from the scan
        self.put("docs/demo/scenes/nul.html", b"<p>ok</p>\0<p>" + HOME_LEAK.encode() + b"</p>\n", mode="b")
        self.assertEqual(self.scan(), 1)
        r = subprocess.run([sys.executable, str(TOOL)], cwd=ROOT, capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)  # the committed tree has none

    def test_nul_message_names_the_file(self):
        self.put("docs/demo/scenes/nul.html", b"a\0b\n", mode="b")
        import contextlib
        import io
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            self.scan()
        self.assertIn("scenes/nul.html", err.getvalue())
        self.assertIn("NUL", err.getvalue())


if __name__ == "__main__":
    unittest.main()
