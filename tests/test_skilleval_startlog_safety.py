"""T-0495 follow-up: start.log is written by the shim OUTSIDE the sandbox into a dir the sandboxed claude can write,
so it must never follow a planted symlink (log/ or log/start.log), and a stop signal arriving before stage 1 exists
must stop the start. Fake scripts and dummy token in temp dirs; no namespaces needed."""
import os
import signal
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from harness import REPO  # noqa: E402
import test_skilleval_startlog as base  # noqa: E402  (module import: its test classes are not collected twice)

sys.path.insert(0, str(REPO))
from scout import nsbox, skilleval  # noqa: E402

VICTIM = b"VICTIM-BYTES-" * 8000  # 104 kB: larger than the 64 KiB cap


class SymlinkAppendTests(unittest.TestCase):
    def setUp(self):
        self.base = Path(tempfile.mkdtemp(prefix="zz-test-startlog-safety-")).resolve()
        self.addCleanup(__import__("shutil").rmtree, self.base, True)
        self.trial = self.base / "trial"
        self.trial.mkdir()
        self.victim = self.base / "victim.txt"
        self.victim.write_bytes(VICTIM)

    def test_start_log_symlink_to_a_host_file_is_not_followed(self):
        (self.trial / "log").mkdir()
        (self.trial / "log" / "start.log").symlink_to(self.victim)
        for _ in range(3):
            nsbox.start_log_append(self.trial, "x" * 40000 + "\n")
        self.assertEqual(self.victim.read_bytes(), VICTIM)

    def test_log_dir_symlink_to_a_host_dir_is_not_followed(self):
        vdir = self.base / "vdir"
        vdir.mkdir()
        (vdir / "start.log").write_bytes(VICTIM)
        (self.trial / "log").symlink_to(vdir)
        for _ in range(3):
            nsbox.start_log_append(self.trial, "x" * 40000 + "\n")
        self.assertEqual((vdir / "start.log").read_bytes(), VICTIM)
        self.assertEqual(os.listdir(vdir), ["start.log"])

    def test_regular_log_is_still_written_with_0600_and_capped(self):
        for _ in range(5):
            nsbox.start_log_append(self.trial, "y" * 30000 + "\n")
        log = self.trial / "log" / "start.log"
        self.assertTrue(0 < log.stat().st_size <= base.CAP)
        self.assertEqual(log.stat().st_mode & 0o777, 0o600)

    def test_fifo_start_log_is_refused_without_blocking(self):
        (self.trial / "log").mkdir()
        os.mkfifo(self.trial / "log" / "start.log")
        r = subprocess.run([sys.executable, "-I", "-c",
                            f"import sys; sys.path.insert(0, {str(REPO)!r}); from scout import nsbox; "
                            f"nsbox.start_log_append({str(self.trial)!r}, 'a')"], timeout=20)
        self.assertEqual(r.returncode, 0)

    def test_reader_does_not_show_a_symlinked_log_or_log_dir(self):
        secret = self.base / "secret.txt"
        secret.write_text("HOST-SECRET-CONTENT\nrc=9\n")
        row = {"status": "error", "tokens_in": None, "tokens_out": None, "sandbox": str(self.trial)}
        (self.trial / "log").mkdir()
        (self.trial / "log" / "start.log").symlink_to(secret)
        self.assertEqual(skilleval.start_log_lines(row), [])
        (self.trial / "log" / "start.log").unlink()
        (self.trial / "log").rmdir()
        vdir = self.base / "vdir"
        vdir.mkdir()
        (vdir / "start.log").write_text("HOST-SECRET-CONTENT\nrc=9\n")
        (self.trial / "log").symlink_to(vdir)
        self.assertEqual(skilleval.start_log_lines(row), [])

    def test_reader_still_shows_a_regular_log(self):
        nsbox.start_log_append(self.trial, "hello rc=4\n")
        row = {"status": "error", "tokens_in": None, "tokens_out": None, "sandbox": str(self.trial)}
        self.assertIn("    hello rc=4", skilleval.start_log_lines(row))


class StopBeforeStartTests(base.ShimBase):
    def run_driver(self, sig):
        marker = self.base / "stage1-ran"
        self.claude.write_text(f"#!/bin/sh\ntouch {marker}\n")
        cfg = self.shim_cfg()
        code = (f"import os, signal, sys; sys.path.insert(0, {str(REPO)!r}); from scout import nsbox\n"
                f"nsbox.start_proxy = lambda *a: os.kill(os.getpid(), {int(sig)})\n"
                f"nsbox.shim_main({cfg!r}, [])\n")
        p = subprocess.run([sys.executable, "-I", "-c", code], capture_output=True, text=True, timeout=60,
                           env={"PATH": os.environ["PATH"], "HOME": str(self.home)})
        self.addCleanup(nsbox.stop_proxy, str(self.trial))
        return p, marker

    def test_sigterm_during_start_stops_the_shim_before_stage1(self):
        p, marker = self.run_driver(signal.SIGTERM)
        self.assertEqual(p.returncode, 128 + signal.SIGTERM, p.stderr)
        self.assertFalse(marker.exists(), "stage 1 started after a stop signal")
        self.assertIn("stopped by signal 15", self.log_text())

    def test_sighup_during_start_stops_the_shim_before_stage1(self):
        p, marker = self.run_driver(signal.SIGHUP)
        self.assertEqual(p.returncode, 128 + signal.SIGHUP, p.stderr)
        self.assertFalse(marker.exists())


