#!/usr/bin/env python3
"""Regenerate the web UI screenshots in docs/images/ (light and dark) from a fictional demo home.

  python3 ci/screenshots.py [--out docs/images] [--pages home,goals,...] [--now ISO]

Run it from a factory checkout. Everything happens in one temp dir that is always removed, with every child process
(server, browser) terminated, on success, error, Ctrl-C and SIGTERM. In order it: downloads the pinned Chrome for
Testing headless shell (the only network access), checks its size and sha256 BEFORE unzipping, runs `ldd` on it, seeds a
demo home with ci/demo_seed.py, serves it on 127.0.0.1 with its own HOME and FACTORY_HOME (the live home is never
touched), scrub-scans each page's HTML (set SCRUB_TERMS_FILE for owner terms), captures each page light and dark at
1280 CSS px wide and 2x device pixels (2560 px PNGs), crops each to its content, checks that text was rendered, that each
theme matches its file name, that the image is not mostly empty and the PNG sizes, then builds docs/images/hero.png
(home light | home dark) and the 1280x640 docs/images/social-preview.png. It first checks
that fonts are installed (fontconfig and fonts-dejavu-core; the browser draws no text without them). It never installs
anything and never escalates privileges.
See docs/images/README.md. Exit codes: 0 ok, 1 failure, 2 usage error, 3 refused (not a factory checkout, --out outside
docs/images, no fonts, checksum mismatch, no rendered text, theme mismatch, mostly empty image, size cap). Stdlib only.
"""
import argparse
import base64
import collections
import datetime as dt
import hashlib
import html
import itertools
import os
import re
import shutil
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import zipfile
import zlib
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
IMAGES = ROOT / "docs" / "images"
VERSION = "131.0.6778.204"
URL = ("https://storage.googleapis.com/chrome-for-testing-public/%s/linux64/chrome-headless-shell-linux64.zip"
       % VERSION)
SIZE = 113084320
SHA256 = "afaac86e302c4874245991a3d509d529c50ecdd447affff0cdc2b08520906c32"
BIN_REL = Path("chrome-headless-shell-linux64") / "chrome-headless-shell"
LDD = "ldd"
PAGES = {"home": "/", "goals": "/goals", "tasks": "/tasks", "agents": "/agents", "throughput": "/throughput",
         "quality": "/quality"}
# theme.js follows the OS when no choice is stored, so the browser's preferred colour scheme picks the theme. Blink's
# PreferredColorScheme enum is {kDark, kLight}: 0 is DARK and 1 is LIGHT (the first owner run had these swapped).
THEMES = {"light": "1", "dark": "0"}
FC_LIST = "fc-list"
FC_MATCH = "fc-match"
FONT_HELP = ("No usable fonts: install fontconfig and fonts-dejavu-core (e.g. sudo apt install fontconfig "
             "fonts-dejavu-core), then re-run.")
CHECK_ROWS = 400  # the text and theme checks only look at the top 400 px of each image
# Calibrated on the owner's fonts re-run (12 real pages): 797-1532 distinct colours in the top 400 px, because
# anti-aliasing blends glyph and background; a text-less layout (flat cards, borders) has a few dozen at most.
MIN_COLOURS = 200
# Mean luminance (0-255, top 400 px): the real pages measured 25-29 vs 247-249 (a gap of about 220); 40 is well under.
MIN_LUMA_GAP = 40
WIDTH = 1280  # CSS px
SCALE = 2  # device pixel ratio: a PNG is WIDTH * SCALE px wide
DPR_FLAG = "--force-device-scale-factor=%d" % SCALE
HMAX = 1600  # CSS px: the capture window height; a page taller than this is truncated here (see crop_to_content)
MARGIN_CSS = 32  # CSS px of page background kept below the last content row
MIN_HEIGHT_CSS = 400  # a cropped image is never shorter than this
CROP_TOL = 8  # per channel: a pixel within this of the page background counts as background
# The app shell (web/static/app.css, >= SIDEBAR_BREAKPOINT_CSS_PX) has a sticky full-height .sidebar, so every row has
# non-background pixels there. Background colour, crop and blank rows are measured on the main column only, right of the
# sidebar and its 1 px hairline; the saved PNG keeps the full width. A test checks this against app.css.
SIDEBAR_CSS_PX = 232
SIDEBAR_BREAKPOINT_CSS_PX = 1024
BLANK_PIXEL_FRAC = 0.99  # a row is blank when this share of its (main column) pixels is background
BLANK_LIMIT_PCT = 35  # abort when more than this share of an image's rows are blank
# Text and line spacing leave single blank rows between lines; only a run of at least this many consecutive blank rows
# (24 CSS px at 2x) is empty space that counts.
BLANK_RUN_MIN = 48
WARN_PNG_BYTES = 307200  # target per PNG: a bigger one only prints WARN (director M6655)
MAX_PNG_BYTES = 716800  # hard cap per PNG (700 KB, director M6655)
MAX_TOTAL_BYTES = 8388608  # hard cap for all PNGs together (8 MB, director M6655)
HERO_DIVIDER_PX = 2  # width of the neutral divider between the light and the dark half of hero.png
HERO_DIVIDER_RGB = (128, 128, 128)
SOCIAL_W, SOCIAL_H = 1280, 640  # GitHub social preview size
SOCIAL_BAND = 160  # px: the top band with wordmark and tagline; the hero fills the other SOCIAL_H - SOCIAL_BAND rows
SOCIAL_MAX_BYTES = 1048576  # GitHub's limit for the social preview
SOCIAL_TAGLINE = "A team of Claude Code agents with roles, independent review and a merge gate"
WORDMARK = "wordmark-light.svg"
WORDMARK_VIEWBOX = "0 0 275 80"  # the file's viewBox is 360 wide but the ink ends near x=268
CHILDREN = []  # Popen objects to terminate in cleanup


