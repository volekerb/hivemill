"""Visual QA for the web UI (G-019 B7): captures every page in both themes at desktop and phone width and checks it.

Per capture: (a) no horizontal overflow, (b) on the overview at 1280 px no blank area above BLANK_MAX of the content
column, (c) WCAG AA contrast of every token pair in app.css (text >= AA_TEXT, non-text >= AA_NON_TEXT), (d) server-side
render time per route (median of RENDER_RUNS GETs) below RENDER_MS_MAX.

Everything runs on a demo DB seeded by ci/demo_seed.py into a temp FACTORY_HOME/HOME (tempfile.mkdtemp); the live
home is never read or written, the server is a child process on 127.0.0.1 and the browser the pinned
chrome-headless-shell, driven over CDP by ci/demo_cdp.py. No server change is needed: the theme is chosen with
Emulation.setEmulatedMedia prefers-color-scheme (the page follows the OS when no theme is stored, see theme.js).

Exit codes: 0 PASS, 1 FAIL, 2 setup error (no browser, seed failed, live home refused). The thresholds below are the
goal's numbers; no option changes them.
"""
import argparse
import datetime as dt
import json
import os
import pwd
import re
import shutil
import signal
import statistics
import sys
import tempfile
import time
import urllib.request
import sqlite3
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "ci"))
sys.path.insert(0, str(ROOT))
import demo_cdp  # noqa: E402  (the only gateway to ci/screenshots.py)

BLANK_MAX = 0.35        # share of the overview content column that may be plain background
AA_TEXT = 4.5           # WCAG AA, text pairs
AA_NON_TEXT = 3.0       # WCAG AA, non-text pairs
RENDER_MS_MAX = 200     # median server-side render per route, demo DB
RENDER_RUNS = 5
CELL = 8                # px side of a blank-check cell
SIDEBAR_PX = 232        # sidebar column at >= 1024 px (app.css); the blank check starts to its right
SIZES = ((1280, 860), (390, 844))
THEMES = ("light", "dark")
STATIC_ROUTES = ("/", "/goals", "/tasks", "/agents", "/resources", "/throughput", "/quality", "/changelog", "/tokens")
OVERVIEW = "/"

# One entry per element whose right edge is past the viewport; in_scroll: an ancestor scrolls horizontally.
JS_OVERFLOW = """(function () {
  function sel(e) { return e.tagName.toLowerCase() + (e.id ? '#' + e.id : '') +
    (typeof e.className === 'string' && e.className.trim() ? '.' + e.className.trim().split(/\\s+/).join('.') : ''); }
  function scrolls(e) { for (var p = e.parentElement; p; p = p.parentElement) {
    var o = getComputedStyle(p).overflowX; if (o === 'auto' || o === 'scroll') return true; } return false; }
  var iw = window.innerWidth, out = [];
  [].forEach.call(document.body.querySelectorAll('*'), function (e) {
    var r = e.getBoundingClientRect();
    if (r.width > 0 && r.right > iw + 1) out.push({sel: sel(e), right: Math.round(r.right), in_scroll: scrolls(e)});
  });
  var cs = getComputedStyle(document.documentElement);
  return {scroll_width: document.documentElement.scrollWidth, inner_width: iw, elements: out.slice(0, 200),
          bg: [cs.getPropertyValue('--surface-0').trim(), cs.getPropertyValue('--surface-1').trim()]};
})()"""


class SetupError(Exception):
    """Something needed before the first capture is missing or refused; exit code 2."""


# ───────────────────────────── checks (pure functions) ─────────────────────────────

def overflow_problems(inner_width, scroll_width, elements):
    """Problem strings: the page scrolls sideways, or an element reaches past the viewport outside a scroller."""
    out = []
    if scroll_width > inner_width:
        out.append("page scrollWidth %d > viewport %d" % (scroll_width, inner_width))
    for e in elements:
        if not e.get("in_scroll") and e["right"] > inner_width + 1:
            out.append("%s right edge %d > viewport %d" % (e["sel"], e["right"], inner_width))
    return out


