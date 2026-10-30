"""HTTP server for the read-only factory web UI (stdlib only, GET/HEAD, LAN bind, Host allowlist).

Opens the factory DB (db_path: a path, or a callable resolved per request) only through web.db (read-only URI, one connection per request); never calls bin/factory.
"""
import datetime as dt
import ipaddress
import json
import os
import re
import signal
import socket
import subprocess
import sys
import threading
import time
import traceback
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, unquote, urlsplit

from web import db as wdb
from web import queries
from web.html import page
from web.tokens import TokenIndex
from web.views import ROUTES

STATIC = Path(__file__).resolve().parent / "static"
STATIC_TYPES = {".css": "text/css; charset=utf-8", ".svg": "image/svg+xml", ".js": "text/javascript; charset=utf-8",
                ".png": "image/png", ".ico": "image/x-icon", ".woff2": "font/woff2"}
CSP = ("default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; "
       "frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
MAX_CONCURRENT = 8
REQUEST_TIMEOUT_S = 10
_ID_RE = re.compile(r"^/(goal|task)/([A-Z]-\d+)$")
_STATIC_NAME_RE = re.compile(r"^(?:fonts/)?[\w][\w.-]*$")  # one level: static/fonts/
RELOAD_INTERVAL_S = 60     # FACTORY_SERVE_RELOAD_INTERVAL overrides it (tests)
ROOT = Path(__file__).resolve().parent.parent
# callables a view may append at import; each runs once in a daemon thread when the server starts
background = []


class Ctx:
    """What a view gets: a per-request read-only connection plus injected helpers."""

    def __init__(self, db, tokens, usage_fn, mem_fn, defaults, now, factory_home=None, query=None):
        self.db, self.tokens, self.usage_fn, self.mem_fn = db, tokens, usage_fn, mem_fn
        self.defaults, self.now, self.factory_home = defaults, now, factory_home
        self.query = query or {}    # str -> first value of the query string; views validate what they use


def _refuse(msg):
    print(f"factory serve: {msg}", file=sys.stderr)
    raise SystemExit(2)


def pick_bind(explicit=None):
    """Bind address: `explicit`, else the source address of the default route (UDP connect, no packet sent).
    Refuses unspecified, multicast and non-private addresses; loopback is allowed."""
    addr = explicit
    if not addr:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        try:
            s.connect(("192.0.2.1", 9))
            addr = s.getsockname()[0]
        except OSError as e:
            _refuse(f"cannot find the LAN address ({e}); pass --bind ADDR")
        finally:
            s.close()
    try:
        ip = ipaddress.ip_address(addr)
    except ValueError:
        _refuse(f"refusing to bind {addr!r}: not an IP address")
    if ip.is_unspecified or ip.is_multicast:
        _refuse(f"refusing to bind {addr}: must be one private LAN address, not a wildcard or multicast")
    if not (ip.is_private or ip.is_loopback):
        _refuse(f"refusing to bind {addr}: not a private (LAN) address")
    return str(ip)


def _host_allowed(bind, port, hostnames):
    bare = f"[{bind}]" if ":" in bind else bind
    ok = {f"{bare}:{port}"}
    names = [n.strip().lower() for n in hostnames if n.strip()]
    ok.update(f"{n}:{port}" for n in names)
    if port == 80:
        ok.add(bare)
        ok.update(names)
    return ok


class _Server(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True


class Handler(BaseHTTPRequestHandler):
    server_version = "factory-web"
    sys_version = ""
    timeout = REQUEST_TIMEOUT_S

    # ── plumbing ──
    def log_message(self, fmt, *args):
        sys.stderr.write("%s %s\n" % (self.address_string(), fmt % args))

    def _send(self, code, body=b"", ctype="text/html; charset=utf-8", extra=None, cache="no-store"):
        if isinstance(body, str):
            body = body.encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Content-Security-Policy", CSP)
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Referrer-Policy", "no-referrer")
        self.send_header("Cache-Control", cache)
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(body)

    def _error(self, code, msg, extra=None):
        self._send(code, f"{code} {msg}\n", "text/plain; charset=utf-8", extra)

    def _dispatch(self):
        app = self.server.app
        if self.command not in ("GET", "HEAD"):
            return self._error(405, "method not allowed", {"Allow": "GET, HEAD"})
        if not app.sem.acquire(blocking=False):
            return self._error(503, "busy", {"Retry-After": "1"})
        try:
            host = (self.headers.get("Host") or "").strip().lower()
            if host not in app.hosts:
                return self._error(421, "misdirected request")
            parts = urlsplit(self.path)
            try:
                self.query = {k: v[0] for k, v in parse_qs(parts.query, keep_blank_values=True, max_num_fields=20).items()}
            except ValueError:  # too many fields: ignore the query string
                self.query = {}
            self._route(parts.path)
        except (BrokenPipeError, ConnectionResetError, TimeoutError):
            raise
        except Exception:  # noqa: BLE001 — never leak a traceback to the client
            traceback.print_exc()
            try:
                self._send(500, page("Error", "<p class=\"empty\">Something went wrong.</p>", "", refresh=False))
            except OSError:
                pass
        finally:
            app.sem.release()

    do_GET = do_HEAD = do_POST = do_PUT = do_DELETE = do_PATCH = do_OPTIONS = do_TRACE = do_CONNECT = _dispatch

    # ── routing ──
    def _route(self, path):
        app = self.server.app
        if path == "/healthz":
            return self._healthz(app)
        if path.startswith("/static/"):
            return self._static(unquote(path[len("/static/"):]))
        if path.startswith("/api/") and path.endswith(".json"):
            name = path[len("/api/"):-len(".json")]
            route = "/" if name == "home" else f"/{name}"
            view = ROUTES.get(route) if "<" not in route else None
            if view is None:
                return self._error(404, "not found")
            return self._view(view, route, {}, as_json=True)
        view, params, active = ROUTES.get(path), {}, path
        if view is None:
            m = _ID_RE.match(path)
            if m:
                view, params, active = ROUTES.get(f"/{m.group(1)}/<id>"), {"id": m.group(2)}, f"/{m.group(1)}s"
        if view is None:
            return self._error(404, "not found")
        self._view(view, active, params)

    def _view(self, view, active, params, as_json=False):
        app = self.server.app
        with wdb.reader(app.current_db()) as con:
            ctx = Ctx(con, app.tokens, app.usage_fn, app.mem_fn, app.defaults, dt.datetime.now().astimezone(), app.factory_home,
                      getattr(self, "query", {}))
            data = view.data(ctx, **params)
            if as_json:
                body, ctype = json.dumps(data, default=str), "application/json"
            else:
                shell = {**queries.shell_status(con, app.usage_fn() if app.usage_fn else None, app.defaults, mem=app.mem_fn() if app.mem_fn else None), "now": ctx.now}
                body, ctype = page(getattr(view, "TITLE", active), view.render(ctx, data), active, shell=shell), "text/html; charset=utf-8"
        self._send(200, body, ctype)  # reader is closed before the response is written

    def _healthz(self, app):
        try:
            age = max(0, int(time.time() - os.stat(app.current_db()).st_mtime))
        except OSError:
            return self._send(503, "db missing\n", "text/plain; charset=utf-8")
        self._send(200, f"ok\ndb_mtime_age_s={age}\n", "text/plain; charset=utf-8")

    def _static(self, name):
        if not _STATIC_NAME_RE.match(name):
            return self._error(404, "not found")
        f = (STATIC / name).resolve()
        ctype = STATIC_TYPES.get(f.suffix)
        if STATIC.resolve() not in f.parents or ctype is None or not f.is_file():
            return self._error(404, "not found")
        self._send(200, f.read_bytes(), ctype, cache="max-age=3600")


class _App:
    def __init__(self, db_path, bind, port, hostnames, tokens, usage_fn, mem_fn, defaults, factory_home):
        self.db_path, self.tokens, self.usage_fn, self.mem_fn = db_path, tokens, usage_fn, mem_fn
        self.defaults, self.factory_home = defaults, factory_home
        self.hosts = _host_allowed(bind, port, hostnames)
        self.sem = threading.BoundedSemaphore(MAX_CONCURRENT)

    def current_db(self):
        """The DB path for this request: db_path may be a callable, so a layout migration under a running server is
        picked up on the next request (never a stale, renamed file)."""
        return self.db_path() if callable(self.db_path) else self.db_path


def make_server(db_path, bind, port, hostnames=(), tokens=None, usage_fn=None, mem_fn=None, defaults=None,
                factory_home=None):
    """Bound but not serving. port 0 picks a free port; the Host allowlist uses the port actually bound."""
    cls = _Server
    if ":" in bind:
        cls = type("_Server6", (_Server,), {"address_family": socket.AF_INET6})
    httpd = cls((bind, port), Handler)
    httpd.app = _App(db_path, bind, httpd.server_address[1], hostnames, tokens, usage_fn, mem_fn, defaults, factory_home)
    return httpd


def code_signature(root=ROOT):
    """(mtime_ns, size) of every web/**/*.py, scout/**/*.py (bin/factory_cli.py imports it) and bin/factory_cli.py under root:
    what the running process imported."""
    sig = {}
    for f in [*sorted((root / "web").rglob("*.py")), *sorted((root / "scout").rglob("*.py")), root / "bin" / "factory_cli.py"]:
        try:
            st = f.stat()
        except OSError:
            continue
        sig[str(f)] = (st.st_mtime_ns, st.st_size)
    return sig


def import_check(root=ROOT):
    """None if the code under root imports and bin/factory compiles in a fresh interpreter, else a one-line reason."""
    code = "import web.server; compile(open('bin/factory_cli.py').read(), 'bin/factory_cli.py', 'exec')"
    env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")
    try:
        r = subprocess.run([sys.executable, "-c", code], cwd=root, env=env, capture_output=True, text=True, timeout=30)
    except (OSError, subprocess.TimeoutExpired) as e:
        return f"import check could not run: {e}"
    if r.returncode:
        return (r.stderr.strip().splitlines() or ["import failed"])[-1]
    return None


def watch_code(httpd, state, interval, seen, root=ROOT):
    """Daemon loop: when the code changed and still imports, flag a reload and stop the accept loop. A broken
    change logs one error line (stderr -> journal; serve never writes the DB) and keeps the old code; the next
    change is checked again."""
    while True:
        time.sleep(interval)
        now = code_signature(root)
        if now == seen:
            continue
        seen = now
        err = import_check(root)
        if err:
            print(f"factory serve: error: code changed but does not import, still serving the old code: {err}",
                  file=sys.stderr, flush=True)
            continue
        state["reload"] = True
        httpd.shutdown()
        return


def watch_ttl(httpd, state, seconds, sleep=time.sleep):
    """Daemon: after `seconds`, flag the TTL and stop the accept loop (the process then exits by itself)."""
    sleep(seconds)
    state["ttl"] = True
    httpd.shutdown()


def run(db_path, bind=None, port=8765, hostnames=(), usage_fn=None, mem_fn=None, defaults=None, factory_home=None,
        ttl_minutes=0):
    try:
        os.nice(10)
    except OSError:
        pass
    bind = pick_bind(bind)
    index = TokenIndex()
    index.start_background(60)
    try:
        httpd = make_server(db_path, bind, port, hostnames, index.snapshot, usage_fn, mem_fn, defaults, factory_home)
    except OSError as e:
        _refuse(f"cannot listen on {bind}:{port}: {e}")
    for fn in background:
        threading.Thread(target=fn, daemon=True).start()

    def stop(signum, frame):
        threading.Thread(target=httpd.shutdown, daemon=True).start()
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    state = {"reload": False, "ttl": False}
    if ttl_minutes and ttl_minutes > 0:
        threading.Thread(target=watch_ttl, args=(httpd, state, ttl_minutes * 60), daemon=True).start()
    try:
        interval = float(os.environ.get("FACTORY_SERVE_RELOAD_INTERVAL") or RELOAD_INTERVAL_S)
    except ValueError:
        interval = RELOAD_INTERVAL_S
    threading.Thread(target=watch_code, args=(httpd, state, max(interval, 0.1), code_signature()), daemon=True).start()
    shown = f"[{bind}]" if ":" in bind else bind
    print(f"factory serve: http://{shown}:{httpd.server_address[1]}/ (read-only, LAN only, no auth)", flush=True)
    try:
        httpd.serve_forever()
    finally:
        httpd.server_close()   # joins in-flight requests: the current request finishes first
    if state["ttl"]:
        print(f"factory serve: ttl of {ttl_minutes} min reached, exiting", flush=True)
    if state["reload"]:
        for _ in range(MAX_CONCURRENT):   # handler threads are daemons: wait for the in-flight requests to answer
            if not httpd.app.sem.acquire(timeout=REQUEST_TIMEOUT_S):
                break
        print("factory serve: code changed, re-executing", flush=True)
        os.execv(sys.executable, sys.orig_argv)
