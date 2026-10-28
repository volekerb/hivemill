#!/usr/bin/env python3
"""Regenerate the web UI screenshots in docs/images/ (light and dark) from a fictional demo home.

  python3 ci/screenshots.py [--out docs/images] [--pages home,goals,...] [--now ISO]

Run it from a factory checkout. Everything happens in one temp dir that is always removed, with every child process
(server, browser) terminated, on success, error, Ctrl-C and SIGTERM. In order it: downloads the pinned Chrome for
Testing headless shell (the only network access), checks its size and sha256 BEFORE unzipping, runs `ldd` on it, seeds a
demo home with ci/demo_seed.py, serves it on 127.0.0.1 with its own HOME and FACTORY_HOME (the live home is never
touched), scrub-scans each page's HTML (set SCRUB_TERMS_FILE for owner terms), captures each page light and dark at
1280 px wide, checks the PNG sizes, that text was rendered and that each theme matches its file name. It first checks
that fonts are installed (fontconfig and fonts-dejavu-core; the browser draws no text without them). It never installs
anything and never escalates privileges.
See docs/images/README.md. Exit codes: 0 ok, 1 failure, 2 usage error, 3 refused (not a factory checkout, --out outside
docs/images, no fonts, checksum mismatch, no rendered text, theme mismatch). Stdlib only.
"""
import argparse
import datetime as dt
import hashlib
import os
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
WIDTH = 1280
HEIGHTS = (860, 640)  # second entry is the one retry when the PNGs are too big
MAX_PNG = 307200
MAX_TOTAL = 3145728
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


def browser_args(binary, theme, dest, url, height, profile):
    """The headless browser command line for one page in one theme (light/dark -> THEMES)."""
    return [str(binary), "--headless", "--disable-gpu", "--no-sandbox", "--hide-scrollbars",
            "--window-size=%d,%d" % (WIDTH, height), "--user-data-dir=" + str(profile),
            "--blink-settings=preferredColorScheme=" + THEMES[theme], "--screenshot=" + str(dest), url]


def capture(binary, tmp, port, pages, height):
    shots = tmp / ("shots-%d" % height)
    shots.mkdir()
    env = {"PATH": os.environ.get("PATH", ""), "HOME": str(tmp / "browser-home"), "LANG": "C.UTF-8"}
    (tmp / "browser-home").mkdir(exist_ok=True)
    for name in pages:
        for theme in THEMES:
            dest = shots / ("%s-%s.png" % (name, theme))
            profile = tmp / "profile"
            shutil.rmtree(profile, ignore_errors=True)
            cmd = browser_args(binary, theme, dest, "http://127.0.0.1:%d%s" % (port, PAGES[name]), height, profile)
            rc, out, err = run_child(cmd, 120, env=env)  # one browser process at a time
            if rc != 0 or not dest.is_file():
                raise Failed("capture of %s-%s failed (exit %d): %s" % (name, theme, rc, err[-500:]))
            if png_width(dest) != WIDTH:
                raise Failed("%s is %d px wide, expected %d." % (dest.name, png_width(dest), WIDTH))
    return shots


def check_sizes(shots):
    """Failed unless every PNG is <= MAX_PNG and the total <= MAX_TOTAL; returns the PNG paths."""
    files = sorted(shots.glob("*.png"))
    big = ["%s (%d bytes)" % (f.name, f.stat().st_size) for f in files if f.stat().st_size > MAX_PNG]
    total = sum(f.stat().st_size for f in files)
    if big:
        raise Failed("over %d bytes: %s" % (MAX_PNG, ", ".join(big)))
    if total > MAX_TOTAL:
        raise Failed("total %d bytes is over %d." % (total, MAX_TOTAL))
    return files


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
            for i in range(ch, stride):
                line[i] = (line[i] + line[i - ch]) & 255
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


def capture_all(binary, tmp, port, pages):
    for attempt, height in enumerate(HEIGHTS):
        shots = capture(binary, tmp, port, pages, height)
        try:
            files = check_sizes(shots)
        except Failed as e:
            if attempt + 1 == len(HEIGHTS):
                raise
            print("%s; retrying once with a %d px window" % (e, HEIGHTS[attempt + 1]))
            continue
        check_themes_differ(files, pages)
        check_rendered(files, pages)
        return files


def publish(files, out):
    out.mkdir(parents=True, exist_ok=True)
    for f in files:
        shutil.copyfile(str(f), str(out / f.name))
        print("wrote %s (%d bytes)" % (out / f.name, f.stat().st_size))
    print("summary: %d images, %d bytes total" % (len(files), sum(f.stat().st_size for f in files)))


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
