#!/usr/bin/env python3
"""Render the demo style frames docs/demo/style/*.html to PNG at 1920x1080 with the pinned chrome-headless-shell.

  python3 ci/demo_stills.py [--browser PATH] [--out docs/demo/style]

Without --browser it downloads the pinned shell (size and sha256 checked) via ci/screenshots.py into a temp dir that
is always removed. With --browser PATH it uses that already-downloaded chrome-headless-shell and downloads nothing.
Each <name>.html becomes <name>.png in --out, which must be docs/demo/style or inside it. Fonts must be installed
(fontconfig, fonts-dejavu-core). Exit codes: 0 ok, 1 failure, 2 usage error, 3 refused (--out outside docs/demo/style,
no fonts, bad --browser, no frames). Stdlib only.
"""
import argparse
import os
import signal
import struct
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import screenshots as shots  # noqa: E402  (shared pinned-browser helpers; this script never edits it)

ROOT = shots.ROOT
STYLE = ROOT / "docs" / "demo" / "style"
WIDTH, HEIGHT = 1920, 1080
MAX_TOTAL = 2097152


def check_out(out):
    """Resolved --out; Refused unless it is docs/demo/style or a directory inside it."""
    target = Path(out).expanduser().resolve()
    allowed = STYLE.resolve()
    if target != allowed and allowed not in target.parents:
        raise shots.Refused("--out %s is outside %s. Use the default or a directory under it." % (target, allowed))
    return target


def browser_args(binary, dest, page, profile):
    return [str(binary), "--headless", "--disable-gpu", "--no-sandbox", "--hide-scrollbars",
            "--force-device-scale-factor=1", "--window-size=%d,%d" % (WIDTH, HEIGHT),
            "--virtual-time-budget=2000", "--user-data-dir=" + str(profile),
            "--screenshot=" + str(dest), page.resolve().as_uri()]


def png_size(path):
    head = Path(path).read_bytes()[:24]
    if head[:8] != b"\x89PNG\r\n\x1a\n":
        raise shots.Failed("%s is not a PNG." % path)
    return struct.unpack(">II", head[16:24])


def render(binary, tmp, frames):
    """Render every frame into tmp/png and return the PNG paths. Nothing is written to the output directory."""
    stage = tmp / "png"
    stage.mkdir(exist_ok=True)
    env = {"PATH": os.environ.get("PATH", ""), "HOME": str(tmp / "browser-home"), "LANG": "C.UTF-8"}
    (tmp / "browser-home").mkdir(exist_ok=True)
    rendered = []
    for page in frames:
        dest = stage / (page.stem + ".png")
        rc, _o, err = shots.run_child(browser_args(binary, dest, page, tmp / "profile"), 120, env=env)
        if rc != 0 or not dest.is_file():
            raise shots.Failed("browser failed on %s (exit %d): %s" % (page.name, rc, err.strip()[-300:]))
        if png_size(dest) != (WIDTH, HEIGHT):
            raise shots.Failed("%s rendered %s, expected %dx%d." % (page.name, png_size(dest), WIDTH, HEIGHT))
        rendered.append(dest)
    return rendered


def install(rendered, out, max_total=MAX_TOTAL):
    """Check the total size cap first, then copy the PNGs into out. A failed cap leaves out untouched."""
    total = sum(p.stat().st_size for p in rendered)
    print("summary: %d images, %d bytes total" % (len(rendered), total))
    if total > max_total:
        raise shots.Failed("PNGs total %d bytes, cap is %d. Simplify the frames; %s was not changed." % (total, max_total, out))
    out.mkdir(parents=True, exist_ok=True)
    for p in rendered:
        final = out / p.name
        final.write_bytes(p.read_bytes())
        print("wrote %s (%d bytes)" % (final, final.stat().st_size))


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--browser", help="path to an already-downloaded chrome-headless-shell (skips the download)")
    ap.add_argument("--max-total", type=int, default=MAX_TOTAL, help="cap on the total PNG bytes (default 2 MB)")
    ap.add_argument("--out", default=str(STYLE), help="output directory, must be docs/demo/style or inside it")
    args = ap.parse_args(argv)
    try:
        out = check_out(args.out)
        frames = sorted(STYLE.glob("*.html"))
        if not frames:
            raise shots.Refused("no frames: %s has no *.html files." % STYLE)
        browser = None
        if args.browser:
            browser = Path(args.browser).expanduser()
            if not browser.is_file():
                raise shots.Refused("--browser %s is not a file." % browser)
        shots.check_fonts()
    except shots.Refused as e:
        print("refused: %s" % e, file=sys.stderr)
        return 3
    tmp = None
    old = {s: signal.signal(s, shots._interrupt) for s in (signal.SIGINT, signal.SIGTERM)}
    code = 0
    try:
        tmp = Path(tempfile.mkdtemp(prefix="factory-demo-stills-")).resolve()
        binary = browser or shots.fetch_browser(tmp)
        install(render(binary, tmp, frames), out, args.max_total)
    except shots.Refused as e:
        print("refused: %s" % e, file=sys.stderr)
        code = 3
    except shots.Failed as e:
        print("demo_stills failed: %s" % e, file=sys.stderr)
        code = 1
    except KeyboardInterrupt:
        print("interrupted", file=sys.stderr)
        code = 130
    finally:
        for s in (signal.SIGINT, signal.SIGTERM):
            signal.signal(s, signal.SIG_IGN)
        try:
            shots.cleanup(tmp)
        finally:
            for s, h in old.items():
                signal.signal(s, h)
    return code


if __name__ == "__main__":
    sys.exit(main())
