"""`factory serve` re-executes itself when web/**/*.py or bin/factory change (temp checkout, temp FACTORY_HOME, --port 0)."""
import http.client
import os
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "tests"))
sys.path.insert(0, str(ROOT))

from web import server  # noqa: E402


def _healthz(port):
    c = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    try:
        c.putrequest("GET", "/healthz", skip_host=True)
        c.putheader("Host", f"127.0.0.1:{port}")
        c.endheaders()
        r = c.getresponse()
        return r.status, r.read().decode()
    finally:
        c.close()


class ReloadCase(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="serve-reload-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.co = self.tmp / "checkout"
        (self.co / "bin").mkdir(parents=True)
        shutil.copy(ROOT / "bin" / "factory", self.co / "bin" / "factory")
        for d in ("web", "scout"):
            shutil.copytree(ROOT / d, self.co / d, ignore=shutil.ignore_patterns("__pycache__"))
        self.home = self.tmp / "home"
        self.env = {**os.environ, "FACTORY_HOME": str(self.home), "FACTORY_SERVE_RELOAD_INTERVAL": "0.1",
                    "PYTHONDONTWRITEBYTECODE": "1"}
        self.env.update(FACTORY_AGENT="owner", FACTORY_ROLE="owner")
        subprocess.run([sys.executable, str(self.co / "bin" / "factory"), "init"], env=self.env, check=True,
                       capture_output=True, text=True)
        self.proc = subprocess.Popen([sys.executable, str(self.co / "bin" / "factory"), "serve", "--bind", "127.0.0.1",
                                      "--port", "0"], env=self.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                     text=True, cwd=self.co)
        self.addCleanup(self._stop)
        self.lines = []   # --port 0 picks a new port at every re-exec: the latest URL line is the live one
        self.reader = threading.Thread(target=lambda: [self.lines.append(ln) for ln in self.proc.stdout], daemon=True)
        self.reader.start()
        self._wait(lambda: self.port, "the listener")
        self.assertEqual(_healthz(self.port)[0], 200)

    @property
    def port(self):
        urls = [m.group(1) for m in (re.search(r"http://[^:]+:(\d+)/", ln) for ln in list(self.lines)) if m]
        return int(urls[-1]) if urls else None

    def _stop(self):
        self.proc.terminate()
        try:
            self.proc.wait(10)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait()
        self.reader.join(5)
        self.proc.stdout.close()
        self.proc.stderr.close()

    def _edit(self, rel, old, new):
        f = self.co / rel
        f.write_text(f.read_text().replace(old, new, 1))
        os.utime(f, (time.time() + 5, time.time() + 5))   # guarantee a different mtime

    def _wait(self, pred, what):
        end = time.monotonic() + 20
        while time.monotonic() < end:
            try:
                if pred():
                    return
            except (OSError, http.client.HTTPException):
                pass   # listener is briefly closed during the re-exec
            time.sleep(0.05)
        self.fail(f"timed out waiting for {what}")

    def test_web_change_served_with_same_pid(self):
        pid = self.proc.pid
        self._edit("web/server.py", 'f"ok\\ndb_mtime_age_s', 'f"reloaded\\ndb_mtime_age_s')
        self._wait(lambda: _healthz(self.port)[1].startswith("reloaded"), "new web code")
        self.assertIsNone(self.proc.poll())
        self.assertEqual(self.proc.pid, pid)
        self.assertTrue(any("re-executing" in ln for ln in self.lines))

    def test_bin_factory_change_is_picked_up(self):
        """The T-0159 incident: the DB path moved in bin/factory; the running server must follow it."""
        shutil.move(str(self.home / "db" / "factory.db"), str(self.home / "moved.db"))
        self.assertEqual(_healthz(self.port)[0], 503)
        self._edit("bin/factory", "    return dbpath.resolve(HOME)\n", '    return HOME / "moved.db"\n')
        self._wait(lambda: _healthz(self.port)[0] == 200, "bin/factory change")

    def test_broken_change_keeps_old_code_and_logs_once(self):
        self._edit("web/server.py", 'f"ok\\ndb_mtime_age_s', "f\"ok\\ndb_mtime_age_s")
        f = self.co / "web" / "server.py"
        f.write_text(f.read_text() + "\ndef broken(:\n")
        os.utime(f, (time.time() + 10, time.time() + 10))
        time.sleep(0.6)   # several check intervals
        self.assertIsNone(self.proc.poll())
        self.assertEqual(_healthz(self.port)[0], 200)
        self._stop_and_collect()

    def _stop_and_collect(self):
        self.proc.terminate()
        self.proc.wait(10)
        err = self.proc.stderr.read()
        self.assertEqual(err.count("does not import, still serving the old code"), 1, err)

    def test_fixed_change_after_broken_one_reloads(self):
        f = self.co / "web" / "server.py"
        good = f.read_text()
        f.write_text(good + "\ndef broken(:\n")
        os.utime(f, (time.time() + 5, time.time() + 5))
        time.sleep(0.4)  # several check intervals
        f.write_text(good.replace('f"ok\\ndb_mtime_age_s', 'f"fixed\\ndb_mtime_age_s', 1))
        os.utime(f, (time.time() + 10, time.time() + 10))
        self._wait(lambda: _healthz(self.port)[1].startswith("fixed"), "retry after the fix")


class SignatureCase(unittest.TestCase):
    def test_import_check_rejects_broken_and_accepts_good(self):
        with tempfile.TemporaryDirectory() as d:
            co = Path(d)
            (co / "bin").mkdir()
            shutil.copy(ROOT / "bin" / "factory", co / "bin" / "factory")
            for d in ("web", "scout"):
                shutil.copytree(ROOT / d, co / d, ignore=shutil.ignore_patterns("__pycache__"))
            self.assertIsNone(server.import_check(co))
            (co / "bin" / "factory").write_text("def (:\n")
            self.assertIn("SyntaxError", server.import_check(co))

    def test_signature_changes_with_edit(self):
        with tempfile.TemporaryDirectory() as d:
            co = Path(d)
            (co / "bin").mkdir()
            (co / "web").mkdir()
            (co / "bin" / "factory").write_text("x")
            (co / "web" / "a.py").write_text("x")
            a = server.code_signature(co)
            (co / "web" / "a.py").write_text("xy")
            self.assertNotEqual(a, server.code_signature(co))


if __name__ == "__main__":
    unittest.main()
