#!/usr/bin/env python3
"""Render the G-019 static mockups to PNGs, and check them.

  python3 docs/design/g-019/capture.py                       # 6 pages x light/dark x 1280x860 and 390x844 -> shots/
  python3 docs/design/g-019/capture.py --blank-report overview   # blank area of the 1280 shots (reads shots/, no browser)
  python3 docs/design/g-019/capture.py --check-width         # every *-390.png is 390 px wide and scrollWidth == 390
  options: --chrome PATH (use this headless shell instead of downloading the pinned one), --pages a,b

The pinned headless Chrome for Testing is fetched with the download and checksum helpers of ci/screenshots.py, imported
read-only (that script is never run or modified). If the import fails, this script exits 2 and says so. The dark variant
is a temp copy of each page with data-theme="dark" on <html> (light: data-theme="light"); the mockups contain no JS and
no ?theme parameter. The width check adds a one-line script to its own temp copy only. Temp files live in one
tempfile.mkdtemp() directory that is always removed. Exit codes: 0 ok, 1 check failed, 2 usage or environment problem.
"""
import argparse
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
SHOTS = HERE / "shots"
PAGES = ["overview", "goals", "goal", "agents", "tasks", "quality"]
THEMES = ["light", "dark"]
SIZES = {1280: 860, 390: 844}
BLANK_LIMIT = 35.0
CELL = 8
SIDEBAR_PX, HEADER_PX = 232, 64  # shell geometry at 1280 wide, for the content-column figure
# page and section surfaces (--surface-0, -1, -2 of mock.css) per theme, as RGB
SURFACES = {"light": {(0xF2, 0xF2, 0xF7), (0xFF, 0xFF, 0xFF)}, "dark": {(0x00, 0x00, 0x00), (0x1C, 0x1C, 0x1E), (0x2C, 0x2C, 0x2E)}}
PROBE = ('<script>document.documentElement.setAttribute("data-sw", String(document.documentElement.scrollWidth));'
         'document.documentElement.setAttribute("data-iw", String(window.innerWidth));</script>')


class Problem(Exception):
    """A usage or environment problem (exit 2)."""


def load_helpers():
    """Import ci/screenshots.py read-only; exit 2 with a clear message when its helpers are gone or changed."""
    sys.path.insert(0, str(ROOT / "ci"))
    try:
        import screenshots
        needed = ("fetch_browser", "decode_png", "png_width", "check_fonts", "clean_env")
        missing = [n for n in needed if not hasattr(screenshots, n)]
    except (ImportError, SyntaxError) as e:
        raise Problem("cannot import ci/screenshots.py (%s). Its download helpers changed or the file is not "
                      "importable; fix capture.py to match, or pass --chrome PATH to skip the download." % e)
    finally:
        sys.path.pop(0)
    if missing:
        raise Problem("ci/screenshots.py no longer provides %s. Update capture.py to the new helper names, or pass "
                      "--chrome PATH." % ", ".join(missing))
    return screenshots


def run(cmd, env, timeout=120):
    """Run one child in its own process group; killed as a group on timeout. Returns (returncode, stdout, stderr)."""
    proc = subprocess.Popen(cmd, env=env, start_new_session=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            text=True)
    try:
        out, err = proc.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        os.killpg(proc.pid, signal.SIGKILL)
        proc.communicate()
        raise Problem("%s timed out after %d s." % (Path(cmd[0]).name, timeout))
    return proc.returncode, out, err


def variant(tmp, page, theme, probe=False):
    """Write a temp copy of page.html with data-theme set (and the width probe when asked); return its path."""
    html = (HERE / ("%s.html" % page)).read_text(encoding="utf-8")
    html, n = re.subn(r"<html lang=\"en\"[^>]*>", '<html lang="en" data-theme="%s">' % theme, html, count=1)
    if n != 1:
        raise Problem("%s.html has no <html lang=\"en\"> opening tag." % page)
    if probe:
        html = html.replace("</body>", PROBE + "</body>")
    dest = tmp / ("%s-%s%s.html" % (page, theme, "-probe" if probe else ""))
    dest.write_text(html, encoding="utf-8")
    return dest


def chrome_args(binary, width, height, profile, extra):
    return [str(binary), "--headless", "--disable-gpu", "--no-sandbox", "--hide-scrollbars",
            "--window-size=%d,%d" % (width, height), "--user-data-dir=" + str(profile)] + extra


def browser_env(tmp):
    (tmp / "browser-home").mkdir(exist_ok=True)
    return {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(tmp / "browser-home"), "LANG": "C.UTF-8"}


def get_browser(helpers, tmp, chrome):
    helpers.check_fonts()
    if chrome:
        if not Path(chrome).is_file():
            raise Problem("--chrome %s is not a file." % chrome)
        return Path(chrome)
    return helpers.fetch_browser(tmp)


def capture(helpers, binary, tmp, pages):
    env = browser_env(tmp)
    SHOTS.mkdir(exist_ok=True)
    for page in pages:
        shutil.copy(HERE / "mock.css", tmp / "mock.css")
        for theme in THEMES:
            src = variant(tmp, page, theme)
            for width, height in SIZES.items():
                dest = SHOTS / ("%s-%s-%d.png" % (page, theme, width))
                profile = tmp / "profile"
                shutil.rmtree(profile, ignore_errors=True)
                rc, out, err = run(chrome_args(binary, width, height, profile,
                                               ["--screenshot=" + str(dest), src.as_uri()]), env)
                if rc != 0 or not dest.is_file():
                    raise Problem("capture of %s failed (exit %d): %s" % (dest.name, rc, err[-300:]))
                got = helpers.png_width(dest)
                if got != width:
                    raise Problem("%s is %d px wide, expected %d." % (dest.name, got, width))
                print("wrote %s (%d bytes)" % (dest.relative_to(ROOT), dest.stat().st_size))


