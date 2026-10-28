#!/usr/bin/env python3
"""Web UI tour recorder (storyboard beat 6): seeds the demo home, serves it, records each page of the tour scene.

    python3 ci/demo_tour.py --out DIR [--browser PATH] [--max-frames N] [--pages /,/goals] [--work DIR]

Pages are addressed by URL path only. Each page is checked for HTTP 200 first (a failing page stops the run), then
recorded with a smooth scroll, a fade through the background colour and the shared caption bar. --max-frames N
limits the whole tour to N frames, split evenly over the pages. Python 3 stdlib only.
Exit codes: 0 ok, 1 failed, 2 usage error, 3 refused (a work or output dir in RAM-backed /tmp).
"""
import argparse
import json
import math
import os
import shutil
import signal
import sys
import urllib.error
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import demo_cdp  # noqa: E402
import demo_record  # noqa: E402

DEMO = demo_record.DEMO
SCROLL_MAX = 600  # px
FADE = 0.4  # seconds of fade at each end of a page
BG = "#0b1020"  # theme.css --bg
TEXT_EVERY = 30
MARKER = ".demo-tour"
# ci/demo_seed.py refuses any directory inside the checkout or ~/factory, so the demo home cannot live under
# docs/demo/out/work; it goes to the user cache dir (on disk, not /tmp) and is rebuilt on every run.
DEFAULT_DEMO = Path(os.environ.get("XDG_CACHE_HOME") or Path.home() / ".cache") / "factory-demo-tour" / "demo"
HTTP_TIMEOUT = 15
ZOOM = 1.5  # page zoom: the 1170px column fills the 1920px frame and body text is ~21px
CAPTION_BOTTOM, CAPTION_MAX, CAPTION_PAD, CAPTION_GAP = 18, 160, 22, 14  # px: caption bar geometry
# The page lives in a box above the caption band, so the bar never covers page content.
CAPTION_RESERVE = CAPTION_BOTTOM + CAPTION_MAX + 2 * CAPTION_PAD + CAPTION_GAP
CAPTION_CSS = ("position:fixed;left:120px;right:120px;bottom:%dpx;z-index:2147483646;background:rgba(5,8,16,0.7);"
               "border-radius:18px;padding:%dpx 40px;font:46px/1.25 'DejaVu Sans',sans-serif;color:#fff;"
               "text-align:center;max-height:%dpx;overflow:hidden" % (CAPTION_BOTTOM, CAPTION_PAD, CAPTION_MAX))
PAGE_CSS = ("position:fixed;left:0;top:0;right:0;bottom:%dpx;overflow:hidden;background:#fff" % CAPTION_RESERVE)
ZOOM_CSS = "zoom:%s" % ZOOM
# the page label sits at the top of the caption band, so it covers no page content either
LABEL_CSS = ("position:fixed;left:48px;top:%dpx;z-index:2147483646;background:rgba(5,8,16,0.7);color:#fff;"
             "border-radius:14px;padding:10px 26px;font:bold 36px 'DejaVu Sans',sans-serif" % (1080 - CAPTION_RESERVE + 8))
FADE_CSS = "position:fixed;inset:0;z-index:2147483647;pointer-events:none;background:" + BG


class PageError(Exception):
    pass


def ease(u):
    """Ease-in-out from 0 to 1 over u in [0, 1] (clamped)."""
    u = min(1.0, max(0.0, u))
    return 0.5 - 0.5 * math.cos(math.pi * u)


def page_height(height=1080):
    """Height of the box the page is shown in: the frame minus the caption band."""
    return height - CAPTION_RESERVE


def caption_top(height=1080):
    """Top edge of a caption bar of maximum height."""
    return height - CAPTION_BOTTOM - CAPTION_MAX - 2 * CAPTION_PAD


def scroll_target(scroll_height, height=1080):
    return max(0, min(SCROLL_MAX, scroll_height - height))


def scroll_y(t, seconds, scroll_height, height=1080):
    """scrollY at time t of a page that lasts `seconds`: 0 at t=0, the target at the end, monotonic."""
    return scroll_target(scroll_height, height) * ease(t / seconds)