class Refused(Exception):
    pass


class Failed(Exception):
    pass


# ───────────────────────────── guards ─────────────────────────────

def check_checkout(cwd=None):
    """Refused unless this script sits in a factory checkout and the working directory is inside it."""
    cwd = Path(cwd or os.getcwd()).resolve()
    if not ((ROOT / "bin" / "factory").is_file() and (ROOT / "ci" / "demo_seed.py").is_file()
            and (ROOT / "web" / "server.py").is_file()):
        raise Refused("%s is not a factory checkout (bin/factory, ci/demo_seed.py or web/server.py missing)." % ROOT)
    if cwd != ROOT and ROOT not in cwd.parents:
        raise Refused("run this from the factory checkout (cd %s), not from %s." % (ROOT, cwd))


def check_out(out):
    """Resolved --out; Refused unless it is docs/images or a directory inside it."""
    target = Path(out).expanduser().resolve()
    allowed = IMAGES.resolve()
    if target != allowed and allowed not in target.parents:
        raise Refused("--out %s is outside %s. Use the default or a directory under it." % (target, allowed))
    return target


def check_fonts():
    """Refused (before any download) unless fontconfig lists fonts and the default sans-serif names an existing file."""
    for tool in (FC_LIST, FC_MATCH):
        if shutil.which(tool) is None:
            raise Refused(FONT_HELP + " (%s is not on PATH)" % tool)
    try:
        listed = subprocess.run([FC_LIST], capture_output=True, text=True, timeout=60).stdout
        matched = subprocess.run([FC_MATCH, "-f", "%{file}", "sans-serif"], capture_output=True, text=True,
                                 timeout=60).stdout.strip()
    except (OSError, subprocess.TimeoutExpired) as e:
        raise Refused(FONT_HELP + " (fontconfig failed: %s)" % e)
    if not [ln for ln in listed.splitlines() if ln.strip()]:
        raise Refused(FONT_HELP + " (fc-list printed no fonts)")
    if not matched or not Path(matched).is_file():
        raise Refused(FONT_HELP + " (fc-match sans-serif gave %r, which is not a file)" % matched)


# ───────────────────────────── processes ─────────────────────────────

def spawn(cmd, **kw):
    """Popen in its own session (so the whole group can be stopped), tracked for cleanup. Argument lists only."""
    proc = subprocess.Popen(cmd, start_new_session=True, **kw)
    CHILDREN.append(proc)
    return proc


GRACE = 5  # seconds a group gets after SIGTERM before SIGKILL


def group_alive(pgid):
    """True while any process (a zombie included) is left in process group pgid. The kernel does not reuse a pid
    that is still a live group id, so a True here is a group of ours (children are started with start_new_session)."""
    try:
        os.killpg(pgid, 0)
    except (ProcessLookupError, PermissionError):
        return False
    return True


def stop(proc):
    """Stop a child's whole process group: SIGTERM, wait up to GRACE seconds for the group to empty, then SIGKILL.
    The group is swept even when the leader has already exited or been reaped: a server's helpers keep the group
    id alive, and killpg on a group with no members is a harmless ProcessLookupError."""
    for sig in (signal.SIGTERM, signal.SIGKILL):
        try:
            os.killpg(proc.pid, sig)
        except (ProcessLookupError, PermissionError):
            break
        end = time.monotonic() + GRACE
        while time.monotonic() < end:
            if proc.poll() is not None and not group_alive(proc.pid):
                return
            time.sleep(0.05)
    proc.poll()


def run_child(cmd, timeout, **kw):
    """Run one child to completion, tracked; returns (returncode, stdout, stderr)."""
    proc = spawn(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, **kw)
    try:
        out, err = proc.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        stop(proc)
        raise Failed("%s timed out after %ds." % (Path(cmd[0]).name, timeout))
    return proc.returncode, out, err


