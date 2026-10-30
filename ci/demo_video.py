#!/usr/bin/env python3
"""One-command demo video build (G-020): record every storyboard scene, encode MP4 + WebM + hero WebP, check them.

    python3 ci/demo_video.py [build|check] [--draft] [--browser PATH] [--out DIR] [--only SCENE] [--keep-work]
                             [--board FILE]

build  records the scenes of docs/demo/storyboard.json one at a time, streaming the PNG frames straight into ffmpeg
       (a near-lossless clip per scene under <work>/clips), then concatenates them into demo.mp4 and demo.webm and
       records the hero loop (the orgchart scene again, captions hidden) into hero.webp.
check  ffprobes the three outputs against the limits of G-020 SC2/SC3 and runs the demo scrub over the frame text.
--board FILE records another storyboard (default docs/demo/storyboard.json), e.g. a shorter cut; a board without a
"hero" entry builds and checks no hero loop.
No subcommand = build, then check. Needs ffmpeg and ffprobe on PATH. Python 3 stdlib only.
Exit codes: 0 ok, 1 failed (build error or a FAIL check), 2 usage error, 3 refused (preflight).
"""
import argparse
import json
import os
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import demo_cdp  # noqa: E402
import demo_record  # noqa: E402
import demo_scrub  # noqa: E402
import demo_tour  # noqa: E402

DEMO = demo_record.DEMO
DEFAULT_OUT = DEMO / "out"
DEFAULT_BOARD = DEMO / "storyboard.json"
MIN_MEM_MB = 1500  # at capture scale 1; mem_floor() grows it with the pixel count of the capture
MEM_PER_EXTRA_PIXEL_MB = 500  # per (scale^2 - 1): 2x capture = 3000 MB (4x PNGs in the browser and the pipe)
SCALE_RANGE = (1.0, 2.0)
OUT_SIZE = "1920:1080"
APT_HELP = "Install it with: sudo apt install ffmpeg"
THREADS = "4"
MP4_LIMIT = 25 * 1024 * 1024
HERO_LIMIT = 5 * 1024 * 1024
VIDEO_SECONDS = (60, 90)
HERO_SECONDS = (10, 15)
MIN_FPS = 30
MIN_HERO_FPS = 15
# Encoder settings; the values that passed the size checks on the final run are recorded in docs/demo/README.md.
CLIP_CRF, FINAL_CRF, WEBM_CRF = "12", "22", "34"


class Refused(Exception):
    pass


class BuildError(Exception):
    pass


# ---- ffmpeg argument builders (pure, unit-tested) ----

def clip_args(out, fps=30):
    """Per-scene near-lossless intermediate, frames arrive as PNGs on stdin."""
    return ["ffmpeg", "-y", "-loglevel", "error", "-f", "image2pipe", "-framerate", str(fps), "-i", "-",
            "-c:v", "libx264", "-crf", CLIP_CRF, "-preset", "veryfast", "-pix_fmt", "yuv420p", "-threads", THREADS,
            "-an", str(out)]


def export_filter(scale=1.0):
    """Video filter args that bring a scaled capture back to 1920x1080 with lanczos; none at scale 1."""
    return ["-vf", "scale=%s:flags=lanczos" % OUT_SIZE] if scale != 1 else []


def mp4_args(list_file, out, fps=30, scale=1.0):
    return ["ffmpeg", "-y", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", str(list_file),
            *export_filter(scale), "-c:v", "libx264", "-crf", FINAL_CRF, "-preset", "slow", "-pix_fmt", "yuv420p", "-r", str(fps),
            "-movflags", "+faststart", "-an", "-threads", THREADS, str(out)]


def webm_args(list_file, out, fps=30, scale=1.0):
    return ["ffmpeg", "-y", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", str(list_file),
            *export_filter(scale), "-c:v", "libvpx-vp9", "-crf", WEBM_CRF, "-b:v", "0", "-row-mt", "1", "-pix_fmt", "yuv420p",
            "-r", str(fps), "-an", "-threads", THREADS, str(out)]