def fade(t, seconds, edge=FADE):
    """Page visibility at time t: 0 at both page edges, 1 after `edge` seconds, and back to 0 at the end."""
    if t <= 0 or t >= seconds:
        return 0.0
    return min(1.0, t / edge, (seconds - t) / edge)


def caption_at(captions, t):
    for c in captions:
        if c["at"] <= t < c["until"]:
            return c["text"]
    return ""


def load_tour():
    board = json.loads((DEMO / "storyboard.json").read_text())
    scene = next((s for s in board["scenes"] if s.get("source", "").startswith("ui:")), None)
    if scene is None:
        raise SystemExit(_usage("storyboard.json has no 'ui:' tour scene."))
    return board, scene


def check_pages(base_url, pages):
    """Fail on the first page that does not answer HTTP 200."""
    for path in pages:
        try:
            with urllib.request.urlopen(base_url + path, timeout=HTTP_TIMEOUT) as r:
                status = r.status
        except urllib.error.HTTPError as e:
            status = e.code
        except (urllib.error.URLError, OSError) as e:
            raise PageError("page %s could not be fetched (%s); is the demo server up?" % (path, e))
        if status != 200:
            raise PageError("page %s answered HTTP %d, not 200; the UI changed or the route is gone. Fix the page "
                            "list in docs/demo/storyboard.json." % (path, status))


def record_tour(browser, base_url, pages, seconds_per_page, fps, sink, text_dir, labels=None, captions=(),
                frames_per_page=None):
    """Record every page of `pages` (URL paths under base_url) to sink(i, png). Returns the number of frames."""
    check_pages(base_url, pages)
    labels = labels or [""] * len(pages)
    per_page = round(seconds_per_page * fps)
    if frames_per_page is not None:
        per_page = min(per_page, frames_per_page)
    if text_dir is not None:
        Path(text_dir).mkdir(parents=True, exist_ok=True)
        for old in Path(text_dir).glob("tour-*.txt"):
            old.unlink()  # dumps of an earlier run must not leak into this one
    n = 0
    for path, label in zip(pages, labels):
        browser.new_page(base_url + path)
        browser.evaluate(_inject_js(label))
        height = browser.evaluate("document.getElementById('demo-page').clientHeight") or page_height()
        scroll_height = browser.evaluate("document.getElementById('demo-page').scrollHeight") or height
        for k in range(per_page):
            t = k / fps
            y = scroll_y(t, seconds_per_page, scroll_height, height)
            cap = caption_at(captions, n / fps)
            browser.evaluate("document.getElementById('demo-page').scrollTop=%s;document.getElementById('demo-fade').style.opacity=%s;"
                             "document.getElementById('demo-cap').textContent=%s;"
                             "document.getElementById('demo-cap').style.display=%s"
                             % (json.dumps(y), json.dumps(1 - fade(t, seconds_per_page)), json.dumps(cap),
                                json.dumps("block" if cap else "none")))
            sink(n, browser.screenshot("png"))
            if text_dir is not None and (k == 0 or n % TEXT_EVERY == 0):  # each page is dumped once at least
                text = browser.evaluate("document.body.innerText") or ""
                (Path(text_dir) / ("tour-%05d.txt" % n)).write_text(text)
            n += 1
    return n


def _inject_js(label):
    return ("(function(){function mk(id,css,txt){var d=document.createElement('div');d.id=id;d.style.cssText=css;"
            "if(txt)d.textContent=txt;document.body.appendChild(d);}"
            "var pg=document.createElement('div');pg.id='demo-page';pg.style.cssText=%s;"
            "var z=document.createElement('div');z.style.cssText=%s;"
            "while(document.body.firstChild)z.appendChild(document.body.firstChild);"
            "pg.appendChild(z);document.body.appendChild(pg);"
            "mk('demo-label',%s,%s);mk('demo-cap',%s);mk('demo-fade',%s);"
            "document.getElementById('demo-label').style.display=%s})()"
            % (json.dumps(PAGE_CSS), json.dumps(ZOOM_CSS), json.dumps(LABEL_CSS), json.dumps(label), json.dumps(CAPTION_CSS), json.dumps(FADE_CSS),
               json.dumps("block" if label else "none")))


