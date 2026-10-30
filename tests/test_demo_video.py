"""ci/demo_video.py: argument builders, ffprobe checks, preflight guards and scene order. No ffmpeg, no browser.

The integration test at the bottom runs only with ffmpeg on PATH and DEMO_BROWSER set (an already-downloaded
chrome-headless-shell); CI has neither and skips it."""
import contextlib
import io
import json
import os
import shutil
import struct
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "ci"))
import demo_video as dv  # noqa: E402

REAL_MEM_READER = dv.mem_available_mb

GOOD_VIDEO = {"streams": [{"codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080,
                           "avg_frame_rate": "30/1"}], "format": {"duration": "76.000000"}}
GOOD_WEBM = {"streams": [{"codec_type": "video", "codec_name": "vp9", "width": 1920, "height": 1080,
                          "avg_frame_rate": "30/1"}], "format": {"duration": "76.0"}}


def labels(results):
    return {label: ok for label, ok, _ in results}


def run_main(*argv):
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        rc = dv.main(list(argv))
    return rc, out.getvalue(), err.getvalue()


class ArgBuilders(unittest.TestCase):
    def pairs(self, args):
        return {args[i]: args[i + 1] for i in range(len(args) - 1)}

    def test_clip_args(self):
        a = dv.clip_args("/w/clips/01-hook.mp4")
        self.assertEqual(a[0], "ffmpeg")
        self.assertEqual(a[a.index("-f") + 1], "image2pipe")
        p = self.pairs(a)
        self.assertEqual((p["-framerate"], p["-i"], p["-c:v"], p["-crf"], p["-preset"], p["-pix_fmt"], p["-threads"]),
                         ("30", "-", "libx264", "12", "veryfast", "yuv420p", "4"))
        self.assertEqual(a[-1], "/w/clips/01-hook.mp4")

    def test_mp4_args(self):
        a = dv.mp4_args("list.txt", "demo.mp4")
        p = self.pairs(a)
        self.assertEqual((p["-c:v"], p["-crf"], p["-preset"], p["-pix_fmt"], p["-r"], p["-movflags"], p["-f"]),
                         ("libx264", "22", "slow", "yuv420p", "30", "+faststart", "concat"))
        self.assertIn("-an", a)
        self.assertEqual(a[-1], "demo.mp4")

    def test_webm_args(self):
        a = dv.webm_args("list.txt", "demo.webm")
        p = self.pairs(a)
        self.assertEqual((p["-c:v"], p["-crf"], p["-b:v"], p["-row-mt"], p["-threads"]),
                         ("libvpx-vp9", "34", "0", "1", "4"))
        self.assertIn("-an", a)

    def test_default_export_has_no_scale_filter(self):
        self.assertNotIn("-vf", dv.mp4_args("l.txt", "o.mp4"))
        self.assertNotIn("-vf", dv.webm_args("l.txt", "o.webm"))

    def test_scaled_capture_is_exported_at_1080p_with_lanczos(self):
        for a in (dv.mp4_args("l.txt", "o.mp4", 30, 2), dv.webm_args("l.txt", "o.webm", 30, 1.333)):
            self.assertEqual(self.pairs(a)["-vf"], "scale=1920:1080:flags=lanczos")
            self.assertLess(a.index("-i"), a.index("-vf"))  # an output option, after the input
        # the output check still demands 1920x1080
        probe = {"streams": [{"codec_name": "h264", "width": 3840, "height": 2160, "avg_frame_rate": "30/1"}],
                 "format": {"duration": "80"}}
        res = dict((l, ok) for l, ok, _ in dv.check_video("demo.mp4", probe, 1000, "h264"))
        self.assertFalse(res["demo.mp4 resolution"])

    def test_hero_args(self):
        a = dv.hero_args("hero.webp")
        p = self.pairs(a)
        self.assertEqual((p["-c:v"], p["-loop"], p["-quality"], p["-vf"]),
                         ("libwebp_anim", "0", "85", "fps=18,scale=1600:-2"))
        q = self.pairs(dv.hero_args("hero.webp", 1280, 15, 30, 90))
        self.assertEqual((q["-quality"], q["-vf"]), ("90", "fps=15,scale=1280:-2"))
        hero = dv.load_board()["hero"]
        self.assertEqual((hero["width"], hero["fps"], hero["quality"]), (1600, 18, 85))

    def test_concat_list_quotes(self):
        self.assertEqual(dv.concat_list(["/w/a.mp4", "/w/it's.mp4"]), "file '/w/a.mp4'\nfile '/w/it'\\''s.mp4'\n")


