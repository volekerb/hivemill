"""Minimal Chrome DevTools Protocol client over --remote-debugging-pipe (stdlib only), for the demo recorder.

The browser reads commands from fd 3 and writes replies and events to fd 4; every message is one JSON document
terminated by a NUL byte. `Pipe` does the framing and id matching on any pair of file descriptors (tests use a
fake pair); `Browser` launches the pinned chrome-headless-shell from ci/screenshots.py and drives one page.
All use of ci/screenshots.py goes through this module.
"""
import base64
import json
import os
import select
import shutil
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import screenshots  # noqa: E402

TIMEOUT = 30  # seconds per call
READ_SIZE = 65536
DEFAULT_WORK = Path(__file__).resolve().parent.parent / "docs" / "demo" / "out" / "work"  # gitignored, on disk
EVENTS_KEPT = 100  # events are only kept for debugging; the list must not grow over a long recording


class CdpError(Exception):
    """The browser answered a command with an error, or the page threw."""


class CdpTimeout(CdpError):
    pass


class CdpClosed(CdpError):
    pass


def browser_binary(work_dir, browser=None):
    """The browser to run: `browser` when given, else the pinned download from ci/screenshots.py (into work_dir)."""
    if browser:
        return Path(browser)
    Path(work_dir).mkdir(parents=True, exist_ok=True)
    return screenshots.fetch_browser(Path(work_dir))


def stop(proc):
    screenshots.stop(proc)
    if proc in screenshots.CHILDREN:
        screenshots.CHILDREN.remove(proc)


class Pipe:
    """NUL-framed JSON messages over a (read fd, write fd) pair, with command id matching."""

    def __init__(self, rfd, wfd, timeout=TIMEOUT):
        self.rfd, self.wfd, self.timeout = rfd, wfd, timeout
        self.buf = b""
        self.next_id = 0
        self.events = []  # events seen while waiting for a reply

    def _message(self, deadline):
        """The next complete message (a dict), reading more bytes as needed."""
        while True:
            if b"\0" in self.buf:
                raw, self.buf = self.buf.split(b"\0", 1)
                if not raw:
                    continue
                try:
                    return json.loads(raw)
                except ValueError as e:
                    raise CdpError("browser sent invalid JSON (%s): %r" % (e, raw[:200]))
            left = deadline - time.monotonic()
            if left <= 0:
                raise CdpTimeout("no reply from the browser within the timeout; it may be hung, restart the run.")
            ready, _, _ = select.select([self.rfd], [], [], left)
            if not ready:
                continue
            chunk = os.read(self.rfd, READ_SIZE)
            if not chunk:
                raise CdpClosed("the browser closed the DevTools pipe (it exited or crashed).")
            self.buf += chunk

    def send(self, method, params=None, session=None, timeout=None):
        self.next_id += 1
        cid = self.next_id
        msg = {"id": cid, "method": method, "params": params or {}}
        if session:
            msg["sessionId"] = session
        data = json.dumps(msg).encode() + b"\0"
        try:
            while data:
                data = data[os.write(self.wfd, data):]
        except OSError as e:
            raise CdpClosed("cannot write to the browser (%s)." % e)
        deadline = time.monotonic() + (self.timeout if timeout is None else timeout)
        while True:
            reply = self._message(deadline)
            if reply.get("id") != cid:
                if "method" in reply:
                    self.events.append(reply)
                    del self.events[:-EVENTS_KEPT]
                continue  # an event or a stale reply
            if "error" in reply:
                err = reply["error"]
                raise CdpError("%s failed: %s (code %s)" % (method, err.get("message"), err.get("code")))
            return reply.get("result", {})