def cleanup(tmp):
    n = 0
    for proc in CHILDREN:
        if proc.poll() is None:
            n += 1
        stop(proc)
    CHILDREN.clear()
    if tmp is not None:
        shutil.rmtree(tmp, ignore_errors=True)
    print("cleanup: removed %s, terminated %d child process(es)" % (tmp, n))


def _interrupt(signum, _frame):
    raise KeyboardInterrupt("signal %d" % signum)


# ───────────────────────────── browser download ─────────────────────────────

def sha256_of(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def download(url, dest):
    """Fetch url into dest; stops reading past SIZE bytes."""
    try:
        with urllib.request.urlopen(url, timeout=60) as r, open(dest, "wb") as f:
            got = 0
            for chunk in iter(lambda: r.read(1 << 20), b""):
                got += len(chunk)
                if got > SIZE:
                    break
                f.write(chunk)
    except (urllib.error.URLError, OSError) as e:
        raise Failed("download of %s failed: %s. Check the network and retry." % (url, e))


def verify(zpath):
    """Refused unless zpath has exactly the pinned size and sha256."""
    size = Path(zpath).stat().st_size
    if size != SIZE:
        raise Refused("size mismatch for %s: expected %d bytes, got %d. Nothing was unzipped; rerun to download again."
                      % (zpath, SIZE, size))
    got = sha256_of(zpath)
    if got != SHA256:
        raise Refused("sha256 mismatch for %s: expected %s, got %s. Nothing was unzipped; rerun to download again."
                      % (zpath, SHA256, got))


def unzip(zpath, dest):
    dest = Path(dest).resolve()
    with zipfile.ZipFile(zpath) as z:
        for name in z.namelist():
            target = (dest / name).resolve()
            if target != dest and dest not in target.parents:
                raise Failed("zip member %r escapes the target directory; aborting." % name)
        z.extractall(str(dest))
    binary = dest / BIN_REL
    if not binary.is_file():
        raise Failed("%s not found in the zip. The pinned archive layout changed; stop and report." % BIN_REL)
    binary.chmod(binary.stat().st_mode | 0o111)
    return binary


def check_libs(binary):
    """Failed with the exact 'not found' lines when ldd reports missing libraries."""
    rc, out, err = run_child([LDD, str(binary)], 60)
    missing = [ln for ln in (out + err).splitlines() if "not found" in ln]
    if missing:
        raise Failed("the browser needs system libraries that are missing (install them yourself; this script never "
                     "installs anything):\n" + "\n".join(missing))
    if rc != 0:
        raise Failed("ldd exited %d, cannot tell whether libraries are missing: %s" % (rc, (out + err).strip()))


def fetch_browser(tmp):
    zpath = tmp / "chrome-headless-shell-linux64.zip"
    print("downloading %s" % URL)
    download(URL, zpath)
    verify(zpath)
    binary = unzip(zpath, tmp / "chrome")
    check_libs(binary)
    return binary


# ───────────────────────────── demo home, server, scrub ─────────────────────────────

def current_hour():
    return dt.datetime.now(dt.timezone.utc).replace(minute=0, second=0, microsecond=0).isoformat()


def clean_env(home, factory_home=None):
    """A fresh environment for a child: only HOME, PATH, LANG and (when given) FACTORY_HOME. Nothing inherited, so no
    FACTORY_*, HERDR_* or token variable of the caller reaches the server or the seed."""
    env = {"HOME": str(home), "PATH": os.environ.get("PATH", "/usr/bin:/bin"), "LANG": "C.UTF-8"}
    if factory_home is not None:
        env["FACTORY_HOME"] = str(factory_home)
    return env


def seed(demo, now):
    tmp = demo.parent
    (tmp / "seed-home").mkdir(exist_ok=True)
    env = clean_env(tmp / "seed-home")  # no FACTORY_HOME: the seed refuses a --home equal to it
    rc, out, err = run_child([sys.executable, str(ROOT / "ci" / "demo_seed.py"), "--home", str(demo), "--now", now],
                             300, env=env)
    if rc != 0:
        raise Failed("ci/demo_seed.py exited %d:\n%s%s" % (rc, out, err))


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def serve(demo):
    port = free_port()
    env = clean_env(demo / "home", demo)
    proc = spawn([str(ROOT / "bin" / "factory"), "serve", "--bind", "127.0.0.1", "--port", str(port)], env=env,
                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    end = time.monotonic() + 30
    while time.monotonic() < end:
        if proc.poll() is not None:
            raise Failed("factory serve exited early (code %s). Run it by hand to see why." % proc.returncode)
        try:
            with urllib.request.urlopen("http://127.0.0.1:%d/" % port, timeout=2) as r:
                if r.status == 200:
                    return port
        except (urllib.error.URLError, OSError):
            pass
        time.sleep(0.3)
    raise Failed("factory serve did not answer 200 on / within 30s (port %d)." % port)


def scrub(tmp, port, pages):
    """Fetch each page's HTML and run ci/scrub_scan.py --path on it (it reads SCRUB_TERMS_FILE itself)."""
    if not os.environ.get("SCRUB_TERMS_FILE"):
        print("warning: SCRUB_TERMS_FILE not set; the scrub scan runs generic patterns only", file=sys.stderr)
    for name in pages:
        html = tmp / ("%s.html" % name)
        try:
            with urllib.request.urlopen("http://127.0.0.1:%d%s" % (port, PAGES[name]), timeout=15) as r:
                html.write_bytes(r.read())
        except (urllib.error.URLError, OSError) as e:
            raise Failed("could not fetch page %s: %s" % (name, e))
        rc, out, err = run_child([sys.executable, str(ROOT / "ci" / "scrub_scan.py"), "--path", str(html)], 120)
        print("scrub %s: exit %d" % (name, rc))
        if rc != 0:
            raise Failed("scrub scan hit on page %s (exit %d):\n%s%s" % (name, rc, out, err))


# ───────────────────────────── capture ─────────────────────────────

def png_width(path):
    head = Path(path).read_bytes()[:24]
    if head[:8] != b"\x89PNG\r\n\x1a\n" or head[12:16] != b"IHDR":
        raise Failed("%s is not a PNG." % path)
    return struct.unpack(">I", head[16:20])[0]


def browser_args(binary, theme, dest, url, height, profile, dpr=False):
    """The headless browser command line for one page in one theme (light/dark -> THEMES). dpr adds DPR_FLAG."""
    return [str(binary), "--headless", "--disable-gpu", "--no-sandbox", "--hide-scrollbars"]\
        + ([DPR_FLAG] if dpr else []) \
        + ["--window-size=%d,%d" % (WIDTH, height), "--user-data-dir=" + str(profile),
           "--blink-settings=preferredColorScheme=" + THEMES[theme], "--screenshot=" + str(dest), url]


def capture(binary, tmp, port, pages, height, dpr=False):
    shots = tmp / ("shots-%d" % height)
    shots.mkdir()
    env = {"PATH": os.environ.get("PATH", ""), "HOME": str(tmp / "browser-home"), "LANG": "C.UTF-8"}
    (tmp / "browser-home").mkdir(exist_ok=True)
    for name in pages:
        for theme in THEMES:
            dest = shots / ("%s-%s.png" % (name, theme))
            profile = tmp / "profile"
            shutil.rmtree(profile, ignore_errors=True)
            cmd = browser_args(binary, theme, dest, "http://127.0.0.1:%d%s" % (port, PAGES[name]), height, profile, dpr)
            rc, out, err = run_child(cmd, 120, env=env)  # one browser process at a time
            if rc != 0 or not dest.is_file():
                raise Failed("capture of %s-%s failed (exit %d): %s" % (name, theme, rc, err[-500:]))
            want = WIDTH * (SCALE if dpr else 1)
            if png_width(dest) != want:
                raise Failed("%s is %d px wide, expected %d." % (dest.name, png_width(dest), want))
    return shots


def check_sizes(files):
    """Print WARN for each PNG over WARN_PNG_BYTES. Refused (every file's size listed) when one is over MAX_PNG_BYTES
    or the total is over MAX_TOTAL_BYTES."""
    sizes = [(f, f.stat().st_size) for f in files]
    for f, n in sizes:
        if n > WARN_PNG_BYTES:
            print("WARN %s: %d bytes is over the %d target" % (f.name, n, WARN_PNG_BYTES))
    total = sum(n for _f, n in sizes)
    big = [f.name for f, n in sizes if n > MAX_PNG_BYTES]
    if big or total > MAX_TOTAL_BYTES:
        listing = "\n".join("  %s: %d bytes" % (f.name, n) for f, n in sizes)
        raise Refused("size cap: %s (per-PNG cap %d bytes, total %d of cap %d). This run's images were not written.\n%s"
                      % (("over the per-PNG cap: " + ", ".join(big)) if big else "total over the cap",
                         MAX_PNG_BYTES, total, MAX_TOTAL_BYTES, listing))


def check_themes_differ(files, pages):
    for name in pages:
        light, dark = [f for f in files if f.name in ("%s-light.png" % name, "%s-dark.png" % name)]
        if light.read_bytes() == dark.read_bytes():
            raise Failed("%s: light and dark images are identical; the theme was not applied." % name)


def decode_png(path, max_rows=CHECK_ROWS):
    """(width, channels, rows) of the first max_rows rows of an 8-bit RGB/RGBA non-interlaced PNG, stdlib only.
    Refused for any other format."""
    data = Path(path).read_bytes()
    if data[:8] != b"\x89PNG\r\n\x1a\n":
        raise Refused("%s is not a PNG." % path)
    pos, idat, head = 8, [], None
    while pos + 8 <= len(data):
        n, kind = struct.unpack(">I4s", data[pos:pos + 8])
        body = data[pos + 8:pos + 8 + n]
        pos += 12 + n
        if kind == b"IHDR":
            head = struct.unpack(">IIBBBBB", body)
        elif kind == b"IDAT":
            idat.append(body)
        elif kind == b"IEND":
            break
    if head is None or not idat:
        raise Refused("%s has no image data." % path)
    width, height, depth, ctype, _comp, _filt, interlace = head
    if depth != 8 or ctype not in (2, 6) or interlace != 0:
        raise Refused("%s is not an 8-bit RGB/RGBA non-interlaced PNG (depth %d, colour type %d, interlace %d)."
                      % (path, depth, ctype, interlace))
    ch = 3 if ctype == 2 else 4
    stride = width * ch
    nrows = min(height, max_rows)
    raw = zlib.decompressobj().decompress(b"".join(idat), (stride + 1) * nrows)
    if len(raw) < (stride + 1) * nrows:
        raise Refused("%s is truncated." % path)
    rows, prev = [], bytearray(stride)
    for y in range(nrows):
        ft, line = raw[y * (stride + 1)], bytearray(raw[y * (stride + 1) + 1:(y + 1) * (stride + 1)])
        if ft == 1:
            for c in range(ch):
                line[c::ch] = bytes(itertools.accumulate(line[c::ch], lambda a, b: (a + b) & 255))
        elif ft == 2:
            line = bytearray((a + b) & 255 for a, b in zip(line, prev))
        elif ft == 3:
            for i in range(stride):
                line[i] = (line[i] + (((line[i - ch] if i >= ch else 0) + prev[i]) >> 1)) & 255
        elif ft == 4:
            for i in range(stride):
                a, b, c = (line[i - ch] if i >= ch else 0), prev[i], (prev[i - ch] if i >= ch else 0)
                pa, pb, pc = abs(b - c), abs(a - c), abs(a + b - 2 * c)
                line[i] = (line[i] + (a if pa <= pb and pa <= pc else b if pb <= pc else c)) & 255
        elif ft != 0:
            raise Refused("%s has an unknown PNG filter type %d." % (path, ft))
        rows.append(line)
        prev = line
    return width, ch, rows


def image_stats(path):
    """(distinct colours, mean luminance 0-255) over the top CHECK_ROWS px of a PNG."""
    _w, ch, rows = decode_png(path)
    colours, luma, n = set(), 0.0, 0
    for row in rows:
        r, g, b = row[0::ch], row[1::ch], row[2::ch]
        colours.update(zip(r, g, b))
        luma += 0.299 * sum(r) + 0.587 * sum(g) + 0.114 * sum(b)
        n += len(r)
    return len(colours), (luma / n if n else 0.0)


def check_rendered(files, pages):
    """Refused (exit 3) when an image shows no rendered text, or when a page's light image is not clearly brighter
    than its dark one (the themes would not match the file names)."""
    stats = {f.name: image_stats(f) for f in files}
    for name, (colours, luma) in sorted(stats.items()):
        print("check %s: %d colours, mean luminance %.1f" % (name, colours, luma))
        if colours < MIN_COLOURS:
            raise Refused("Captured images have no rendered text; check fonts. (%s has only %d distinct colours in "
                          "its top %d px, need %d.)" % (name, colours, CHECK_ROWS, MIN_COLOURS))
    for name in pages:
        light, dark = stats["%s-light.png" % name][1], stats["%s-dark.png" % name][1]
        if light < dark + MIN_LUMA_GAP:
            raise Refused("Theme mismatch: %s light/dark images do not match their names. (mean luminance light "
                          "%.1f, dark %.1f, need light > dark + %d.)" % (name, light, dark, MIN_LUMA_GAP))


# ───────────────────────────── crop, blank rows, hero, social preview ─────────────────────────────

def rgb_rows(rows, ch):
    """The rows as 8-bit RGB (alpha dropped)."""
    if ch == 3:
        return rows
    out = []
    for row in rows:
        px = bytearray(len(row) // 4 * 3)
        for c in range(3):
            px[c::3] = row[c::4]
        out.append(px)
    return out


def main_x0(width):
    """First pixel column of the main column: right of the sidebar and its 1 px hairline when the image is wide enough
    to show the sidebar (SIDEBAR_BREAKPOINT_CSS_PX), else 0 (the whole width)."""
    return (SIDEBAR_CSS_PX + 1) * SCALE if width >= SIDEBAR_BREAKPOINT_CSS_PX * SCALE else 0


def background(rows, x0):
    """The page background: the most frequent colour of the LAST row (below the content), main column only. Not the
    first row, which is the nav bar (--card) and differs from --bg."""
    last = rows[-1][x0 * 3:]
    return collections.Counter(zip(last[0::3], last[1::3], last[2::3])).most_common(1)[0][0]


def row_bad(row, x0, x1, bg):
    """How many pixels in columns [x0, x1) of an RGB row differ from bg by more than CROP_TOL on any channel."""
    seg = bytes(row[x0 * 3:x1 * 3])
    if seg == bytes(bg) * (x1 - x0):
        return 0
    r0, g0, b0 = bg
    return sum(1 for r, g, b in zip(seg[0::3], seg[1::3], seg[2::3])
               if abs(r - r0) > CROP_TOL or abs(g - g0) > CROP_TOL or abs(b - b0) > CROP_TOL)


def crop_to_content(rows, x0, bg):
    """(rows, truncated). Drops the trailing rows that are background on the main column, keeping a MARGIN_CSS bottom
    margin and at least MIN_HEIGHT_CSS. When content reaches the last row of the capture (a page taller than HMAX) all
    rows are kept and truncated is True."""
    width = len(rows[0]) // 3
    y = len(rows) - 1
    while y >= 0 and row_bad(rows[y], x0, width, bg) == 0:
        y -= 1
    if y == len(rows) - 1:
        return rows, True
    return rows[:min(len(rows), max(y + 1 + MARGIN_CSS * SCALE, MIN_HEIGHT_CSS * SCALE))], False


def blank_percent(rows, regions):
    """Percent of rows that sit in a run of at least BLANK_RUN_MIN blank rows. regions is [(x0, x1, bg), ...]: a row
    is blank when at least BLANK_PIXEL_FRAC of the pixels in those column ranges match their background."""
    total = sum(x1 - x0 for x0, x1, _bg in regions)
    allowed = total * (1 - BLANK_PIXEL_FRAC)
    flags = [sum(row_bad(row, x0, x1, bg) for x0, x1, bg in regions) <= allowed for row in rows]
    blank = 0
    for blank_row, run in itertools.groupby(flags):
        n = len(list(run))
        if blank_row and n >= BLANK_RUN_MIN:
            blank += n
    return 100.0 * blank / len(rows) if rows else 0.0


def check_blank(name, pct):
    if pct > BLANK_LIMIT_PCT:
        raise Refused("%s: %.0f%% blank rows (limit %d%%). The page is mostly empty; check the demo data." %
                      (name, pct, BLANK_LIMIT_PCT))


_ABS = bytes(min(i, 256 - i) for i in range(256))  # |signed byte| for the filter-choice score


def _paeth_row(row, prev):
    out = bytearray(len(row))
    for i in range(len(row)):
        a, b, c = (row[i - 3] if i >= 3 else 0), prev[i], (prev[i - 3] if i >= 3 else 0)
        pa, pb, pc = abs(b - c), abs(a - c), abs(a + b - 2 * c)
        out[i] = (row[i] - (a if pa <= pb and pa <= pc else b if pb <= pc else c)) & 255
    return bytes(out)


def filter_row(row, prev):
    """(filter type, filtered bytes) for one RGB row: whichever of Sub, Up and Paeth has the smallest sum of absolute
    values. A row equal to the one above is Up (all zeros); Paeth, the slow one, is only tried when Sub and Up both
    leave a lot of data (more than 1/8 of the row)."""
    if row == prev:
        return 2, bytes(len(row))
    up = bytes((a - b) & 255 for a, b in zip(row, prev))
    sub = bytes((a - b) & 255 for a, b in zip(row, bytes(3) + bytes(row[:-3])))
    best = min(((sum(up.translate(_ABS)), 2, up), (sum(sub.translate(_ABS)), 1, sub)))
    if best[0] > len(row) // 8:
        paeth = _paeth_row(row, prev)
        best = min(best, (sum(paeth.translate(_ABS)), 4, paeth))
    return best[1], best[2]


def encode_png(rows):
    """Bytes of an 8-bit RGB, non-interlaced PNG from RGB rows (zlib level 9, per-row filter)."""
    width, raw, prev = len(rows[0]) // 3, bytearray(), bytes(len(rows[0]))
    for row in rows:
        ft, data = filter_row(bytes(row), prev)
        raw += bytes([ft]) + data
        prev = bytes(row)

    def chunk(kind, body):
        return struct.pack(">I", len(body)) + kind + body + struct.pack(">I", zlib.crc32(kind + body) & 0xffffffff)

    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, len(rows), 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(bytes(raw), 9)) + chunk(b"IEND", b""))


def write_png(path, rows):
    Path(path).write_bytes(encode_png(rows))


def read_rows(path):
    """(width, RGB rows) of every row of a PNG."""
    width, ch, rows = decode_png(path, max_rows=1 << 30)
    return width, rgb_rows(rows, ch)


def downscale2(rows):
    """Halve an RGB image by averaging each 2x2 box. An odd trailing row or column is dropped."""
    out = []
    for y in range(0, len(rows) - 1, 2):
        a, b = rows[y], rows[y + 1]
        half = bytearray(len(a) // 6 * 3)
        for c in range(3):
            half[c::3] = bytes((p + q + r + t + 2) >> 2 for p, q, r, t in
                               zip(a[c::6], a[c + 3::6], b[c::6], b[c + 3::6]))
        out.append(half)
    return out


def compose_hero(left, right):
    """Left half of the light image next to the right half of the dark one, with a HERO_DIVIDER_PX divider between.
    Height is the smaller of the two."""
    width = len(left[0]) // 3
    if len(right[0]) // 3 != width:
        raise Failed("the light and dark home images differ in width (%d vs %d)." % (width, len(right[0]) // 3))
    half = width // 2
    d0 = half - HERO_DIVIDER_PX // 2
    divider = bytes(HERO_DIVIDER_RGB) * HERO_DIVIDER_PX
    rows = []
    for lrow, rrow in zip(left, right):
        row = bytearray(lrow[:half * 3] + rrow[half * 3:])
        row[d0 * 3:(d0 + HERO_DIVIDER_PX) * 3] = divider
        rows.append(row)
    return rows


def social_html(hero_small_png, bg, tagline=SOCIAL_TAGLINE):
    """The self-contained page the browser screenshots at SOCIAL_W x SOCIAL_H, DPR 1: the wordmark inline, the tagline
    and the (already downscaled) hero as a data: image. No external URL."""
    svg = (ROOT / "docs" / "images" / WORDMARK).read_text()
    svg = svg[svg.index("<svg"):]
    svg = re.sub(r'\sxmlns="[^"]*"', "", svg, count=1)
    old = 'width="360" height="80" viewBox="0 0 360 80"'
    if old not in svg:
        raise Failed("%s no longer has the root attributes %r; update WORDMARK_VIEWBOX handling." % (WORDMARK, old))
    height = SOCIAL_BAND - 20
    svg = svg.replace(old, 'width="%d" height="%d" viewBox="%s"' % (round(height * 275 / 80), height, WORDMARK_VIEWBOX), 1)
    colour = "rgb(%d,%d,%d)" % tuple(bg)
    return ('''<!doctype html>
<html><head><meta charset="utf-8"><title>Hivemill</title><style>
html, body { margin: 0; padding: 0; width: %(w)dpx; height: %(h)dpx; overflow: hidden; background: %(bg)s; }
.band { box-sizing: border-box; height: %(band)dpx; display: flex; align-items: center; gap: 32px; padding: 0 32px; background: %(bg)s; }
.band svg { flex: none; display: block; }
.tag { font: 600 21px/1.35 "DejaVu Sans", sans-serif; color: #1f2328; }
img { display: block; width: %(w)dpx; height: %(img)dpx; }
</style></head><body>
<div class="band">%(svg)s<div class="tag">%(tag)s</div></div>
<img alt="" src="data:image/png;base64,%(b64)s">
</body></html>
''' % {"w": SOCIAL_W, "h": SOCIAL_H, "bg": colour, "band": SOCIAL_BAND, "img": SOCIAL_H - SOCIAL_BAND, "svg": svg,
       "tag": html.escape(tagline), "b64": base64.b64encode(hero_small_png).decode("ascii")})


def hero_small(hero_rows, bg):
    """The hero downscaled by 2 to fill the SOCIAL_H - SOCIAL_BAND rows under the band: cropped from the top, padded
    with the background colour when it is shorter."""
    need = (SOCIAL_H - SOCIAL_BAND) * 2
    rows = [bytearray(r) for r in hero_rows[:need]]
    pad = bytearray(bytes(bg) * (len(hero_rows[0]) // 3))
    rows += [bytearray(pad) for _ in range(need - len(rows))]
    return downscale2(rows)


def png_size(path):
    head = Path(path).read_bytes()[:24]
    if head[:8] != b"\x89PNG\r\n\x1a\n" or head[12:16] != b"IHDR":
        raise Failed("%s is not a PNG." % path)
    return struct.unpack(">II", head[16:24])


def capture_social(binary, tmp, page_html, dest):
    """Screenshot the social page at SOCIAL_W x SOCIAL_H, DPR 1, then re-encode it as plain 8-bit RGB (smaller)."""
    page = tmp / "social.html"
    page.write_text(page_html)
    raw = tmp / "social-raw.png"
    profile = tmp / "profile"
    shutil.rmtree(profile, ignore_errors=True)
    env = {"PATH": os.environ.get("PATH", ""), "HOME": str(tmp / "browser-home"), "LANG": "C.UTF-8"}
    (tmp / "browser-home").mkdir(exist_ok=True)
    cmd = browser_args(binary, "light", raw, page.as_uri(), SOCIAL_H, profile)
    rc, out, err = run_child(cmd, 120, env=env)
    if rc != 0 or not raw.is_file():
        raise Failed("capture of the social preview failed (exit %d): %s" % (rc, err[-500:]))
    if png_size(raw) != (SOCIAL_W, SOCIAL_H):
        raise Failed("social preview is %dx%d, expected %dx%d." % (png_size(raw) + (SOCIAL_W, SOCIAL_H)))
    _w, rows = read_rows(raw)
    write_png(dest, rows)
    if dest.stat().st_size > SOCIAL_MAX_BYTES:
        raise Refused("%s is %d bytes, over GitHub's %d limit for a social preview." %
                      (dest.name, dest.stat().st_size, SOCIAL_MAX_BYTES))


def capture_all(binary, tmp, port, pages):
    """Capture, crop and check every page, build hero.png and social-preview.png (when home is among the pages).
    Everything is written under tmp/out; the caller publishes the returned files only when every check passed."""
    shots = capture(binary, tmp, port, pages, HMAX, dpr=True)
    outdir = tmp / "out"
    outdir.mkdir()
    files, meta = [], {}
    for name in pages:
        for theme in THEMES:
            fname = "%s-%s.png" % (name, theme)
            width, rows = read_rows(shots / fname)
            x0 = main_x0(width)
            bg = background(rows, x0)
            rows, truncated = crop_to_content(rows, x0, bg)
            if truncated:
                print("INFO %s: truncated at HMAX (%d CSS px)" % (fname, HMAX))
            write_png(outdir / fname, rows)
            files.append(outdir / fname)
            meta[fname] = (width, bg, blank_percent(rows, [(x0, width, bg)]))
    check_themes_differ(files, pages)
    check_rendered(files, pages)
    for fname, (_w, _bg, pct) in sorted(meta.items()):
        check_blank(fname, pct)
    allfiles = list(files)
    if "home" in pages:
        (width, bgl, _p), (_w2, bgr, _p2) = meta["home-light.png"], meta["home-dark.png"]
        _wl, left = read_rows(outdir / "home-light.png")
        _wr, right = read_rows(outdir / "home-dark.png")
        hero = compose_hero(left, right)
        half = width // 2
        regions = [(main_x0(width), half - HERO_DIVIDER_PX // 2, bgl), (half + HERO_DIVIDER_PX // 2, width, bgr)]
        check_blank("hero.png", blank_percent(hero, regions))
        write_png(outdir / "hero.png", hero)
        small = encode_png(hero_small(hero, bgl))
        capture_social(binary, tmp, social_html(small, bgl), outdir / "social-preview.png")
        allfiles += [outdir / "hero.png", outdir / "social-preview.png"]
    check_sizes(allfiles)
    return allfiles


def publish(files, out):
    out.mkdir(parents=True, exist_ok=True)
    for f in files:
        shutil.copyfile(str(f), str(out / f.name))
        print("wrote %s (%d bytes)" % (out / f.name, f.stat().st_size))
    print("summary: %d images, %d bytes total" % (len(files), sum(f.stat().st_size for f in files)))
    print("files: " + " ".join(f.name for f in files))


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--out", default=str(IMAGES), help="output directory, must be docs/images or inside it")
    ap.add_argument("--pages", default=",".join(PAGES), help="comma list from: " + ",".join(PAGES))
    ap.add_argument("--now", help="demo seed reference time (ISO); default: the current UTC hour, floored")
    args = ap.parse_args(argv)
    try:
        check_checkout()
        out = check_out(args.out)
    except Refused as e:
        print("refused: %s" % e, file=sys.stderr)
        return 3
    pages = [n for n in args.pages.split(",") if n]
    bad = [n for n in pages if n not in PAGES]
    if bad or not pages:
        print("unknown page(s) %s; choose from %s" % (bad, ",".join(PAGES)), file=sys.stderr)
        return 2
    try:
        check_fonts()
    except Refused as e:
        print("refused: %s" % e, file=sys.stderr)
        return 3
    now = args.now or current_hour()
    tmp = None
    old = {s: signal.signal(s, _interrupt) for s in (signal.SIGINT, signal.SIGTERM)}
    code = 0
    try:
        tmp = Path(tempfile.mkdtemp(prefix="factory-screenshots-")).resolve()
        binary = fetch_browser(tmp)
        demo = tmp / "demo"
        seed(demo, now)
        port = serve(demo)
        scrub(tmp, port, pages)
        files = capture_all(binary, tmp, port, pages)
        publish(files, out)
        print("--now %s" % now)
    except Refused as e:
        print("refused: %s" % e, file=sys.stderr)
        code = 3
    except Failed as e:
        print("screenshots failed: %s" % e, file=sys.stderr)
        code = 1
    except KeyboardInterrupt:
        print("interrupted", file=sys.stderr)
        code = 130
    finally:
        # a second Ctrl-C or SIGTERM must not interrupt the cleanup and leave the server running
        for s in (signal.SIGINT, signal.SIGTERM):
            signal.signal(s, signal.SIG_IGN)
        try:
            cleanup(tmp)
        finally:
            for s, h in old.items():
                signal.signal(s, h)
    return code


if __name__ == "__main__":
    sys.exit(main())