class StopDuringTokenReadTests(base.ShimBase):
    def run_driver(self, sig):
        marker = self.base / "stage1-ran"
        proxy_marker = self.base / "proxy-called"
        self.claude.write_text(f"#!/bin/sh\ntouch {marker}\n")
        cfg = self.shim_cfg()
        code = (f"import os, signal, sys; sys.path.insert(0, {str(REPO)!r}); from scout import nsbox\n"
                f"real = nsbox.read_token\n"
                f"def rt(path):\n    t = real(path)\n    os.kill(os.getpid(), {int(sig)})\n    return t\n"
                f"nsbox.read_token = rt\n"
                f"rp = nsbox.start_proxy\n"
                f"def sp(*a):\n    open({str(proxy_marker)!r}, 'w').close()\n    return rp(*a)\n"
                f"nsbox.start_proxy = sp\n"
                f"nsbox.shim_main({cfg!r}, [])\n")
        p = subprocess.run([sys.executable, "-I", "-c", code], capture_output=True, text=True, timeout=60,
                           env={"PATH": os.environ["PATH"], "HOME": str(self.home)})
        return p, marker, proxy_marker

    def test_sigterm_during_read_token_never_starts_the_proxy(self):
        p, marker, proxy_marker = self.run_driver(signal.SIGTERM)
        self.addCleanup(nsbox.stop_proxy, str(self.trial))
        self.assertEqual(p.returncode, 128 + signal.SIGTERM, p.stderr)
        self.assertFalse(proxy_marker.exists(), "proxy started after a stop signal")
        self.assertFalse(marker.exists())
        self.assertFalse((self.trial / "proxy.pid").exists())
        self.assertFalse((self.trial / "proxy.sock").exists())
        self.assertIn("stopped by signal 15", self.log_text())


class ForwardedSignalTests(base.ShimBase):
    def test_quit_usr1_usr2_reach_stage1(self):
        for sig in (signal.SIGQUIT, signal.SIGUSR1, signal.SIGUSR2):
            with self.subTest(sig=sig.name):
                got = self.base / f"got-{sig.name}"
                ready = self.base / f"ready-{sig.name}"
                self.fake_unshare.write_text(  # stage 1 is whatever unshare runs
                    f"#!/bin/sh\ntrap 'touch {got}; exit 0' {sig.name[3:]}\ntouch {ready}\n"
                    "while :; do sleep 0.05; done\n")
                cfg = self.shim_cfg()
                code = (f"import sys; sys.path.insert(0, {str(REPO)!r}); from scout import nsbox\n"
                        "nsbox.start_proxy = lambda *a: 0\n"
                        f"nsbox.shim_main({cfg!r}, [])\n")
                p = subprocess.Popen([sys.executable, "-I", "-c", code], stderr=subprocess.PIPE,
                                     env={"PATH": os.environ["PATH"], "HOME": str(self.home)})
                for _ in range(200):
                    if ready.exists():
                        break
                    __import__("time").sleep(0.05)
                else:
                    p.kill()
                    self.fail("stage 1 never started")
                p.send_signal(sig)
                p.wait(timeout=20)
                self.assertTrue(got.exists(), f"{sig.name} did not reach stage 1")


