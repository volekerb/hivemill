"""Tests for web/server.py, web/html.py and the `factory serve` shim (stdlib http.client against an in-process server)."""
import argparse
import ast
import contextlib
import hashlib
import http.client
import io
import json
import os
import re
import sqlite3
import sys
import tempfile
import threading
import time
import unittest
from html.parser import HTMLParser
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from harness import FactoryTestCase  # noqa: E402
from test_web_queries import make_db  # noqa: E402
from web import html as wh  # noqa: E402
from web import queries, server  # noqa: E402
from web.views import ROUTES  # noqa: E402

XSS = "<script>alert(1)</script>"
PAGES = ["/", "/goals", "/goal/G-1", "/tasks", "/task/T-1", "/agents", "/resources", "/throughput", "/quality",
         "/changelog", "/tokens"]
APIS = ["/api/home.json", "/api/goals.json", "/api/tasks.json", "/api/agents.json", "/api/resources.json",
        "/api/throughput.json", "/api/quality.json", "/api/changelog.json", "/api/tokens.json"]
ALL_ROUTES = PAGES + APIS + ["/healthz", "/static/app.css", "/static/favicon.svg"]


def get(httpd, path, host="auto", method="GET"):
    port = httpd.server_address[1]
    c = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
    try:
        c.putrequest(method, path, skip_host=True, skip_accept_encoding=True)
        c.putheader("Host", f"127.0.0.1:{port}" if host == "auto" else host)
        c.endheaders()
        r = c.getresponse()
        return r.status, dict(r.getheaders()), r.read()
    finally:
        c.close()


class ServerCase(unittest.TestCase):
    hostnames = ()

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.db_path = Path(self.tmp.name) / "factory.db"
        self.con = make_db(self.db_path)
        self.addCleanup(self.con.close)
        self.httpd = server.make_server(self.db_path, "127.0.0.1", 0, self.hostnames, tokens=lambda: {"rows": []},
                                        usage_fn=lambda: {"five_hour": 10.0, "seven_day": 20.0, "fresh": True, "age_min": 1},
                                        mem_fn=lambda: 2048, defaults={"max_agents": "6"})
        t = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        t.start()
        self.addCleanup(lambda: (self.httpd.shutdown(), self.httpd.server_close()))
        self.port = self.httpd.server_address[1]

    def get(self, *a, **k):
        with contextlib.redirect_stderr(io.StringIO()):
            return get(self.httpd, *a, **k)