def hex_rgb(value):
    m = re.fullmatch(r"#([0-9a-fA-F]{6})", value.strip())
    if not m:
        raise ValueError("not a #rrggbb colour: %r" % value)
    n = int(m.group(1), 16)
    return (n >> 16) & 255, (n >> 8) & 255, n & 255


def blank_share(width, channels, rows, backgrounds, x0, cell=CELL):
    """Share of cell x cell blocks (x >= x0) that are one uniform colour equal to a background. Rows are the
    decoded PNG scanlines (ci/screenshots.decode_png); partial edge blocks are ignored."""
    bgs = set(backgrounds)
    blank = total = 0
    for y0 in range(0, len(rows) - cell + 1, cell):
        block = rows[y0:y0 + cell]
        for x in range(x0, width - cell + 1, cell):
            first = tuple(block[0][x * channels:x * channels + 3])
            total += 1
            if first not in bgs:
                continue
            if all(tuple(r[(x + i) * channels:(x + i) * channels + 3]) == first for r in block for i in range(cell)):
                blank += 1
    return blank / total if total else 0.0


def blank_problem(share):
    return ["blank share %.1f%% > %.0f%%" % (share * 100, BLANK_MAX * 100)] if share > BLANK_MAX else []


def contrast(a, b):
    from tests import test_web_design as twd
    return twd.contrast(a, b)


def pair_problems(pairs, minimum, theme):
    """pairs: (name, fg, bg) hex triples. A problem string for each below `minimum`."""
    out = []
    for name, fg, bg in pairs:
        ratio = contrast(fg, bg)
        if ratio < minimum:
            out.append("AA %s %s: %s on %s = %.2f < %.1f" % (theme, name, fg, bg, ratio, minimum))
    return out


def aa_problems():
    """Token pairs of tokens v2 from web/static/app.css, with the pair lists of tests/test_web_design.py."""
    from tests import test_web_design as twd
    helper = twd.Contrast
    out = []
    for theme, t in twd.THEMES.items():
        out += pair_problems(helper.text_pairs(None, t), AA_TEXT, theme)
        out += pair_problems(helper.non_text_pairs(None, t), AA_NON_TEXT, theme)
    return out


def speed_problem(route, times_ms):
    med = statistics.median(times_ms)
    return ["render %s median %.0f ms >= %d ms" % (route, med, RENDER_MS_MAX)] if med >= RENDER_MS_MAX else []


def summary(rows, aa, speed, problems):
    """(text, problem count). The last line is exactly 'visual-qa: PASS' or 'visual-qa: FAIL (<n> problems)'."""
    lines = ["%-26s %-5s %5s  %-8s %-8s %s" % ("route", "theme", "width", "overflow", "blank", "ms")]
    for r in rows:
        lines.append("%-26s %-5s %5d  %-8s %-8s %s" % (
            r["route"], r["theme"], r["width"], "FAIL" if r["overflow"] else "ok",
            "-" if r["blank"] is None else "%.1f%%" % (r["blank"] * 100),
            "%.0f" % speed[r["route"]] if r["route"] in speed else "-"))
    lines.append("captures: %d" % len(rows))
    lines.append("aa: %s" % ("%d failing pair(s)" % len(aa) if aa else "all pairs pass (text >= %.1f, non-text >= %.1f)"
                             % (AA_TEXT, AA_NON_TEXT)))
    lines += ["  " + p for p in aa]
    lines += ["problem: " + p for p in problems if not p.startswith("AA ")]
    n = len(problems)
    lines.append("visual-qa: PASS" if n == 0 else "visual-qa: FAIL (%d problems)" % n)
    return "\n".join(lines), n


def exit_code(n_problems):
    return 0 if n_problems == 0 else 1


# ───────────────────────────── setup, capture ─────────────────────────────

