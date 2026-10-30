#!/usr/bin/env python3
"""Web UI tour recorder (storyboard beat 6): seeds the demo home, serves it, records each page of the tour scene.

    python3 ci/demo_tour.py --out DIR [--browser PATH] [--max-frames N] [--pages /,/goals] [--work DIR]
                            [--scene ID] [--scale S]

A page of the storyboard is a URL path or an object {"path", "duration", "hold", "scroll_to", "fade_in", "push_in",
"label"}; see docs/demo/README.md. Each page is checked for HTTP 200 first (a failing page stops the run), then
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
# px: caption bar geometry. The bar sits >= 70 px above the bottom edge: the README player draws its controls over the
# bottom 60 px of the video.
CAPTION_BOTTOM, CAPTION_MAX, CAPTION_PAD, CAPTION_GAP = 72, 160, 22, 14
# The page lives in a box above the caption band, so the bar never covers page content.
CAPTION_RESERVE = CAPTION_BOTTOM + CAPTION_MAX + 2 * CAPTION_PAD + CAPTION_GAP
CAPTION_CSS = ("position:fixed;left:120px;right:120px;bottom:%dpx;z-index:2147483646;background:rgba(5,8,16,0.7);"
               "border-radius:18px;padding:%dpx 40px;font:46px/1.25 'Inter','DejaVu Sans',sans-serif;color:#fff;"
               "text-align:center;max-height:%dpx;overflow:hidden" % (CAPTION_BOTTOM, CAPTION_PAD, CAPTION_MAX))
PAGE_CSS = ("position:fixed;left:0;top:0;right:0;bottom:%dpx;overflow:hidden;background:#fff" % CAPTION_RESERVE)
ZOOM_CSS = "zoom:%s;transform-origin:50%% 0" % ZOOM
# the page label sits at the top of the caption band, so it covers no page content either
LABEL_CSS = ("position:fixed;left:48px;top:%dpx;z-index:2147483646;background:rgba(5,8,16,0.7);color:#fff;"
             "border-radius:14px;padding:10px 26px;font:bold 36px 'Inter','DejaVu Sans',sans-serif" % (1080 - CAPTION_RESERVE + 8))
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


def scroll_target(scroll_height, height=1080, scroll_to=None):
    """Where the page scrolls to: `scroll_to` px (capped at the end of the page), default min(SCROLL_MAX, end)."""
    end = max(0, scroll_height - height)
    if scroll_to is None:
        return min(SCROLL_MAX, end)
    return min(end, max(0, scroll_to))


def scroll_y(t, seconds, scroll_height, height=1080, hold=0.0, scroll_to=None):
    """scrollY at time t of a page that lasts `seconds`: 0 until `hold`, then eased to the target at the end."""
    span = seconds - hold
    u = (t - hold) / span if span > 0 else 1.0
    return scroll_target(scroll_height, height, scroll_to) * ease(u)


def fade(t, seconds, edge=FADE, fade_in=True):
    """Page visibility at time t: 0 at both page edges, 1 after `edge` seconds, and back to 0 at the end.
    fade_in=False starts fully visible on frame 0 (a hard open) and only fades out."""
    if t >= seconds or (fade_in and t <= 0):
        return 0.0
    return min(1.0, (t / edge) if fade_in else 1.0, (seconds - t) / edge)


def push_scale(t, seconds, push_in=1.0):
    """Scale of the page at time t: 1.0 at t=0, eased to `push_in` at the end."""
    return 1.0 + (push_in - 1.0) * ease(t / seconds)


def shots(pages, labels=None, duration=4):
    """Normalise a storyboard page list (path strings or objects) to one dict per page with every option set."""
    labels = labels or []
    out = []
    for i, p in enumerate(pages):
        o = {"path": p} if isinstance(p, str) else dict(p)
        if not isinstance(o.get("path"), str) or not o["path"].startswith("/"):
            raise PageError("page %d needs a URL path starting with /; give a path or an object with a 'path'." % i)
        label = o.get("label", labels[i] if i < len(labels) else "")
        shot = {"path": o["path"], "label": label, "duration": o.get("duration", duration), "hold": o.get("hold", 0),
                "scroll_to": o.get("scroll_to"), "fade_in": o.get("fade_in", True), "push_in": o.get("push_in", 1.0)}
        if not shot["duration"] > 0:
            raise PageError("page %s: duration must be > 0 seconds." % shot["path"])
        if not 0 <= shot["hold"] < shot["duration"]:
            raise PageError("page %s: hold must be >= 0 and shorter than its duration." % shot["path"])
        if not isinstance(shot["scroll_to"], (int, float, str, type(None))) or isinstance(shot["scroll_to"], bool):
            raise PageError("page %s: scroll_to must be a pixel count or a CSS selector." % shot["path"])
        if not isinstance(shot["fade_in"], bool):
            raise PageError("page %s: fade_in must be true or false." % shot["path"])
        if not 1.0 <= shot["push_in"] <= 2.0:
            raise PageError("page %s: push_in must be between 1.0 and 2.0 (end scale)." % shot["path"])
        out.append(shot)
    return out


def caption_at(captions, t):
    for c in captions:
        if c["at"] <= t < c["until"]:
            return c["text"]
    return ""


def load_tour(scene_id=None):
    """The storyboard and its tour scene: the one named `scene_id`, else the first 'ui:' scene."""
    board = json.loads((DEMO / "storyboard.json").read_text())
    ui = [s for s in board["scenes"] if s.get("source", "").startswith("ui:")]
    if scene_id is not None:
        scene = next((s for s in ui if s["id"] == scene_id), None)
        if scene is None:
            raise SystemExit(_usage("storyboard.json has no 'ui:' scene '%s' (have: %s)."
                                    % (scene_id, ", ".join(s["id"] for s in ui) or "none")))
    elif ui:
        scene = ui[0]
    else:
        raise SystemExit(_usage("storyboard.json has no 'ui:' tour scene."))
    return board, scene


def check_pages(base_url, pages):
    """Fail on the first page that does not answer HTTP 200."""
    for path in (p if isinstance(p, str) else p["path"] for p in pages):
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


def selector_top(browser, selector):
    """scrollTop that puts the first element matching `selector` at the top of the page box."""
    y = browser.evaluate(
        "(function(s){var e=document.querySelector(s);if(!e)return null;var p=document.getElementById('demo-page');"
        "return Math.round(e.getBoundingClientRect().top-p.getBoundingClientRect().top+p.scrollTop)})(%s)"
        % json.dumps(selector))
    if y is None:
        raise PageError("scroll_to selector %r matched nothing on the page; fix it in docs/demo/storyboard.json." % selector)
    return y


def freeze(browser):
    """Pause the page's virtual time. The web UI reloads itself every 60 s (meta refresh), and a 2x capture spends
    longer than that on one page; with time paused no timer fires, while screenshots still render."""
    browser.send("Emulation.setVirtualTimePolicy", {"policy": "pause"})


def record_tour(browser, base_url, pages, seconds_per_page, fps, sink, text_dir, labels=None, captions=(),
                frames_per_page=None):
    """Record every page of `pages` (URL paths or page objects, see shots()) to sink(i, png). Returns the frame count."""
    plan = shots(pages, labels, seconds_per_page)
    check_pages(base_url, [s["path"] for s in plan])
    if text_dir is not None:
        Path(text_dir).mkdir(parents=True, exist_ok=True)
        for old in Path(text_dir).glob("tour-*.txt"):
            old.unlink()  # dumps of an earlier run must not leak into this one
    n = 0
    for shot in plan:
        seconds = shot["duration"]
        per_page = round(seconds * fps)
        if frames_per_page is not None:
            per_page = min(per_page, frames_per_page)
        browser.new_page(base_url + shot["path"])
        browser.evaluate(_inject_js(shot["label"]))
        freeze(browser)
        height = browser.evaluate("document.getElementById('demo-page').clientHeight") or page_height()
        scroll_height = browser.evaluate("document.getElementById('demo-page').scrollHeight") or height
        scroll_to = shot["scroll_to"]
        if isinstance(scroll_to, str):
            scroll_to = selector_top(browser, scroll_to)
        for k in range(per_page):
            t = k / fps
            y = scroll_y(t, seconds, scroll_height, height, shot["hold"], scroll_to)
            cap = caption_at(captions, n / fps)
            browser.evaluate("document.getElementById('demo-page').scrollTop=%s;document.getElementById('demo-fade').style.opacity=%s;"
                             "document.getElementById('demo-zoom').style.transform='scale(%s)';"
                             "document.getElementById('demo-cap').textContent=%s;"
                             "document.getElementById('demo-cap').style.display=%s"
                             % (json.dumps(y), json.dumps(1 - fade(t, seconds, fade_in=shot["fade_in"])),
                                round(push_scale(t, seconds, shot["push_in"]), 5), json.dumps(cap),
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
            "var z=document.createElement('div');z.id='demo-zoom';z.style.cssText=%s;"
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
    ap.add_argument("--scene", help="id of the 'ui:' scene to record (default: the first one)")
    ap.add_argument("--scale", type=float, help="capture scale (deviceScaleFactor, 1-2); default: storyboard 'scale' or 1")
    ap.add_argument("--now", help="demo seed reference time (ISO); default: the current UTC hour")
    args = ap.parse_args(argv)
    try:
        demo_record.check_dirs(args.work, args.out)
        demo_record.check_dirs(args.demo_dir, args.demo_dir)
    except demo_record.Refused as e:
        print("demo_tour: refused: %s" % e, file=sys.stderr)
        return 3
    try:
        board, scene = load_tour(args.scene)
    except (OSError, ValueError) as e:
        print("demo_tour: cannot read docs/demo/storyboard.json: %s" % e, file=sys.stderr)
        return 1
    try:
        plan = shots(scene["pages"], scene.get("labels"), scene["page_duration"])
    except PageError as e:
        return _usage(str(e))
    if args.pages:
        known = {s["path"]: s for s in plan}
        paths = [p for p in args.pages.split(",") if p]
        if not paths or any(not p.startswith("/") for p in paths):
            return _usage("--pages must be a comma list of URL paths starting with /.")
        plan = [known.get(p, {"path": p}) for p in paths]
    pages = [{k: v for k, v in s.items() if v is not None} for s in plan]
    scale = args.scale if args.scale is not None else board.get("scale", 1)
    if not 1 <= scale <= 2:
        return _usage("--scale must be between 1 and 2 (2 = 3840x2160 capture of the 1920x1080 layout).")
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
        browser.scale = scale
        n = record_tour(browser, "http://127.0.0.1:%d" % port, pages, scene["page_duration"], board["fps"],
                        demo_record.DirSink(args.out), work / "text", None, scene.get("captions", []), per_page)
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