class TestRoutes(ServerCase):
    def test_every_route_returns_200(self):
        for p in ALL_ROUTES:
            with self.subTest(p):
                self.assertEqual(self.get(p)[0], 200)

    def test_api_json_is_valid_json(self):
        for p in APIS:
            with self.subTest(p):
                st, h, body = self.get(p)
                self.assertEqual(h["Content-Type"], "application/json")
                self.assertIsInstance(json.loads(body), dict)

    def test_home_shows_kpis(self):
        d = json.loads(self.get("/api/home.json")[2])
        self.assertEqual(d["live_agents"], 2)
        self.assertEqual(d["usage"]["five_hour"], 10.0)

    def test_tasks_query_filters(self):
        all_ids = {t["id"] for t in json.loads(self.get("/api/tasks.json")[2])["tasks"]}
        d = json.loads(self.get("/api/tasks.json?goal=G-1&status=done")[2])
        self.assertEqual(d["filter"], {"goal": "G-1", "status": "done"})
        self.assertTrue(d["tasks"])
        self.assertTrue(all(t["goal_id"] == "G-1" and t["status"] == "done" for t in d["tasks"]))
        st, _, body = self.get("/tasks?goal=G-1&status=done")
        self.assertEqual(st, 200)
        self.assertIn(b"Filtered: goal=G-1, status=done", body)
        bad = self.get("/api/tasks.json?goal=<script>&status=nope&x=1")
        self.assertEqual(bad[0], 200)
        dd = json.loads(bad[2])
        self.assertEqual(dd["filter"], {"goal": None, "status": None})
        self.assertEqual({t["id"] for t in dd["tasks"]}, all_ids)

    def test_healthz(self):
        st, h, body = self.get("/healthz")
        self.assertTrue(body.startswith(b"ok\n"))
        self.assertIn(b"db_mtime_age_s=", body)

    def test_unknown_paths_404(self):
        for p in ["/nope", "/goal/xx", "/goal/G-1/x", "/task/T-1%0a", "/api/nope.json", "/api/goal/G-1.json",
                  "/api/goal/<id>.json", "/static/", "/static/missing.css"]:
            with self.subTest(p):
                self.assertEqual(self.get(p)[0], 404)

    def test_405_for_writes(self):
        for m in ("POST", "PUT", "DELETE", "PATCH", "OPTIONS"):
            with self.subTest(m):
                st, h, _ = self.get("/", method=m)
                self.assertEqual(st, 405)
                self.assertEqual(h["Allow"], "GET, HEAD")

    def test_head_has_no_body(self):
        st, h, body = self.get("/", method="HEAD")
        self.assertEqual((st, body), (200, b""))
        self.assertGreater(int(h["Content-Length"]), 0)

    def test_security_headers(self):
        for p in ("/", "/static/app.css", "/nope"):
            h = self.get(p)[1]
            self.assertIn("default-src 'self'", h["Content-Security-Policy"])
            self.assertIn("frame-ancestors 'none'", h["Content-Security-Policy"])
            self.assertEqual(h["X-Content-Type-Options"], "nosniff")
            self.assertEqual(h["Referrer-Policy"], "no-referrer")
        self.assertEqual(self.get("/")[1]["Cache-Control"], "no-store")
        self.assertEqual(self.get("/static/app.css")[1]["Cache-Control"], "max-age=3600")

    def test_static_traversal_404(self):
        for p in ["/static/../server.py", "/static/%2e%2e/x", "/static/%2e%2e%2fserver.py", "/static/..%5cserver.py",
                  "/static/.hidden", "/static/a/../app.css", "/static//etc/passwd"]:
            with self.subTest(p):
                self.assertEqual(self.get(p)[0], 404)

    def test_static_types(self):
        self.assertTrue(self.get("/static/app.css")[1]["Content-Type"].startswith("text/css"))
        self.assertEqual(self.get("/static/favicon.svg")[1]["Content-Type"], "image/svg+xml")

    def test_view_exception_gives_generic_500(self):
        boom = SimpleNamespace(TITLE="x", data=lambda ctx, **p: 1 / 0, render=lambda c, d: "")
        err = io.StringIO()
        with mock.patch.dict(ROUTES, {"/goals": boom}), contextlib.redirect_stderr(err):
            st, _, body = get(self.httpd, "/goals")
        self.assertEqual(st, 500)
        self.assertNotIn(b"Traceback", body)
        self.assertNotIn(b"division", body)
        self.assertIn("ZeroDivisionError", err.getvalue())

    def test_busy_gives_503(self):
        self.httpd.app.sem = server.threading.BoundedSemaphore(1)
        self.httpd.app.sem.acquire()
        st, h, _ = self.get("/")
        self.assertEqual(st, 503)

    def test_titles_are_escaped(self):
        self.con.execute("UPDATE goals SET title=? WHERE id='G-1'", (XSS,))
        self.con.execute("UPDATE tasks SET title=? WHERE id='T-1'", (XSS,))

        def render(ctx, d):
            return wh.table(["goal", "task"], [[g["title"], t["title"]] for g in d["g"] for t in d["t"]])
        v = SimpleNamespace(TITLE="x", data=lambda ctx, **p: {"g": queries.goals(ctx.db), "t": queries.tasks(ctx.db)},
                            render=render)
        with mock.patch.dict(ROUTES, {"/goals": v}):
            st, _, body = self.get("/goals")
        self.assertEqual(st, 200)
        text = body.decode()
        self.assertNotIn("<script>", text)
        self.assertIn("&lt;script&gt;alert(1)&lt;/script&gt;", text)