def live_homes():
    """Protected paths: the account factory dir (passwd entry), this checkout and $FACTORY_HOME when set."""
    roots = [Path(pwd.getpwuid(os.getuid()).pw_dir) / "factory", ROOT]
    if os.environ.get("FACTORY_HOME"):
        roots.append(Path(os.environ["FACTORY_HOME"]))
    return [r.resolve() for r in roots]


def check_home(home, lives):
    h = Path(home).resolve()
    for live in lives:
        if h == live or live in h.parents or h in live.parents:
            raise SetupError("refusing to use %s as the QA home: it is or contains the live home %s." % (h, live))
    return h


def now_hour():
    return dt.datetime.now(dt.timezone.utc).replace(minute=0, second=0, microsecond=0).isoformat()


def start_demo(tmp):
    """Seed tmp/demo and serve it; returns (port, server process). Failures become SetupError."""
    demo = tmp / "demo"
    try:
        demo_cdp.seed_demo(demo, now_hour())
        return demo, *demo_cdp.serve_demo(demo)
    except (demo_cdp.screenshots.Failed, demo_cdp.screenshots.Refused) as e:
        raise SetupError("demo seed or server failed: %s" % e)


def launch_browser(tmp, browser_path):
    try:
        binary = demo_cdp.browser_binary(tmp / "browser-dl", browser_path)
        return demo_cdp.Browser.launch(binary, *SIZES[0], work=tmp / "browser")
    except (demo_cdp.screenshots.Failed, demo_cdp.screenshots.Refused, OSError) as e:
        raise SetupError("no usable browser: %s Pass --browser PATH to use an installed chrome-headless-shell." % e)


def detail_ids(demo):
    """(a goal id, a task id) from the seeded DB, read-only."""
    db = Path(demo) / "db" / "factory.db"
    try:
        con = sqlite3.connect("file:%s?mode=ro" % db, uri=True)
        try:
            g = con.execute("SELECT id FROM goals ORDER BY id LIMIT 1").fetchone()
            t = con.execute("SELECT id FROM tasks ORDER BY id LIMIT 1").fetchone()
        finally:
            con.close()
    except sqlite3.Error as e:
        raise SetupError("cannot read goal/task ids from the demo DB %s: %s" % (db, e))
    if not g or not t:
        raise SetupError("the demo DB has no goal or no task to open a detail page for.")
    return g[0], t[0]


def get_ms(url):
    t0 = time.perf_counter()
    with urllib.request.urlopen(url, timeout=30) as r:
        r.read()
    return (time.perf_counter() - t0) * 1000


def capture_page(b, base, route, theme, size, png_path):
    """Load route in the theme at size; returns (JS_OVERFLOW result, png_path)."""
    w, h = size
    b.send("Emulation.setDeviceMetricsOverride", {"width": w, "height": h, "deviceScaleFactor": 1, "mobile": False})
    b.send("Emulation.setEmulatedMedia", {"features": [{"name": "prefers-color-scheme", "value": theme}]})
    b.send("Page.navigate", {"url": base + route})
    end = time.monotonic() + demo_cdp.TIMEOUT
    time.sleep(0.1)
    while b.evaluate("document.readyState") != "complete":
        if time.monotonic() > end:
            raise demo_cdp.CdpTimeout("page %s did not finish loading." % route)
        time.sleep(0.05)
    data = b.evaluate(JS_OVERFLOW)
    Path(png_path).write_bytes(b.screenshot())
    return data


