#!/usr/bin/env python3
"""Frame-exact scene recorder: seeks a demo scene to i/fps, captures a PNG per frame through demo_cdp.

    python3 ci/demo_record.py --scene hook --out docs/demo/out/hook [--browser PATH] [--max-frames N] [--work DIR]

Records one non-"ui:" scene of docs/demo/storyboard.json to PNG frames in --out. Python 3 stdlib only.
Exit codes: 0 ok, 1 failed, 2 usage error, 3 refused (a work or output dir in RAM-backed /tmp).
"""
import argparse
import json
import signal
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import demo_cdp  # noqa: E402

ROOT = Path(__file__).resolve().parent.parent
DEMO = ROOT / "docs" / "demo"
DEFAULT_WORK = DEMO / "out" / "work"
RAM_DIRS = (Path("/tmp"), Path("/dev/shm"), Path("/var/tmp"))
READY_TIMEOUT = 30


class Refused(Exception):
    pass


class DirSink:
    """Writes frame-00000.png, frame-00001.png ... into a directory."""

    def __init__(self, directory):
        self.dir = Path(directory)
        self.dir.mkdir(parents=True, exist_ok=True)

    def __call__(self, i, png):
        (self.dir / ("frame-%05d.png" % i)).write_bytes(png)


class PipeSink:
    """Writes each PNG to the stdin of a process (ffmpeg -f image2pipe)."""

    def __init__(self, proc):
        self.proc = proc

    def __call__(self, i, png):
        try:
            self.proc.stdin.write(png)
            self.proc.stdin.flush()
        except (BrokenPipeError, OSError) as e:
            raise demo_cdp.CdpError("the encoder closed its input at frame %d (%s); check its stderr." % (i, e))


def wait_for_demo(browser, timeout=READY_TIMEOUT):
    end = time.monotonic() + timeout
    while not browser.evaluate("typeof window.demo === 'object' && window.demo !== null"):
        if time.monotonic() > end:
            raise demo_cdp.CdpTimeout("window.demo never appeared; the scene must load docs/demo/scenes/demo.js.")
        time.sleep(0.05)


def record_scene(browser, url, duration, fps, sink, text_every=30, text_dir=None, scene_id="scene"):
    """Load url, wait for window.demo, then for each of round(duration*fps) frames seek to i/fps and capture.
    Every text_every frames the page text goes to <text_dir>/<scene_id>-<i:05d>.txt (when text_dir is given)."""
    browser.new_page(url)
    wait_for_demo(browser)
    if text_dir is not None:
        Path(text_dir).mkdir(parents=True, exist_ok=True)
    frames = round(duration * fps)
    for i in range(frames):
        browser.evaluate("window.demo.seek(%s)" % json.dumps(i / fps))
        sink(i, browser.screenshot("png"))
        if text_dir is not None and text_every and i % text_every == 0:
            text = browser.evaluate("document.body.innerText") or ""
            (Path(text_dir) / ("%s-%05d.txt" % (scene_id, i))).write_text(text)
    return frames


def under_ram(path):
    p = Path(path).resolve()
    return any(p == r.resolve() or r.resolve() in p.parents for r in RAM_DIRS)


def check_dirs(work, out):
    for what, path in (("--work", work), ("--out", out)):
        if under_ram(path):
            raise Refused("%s %s is under /tmp (RAM-backed tmpfs); frames would eat memory. Use a path on disk, "
                          "e.g. docs/demo/out/." % (what, path))


def load_scene(scene_id, source=None):
    board = json.loads((DEMO / "storyboard.json").read_text())
    for s in board["scenes"]:
        if s["id"] == scene_id:
            if s["source"].startswith("ui:"):
                raise SystemExit(_usage("scene %s is a web UI tour (%s); this recorder does only HTML scenes."
                                        % (scene_id, s["source"])))
            path = Path(source) if source else DEMO / s["source"]
            if not path.is_file():
                raise SystemExit(_usage("scene file %s does not exist yet; pass --source PATH to record another "
                                        "page, e.g. docs/demo/style/frame-1-hook.html." % path))
            return board, s, path.resolve()
    raise SystemExit(_usage("no scene '%s' in storyboard.json (have: %s)"
                            % (scene_id, ", ".join(s["id"] for s in board["scenes"]))))


def _usage(msg):
    print("demo_record: " + msg, file=sys.stderr)
    return 2


def _interrupt(signum, _frame):
    raise KeyboardInterrupt("signal %d" % signum)


def main(argv=None):
    ap = argparse.ArgumentParser(description="Record one demo scene to PNG frames.")
    ap.add_argument("--scene", required=True, help="scene id from docs/demo/storyboard.json")
    ap.add_argument("--out", required=True, help="directory for frame-NNNNN.png")
    ap.add_argument("--browser", help="path to an existing chrome-headless-shell (default: download the pinned one)")
    ap.add_argument("--max-frames", type=int, help="stop after N frames")
    ap.add_argument("--work", default=str(DEFAULT_WORK), help="work dir for the browser profile and text dumps")
    ap.add_argument("--source", help="HTML file to record instead of the scene's own source")
    args = ap.parse_args(argv)
    try:
        check_dirs(args.work, args.out)
    except Refused as e:
        print("demo_record: refused: %s" % e, file=sys.stderr)
        return 3
    board, scene, path = load_scene(args.scene, args.source)
    duration = scene["duration"]
    if args.max_frames is not None:
        duration = min(duration, args.max_frames / board["fps"])
    work = Path(args.work)
    work.mkdir(parents=True, exist_ok=True)
    signal.signal(signal.SIGTERM, _interrupt)
    browser = None
    try:
        binary = demo_cdp.browser_binary(work, args.browser)
        browser = demo_cdp.Browser.launch(binary, board["width"], board["height"], work)
        n = record_scene(browser, path.as_uri(), duration, board["fps"], DirSink(args.out),
                         text_dir=work / "text", scene_id=scene["id"])
    except (demo_cdp.CdpError, demo_cdp.screenshots.Failed, demo_cdp.screenshots.Refused, OSError) as e:
        print("demo_record: failed: %s" % e, file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("demo_record: interrupted", file=sys.stderr)
        return 130
    finally:
        if browser is not None:
            browser.close()
        for proc in list(demo_cdp.screenshots.CHILDREN):  # interrupted between spawn and assignment: no orphans
            demo_cdp.stop(proc)
    print("recorded %d frames of %s to %s" % (n, scene["id"], args.out))
    return 0


if __name__ == "__main__":
    sys.exit(main())
