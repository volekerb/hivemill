"""ci/web_render_bench.py on the test DB (generous bound, CI-safe) and the no-external-URLs guard for every rendered page."""
import contextlib
import io
import re
import shutil
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "ci"))
sys.path.insert(0, str(ROOT / "tests"))

import web_render_bench as bench  # noqa: E402
from test_web_queries import make_db  # noqa: E402
from test_web_server import PAGES, get  # noqa: E402

BOUND_MS = 1000  # generous: the real 200 ms limit is checked by the script itself, not by a wall-clock assert in CI
ATTR = re.compile(r'(?:src|href|poster|action|data)\s*=\s*["\']([^"\']*)["\']', re.I)
CSS_URL = re.compile(r'url\(\s*["\']?([^)"\']*)', re.I)
CSS_IMPORT = re.compile(r'@import\s+(?:url\(\s*)?["\']?([^)"\';\s]*)', re.I)
EXTERNAL = re.compile(r'^(?:https?:)?//(?!(?:127\.0\.0\.1|localhost|\[::1\])(?:[:/]|$))', re.I)
PR_LINK = re.compile(r"^https://github\.com/[\w.-]+/[\w.-]+/pull/\d+$")  # outbound PR links exist on main (web.html.link)


def external(text):
    urls = ATTR.findall(text) + CSS_URL.findall(text) + CSS_IMPORT.findall(text)
    return [u for u in urls if EXTERNAL.match(u.strip()) and not PR_LINK.match(u.strip())]


class BenchCase(unittest.TestCase):
    def setUp(self):
        tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, tmp, True)
        self.db = tmp / "factory.db"
        con = make_db(self.db)
        self.addCleanup(con.close)


class TestBench(BenchCase):
    def test_script_covers_every_page_route_under_bound(self):
        routes = bench.page_routes(self.db)
        self.assertEqual(sorted(routes), sorted(PAGES))
        with bench.serving(self.db) as port, contextlib.redirect_stderr(io.StringIO()):
            rows = bench.bench(port, routes, runs=2)
        self.assertEqual([r[0] for r in rows], routes)
        for route, med, hi, size in rows:
            with self.subTest(route):
                self.assertLess(med, BOUND_MS)
                self.assertGreaterEqual(hi, med)
                self.assertGreater(size, 0)
        text = bench.table(rows)
        for route in PAGES:
            self.assertIn(route, text)

    def test_slow_flags_median_at_or_over_limit(self):
        rows = [("/a", 199.9, 300.0, 1), ("/b", 200.0, 210.0, 1)]
        self.assertEqual([r[0] for r in bench.slow(rows)], ["/b"])

    def test_p95(self):
        self.assertEqual(bench.p95([1, 2, 3, 4, 100]), 100)
        self.assertEqual(bench.p95([5.0]), 5.0)

    def test_main_exit_codes(self):
        for rows, want in (([("/", 1.0, 2.0, 10)], 0), ([("/", 250.0, 260.0, 10)], 1)):
            with self.subTest(want=want), mock.patch.object(bench, "bench", return_value=rows), \
                    mock.patch.object(bench, "page_routes", return_value=["/"]), \
                    mock.patch.object(bench, "serving", return_value=contextlib.nullcontext(1)), \
                    mock.patch("screenshots.seed"), \
                    contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(bench.main(["--runs", "1"]), want)


class TestNoExternalUrls(BenchCase):
    def test_detector_rejects_and_accepts(self):
        for bad in ('<script src="https://cdn.example.com/x.js">', '<link href="http://fonts.example.com/a.css">',
                    '<img src="//evil.example/a.png">', 'a{background:url(https://x.example/a.png)}',
                    '@import "https://x.example/a.css";', "@import url(//x.example/a.css);",
                    '<a href="http://127.0.0.1.evil.example/">'):
            with self.subTest(bad):
                self.assertTrue(external(bad), bad)
        for good in ('<script src="/static/theme.js" defer>', '<link href="/static/app.css">',
                     '<img src="http://127.0.0.1:8765/a.png">', 'a{background:url(data:image/svg+xml;base64,AA)}',
                     '<a href="https://github.com/o/r/pull/12">'):
            with self.subTest(good):
                self.assertEqual(external(good), [], good)

    def test_no_page_or_stylesheet_references_an_external_host(self):
        with bench.serving(self.db) as port, contextlib.redirect_stderr(io.StringIO()):
            class H:  # test_web_server.get() only needs server_address
                server_address = ("127.0.0.1", port)
            for path in PAGES + ["/static/app.css", "/static/theme.js"]:
                with self.subTest(path):
                    st, _, body = get(H, path)
                    self.assertEqual(st, 200)
                    self.assertEqual(external(body.decode()), [])


if __name__ == "__main__":
    unittest.main()