def prepare_demo_dir(demo):
    """Empty the demo dir for a fresh seed, but only a dir this recorder made (marker file) or an empty one."""
    demo = Path(demo)
    if demo.exists() and any(demo.iterdir()):
        if not (demo / MARKER).is_file():
            raise demo_record.Refused("--demo-dir %s is not empty and was not created by demo_tour; refusing to "
                                      "delete it. Pass a new or empty directory." % demo)
        shutil.rmtree(demo)
    demo.mkdir(parents=True, exist_ok=True)
    (demo / MARKER).write_text("made by ci/demo_tour.py; safe to delete\n")


def _usage(msg):
    print("demo_tour: " + msg, file=sys.stderr)
    return 2


def main(argv=None):
    ap = argparse.ArgumentParser(description="Record the web UI tour to PNG frames.")
    ap.add_argument("--out", required=True, help="directory for frame-NNNNN.png")
    ap.add_argument("--browser", help="path to an existing chrome-headless-shell (default: download the pinned one)")
    ap.add_argument("--max-frames", type=int, help="stop after N frames in total, split evenly over the pages (rounded down; N must be >= the page count)")
    ap.add_argument("--pages", help="comma list of URL paths (default: the tour pages of storyboard.json)")
    ap.add_argument("--work", default=str(demo_record.DEFAULT_WORK), help="work dir: demo home, profile, text dumps")
    ap.add_argument("--demo-dir", default=str(DEFAULT_DEMO), help="demo home to seed and serve (outside the checkout)")
    ap.add_argument("--now", help="demo seed reference time (ISO); default: the current UTC hour")
    args = ap.parse_args(argv)
    try:
        demo_record.check_dirs(args.work, args.out)
        demo_record.check_dirs(args.demo_dir, args.demo_dir)
    except demo_record.Refused as e:
        print("demo_tour: refused: %s" % e, file=sys.stderr)
        return 3
    try:
        board, scene = load_tour()
    except (OSError, ValueError) as e:
        print("demo_tour: cannot read docs/demo/storyboard.json: %s" % e, file=sys.stderr)
        return 1
    pages = [p for p in args.pages.split(",") if p] if args.pages else scene["pages"]
    if not pages or any(not p.startswith("/") for p in pages):
        return _usage("--pages must be a comma list of URL paths starting with /.")
    known = dict(zip(scene["pages"], scene.get("labels", [])))
    labels = [known.get(p, "") for p in pages]
    per_page = None
    if args.max_frames is not None:
        if args.max_frames < len(pages):
            return _usage("--max-frames must be at least the number of pages (%d): every page gets a frame." % len(pages))
        per_page = max(1, args.max_frames // len(pages))
    work = Path(args.work)
    work.mkdir(parents=True, exist_ok=True)
    signal.signal(signal.SIGTERM, demo_record._interrupt)
    browser = server = None
    try:
        now = args.now or demo_cdp.screenshots.current_hour()
        demo = Path(args.demo_dir)
        prepare_demo_dir(demo)
        demo_cdp.seed_demo(demo, now)
        port, server = demo_cdp.serve_demo(demo)
        binary = demo_cdp.browser_binary(work, args.browser)
        browser = demo_cdp.Browser.launch(binary, board["width"], board["height"], work)
        n = record_tour(browser, "http://127.0.0.1:%d" % port, pages, scene["page_duration"], board["fps"],
                        demo_record.DirSink(args.out), work / "text", labels, scene.get("captions", []), per_page)
    except demo_record.Refused as e:
        print("demo_tour: refused: %s" % e, file=sys.stderr)
        return 3
    except PageError as e:
        print("demo_tour: failed: %s" % e, file=sys.stderr)
        return 1
    except (demo_cdp.CdpError, demo_cdp.screenshots.Failed, demo_cdp.screenshots.Refused, OSError) as e:
        print("demo_tour: failed: %s" % e, file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("demo_tour: interrupted", file=sys.stderr)
        return 130
    finally:
        if browser is not None:
            browser.close()
        if server is not None:
            demo_cdp.stop(server)
        for proc in list(demo_cdp.screenshots.CHILDREN):  # seed/serve interrupted before returning: no orphans
            demo_cdp.stop(proc)
    print("recorded %d frames of the tour to %s" % (n, args.out))
    return 0


if __name__ == "__main__":
    sys.exit(main())
