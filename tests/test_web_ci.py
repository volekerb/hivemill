"""Tests for web/ci.py: TTL, the PR cap, one gh call in flight, failures, and a request that never waits on gh."""
import json
import os
import stat
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from web import ci  # noqa: E402

PR = "https://github.com/o/r/pull/%d"
GOOD = {"state": "OPEN", "statusCheckRollup": [{"name": "unittest", "conclusion": "SUCCESS", "status": "COMPLETED"}]}


class FakeGh(unittest.TestCase):
    """A fake gh script in a temp dir; every call is appended to calls.log, behaviour comes from env-free files."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name)
        self.log = self.dir / "calls.log"
        self.gh = self.dir / "gh"
        self.script(GOOD)

    def script(self, payload, sleep=0, code=0):
        self.gh.write_text(f"""#!/usr/bin/env python3
import json, sys, time
open({str(self.log)!r}, "a").write(json.dumps(sys.argv[1:]) + "\\n")
time.sleep({sleep})
print({json.dumps(json.dumps(payload))})
sys.exit({code})
""")
        self.gh.chmod(self.gh.stat().st_mode | stat.S_IXUSR)

    def calls(self):
        return [json.loads(l) for l in self.log.read_text().splitlines()] if self.log.exists() else []

    def cache(self, **kw):
        self.now = 1000.0
        return ci.CiCache(gh=str(self.gh), clock=lambda: self.now, autostart=False, **kw)


class TestCiCache(FakeGh):
    def test_miss_returns_none_then_fills(self):
        c = self.cache()
        self.assertIsNone(c.get(PR % 1))
        self.assertTrue(c.step())
        e = c.get(PR % 1)
        self.assertEqual(e["state"], "open")
        self.assertEqual(e["checks"], [{"name": "unittest", "conclusion": "success"}])
        self.assertEqual(ci.summarize(e), "green")

    def test_argv_list_without_shell(self):
        c = self.cache()
        c.get(PR % 1)
        c.step()
        self.assertEqual(self.calls(), [["pr", "view", PR % 1, "--json", "state,statusCheckRollup"]])

    def test_ttl_no_second_call_within_300s(self):
        c = self.cache()
        c.get(PR % 1)
        c.step()
        self.now += 299
        self.assertIsNotNone(c.get(PR % 1))
        self.assertFalse(c.step(wait=0))
        self.assertEqual(len(self.calls()), 1)
        self.now += 2   # past the ttl: stale value is still served, a refresh is queued
        self.assertIsNotNone(c.get(PR % 1))
        self.assertTrue(c.step())
        self.assertEqual(len(self.calls()), 2)

    def test_queued_pr_is_not_queued_twice(self):
        c = self.cache()
        for _ in range(5):
            c.get(PR % 1)
        self.assertTrue(c.step())
        self.assertFalse(c.step(wait=0))
        self.assertEqual(len(self.calls()), 1)

    def test_cap_of_20_prs(self):
        c = self.cache()
        for n in range(30):
            c.get(PR % n)
        while c.step(wait=0):
            pass
        self.assertEqual(len(self.calls()), 20)
        self.assertIsNone(c.get(PR % 25))
        self.assertIsNotNone(c.get(PR % 3))

    def test_cap_frees_up_when_old_prs_stop_being_asked_for(self):
        c = self.cache(max_prs=2)
        c.get(PR % 1); c.get(PR % 2); c.get(PR % 3)
        while c.step(wait=0):
            pass
        self.assertEqual(len(self.calls()), 2)
        self.now += 2 * 300 + 1
        c.get(PR % 3)
        self.assertTrue(c.step())
        self.assertEqual(self.calls()[-1][2], PR % 3)

    def test_failure_is_unknown(self):
        self.script({}, code=1)
        c = self.cache()
        c.get(PR % 1)
        c.step()
        e = c.get(PR % 1)
        self.assertEqual(e["state"], "unknown")
        self.assertEqual(ci.summarize(e), "unknown")

    def test_missing_gh_is_unknown(self):
        c = ci.CiCache(gh=str(self.dir / "nope"), autostart=False)
        c.get(PR % 1)
        c.step()
        self.assertEqual(ci.summarize(c.get(PR % 1)), "unknown")

    def test_garbage_output_is_unknown(self):
        self.gh.write_text("#!/bin/sh\necho not-json\n")
        c = self.cache()
        c.get(PR % 1)
        c.step()
        self.assertEqual(ci.summarize(c.get(PR % 1)), "unknown")

    def test_timeout_is_unknown(self):
        self.script(GOOD, sleep=3)
        c = self.cache(timeout=0.3)
        c.get(PR % 1)
        t = time.time()
        c.step()
        self.assertLess(time.time() - t, 2)
        self.assertEqual(ci.summarize(c.get(PR % 1)), "unknown")

    def test_summaries(self):
        mk = lambda *cs: {"state": "open", "checks": [{"name": "x", "conclusion": c} for c in cs]}
        self.assertEqual(ci.summarize(mk("success", "skipped")), "green")
        self.assertEqual(ci.summarize(mk("success", "failure")), "failing")
        self.assertEqual(ci.summarize(mk("success", "in_progress")), "pending")
        self.assertEqual(ci.summarize(mk()), "pending")
        self.assertEqual(ci.summarize(None), "unknown")

    def test_get_never_waits_on_slow_gh(self):
        self.script(GOOD, sleep=2)
        c = ci.CiCache(gh=str(self.gh))
        t = time.time()
        for n in range(5):
            self.assertIsNone(c.get(PR % n))
        self.assertLess(time.time() - t, 0.5)   # the on-demand worker thread is the one that waits

    def test_at_most_one_gh_call_in_flight(self):
        self.gh.write_text(f"""#!/usr/bin/env python3