class TestHostAllowlist(ServerCase):
    hostnames = ("factory.example",)

    def test_bad_host_421(self):
        for host in ("evil.example", "evil.example:%d" % self.port, "127.0.0.1:1", "", "factory.example.evil.example"):
            with self.subTest(host):
                self.assertEqual(self.get("/", host=host)[0], 421)

    def test_bind_and_configured_name_with_port_ok(self):
        self.assertEqual(self.get("/", host=f"127.0.0.1:{self.port}")[0], 200)
        self.assertEqual(self.get("/", host=f"factory.example:{self.port}")[0], 200)
        self.assertEqual(self.get("/", host=f"Factory.Example:{self.port}")[0], 200)

    def test_bare_name_only_on_port_80(self):
        self.assertEqual(self.get("/", host="factory.example")[0], 421)
        self.assertEqual(self.get("/", host="127.0.0.1")[0], 421)
        self.assertEqual(server._host_allowed("10.0.0.5", 80, ["factory.example"]),
                         {"10.0.0.5:80", "10.0.0.5", "factory.example:80", "factory.example"})


class TestHostAllowlistEmpty(ServerCase):
    hostnames = ()

    def test_only_bind_address(self):
        self.assertEqual(self.get("/", host=f"127.0.0.1:{self.port}")[0], 200)
        self.assertEqual(self.get("/", host=f"factory.example:{self.port}")[0], 421)


class TestPickBind(unittest.TestCase):
    def refused(self, addr):
        with contextlib.redirect_stderr(io.StringIO()) as err, self.assertRaises(SystemExit) as cm:
            server.pick_bind(addr)
        self.assertEqual(cm.exception.code, 2)
        self.assertIn("refusing", err.getvalue()) if addr else None

    def test_refuses(self):
        for a in ("0.0.0.0", "::", "8.8.8.8", "224.0.0.1", "example.com", "1.1.1.1", "2001:4860:4860::8888"):
            with self.subTest(a):
                self.refused(a)

    def test_accepts(self):
        for a in ("127.0.0.1", "192.0.2.10", "10.0.0.5", "::1", "172.16.3.4"):
            with self.subTest(a):
                self.assertEqual(server.pick_bind(a), a)

    def test_auto_uses_default_route_source(self):
        fake = mock.MagicMock()
        fake.getsockname.return_value = ("192.0.2.10", 5555)
        with mock.patch("web.server.socket.socket", return_value=fake):
            self.assertEqual(server.pick_bind(None), "192.0.2.10")
        fake.connect.assert_called_once_with(("192.0.2.1", 9))
        fake.send.assert_not_called()
        fake.sendto.assert_not_called()

    def test_auto_refuses_public_source(self):
        fake = mock.MagicMock()
        fake.getsockname.return_value = ("8.8.8.8", 5555)
        with mock.patch("web.server.socket.socket", return_value=fake):
            self.refused(None)