def hero_args(out, width=1600, fps=18, src_fps=30, quality=85):
    """Hero loop, frames arrive as PNGs on stdin."""
    return ["ffmpeg", "-y", "-loglevel", "error", "-f", "image2pipe", "-framerate", str(src_fps), "-i", "-",
            "-vf", "fps=%d,scale=%d:-2" % (fps, width), "-c:v", "libwebp_anim", "-loop", "0",
            "-quality", str(quality), "-an", str(out)]


def probe_args(path):
    return ["ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", str(path)]


def concat_list(clips):
    """Text of an ffmpeg concat-demuxer list file for absolute clip paths."""
    return "".join("file '%s'\n" % str(c).replace("'", "'\\''") for c in clips)


# ---- preflight ----

def mem_floor(scale=1.0):
    """MemAvailable (MB) the build needs at capture scale `scale`."""
    return round(MIN_MEM_MB + MEM_PER_EXTRA_PIXEL_MB * (scale * scale - 1))


def check_scale(scale):
    if not SCALE_RANGE[0] <= scale <= SCALE_RANGE[1]:
        raise Refused("--scale %s is outside %g-%g. Use 1 (1080p), 1.333 (2560x1440) or 2 (3840x2160)."
                      % (scale, *SCALE_RANGE))


def mem_available_mb(path="/proc/meminfo"):
    """MemAvailable in MB; Refused when it cannot be read (an unknown amount is not enough)."""
    try:
        for line in Path(path).read_text().splitlines():
            if line.startswith("MemAvailable:"):
                return int(line.split()[1]) // 1024
    except (OSError, ValueError, IndexError) as e:
        raise Refused("cannot read MemAvailable from %s (%s); check free RAM by hand (need %d MB) and retry."
                      % (path, e, MIN_MEM_MB))
    raise Refused("%s has no MemAvailable line; check free RAM by hand (need %d MB) and retry." % (path, MIN_MEM_MB))


def placeholders(brand):
    return sorted(k for k, v in brand.items() if "{{" in str(v))


def preflight(work, out, draft, brand_path=None, scale=1.0):
    check_scale(scale)
    for tool in ("ffmpeg", "ffprobe"):
        if shutil.which(tool) is None:
            raise Refused("%s not found on PATH. %s" % (tool, APT_HELP))
    mb = mem_available_mb()
    need = mem_floor(scale)
    if mb < need:
        raise Refused("only %d MB RAM available, the build needs %d MB at capture scale %g. Close other jobs (see "
                      "factory res), or lower --scale, and retry." % (mb, need, scale))
    try:
        demo_record.check_dirs(work, out)
    except demo_record.Refused as e:
        raise Refused(str(e))
    if not draft:
        path = Path(brand_path) if brand_path else DEMO / "brand.json"
        try:
            brand = json.loads(path.read_text())
        except (OSError, ValueError) as e:
            raise Refused("cannot read %s (%s)." % (path, e))
        left = placeholders(brand)
        if left:
            raise Refused("brand.json (%s) still has placeholders in: %s. Fill in the real values, or pass --draft for a "
                          "review build." % (path, ", ".join(left)))


# ---- running ffmpeg ----

def run(args, cwd=None):
    """Run one ffmpeg/ffprobe command to completion; BuildError with its stderr tail on failure."""
    with tempfile.TemporaryFile(dir=cwd) as err:
        try:
            r = subprocess.run(args, stdout=subprocess.PIPE, stderr=err, cwd=cwd)
        except OSError as e:
            raise BuildError("cannot run %s: %s. %s" % (args[0], e, APT_HELP))
        if r.returncode != 0:
            raise BuildError("%s exited %d: %s" % (args[0], r.returncode, _tail(err)))
        return r.stdout.decode("utf-8", "replace")


def _tail(f):
    f.seek(0)
    return f.read()[-600:].decode("utf-8", "replace").strip()


def stream(args, feed, cwd=None):
    """Start an ffmpeg reading PNGs on stdin, call feed(PipeSink), then wait for it. One process at a time."""
    with tempfile.TemporaryFile(dir=cwd) as err:
        try:
            proc = subprocess.Popen(args, stdin=subprocess.PIPE, stderr=err)
        except OSError as e:
            raise BuildError("cannot run %s: %s. %s" % (args[0], e, APT_HELP))
        try:
            frames = feed(demo_record.PipeSink(proc))
            proc.stdin.close()
        except BaseException as e:
            try:
                proc.stdin.close()
            except OSError:
                pass
            proc.terminate()
            proc.wait()
            if isinstance(e, demo_cdp.CdpError) and "encoder closed" in str(e):
                raise BuildError("%s ffmpeg said: %s" % (e, _tail(err)))
            raise
        if proc.wait() != 0:
            raise BuildError("ffmpeg exited %d: %s" % (proc.returncode, _tail(err)))
        return frames