import os, sys, time
d = {str(self.dir)!r}
open(d + "/running", "a").write("x")
n = len(open(d + "/running").read())
open(d + "/max", "a").write(str(n) + "\\n")
time.sleep(0.2)
open(d + "/running", "w").write("x" * (n - 1))
print('{{"state": "OPEN", "statusCheckRollup": []}}')
""")
        self.gh.chmod(0o755)
        c = ci.CiCache(gh=str(self.gh))
        threads = [threading.Thread(target=c.run, daemon=True) for _ in range(3)]   # extra loops return at once
        for t in threads:
            t.start()
        for n in range(4):
            c.get(PR % n)
        deadline = time.time() + 10
        while time.time() < deadline and len(c._entries) < 4:
            time.sleep(0.05)
        self.assertEqual(len(c._entries), 4)
        self.assertEqual({l for l in (self.dir / "max").read_text().split()}, {"1"})

    def test_worker_starts_lazily_on_first_miss(self):
        # no import-time registration: the worker thread appears on the first get() and only once
        count = lambda: [t.name for t in threading.enumerate()].count("ci-cache")
        before = count()
        c = ci.CiCache(gh=str(self.gh))
        self.assertEqual(count(), before)       # constructing starts nothing
        c.get(PR % 1)
        deadline = time.time() + 10
        while time.time() < deadline and PR % 1 not in c._entries:
            time.sleep(0.05)
        self.assertIn(PR % 1, c._entries)
        self.assertEqual(count(), before + 1)

    def test_import_does_not_touch_server_background(self):
        from web import server
        self.assertNotIn(ci.CACHE.run, server.background)


class TestStubGh(unittest.TestCase):
    """The shared tests/stubs/gh on PATH, as the harness does."""

    def test_stub_gh(self):
        with tempfile.TemporaryDirectory() as d:
            state, calls = Path(d) / "state.json", Path(d) / "calls.jsonl"
            state.write_text(json.dumps({"prs": {PR % 7: GOOD}}))
            old = {k: os.environ.get(k) for k in ("FAKE_GH_STATE", "FAKE_CALLS")}
            os.environ.update(FAKE_GH_STATE=str(state), FAKE_CALLS=str(calls))
            self.addCleanup(lambda: [os.environ.pop(k, None) if v is None else os.environ.__setitem__(k, v) for k, v in old.items()])
            c = ci.CiCache(gh=str(ROOT / "tests" / "stubs" / "gh"), autostart=False)
            c.get(PR % 7)
            c.get(PR % 8)   # not in the stub: gh fails
            c.step(); c.step()
            self.assertEqual(ci.summarize(c.get(PR % 7)), "green")
            self.assertEqual(ci.summarize(c.get(PR % 8)), "unknown")


if __name__ == "__main__":
    unittest.main()