class Checks(unittest.TestCase):
    def test_good_mp4_and_webm_pass(self):
        self.assertTrue(all(labels(dv.check_video("demo.mp4", GOOD_VIDEO, 20 * 2 ** 20, "h264")).values()))
        self.assertTrue(all(labels(dv.check_video("demo.webm", GOOD_WEBM, 10 * 2 ** 20, "vp9")).values()))

    def only_fails(self, probe, size, *names):
        res = labels(dv.check_video("v", probe, size, "h264"))
        self.assertEqual({k for k, v in res.items() if not v}, {"v " + n for n in names})

    def test_each_limit_fails(self):
        def with_(stream=None, duration=None):
            p = json.loads(json.dumps(GOOD_VIDEO))
            p["streams"][0].update(stream or {})
            if duration:
                p["format"]["duration"] = duration
            return p
        self.only_fails(with_({"codec_name": "hevc"}), 1, "codec")
        self.only_fails(with_({"width": 1280, "height": 720}), 1, "resolution")
        self.only_fails(with_({"avg_frame_rate": "25/1"}), 1, "fps")
        self.only_fails(with_(duration="59.9"), 1, "duration")
        self.only_fails(with_(duration="90.5"), 1, "duration")
        self.only_fails(with_(), dv.MP4_LIMIT, "size")

    def test_garbage_probe_fails_not_passes(self):
        res = labels(dv.check_video("v", {}, 1, "h264"))
        self.assertEqual({k for k, v in res.items() if v}, {"v size"}, res)

    def test_hero_limits(self):
        p = {"streams": [{"codec_type": "video", "codec_name": "webp"}]}
        self.assertTrue(all(labels(dv.check_hero(p, 3 * 2 ** 20, 13.0, 260)).values()))
        self.assertFalse(labels(dv.check_hero(p, dv.HERO_LIMIT, 13.0, 260))["hero.webp size"])
        self.assertFalse(labels(dv.check_hero(p, 1, 9.9, 200))["hero.webp duration"])
        self.assertFalse(labels(dv.check_hero(p, 1, 15.1, 300))["hero.webp duration"])
        self.assertFalse(labels(dv.check_hero(p, 1, 13.0, 130))["hero.webp fps"])

    def test_webp_animation_parses_riff(self):
        def anmf(ms):
            body = b"\0" * 12 + ms.to_bytes(3, "little") + b"\0"
            return b"ANMF" + struct.pack("<I", len(body)) + body
        chunks = b"WEBP" + anmf(50) * 4
        data = b"RIFF" + struct.pack("<I", len(chunks)) + chunks
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "h.webp"
            p.write_bytes(data)
            self.assertEqual(dv.webp_animation(p), (0.2, 4))
            p.write_bytes(b"not a webp")
            self.assertEqual(dv.webp_animation(p), (0.0, 0))
            self.assertEqual(dv.webp_animation(Path(d) / "missing.webp"), (0.0, 0))

    def test_check_reports_missing_outputs_exit_1(self):
        with tempfile.TemporaryDirectory() as d:
            rc, out, _ = run_main("check", "--out", d)
        self.assertEqual(rc, 1)
        self.assertIn("FAIL: demo.mp4", out)
        self.assertIn("FAIL: hero.webp", out)
        self.assertNotIn("\nok", "\n" + out)