class TestHtml(unittest.TestCase):
    def test_escape(self):
        self.assertEqual(wh.h(None), "")
        self.assertEqual(wh.h('<a href="x">&\''), "&lt;a href=&quot;x&quot;&gt;&amp;&#x27;")
        self.assertEqual(str(wh.el("p", XSS, wh.Safe("<b>ok</b>"), class_="muted")),
                         '<p class="muted">&lt;script&gt;alert(1)&lt;/script&gt;<b>ok</b></p>')
        self.assertEqual(str(wh.el("a", "x", href='"><script>')), '<a href="&quot;&gt;&lt;script&gt;">x</a>')

    def test_attribute_names_validated(self):
        for bad in ({"onclick=alert(1) x": "1"}, {"Class": "x"}, {"a1": "x"}):
            with self.assertRaises(ValueError):
                wh.el("p", **bad)
        with self.assertRaises(ValueError):
            wh.el("p><script", "x")

    def test_link_only_safe_targets(self):
        pr = "https://github.com/example-org/factory/pull/46"
        self.assertIn(f'href="{pr}"', wh.link(pr, "pr"))
        self.assertIn('href="/goals"', wh.link("/goals", "g"))
        for bad in ("javascript:alert(1)", "//evil.example/x", "https://evil.example/", "https://github.com/a/b/pull/1x",
                    "https://github.com/a/b/issues/1", "/\\evil", "", None):
            with self.subTest(bad):
                self.assertEqual(str(wh.link(bad, "<t>")), "&lt;t&gt;")

    def test_bar_clamped(self):
        self.assertIn("bar--100", str(wh.bar(250)))
        self.assertIn("bar--0", str(wh.bar(-5)))
        self.assertIn("bar--0", str(wh.bar("x")))
        out = str(wh.bar(42.6))
        self.assertIn("bar--45", out)
        self.assertIn("43%", out)
        self.assertNotIn("style=", out)

    def test_bar_label_and_aria(self):
        scaled = str(wh.bar(50, label=7, aria="7 of max 14"))
        self.assertIn("bar--50", scaled)
        self.assertIn('<span class="mono">7</span>', scaled)
        self.assertIn('aria-label="7 of max 14"', scaled)
        self.assertNotIn("%", scaled.replace("7 of max 14", ""))
        share = str(wh.bar(30, label="30% of total", aria="30%"))
        self.assertIn("30% of total", share)
        self.assertIn('aria-label="30%"', share)
        esc = str(wh.bar(5, label="<b>", aria='"x'))
        self.assertIn("&lt;b&gt;", esc)
        self.assertIn('aria-label="&quot;x"', esc)

    def test_bar_classes_defined_in_css(self):
        css = (Path(__file__).resolve().parent.parent / "web" / "static" / "app.css").read_text()
        for n in range(0, 101, 5):
            self.assertIn(f".bar--{n} ", css)
            self.assertIn(f"bar--{n}", str(wh.bar(n)))

    def test_ts_and_dur(self):
        self.assertIn('title="2026-10-24T08:00:00+02:00"', wh.ts("2026-10-24T08:00:00+02:00"))
        self.assertEqual(str(wh.ts(None)), "")
        self.assertEqual([wh.dur(x) for x in (None, 5, 65, 3700, 90000)], ["-", "5s", "1m 05s", "1h 01m", "1d 1h"])

    def test_table_escapes_cells(self):
        t = str(wh.table(["<h>"], [[XSS], [(5, "num")]]))
        self.assertNotIn("<script>", t)
        self.assertIn("&lt;h&gt;", t)
        self.assertIn('<td class="num">5</td>', t)


class _Classes(HTMLParser):
    def __init__(self):
        super().__init__()
        self.classes, self.links = set(), []

    def handle_starttag(self, tag, attrs):
        for k, v in attrs:
            if k == "class":
                self.classes.update(v.split())
            if k == "href":
                self.links.append(v)


class TestVocabularyAndStubs(ServerCase):
    def parse(self, text):
        p = _Classes()
        p.feed(text)
        return p

    def test_helpers_and_pages_use_only_vocabulary(self):
        sample = str(wh.el("div", wh.badge("ok", "ok"), wh.kpi("a", 1), wh.bar(5), wh.table(["a"], [[1]], "table table--compact"),
                           wh.card("t", wh.empty("e")), wh.el("span", "x", class_="mono stale muted")))
        p = self.parse(sample + wh.page("T", "<p>x</p>", "/goals"))
        self.assertFalse(p.classes - wh.CLASSES, p.classes - wh.CLASSES)
        for path in PAGES:
            with self.subTest(path):
                p = self.parse(self.get(path)[2].decode())
                self.assertFalse(p.classes - wh.CLASSES, p.classes - wh.CLASSES)

    def test_every_page_has_nav_to_all_pages(self):
        want = [p for p, _ in wh.NAV]
        self.assertEqual(want, ["/", "/goals", "/tasks", "/agents", "/throughput", "/quality",
                                "/tokens", "/changelog", "/resources"])
        for path in PAGES:
            with self.subTest(path):
                text = self.get(path)[2].decode()
                self.assertTrue(text.startswith("<!doctype html>"))
                self.assertIn('name="viewport" content="width=device-width, initial-scale=1"', text)
                self.assertIn('name="color-scheme" content="light dark"', text)
                self.assertIn('href="/static/app.css"', text)
                links = self.parse(text).links
                for w in want:
                    self.assertIn(w, links)

    def test_active_nav_item(self):
        text = self.get("/goal/G-1")[2].decode()
        self.assertEqual(text.count("nav-item--active"), 1)
        self.assertRegex(text, r'<a [^>]*class="nav-item nav-item--active"[^>]*href="/goals"|<a [^>]*href="/goals"[^>]*class="nav-item nav-item--active"')

    def test_no_external_assets(self):
        for path in PAGES:
            text = self.get(path)[2].decode()
            self.assertNotRegex(text, r'src="(?:https?:)?//')
            # outbound links are allowed only to GitHub pull requests (web.html.link); never to assets
            self.assertNotRegex(text, r'href="(?:https?:)?//(?!github\.com/[\w.-]+/[\w.-]+/pull/\d+")')
            for href in re.findall(r'href="(https?://[^"]*)"', text):
                self.assertRegex(href, r"^https://github\.com/[\w.-]+/[\w.-]+/pull/\d+$")
            self.assertNotIn("<script", text.replace('<script src="/static/theme.js" defer></script>', ""))  # only the deferred theme toggle

    def test_registry_complete(self):
        self.assertEqual(set(ROUTES), {"/", "/goals", "/goal/<id>", "/tasks", "/task/<id>", "/agents", "/resources",
                                       "/throughput", "/quality", "/changelog", "/tokens"})
        for v in ROUTES.values():
            self.assertTrue(callable(v.data) and callable(v.render))