# ---- checks ----

def parse_rate(text):
    try:
        num, _, den = str(text).partition("/")
        return float(num) / float(den or 1)
    except (ValueError, ZeroDivisionError):
        return 0.0


def video_stream(probe):
    for s in probe.get("streams", []):
        if s.get("codec_type", "video") == "video":
            return s
    return {}


def check_video(name, probe, size, codec):
    """List of (label, ok, detail) for demo.mp4 / demo.webm against SC2."""
    s, fmt = video_stream(probe), probe.get("format", {})
    try:
        dur = float(fmt.get("duration", s.get("duration", "nan")))
    except (TypeError, ValueError):
        dur = float("nan")
    fps = parse_rate(s.get("avg_frame_rate", "0/1"))
    return [
        ("%s codec" % name, s.get("codec_name") == codec, "%s (want %s)" % (s.get("codec_name"), codec)),
        ("%s resolution" % name, (s.get("width"), s.get("height")) == (1920, 1080),
         "%sx%s (want 1920x1080)" % (s.get("width"), s.get("height"))),
        ("%s fps" % name, fps >= MIN_FPS, "%.2f (want >= %d)" % (fps, MIN_FPS)),
        ("%s duration" % name, VIDEO_SECONDS[0] <= dur <= VIDEO_SECONDS[1],
         "%.1f s (want %d-%d)" % (dur, *VIDEO_SECONDS)),
        ("%s size" % name, size < MP4_LIMIT, "%d B (limit %d)" % (size, MP4_LIMIT)),
    ]


def webp_animation(path):
    """(seconds, frames) of an animated WebP read from its RIFF ANMF chunks; ffprobe cannot time these.
    (0.0, 0) for a file that is not an animated WebP."""
    try:
        data = Path(path).read_bytes()
    except OSError:
        return 0.0, 0
    if data[:4] != b"RIFF" or data[8:12] != b"WEBP":
        return 0.0, 0
    pos, ms, frames = 12, 0, 0
    while pos + 8 <= len(data):
        tag, size = data[pos:pos + 4], struct.unpack("<I", data[pos + 4:pos + 8])[0]
        if tag == b"ANMF" and pos + 24 <= len(data):
            ms += int.from_bytes(data[pos + 8 + 12:pos + 8 + 15], "little")
            frames += 1
        pos += 8 + size + (size & 1)
    return ms / 1000.0, frames


def check_hero(probe, size, seconds, frames):
    s = video_stream(probe)
    fps = frames / seconds if seconds else 0.0
    return [
        ("hero.webp codec", s.get("codec_name") == "webp", "%s (want webp)" % s.get("codec_name")),
        ("hero.webp duration", HERO_SECONDS[0] <= seconds <= HERO_SECONDS[1],
         "%.1f s (want %d-%d)" % (seconds, *HERO_SECONDS)),
        ("hero.webp fps", fps >= MIN_HERO_FPS, "%.1f (want >= %d)" % (fps, MIN_HERO_FPS)),
        ("hero.webp size", size < HERO_LIMIT, "%d B (limit %d)" % (size, HERO_LIMIT)),
    ]


def probe(path):
    try:
        return json.loads(run(probe_args(path)))
    except ValueError as e:
        raise BuildError("ffprobe printed no JSON for %s: %s" % (path, e))