class TeeBoundedTests(unittest.TestCase):
    """The tee reads the sandbox-controlled stderr of claude: memory must not grow with newline-free input."""
    FAKE = "sk-ant-oat01-" + "Q" * 40  # fake: 53 chars

    def setUp(self):
        self.base = Path(tempfile.mkdtemp(prefix="zz-test-startlog-tee-")).resolve()
        self.addCleanup(__import__("shutil").rmtree, self.base, True)
        self.trial = self.base / "trial"
        self.trial.mkdir()

    def drive(self, child_code):
        code = (f"import resource, subprocess, sys; sys.path.insert(0, {str(REPO)!r}); from scout import nsbox\n"
                f"p = subprocess.Popen([sys.executable, '-c', {child_code!r}], stderr=subprocess.PIPE)\n"
                "before = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss; import time; t0 = time.process_time()\n"
                f"nsbox._tee_stderr(p.stderr, {str(self.trial)!r}, {self.FAKE!r})\n"
                "p.wait()\n"
                "print(resource.getrusage(resource.RUSAGE_SELF).ru_maxrss - before, time.process_time() - t0)\n")
        r = subprocess.run([sys.executable, "-I", "-c", code], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                           timeout=120)
        self.assertEqual(r.returncode, 0)
        kb, cpu = r.stdout.split()
        self.cpu = float(cpu)
        return int(kb)

    def test_50_mb_without_a_newline_stays_in_bounded_memory_and_under_the_cap(self):
        kb = self.drive("import os\nfor _ in range(50 * 1024): os.write(2, b'z' * 1024)")
        self.assertLess(kb, 64 * 1024, f"maxrss grew by {kb} KiB")  # ru_maxrss is KiB on Linux
        log = self.trial / "log" / "start.log"
        self.assertLessEqual(log.stat().st_size, base.CAP)
        self.assertEqual(log.read_text().count("[truncated]"), 1)

    def test_a_token_split_across_a_4096_byte_boundary_is_redacted(self):
        child = (f"import os\nos.write(2, b'a' * (4096 - 20) + {self.FAKE!r}.encode() + b'b' * 5000)")
        self.drive(child)
        text = (self.trial / "log" / "start.log").read_text()
        self.assertNotIn("Q" * 5, text)
        self.assertNotIn("sk-ant-oat01", text)
        self.assertIn("[redacted-token]", text)

    def test_the_exact_token_split_at_every_offset_of_a_4096_byte_boundary_is_absent(self):
        code = (f"import os, sys, tempfile; sys.path.insert(0, {str(REPO)!r}); from scout import nsbox\n"
                f"tok = {self.FAKE!r}; devnull = os.open(os.devnull, os.O_WRONLY); os.dup2(devnull, 2)\n"
                "bad = []\n"
                "for pad in range(4096 - len(tok) - 3, 4096 + 3):\n"
                "    trial = tempfile.mkdtemp(prefix='zz-test-startlog-split-')\n"
                "    src = os.path.join(trial, 'in'); open(src, 'wb').write(b'a' * pad + tok.encode() + b'b' * 6000)\n"
                "    with open(src, 'rb') as f: nsbox._tee_stderr(f, trial, tok)\n"
                "    text = open(os.path.join(trial, 'log', 'start.log')).read()\n"
                "    if 'Q' * 5 in text or 'sk-ant-oat01' in text or text.count('[redacted-token]') != 1: bad.append(pad)\n"
                "print(bad)\n")
        r = subprocess.run([sys.executable, "-I", "-c", code], stdout=subprocess.PIPE, timeout=120)
        self.assertEqual(r.stdout.strip(), b"[]", r.stdout)

    def test_endless_token_shaped_input_is_linear_in_time_and_memory_at_2_4_8_mib(self):
        for mib in (2, 4, 8):
            code = ("import os, time\nos.write(2, b'sk-ant-')\n"
                    f"for _ in range({mib} * 1024): os.write(2, b'A' * 1024)")
            kb = self.drive(code)
            print(f"tee {mib} MiB: cpu {self.cpu:.3f}s rss +{kb} KiB", file=sys.__stderr__)
            self.assertLess(kb, 64 * 1024, f"{mib} MiB: maxrss grew by {kb} KiB")
            # the log holds the stream up to the cap (shapes are only redacted when displayed), never more
            self.assertLessEqual((self.trial / "log" / "start.log").stat().st_size, base.CAP)
            self.assertLess(self.cpu, 1.0)  # the shape-aware carry took 0.9 / 3.5 / 13.6 s here


class ControlCharTests(unittest.TestCase):
    def test_escape_and_c0_characters_are_stripped_from_the_displayed_log(self):
        with tempfile.TemporaryDirectory(prefix="zz-test-startlog-ctrl-") as d:
            trial = Path(d)
            nsbox.start_log_append(trial, "a\x1b[2Jb\x07c\td\x00e\rrc=2\n")
            row = {"status": "error", "tokens_in": None, "tokens_out": None, "sandbox": str(trial)}
            out = "\n".join(skilleval.start_log_lines(row))
        for bad in ("\x1b", "\x07", "\x00", "\r"):
            self.assertNotIn(bad, out)
        self.assertIn("a[2Jbc\tde", out)

    def test_token_shape_split_by_a_control_character_is_still_redacted(self):
        with tempfile.TemporaryDirectory(prefix="zz-test-startlog-ctrl-") as d:
            trial = Path(d)
            nsbox.start_log_append(trial, "boom sk-ant-\x1bAAAAAAAAAAAAAAAAAAAAAAAA tail\nrc=2\n")
            row = {"status": "error", "tokens_in": None, "tokens_out": None, "sandbox": str(trial)}
            out = "\n".join(skilleval.start_log_lines(row))
        self.assertNotIn("sk-ant-", out)
        self.assertIn("[redacted-token]", out)


if __name__ == "__main__":
    unittest.main()