class TestNoTickBlocking(ServerCase):
    def test_writer_not_blocked_by_readers(self):
        stop = threading.Event()
        errors = []

        def hammer():
            try:
                for i in range(13):
                    if self.get(PAGES[i % len(PAGES)])[0] != 200:
                        errors.append(i)
            except Exception as e:  # noqa: BLE001
                errors.append(e)
        threads = [threading.Thread(target=hammer) for _ in range(4)]
        for t in threads:
            t.start()
        w = sqlite3.connect(self.db_path, isolation_level=None, timeout=1)
        slow = []
        for i in range(20):
            t0 = time.perf_counter()
            w.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('t','w','x','y','z',?)", (str(i),))
            slow.append(time.perf_counter() - t0)
            time.sleep(0.005)
        w.close()
        for t in threads:
            t.join()
        stop.set()
        self.assertEqual(errors, [])
        self.assertLess(max(slow), 0.1, slow)


class TestWebCodeNeverCallsFactoryDb(unittest.TestCase):
    def test_no_db_call_or_bin_factory_import_in_web(self):
        for f in (ROOT / "web").rglob("*.py"):
            tree = ast.parse(f.read_text())
            for n in ast.walk(tree):
                if isinstance(n, ast.Call):
                    name = n.func.id if isinstance(n.func, ast.Name) else getattr(n.func, "attr", "")
                    self.assertNotEqual(name, "db", f"{f}:{n.lineno} calls db()")
                if isinstance(n, (ast.Import, ast.ImportFrom)):
                    mod = getattr(n, "module", None) or ""
                    self.assertNotIn("bin", mod.split("."), f"{f}:{n.lineno}")