def check(out, work, hero=True):
    """Run every check, print one ok/FAIL line each; returns 0 when all ok, else 1. hero=False skips hero.webp."""
    results = []
    out = Path(out)
    for name, codec in (("demo.mp4", "h264"), ("demo.webm", "vp9")):
        path = out / name
        if not path.is_file():
            results.append((name, False, "missing: run build first"))
            continue
        try:
            results += check_video(name, probe(path), path.stat().st_size, codec)
        except BuildError as e:
            results.append((name, False, str(e)))
    hero = out / "hero.webp" if hero else None
    if hero is not None and not hero.is_file():
        results.append(("hero.webp", False, "missing: run build first"))
    elif hero is not None:
        try:
            seconds, frames = webp_animation(hero)
            results += check_hero(probe(hero), hero.stat().st_size, seconds, frames)
        except BuildError as e:
            results.append(("hero.webp", False, str(e)))
    text_dir = Path(work) / "text"
    if text_dir.is_dir():
        rc = demo_scrub.scan(text_dir=text_dir)
        results.append(("scrub", rc == 0, "demo_scrub exit %d" % rc))
    else:
        results.append(("scrub", False, "%s missing: build with this --out (frame text is dumped there)" % text_dir))
    for label, ok, detail in results:
        print("%s: %s - %s" % ("ok" if ok else "FAIL", label, detail))
    return 0 if all(ok for _, ok, _ in results) else 1


# ---- build ----

def load_board(path=DEFAULT_BOARD):
    return json.loads(Path(path).read_text())


def board_scale(path=DEFAULT_BOARD):
    """Capture scale from the storyboard; 1 when it is absent (build then reports an unreadable storyboard)."""
    try:
        return float(load_board(path).get("scale", 1))
    except (OSError, ValueError):
        return 1.0


def board_has_hero(path=DEFAULT_BOARD):
    """True when the storyboard has a hero entry; True when it cannot be read, so check still demands hero.webp."""
    try:
        return "hero" in load_board(path)
    except (OSError, ValueError):
        return True


def record_one(scene, board, browser_path, work, sink, hero=False, tour_ctx=None, scale=1.0):
    """Record one scene (or the hero range of it) with a fresh browser; returns the frame count."""
    fps = board["fps"]
    browser = None
    try:
        binary = demo_cdp.browser_binary(work, browser_path)
        browser = demo_cdp.Browser.launch(binary, board["width"], board["height"], work)
        browser.scale = scale
        text_dir = None if hero else Path(work) / "text"
        if scene["source"].startswith("ui:"):
            base_url = tour_ctx["base_url"]
            return demo_tour.record_tour(browser, base_url, scene["pages"], scene["page_duration"], fps, sink,
                                         text_dir, scene.get("labels"), scene.get("captions", []))
        url = (DEMO / scene["source"]).resolve().as_uri()
        duration = scene["duration"]
        if hero:
            url += "?nocaptions=1"
            duration = board["hero"]["end"]
            first = round(board["hero"]["start"] * fps)
            inner = sink
            sink = lambda i, png: inner(i, png) if i >= first else None  # noqa: E731
        # the storyboard being recorded owns the caption timing, so one scene file serves every cut
        return demo_record.record_scene(browser, url, duration, fps, sink, text_dir=text_dir, scene_id=scene["id"],
                                        captions=scene.get("captions", []))
    finally:
        if browser is not None:
            browser.close()