def run_checks(opts, tmp):
    """All captures and checks; returns (rows, speed, problems, aa). Tests replace the setup functions."""
    if opts.routes is not None and not [r for r in opts.routes.split(",") if r]:
        raise SetupError("--routes is empty; give a comma list such as /,/goals.")
    demo, port, _server = start_demo(tmp)
    base = "http://127.0.0.1:%d" % port
    gid, tid = detail_ids(demo)
    routes = ["/", "/goals", "/goal/%s" % gid, "/tasks", "/task/%s" % tid] + list(STATIC_ROUTES[3:])
    if opts.routes:
        want = [r for r in opts.routes.split(",") if r]
        unknown = [r for r in want if r not in routes and not r.startswith(("/goal/", "/task/"))]
        if unknown:
            raise SetupError("unknown route(s) %s; known: %s" % (",".join(unknown), ",".join(routes)))
        routes = want
    out = Path(opts.out) if opts.out else tmp / "shots"
    out.mkdir(parents=True, exist_ok=True)
    rows, problems = [], []
    speed = {}
    for r in routes:
        times = [get_ms(base + r) for _ in range(RENDER_RUNS)]
        speed[r] = statistics.median(times)
        problems += speed_problem(r, times)
    b = launch_browser(tmp, opts.browser)
    try:
        b.new_page("about:blank")
        for r in routes:
            for theme in THEMES:
                for size in SIZES:
                    name = "%s-%s-%d.png" % (re.sub(r"[^a-z0-9]+", "-", r.lower()).strip("-") or "home", theme, size[0])
                    data = capture_page(b, base, r, theme, size, out / name)
                    ov = overflow_problems(data["inner_width"], data["scroll_width"], data["elements"])
                    share = None
                    if r == OVERVIEW and size[0] == SIZES[0][0]:
                        share = png_blank(out / name, data["bg"])
                        ov_b = blank_problem(share)
                    else:
                        ov_b = []
                    rows.append({"route": r, "theme": theme, "width": size[0], "overflow": ov, "blank": share,
                                 "png": str(out / name)})
                    problems += ["%s %s %d: %s" % (r, theme, size[0], p) for p in ov + ov_b]
    finally:
        b.close()
    aa = aa_problems()
    return rows, speed, problems + aa, aa


def png_blank(path, bg_tokens):
    width, ch, rows = demo_cdp.screenshots.decode_png(path, max_rows=100000)
    return blank_share(width, ch, rows, [hex_rgb(v) for v in bg_tokens], SIDEBAR_PX)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--routes", help="comma list of routes to check, e.g. /,/goals (default: all 11)")
    ap.add_argument("--out", help="directory for the PNGs (default: a temp dir that is removed)")
    ap.add_argument("--browser", help="path of a chrome-headless-shell binary (default: the pinned download)")
    ap.add_argument("--json", help="write the machine-readable result to this file")
    opts = ap.parse_args(argv)
    tmp = None
    old = {s: signal.signal(s, signal.default_int_handler) for s in (signal.SIGINT,)}
    try:
        try:
            made = Path(tempfile.mkdtemp(prefix="factory-visual-qa-"))
            try:
                tmp = check_home(made, live_homes())
            except SetupError:
                os.rmdir(made)  # empty by construction; never a recursive delete of a refused path
                raise
            rows, speed, problems, aa = run_checks(opts, tmp)
        except SetupError as e:
            print("setup error: %s" % e, file=sys.stderr)
            return 2
        except (demo_cdp.CdpError, OSError, ValueError, demo_cdp.screenshots.Refused, demo_cdp.screenshots.Failed) as e:
            print("setup error: browser or server failed: %s" % e, file=sys.stderr)
            return 2
        except KeyboardInterrupt:
            print("interrupted", file=sys.stderr)
            return 130
        text, n = summary(rows, aa, speed, problems)
        print(text)
        if opts.json:
            Path(opts.json).write_text(json.dumps({"rows": rows, "render_ms": speed, "aa": aa, "problems": problems,
                                                   "result": "PASS" if n == 0 else "FAIL"}, indent=1))
        return exit_code(n)
    finally:
        for s in (signal.SIGINT, signal.SIGTERM):
            signal.signal(s, signal.SIG_IGN)
        try:
            cleanup(tmp)
        finally:
            for s, h in old.items():
                signal.signal(s, h)


def cleanup(tmp):
    """Stop every child the run started (server, browser) and remove the temp dir."""
    for proc in list(demo_cdp.screenshots.CHILDREN):
        demo_cdp.stop(proc)
    if tmp is not None:
        shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