class TestServeShim(FactoryTestCase):
    def test_ui_cannot_write_db(self):
        """SC1: serve opens the DB read-only, never via bin/factory db(); every route leaves the DB untouched."""
        e = self.env
        e.seed_goal(title=XSS)
        e.seed_team(lead="zz-test-lead")
        e.seed_task()
        e.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", task="T-0001")
        e.sql("UPDATE tasks SET title=? WHERE id='T-0001'", (XSS,))
        e.write_usage(five_hour=12, seven_day=34)
        db_file = e.db_path
        self.assertEqual(e.sql("PRAGMA journal_mode")[0][0], "wal")
        started, captured = [], {}

        def fake_run(**kw):
            captured.update(kw)
            httpd = server.make_server(kw["db_path"], "127.0.0.1", 0, kw["hostnames"], tokens=lambda: {"rows": []},
                                       usage_fn=kw["usage_fn"], mem_fn=kw["mem_fn"], defaults=kw["defaults"],
                                       factory_home=kw["factory_home"])
            threading.Thread(target=httpd.serve_forever, daemon=True).start()
            started.append(httpd)

        attempts = []

        def probe(ctx, **p):
            try:
                ctx.db.execute("INSERT INTO config VALUES('zz','1')")
            except sqlite3.OperationalError as ex:
                attempts.append(str(ex))
            return {}
        probe_view = SimpleNamespace(TITLE="p", data=probe, render=lambda c, d: "")

        sha = lambda: hashlib.sha256(db_file.read_bytes()).hexdigest()  # noqa: E731
        e.sql("UPDATE config SET value='factory.example' WHERE key='serve_hostnames'")
        with e.load_factory() as mod:
            mod.db = mock.Mock(side_effect=AssertionError("serve must not call db()"))
            with mock.patch("web.server.run", fake_run):
                mod.cmd_serve(argparse.Namespace(bind=None, port=None))
            httpd = started[0]
            self.addCleanup(lambda: (httpd.shutdown(), httpd.server_close()))
            self.assertEqual(captured["hostnames"], ["factory.example"])
            self.assertEqual(captured["db_path"](), db_file)  # a resolver: serve follows a layout migration per request
            mod.db.assert_not_called()
            other = sqlite3.connect(db_file)
            self.addCleanup(other.close)
            dv0, sha0 = other.execute("PRAGMA data_version").fetchone()[0], sha()
            with mock.patch.dict(ROUTES, {"/tokens": probe_view}), contextlib.redirect_stderr(io.StringIO()):
                for p in ALL_ROUTES:
                    self.assertEqual(get(httpd, p)[0], 200, p)
                body = get(httpd, "/goals")[2]
                # acceptance 10: the configured name factory.example:<port> is accepted, not evil.example
                port = httpd.server_address[1]
                self.assertEqual(get(httpd, "/", host=f"factory.example:{port}")[0], 200)
                self.assertEqual(get(httpd, "/", host="evil.example")[0], 421)
            self.assertTrue(attempts and "readonly" in attempts[0].lower(), attempts)
            mod.db.assert_not_called()
            self.assertEqual(other.execute("PRAGMA data_version").fetchone()[0], dv0)
            self.assertEqual(sha(), sha0)
        self.assertNotIn(b"<script>alert", body)

    def test_main_serve_skips_identity_and_db(self):
        """B1: main() exempts serve before actor()/require_known(), so a herdr pane never makes it touch db()."""
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        e.seed_task()
        e.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", task="T-0001", pane_id="w9:p9")
        db_file = e.db_path
        sha = lambda: hashlib.sha256(db_file.read_bytes()).hexdigest()  # noqa: E731
        sha0 = sha()
        cases = {"unset": (None, "w8:p8"), "owner": ("owner", "w8:p8"),
                 "registered pane, unset": (None, "w9:p9"), "registered pane, owner": ("owner", "w9:p9")}
        for label, (fa, pane) in cases.items():
            with self.subTest(label), e.load_factory() as mod:
                os.environ["HERDR_PANE_ID"] = pane
                if fa:
                    os.environ["FACTORY_AGENT"] = fa
                mod.db = mock.Mock(side_effect=AssertionError("serve must not call db()"))
                run = mock.Mock()
                with mock.patch.object(sys, "argv", ["factory", "serve"]), mock.patch("web.server.run", run):
                    mod.main()
                run.assert_called_once()
                mod.db.assert_not_called()
                self.assertEqual(sha(), sha0)
        self.assertEqual(e.sql("SELECT count(*) c FROM events WHERE action='identity-spoof'")[0]["c"], 0)

    def test_config_keys_and_cli(self):
        r = self.env.run("--help")
        self.assertIn("serve", r.stdout)
        with self.env.load_factory() as mod:
            self.assertEqual((mod.DEFAULT_CONFIG["serve_bind"], mod.DEFAULT_CONFIG["serve_port"],
                              mod.DEFAULT_CONFIG["serve_hostnames"]), ("", "8765", ""))

    def test_serve_refuses_wildcard_and_public_bind(self):
        for addr in ("0.0.0.0", "8.8.8.8"):
            with self.subTest(addr):
                r = self.env.run("serve", "--bind", addr, "--port", "0")
                self.assertEqual(r.returncode, 2, r.stderr)
                self.assertIn("refusing", r.stderr)

    def test_serve_config_overrides(self):
        e = self.env
        e.run("config", "serve_hostnames", "a.lan, b.lan")
        e.run("config", "serve_port", "18999")
        captured = {}
        with e.load_factory() as mod, mock.patch("web.server.run", lambda **kw: captured.update(kw)):
            mod.cmd_serve(argparse.Namespace(bind="127.0.0.1", port=None))
        self.assertEqual((captured["bind"], captured["port"], captured["hostnames"]), ("127.0.0.1", 18999, ["a.lan", "b.lan"]))


if __name__ == "__main__":
    unittest.main()
