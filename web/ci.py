"""Cached CI status for open PRs (G-008 plan 3.10).

`CiCache.get(pr_url)` never blocks and never calls gh itself: a miss (or an expired entry) queues the PR, and one
background worker runs `gh pr view <url> --json state,statusCheckRollup` (argv list, no shell, timeout) for at most
one PR at a time. The worker starts lazily, on the first cache miss (no import-order dependent registration). Any gh failure is cached as state "unknown". Only `max_prs` PRs are tracked at once.
"""
import collections
import json
import subprocess
import threading
import time

FAIL = frozenset({"failure", "failed", "error", "timed_out", "cancelled", "action_required", "startup_failure"})
OK = frozenset({"success", "neutral", "skipped"})


def _checks(rollup):
    out = []
    for c in rollup or []:
        if not isinstance(c, dict):
            continue
        name = c.get("name") or c.get("context") or "?"
        concl = c.get("conclusion") or c.get("state") or c.get("status") or ""
        out.append({"name": str(name)[:80], "conclusion": str(concl).lower() or "pending"})
    return out


def summarize(entry):
    """Short label for a cache entry: green | failing | pending | unknown."""
    if not entry or entry.get("state") == "unknown":
        return "unknown"
    concl = [c["conclusion"] for c in entry.get("checks", [])]
    if any(c in FAIL for c in concl):
        return "failing"
    if not concl or any(c not in OK for c in concl):
        return "pending"
    return "green"


class CiCache:
    def __init__(self, gh="gh", ttl=300, timeout=10, max_prs=20, clock=time.time, autostart=True):
        self.gh, self.ttl, self.timeout, self.max_prs, self.clock, self.autostart = gh, ttl, timeout, max_prs, clock, autostart
        self._lock = threading.Lock()
        self._cv = threading.Condition(self._lock)
        self._entries = {}              # url -> {state, checks, fetched}
        self._seen = {}                 # url -> last time get() asked for it
        self._queue = collections.deque()
        self._queued = set()
        self._run_lock = threading.Lock()   # held by the one worker loop, so at most one gh call is in flight

    def get(self, pr_url):
        """Cached {state, checks, fetched} (possibly expired, never blocking) or None. Queues a refresh when stale."""
        if not pr_url:
            return None
        now = self.clock()
        with self._cv:
            for u in [u for u, t in self._seen.items() if now - t > 2 * self.ttl and u not in self._queued]:
                self._seen.pop(u, None)
                self._entries.pop(u, None)
            if pr_url not in self._seen and len(self._seen) >= self.max_prs:
                return None
            self._seen[pr_url] = now
            e = self._entries.get(pr_url)
            if (e is None or now - e["fetched"] >= self.ttl) and pr_url not in self._queued:
                self._queued.add(pr_url)
                self._queue.append(pr_url)
                self._cv.notify()
                self._ensure_worker()
            return dict(e) if e else None

    def _ensure_worker(self):
        if self.autostart and not self._run_lock.locked():
            threading.Thread(target=self.run, name="ci-cache", daemon=True).start()

    def fetch(self, pr_url):
        """One blocking gh call; returns a cache entry. Every failure becomes state "unknown"."""
        entry = {"state": "unknown", "checks": [], "fetched": self.clock()}
        try:
            r = subprocess.run([self.gh, "pr", "view", pr_url, "--json", "state,statusCheckRollup"],
                               capture_output=True, text=True, timeout=self.timeout, stdin=subprocess.DEVNULL)
            if r.returncode == 0:
                j = json.loads(r.stdout)
                entry["state"] = str(j.get("state") or "unknown").lower()
                entry["checks"] = _checks(j.get("statusCheckRollup"))
        except (OSError, subprocess.SubprocessError, ValueError, AttributeError):
            pass
        return entry

    def step(self, wait=None):
        """Process one queued PR; True if one was handled. Only the worker thread (or a test) calls this."""
        with self._cv:
            if not self._queue:
                self._cv.wait(wait)
            if not self._queue:
                return False
            url = self._queue.popleft()
        entry = self.fetch(url)
        with self._cv:
            self._entries[url] = entry
            self._queued.discard(url)
        return True

    def run(self):
        if not self._run_lock.acquire(blocking=False):
            return
        try:
            while True:
                self.step(wait=60)
        finally:
            self._run_lock.release()


CACHE = CiCache()


def get(pr_url):
    return CACHE.get(pr_url)