def blank_cells(helpers, path, theme):
    """(grid, cols, rows): grid[y][x] is True for an 8x8 px cell of one uniform colour equal to a page/section surface."""
    width, ch, rows = helpers.decode_png(path, max_rows=SIZES[1280])
    surfaces, grid = SURFACES[theme], []
    for cy in range(len(rows) // CELL):
        band = rows[cy * CELL:(cy + 1) * CELL]
        line = []
        for cx in range(width // CELL):
            lo, hi = cx * CELL * ch, (cx + 1) * CELL * ch
            first = bytes(band[0][lo:lo + 3])
            line.append(tuple(first) in surfaces and all(bytes(r[lo:hi]) == bytes(r[lo:lo + ch]) * CELL for r in band)
                        and all(bytes(r[lo:lo + 3]) == first for r in band))
        grid.append(line)
    return grid, width // CELL, len(grid)


def largest_blank_rectangle(grid, cols, rows):
    """Cell count of the largest axis-aligned rectangle made only of blank cells (histogram method)."""
    heights, best = [0] * cols, 0
    for y in range(rows):
        for x in range(cols):
            heights[x] = heights[x] + 1 if grid[y][x] else 0
        stack = []
        for x in range(cols + 1):
            cur, start = (heights[x] if x < cols else 0), x
            while stack and stack[-1][1] >= cur:
                sx, sh = stack.pop()
                best = max(best, sh * (x - sx))
                start = sx
            stack.append((start, cur))
    return best


def blank_report(helpers, page):
    """The gate is the largest blank rectangle: no empty block bigger than BLANK_LIMIT % of the viewport (the wording of
    success criterion 3). The share of all uniform cells and the share inside the content column are printed as well."""
    bad = 0
    for theme in THEMES:
        path = SHOTS / ("%s-%s-1280.png" % (page, theme))
        if not path.is_file():
            raise Problem("%s is missing; run capture.py without options first." % path.relative_to(ROOT))
        grid, cols, rows = blank_cells(helpers, path, theme)
        total = cols * rows
        rect = 100.0 * largest_blank_rectangle(grid, cols, rows) / total
        every = 100.0 * sum(map(sum, grid)) / total
        side = SIDEBAR_PX // CELL  # first cell column right of the sidebar (232 px = 29 cells), below the first 8-cell header band
        inner = [row[side:] for row in grid[HEADER_PX // CELL:]]
        content = 100.0 * sum(map(sum, inner)) / (len(inner) * len(inner[0]))
        ok = rect <= BLANK_LIMIT
        bad += not ok
        print("%s-%s-1280: blank %s 35%% (largest empty block %.1f%% of the viewport; uniform cells overall %.1f%%, "
              "in the content column %.1f%%)" % (page, theme, "<=" if ok else ">", rect, every, content))
    return 1 if bad else 0


def check_width(helpers, binary, tmp, pages):
    env = browser_env(tmp)
    shutil.copy(HERE / "mock.css", tmp / "mock.css")
    bad = 0
    for page in pages:
        for theme in THEMES:
            src = variant(tmp, page, theme, probe=True)
            profile = tmp / "profile"
            shutil.rmtree(profile, ignore_errors=True)
            rc, out, err = run(chrome_args(binary, 390, 844, profile, ["--dump-dom", "--virtual-time-budget=2000",
                                                                       src.as_uri()]), env)
            sw, iw = re.search(r'data-sw="(\d+)"', out), re.search(r'data-iw="(\d+)"', out)
            if rc != 0 or not sw or not iw:
                raise Problem("width probe of %s-%s failed (exit %d): %s" % (page, theme, rc, err[-300:]))
            png = SHOTS / ("%s-%s-390.png" % (page, theme))
            pw = helpers.png_width(png) if png.is_file() else None
            ok = int(sw.group(1)) == 390 and int(iw.group(1)) == 390 and pw == 390
            bad += not ok
            print("%s-%s-390: scrollWidth %s, innerWidth %s, png width %s -> %s"
                  % (page, theme, sw.group(1), iw.group(1), pw, "ok" if ok else "FAIL"))
    return 1 if bad else 0


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--blank-report", metavar="PAGE", help="blank-area share of PAGE-light/dark-1280.png")
    ap.add_argument("--check-width", action="store_true", help="every *-390.png is 390 px wide, scrollWidth == 390")
    ap.add_argument("--chrome", help="path of a headless Chrome to use instead of downloading the pinned one")
    ap.add_argument("--pages", help="comma-separated pages (default: all six)")
    args = ap.parse_args(argv)
    pages = args.pages.split(",") if args.pages else PAGES
    unknown = [p for p in pages if p not in PAGES]
    if unknown or (args.blank_report and args.blank_report not in PAGES):
        print("unknown page %s; pages are %s" % (unknown or args.blank_report, ", ".join(PAGES)), file=sys.stderr)
        return 2
    tmp = Path(tempfile.mkdtemp(prefix="g019-capture-"))
    errors = (Problem, OSError)  # plus the helpers' own Refused and Failed once they are imported
    try:
        helpers = load_helpers()
        errors += (helpers.Refused, helpers.Failed)
        if args.blank_report:
            return blank_report(helpers, args.blank_report)
        binary = get_browser(helpers, tmp, args.chrome)
        if args.check_width:
            return check_width(helpers, binary, tmp, pages)
        capture(helpers, binary, tmp, pages)
        return 0
    except errors as e:  # each carries its own next step
        print("capture.py: %s" % e, file=sys.stderr)
        return 2
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