class Browser:
    def __init__(self, proc, pipe, profile):
        self.proc, self.pipe, self.profile = proc, pipe, profile
        self.session = None

    @classmethod
    def launch(cls, binary, width=1920, height=1080, work=None, timeout=TIMEOUT):
        """Start the headless shell with a DevTools pipe. `work` is the directory for the browser profile
        (default docs/demo/out/work, never /tmp: that is RAM). The profile-<pid> dir is removed by close() and when
        launch fails."""
        base = Path(work) if work is not None else DEFAULT_WORK
        base.mkdir(parents=True, exist_ok=True)
        profile = base / ("profile-%d" % os.getpid())
        cmd = [str(binary), "--headless", "--disable-gpu", "--no-sandbox", "--hide-scrollbars",
               "--window-size=%d,%d" % (width, height), "--user-data-dir=" + str(profile),
               "--remote-debugging-pipe", "about:blank"]
        to_browser = os.pipe()    # we write [1], the browser reads [0] as fd 3
        from_browser = os.pipe()  # the browser writes [1] as fd 4, we read [0]

        def child():
            os.dup2(to_browser[0], 3)
            os.dup2(from_browser[1], 4)

        proc = None
        try:
            try:
                proc = screenshots.spawn(cmd, pass_fds=(3, 4), preexec_fn=child)
            finally:
                os.close(to_browser[0])
                os.close(from_browser[1])
            # a signal landing here (spawn returned, no Browser yet) must not leave the process group running
            pipe = Pipe(from_browser[0], to_browser[1], timeout)
            self = cls(proc, pipe, profile)
            self.width, self.height = width, height
            self.scale = 1  # deviceScaleFactor of new pages; set by the recorder before new_page
            return self
        except BaseException:
            os.close(to_browser[1])
            os.close(from_browser[0])
            if proc is not None:
                stop(proc)
            shutil.rmtree(profile, ignore_errors=True)
            raise

    def send(self, method, params=None, session=None, timeout=None):
        return self.pipe.send(method, params, self.session if session is None else session, timeout)

    def new_page(self, url):
        """Open url in a new tab sized to the window and wait for it to finish loading."""
        tid = self.send("Target.createTarget", {"url": "about:blank"}, session="")["targetId"]
        self.session = self.send("Target.attachToTarget", {"targetId": tid, "flatten": True}, session="")["sessionId"]
        self.send("Page.enable")
        self.send("Emulation.setDeviceMetricsOverride", {"width": self.width, "height": self.height,
                                                         "deviceScaleFactor": self.scale, "mobile": False})
        self.send("Page.navigate", {"url": url})
        end = time.monotonic() + self.pipe.timeout
        while self.evaluate("document.readyState") != "complete":
            if time.monotonic() > end:
                raise CdpTimeout("page %s did not finish loading." % url)
            time.sleep(0.05)

    def evaluate(self, expr):
        res = self.send("Runtime.evaluate", {"expression": expr, "returnByValue": True, "awaitPromise": True})
        if "exceptionDetails" in res:
            d = res["exceptionDetails"]
            text = (d.get("exception") or {}).get("description") or d.get("text")
            raise CdpError("page script failed: %s" % text)
        return res["result"].get("value")

    def screenshot(self, fmt="png"):
        res = self.send("Page.captureScreenshot", {"format": fmt})
        return base64.b64decode(res["data"])

    def close(self):
        """Always stop the browser's whole process group and release the pipe and profile."""
        for fd in (self.pipe.rfd, self.pipe.wfd):
            try:
                os.close(fd)
            except OSError:
                pass
        stop(self.proc)
        shutil.rmtree(self.profile, ignore_errors=True)

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        self.close()


def seed_demo(demo, now):
    """Seed the demo home `demo` (ci/screenshots.py seed; its seed-home lands next to it, in demo's parent)."""
    Path(demo).parent.mkdir(parents=True, exist_ok=True)
    screenshots.seed(Path(demo), now)


def serve_demo(demo):
    """Serve the seeded demo on 127.0.0.1 with HOME=<demo>/home FACTORY_HOME=<demo> (screenshots.serve sets both).
    Returns (port, proc); stop the server with stop(proc)."""
    try:
        port = screenshots.serve(Path(demo))
    except screenshots.Failed:
        for proc in list(screenshots.CHILDREN):  # serve spawned the server before it gave up: do not leave it running
            stop(proc)
        raise
    return port, screenshots.CHILDREN[-1]