def build(out, work, browser_path, only=None, keep_work=False, scale=None, board_path=DEFAULT_BOARD):
    board = load_board(board_path)
    scale = float(board.get("scale", 1)) if scale is None else scale
    check_scale(scale)
    scenes = board["scenes"]
    if only:
        scenes = [s for s in scenes if s["id"] == only]
        if not scenes:
            raise BuildError("no scene '%s' in storyboard.json (have: %s)"
                             % (only, ", ".join(s["id"] for s in board["scenes"])))
    out, work = Path(out), Path(work)
    clips_dir = work / "clips"
    shutil.rmtree(work / "text", ignore_errors=True)  # dumps of an earlier run must not leak into this one
    shutil.rmtree(clips_dir, ignore_errors=True)
    clips_dir.mkdir(parents=True)
    out.mkdir(parents=True, exist_ok=True)
    clips, server = [], None
    try:
        for n, scene in enumerate(board["scenes"], 1):
            if scene not in scenes:
                continue
            clip = clips_dir / ("%02d-%s.mp4" % (n, scene["id"]))
            ctx = None
            if scene["source"].startswith("ui:"):
                demo = demo_tour.DEFAULT_DEMO
                demo_record.check_dirs(demo, demo)
                demo_tour.prepare_demo_dir(demo)
                demo_cdp.seed_demo(demo, demo_cdp.screenshots.current_hour())
                port, server = demo_cdp.serve_demo(demo)
                ctx = {"base_url": "http://127.0.0.1:%d" % port}
            print("recording %s (beat %d) -> %s" % (scene["id"], scene["beat"], clip), flush=True)
            frames = stream(clip_args(clip, board["fps"]),
                            lambda sink, s=scene, c=ctx: record_one(s, board, browser_path, work, sink, tour_ctx=c, scale=scale),
                            cwd=work)
            print("  %d frames" % frames, flush=True)
            if server is not None:
                demo_cdp.stop(server)
                server = None
            clips.append(clip)
        if only:
            print("clip: %s" % clips[0])
            return
        listing = work / "clips.txt"
        listing.write_text(concat_list(clips))
        print("encoding demo.mp4", flush=True)
        run(mp4_args(listing, out / "demo.mp4", board["fps"], scale), cwd=work)
        print("encoding demo.webm", flush=True)
        run(webm_args(listing, out / "demo.webm", board["fps"], scale), cwd=work)
        hero = board.get("hero")
        if hero is None:
            print("no hero entry in %s: no hero loop" % board_path, flush=True)
        else:
            scene = next(s for s in board["scenes"] if s["id"] == hero["scene"])
            print("recording hero loop (%s %.1f-%.1f s)" % (scene["id"], hero["start"], hero["end"]), flush=True)
            stream(hero_args(out / "hero.webp", hero["width"], hero["fps"], board["fps"], hero["quality"]),
                   lambda sink: record_one(scene, board, browser_path, work, sink, hero=True, scale=scale), cwd=work)
    finally:
        if server is not None:
            demo_cdp.stop(server)
        for proc in list(demo_cdp.screenshots.CHILDREN):
            demo_cdp.stop(proc)
    if not keep_work:
        shutil.rmtree(clips_dir, ignore_errors=True)
        (work / "clips.txt").unlink(missing_ok=True)


def main(argv=None):
    ap = argparse.ArgumentParser(description="Build and check the launch demo video, see docs/demo/README.md.")
    ap.add_argument("command", nargs="?", choices=("build", "check"), help="default: build, then check")
    ap.add_argument("--draft", action="store_true", help="allow {{placeholders}} in brand.json (review build)")
    ap.add_argument("--browser", help="path to an existing chrome-headless-shell (default: download the pinned one)")
    ap.add_argument("--out", default=str(DEFAULT_OUT), help="output directory (default docs/demo/out)")
    ap.add_argument("--work", help="work dir (default <out>/work)")
    ap.add_argument("--only", help="build: record just this scene to a clip, no encode")
    ap.add_argument("--scale", type=float, help="build: capture scale, 1-2 (2 = 3840x2160); exports stay 1920x1080 "
                    "(default: 'scale' in storyboard.json, else 1)")
    ap.add_argument("--keep-work", action="store_true", help="build: keep the per-scene clips")
    ap.add_argument("--board", default=str(DEFAULT_BOARD), help="storyboard to record and check (default "
                    "docs/demo/storyboard.json); without a 'hero' entry no hero loop is built or checked")
    args = ap.parse_args(argv)
    out = Path(args.out).resolve()  # ffmpeg runs with cwd=work, so every path it gets must be absolute
    work = Path(args.work).resolve() if args.work else out / "work"
    cmd = args.command or "all"
    signal.signal(signal.SIGTERM, demo_record._interrupt)
    try:
        if cmd in ("build", "all"):
            scale = args.scale if args.scale is not None else board_scale(args.board)
            preflight(work, out, args.draft, scale=scale)
            build(out, work, args.browser, args.only, args.keep_work, scale, args.board)
        if cmd == "check" or (cmd == "all" and not args.only):
            return check(out, work, board_has_hero(args.board))
    except Refused as e:
        print("demo_video: refused: %s" % e, file=sys.stderr)
        return 3
    except (BuildError, demo_record.Refused, demo_tour.PageError, demo_cdp.CdpError, demo_cdp.screenshots.Failed,
            demo_cdp.screenshots.Refused, OSError) as e:
        print("demo_video: failed: %s" % e, file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("demo_video: interrupted", file=sys.stderr)
        return 130
    return 0


if __name__ == "__main__":
    sys.exit(main())