class Guards(unittest.TestCase):
    """Every refusal is exit 3 through main()."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.which = mock.patch.object(dv.shutil, "which", lambda t: "/usr/bin/" + t)
        self.which.start()
        self.addCleanup(self.which.stop)
        self.mem = mock.patch.object(dv, "mem_available_mb", lambda: 4000)
        self.mem.start()
        self.addCleanup(self.mem.stop)

    def test_missing_ffmpeg(self):
        with mock.patch.object(dv.shutil, "which", lambda t: None):
            rc, _, err = run_main("build", "--draft", "--out", "/nonexistent-disk/out")
        self.assertEqual(rc, 3)
        self.assertIn("sudo apt install ffmpeg", err)

    def test_missing_ffprobe_only(self):
        with mock.patch.object(dv.shutil, "which", lambda t: "/usr/bin/ffmpeg" if t == "ffmpeg" else None):
            rc, _, err = run_main("build", "--draft", "--out", "/nonexistent-disk/out")
        self.assertEqual((rc, "ffprobe" in err), (3, True))

    def test_missing_ffmpeg_real_subprocess(self):
        r = subprocess.run([sys.executable, str(ROOT / "ci" / "demo_video.py"), "build", "--draft"],
                           env={"PATH": "/nonexistent-bin"}, capture_output=True, text=True)
        self.assertEqual(r.returncode, 3, r.stderr)
        self.assertIn("sudo apt install ffmpeg", r.stderr)

    def test_low_memory(self):
        with mock.patch.object(dv, "mem_available_mb", lambda: 1499):
            rc, _, err = run_main("build", "--draft", "--out", "/nonexistent-disk/out")
        self.assertEqual(rc, 3)
        self.assertIn("1499 MB", err)

    def test_memory_floor_grows_with_capture_scale(self):
        self.assertEqual(dv.mem_floor(1), 1500)
        self.assertEqual(dv.mem_floor(2), 3000)
        self.assertEqual(dv.mem_floor(1.333), 1888)
        with mock.patch.object(dv, "mem_available_mb", lambda: 2999):
            rc, _, err = run_main("build", "--draft", "--scale", "2", "--out", "/nonexistent-disk/out")
        self.assertEqual(rc, 3)
        self.assertIn("2999 MB", err)
        self.assertIn("3000 MB", err)
        with mock.patch.object(dv, "mem_available_mb", lambda: 2999):  # same RAM is fine at 1x
            rc, _, err = run_main("build", "--draft", "--scale", "1", "--out", "/tmp/x")
        self.assertIn("/tmp", err)  # got past the memory check to the dir check

    def test_board_scale_reads_the_storyboard_for_the_memory_floor(self):
        with mock.patch.object(dv, "load_board", lambda *a: {"scale": 2}):
            self.assertEqual(dv.board_scale(), 2.0)
        with mock.patch.object(dv, "load_board", lambda *a: {}):
            self.assertEqual(dv.board_scale(), 1.0)
        with mock.patch.object(dv, "load_board", side_effect=OSError("gone")):
            self.assertEqual(dv.board_scale(), 1.0)

    def test_scale_out_of_range_is_refused(self):
        for bad in ("0.9", "2.5"):
            rc, _, err = run_main("build", "--draft", "--scale", bad, "--out", "/nonexistent-disk/out")
            self.assertEqual(rc, 3, bad)
            self.assertIn("--scale", err)

    def test_meminfo_reader(self):
        p = Path(self.tmp.name) / "meminfo"
        p.write_text("MemTotal: 8000000 kB\nMemAvailable:    1536000 kB\n")
        self.assertEqual(REAL_MEM_READER(p), 1500)
        p.write_text("MemTotal: 8000000 kB\n")
        with self.assertRaises(dv.Refused):
            REAL_MEM_READER(p)
        with self.assertRaises(dv.Refused):
            REAL_MEM_READER(Path(self.tmp.name) / "absent")

    def test_tmp_refused(self):
        for args in (["--out", "/tmp/demo-out"], ["--out", "/nonexistent-disk/out", "--work", "/dev/shm/w"]):
            rc, _, err = run_main("build", "--draft", *args)
            self.assertEqual(rc, 3, args)
            self.assertIn("/tmp", err)

    def test_placeholders_refused_unless_draft(self):
        demo = Path(self.tmp.name) / "demo"
        demo.mkdir()
        (demo / "brand.json").write_text(json.dumps({"name": "{{PRODUCT_NAME}}", "tagline": "ok"}))
        out = "/nonexistent-disk/out"
        with mock.patch.object(dv, "DEMO", demo), mock.patch.object(dv.demo_record, "check_dirs"):
            rc, _, err = run_main("build", "--out", out)
            self.assertEqual(rc, 3)
            self.assertIn("brand.json", err)
            self.assertIn("name", err)
            # --draft passes preflight (the build itself then fails on the missing storyboard, exit 1, not 3)
            with mock.patch.object(dv, "build", side_effect=dv.BuildError("stop")):
                rc, _, _ = run_main("build", "--draft", "--out", out)
            self.assertEqual(rc, 1)

    def test_real_brand_passes_preflight(self):
        demo = Path(self.tmp.name) / "demo"
        demo.mkdir()
        (demo / "brand.json").write_text(json.dumps({"name": "Real", "tagline": "ok"}))
        with mock.patch.object(dv, "DEMO", demo), mock.patch.object(dv.demo_record, "check_dirs"):
            dv.preflight("w", "o", draft=False)


class SceneOrder(unittest.TestCase):
    def test_scenes_recorded_sequentially_in_storyboard_order(self):
        board = dv.load_board()
        events = []

        def fake_record(scene, board_, browser, work, sink, hero=False, tour_ctx=None, scale=1.0):
            events.append(("record", scene["id"], hero))
            return 1

        def fake_stream(args, feed, cwd=None):
            events.append(("start", Path(args[-1]).name))
            n = feed(mock.Mock())
            events.append(("end", Path(args[-1]).name))
            return n

        def fake_run(args, cwd=None):
            events.append(("run", Path(args[-1]).name))
            return ""

        with tempfile.TemporaryDirectory() as d, \
                mock.patch.object(dv, "record_one", fake_record), mock.patch.object(dv, "stream", fake_stream), \
                mock.patch.object(dv, "run", fake_run), \
                mock.patch.object(dv.demo_tour, "prepare_demo_dir"), mock.patch.object(dv.demo_record, "check_dirs"), \
                mock.patch.object(dv.demo_cdp, "seed_demo"), mock.patch.object(dv.demo_cdp, "stop"), \
                mock.patch.object(dv.demo_cdp, "serve_demo", return_value=(1234, mock.Mock())), \
                mock.patch.object(dv.demo_cdp.screenshots, "current_hour", return_value="2026-01-01T00:00:00"):
            with contextlib.redirect_stdout(io.StringIO()):
                dv.build(Path(d) / "out", Path(d) / "work", None)
            clips_left = (Path(d) / "work" / "clips").exists()
        ids = [s["id"] for s in board["scenes"]]
        # strictly start/record/end per clip: nothing overlaps
        expect = []
        for n, i in enumerate(ids, 1):
            expect += [("start", "%02d-%s.mp4" % (n, i)), ("record", i, False), ("end", "%02d-%s.mp4" % (n, i))]
        expect += [("run", "demo.mp4"), ("run", "demo.webm"),
                   ("start", "hero.webp"), ("record", board["hero"]["scene"], True), ("end", "hero.webp")]
        self.assertEqual(events, expect)
        self.assertFalse(clips_left, "clips are removed after a successful build")

    def test_scale_reaches_the_recorder_and_the_exports(self):
        seen, runs = [], []
        with tempfile.TemporaryDirectory() as d, \
                mock.patch.object(dv, "record_one", lambda s, *a, **k: seen.append(k.get("scale")) or 1), \
                mock.patch.object(dv, "stream", lambda args, feed, cwd=None: feed(mock.Mock())), \
                mock.patch.object(dv, "run", lambda args, cwd=None: runs.append(args) or ""), \
                mock.patch.object(dv.demo_tour, "prepare_demo_dir"), mock.patch.object(dv.demo_record, "check_dirs"), \
                mock.patch.object(dv.demo_cdp, "seed_demo"), mock.patch.object(dv.demo_cdp, "stop"), \
                mock.patch.object(dv.demo_cdp, "serve_demo", return_value=(1234, mock.Mock())), \
                mock.patch.object(dv.demo_cdp.screenshots, "current_hour", return_value="2026-01-01T00:00:00"):
            with contextlib.redirect_stdout(io.StringIO()):
                dv.build(Path(d) / "out", Path(d) / "work", None, scale=2.0)
        self.assertEqual(set(seen), {2.0})
        self.assertEqual(len(runs), 2)
        for a in runs:
            self.assertIn("scale=1920:1080:flags=lanczos", a)

    def test_default_scale_comes_from_the_storyboard_else_one(self):
        seen = []
        for board_scale, want in ((None, 1.0), (1.5, 1.5)):
            board = dict(dv.load_board())
            board.pop("scale", None)  # the repo storyboard captures at 2x
            if board_scale:
                board["scale"] = board_scale
            with tempfile.TemporaryDirectory() as d, mock.patch.object(dv, "load_board", lambda *a, b=board: b), \
                    mock.patch.object(dv, "record_one", lambda s, *a, **k: seen.append(k.get("scale")) or 1), \
                    mock.patch.object(dv, "stream", lambda args, feed, cwd=None: feed(mock.Mock())), \
                    mock.patch.object(dv.demo_tour, "prepare_demo_dir"), mock.patch.object(dv.demo_record, "check_dirs"), \
                    mock.patch.object(dv.demo_cdp, "seed_demo"), mock.patch.object(dv.demo_cdp, "stop"), \
                    mock.patch.object(dv.demo_cdp, "serve_demo", return_value=(1234, mock.Mock())), \
                    mock.patch.object(dv.demo_cdp.screenshots, "current_hour", return_value="x"):
                with contextlib.redirect_stdout(io.StringIO()):
                    dv.build(Path(d) / "out", Path(d) / "work", None, only="problem")
            self.assertEqual(seen[-1], want)

    def test_only_records_one_clip_and_skips_encode(self):
        calls = []
        with tempfile.TemporaryDirectory() as d, \
                mock.patch.object(dv, "record_one", lambda s, *a, **k: calls.append(s["id"]) or 1), \
                mock.patch.object(dv, "stream", lambda args, feed, cwd=None: feed(mock.Mock())), \
                mock.patch.object(dv, "run", side_effect=AssertionError("no encode with --only")):
            with contextlib.redirect_stdout(io.StringIO()):
                dv.build(Path(d) / "out", Path(d) / "work", None, only="problem")
        self.assertEqual(calls, ["problem"])

    def test_relative_out_is_made_absolute(self):
        seen = {}
        with tempfile.TemporaryDirectory(dir=ROOT / "docs" / "demo") as d, \
                mock.patch.object(dv, "preflight"), \
                mock.patch.object(dv, "build", lambda out, work, *a, **k: seen.update(out=out, work=work)):
            rel = os.path.relpath(d)
            run_main("build", "--draft", "--out", rel)
        self.assertTrue(seen["out"].is_absolute() and seen["work"].is_absolute(), seen)

    def test_board_without_hero_builds_no_hero_loop(self):
        events = []
        board = dict(dv.load_board(dv.DEMO / "storyboard-linkedin.json"))
        self.assertNotIn("hero", board)
        with tempfile.TemporaryDirectory() as d, \
                mock.patch.object(dv, "record_one", lambda s, *a, **k: events.append((s["id"], k.get("hero", False))) or 1), \
                mock.patch.object(dv, "stream", lambda args, feed, cwd=None: events.append(Path(args[-1]).name) or feed(mock.Mock())), \
                mock.patch.object(dv, "run", lambda args, cwd=None: events.append(Path(args[-1]).name) or ""), \
                mock.patch.object(dv.demo_tour, "prepare_demo_dir"), mock.patch.object(dv.demo_record, "check_dirs"), \
                mock.patch.object(dv.demo_cdp, "seed_demo"), mock.patch.object(dv.demo_cdp, "stop"), \
                mock.patch.object(dv.demo_cdp, "serve_demo", return_value=(1234, mock.Mock())), \
                mock.patch.object(dv.demo_cdp.screenshots, "current_hour", return_value="x"):
            with contextlib.redirect_stdout(io.StringIO()):
                dv.build(Path(d) / "out", Path(d) / "work", None, board_path=dv.DEMO / "storyboard-linkedin.json")
        self.assertIn("demo.mp4", events)
        self.assertNotIn("hero.webp", events)
        self.assertEqual([e for e in events if isinstance(e, tuple)], [(s["id"], False) for s in board["scenes"]])

    def test_html_scenes_get_the_captions_of_the_board_being_recorded(self):
        seen = {}

        def fake_scene(browser, url, duration, fps, sink, **k):
            seen[k["scene_id"]] = (duration, k["captions"])
            return 1

        board = dv.load_board(dv.DEMO / "storyboard-linkedin.json")
        org = next(s for s in board["scenes"] if s["id"] == "orgchart")
        with mock.patch.object(dv.demo_cdp, "browser_binary", return_value="x"), \
                mock.patch.object(dv.demo_cdp.Browser, "launch", return_value=mock.Mock()), \
                mock.patch.object(dv.demo_record, "record_scene", fake_scene):
            dv.record_one(org, board, None, Path("w"), lambda i, p: None)
        self.assertEqual(seen["orgchart"], (8, org["captions"]))
        self.assertEqual(org["captions"][-1]["until"], 8.0)

    def test_check_skips_hero_only_for_a_board_without_one(self):
        with tempfile.TemporaryDirectory() as d:
            board = Path(d) / "b.json"
            board.write_text(json.dumps({"scenes": []}))
            rc, out, _ = run_main("check", "--out", d, "--board", str(board))
            self.assertEqual(rc, 1)
            self.assertNotIn("hero.webp", out)
            self.assertIn("FAIL: demo.mp4", out)
            board.write_text(json.dumps({"scenes": [], "hero": {}}))
            _, out, _ = run_main("check", "--out", d, "--board", str(board))
            self.assertIn("FAIL: hero.webp", out)
            _, out, _ = run_main("check", "--out", d, "--board", str(Path(d) / "missing.json"))
            self.assertIn("FAIL: hero.webp", out)  # an unreadable board cannot waive the hero check

    def test_unknown_only_scene(self):
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(dv.BuildError):
                dv.build(Path(d) / "o", Path(d) / "w", None, only="nope")


@unittest.skipUnless(shutil.which("ffmpeg") and shutil.which("ffprobe"), "ffmpeg/ffprobe not installed")
@unittest.skipUnless(os.environ.get("DEMO_BROWSER"), "DEMO_BROWSER (path to chrome-headless-shell) not set")
class Integration(unittest.TestCase):
    def test_draft_problem_clip_has_180_frames(self):
        with tempfile.TemporaryDirectory(dir=ROOT / "docs" / "demo") as d:
            r = subprocess.run([sys.executable, str(ROOT / "ci" / "demo_video.py"), "build", "--draft", "--only", "problem",
                                "--browser", os.environ["DEMO_BROWSER"], "--out", d],
                               capture_output=True, text=True)
            self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
            clip = Path(d) / "work" / "clips" / "02-problem.mp4"
            p = subprocess.run(["ffprobe", "-v", "error", "-select_streams", "v:0", "-count_frames",
                                "-show_entries", "stream=nb_read_frames,r_frame_rate", "-of", "json", str(clip)],
                               capture_output=True, text=True, check=True)
            s = json.loads(p.stdout)["streams"][0]
            self.assertEqual((s["nb_read_frames"], s["r_frame_rate"]), ("180", "30/1"))


if __name__ == "__main__":
    unittest.main()
