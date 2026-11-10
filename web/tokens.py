"""Incremental index of Claude Code token usage from ~/.claude/projects/**/*.jsonl, plus attribution to factory
agents/tasks/goals. Stdlib only. Only usage numbers, ids, timestamps, model, agent name, cwd and the cost-state
total are ever read out of a log line; message content, prompts and attachments are never kept.

Memory: aggregates only. A usage row is ~6 ints + key tuple, roughly 400 bytes per (session, model, day, fast)
row; the dedupe key set costs ~150 bytes per unique request until the file has been idle for 48 h.
"""
import datetime as dt
import json
import os
import re
import sqlite3
import stat
import sys
import threading
import time
from pathlib import Path

from scout.workerbox import WORKERS_ROOT
from web.prices import cost

COUNTERS = ("input", "output", "cache_read", "cache_write_5m", "cache_write_1h", "requests")
IDLE_DROP_SECONDS = 48 * 3600
MAX_READ_BYTES = 64 << 20    # most bytes read from one file in one refresh; the rest is picked up on later passes
TASK_RE = re.compile(r"worktrees[-/](G-\d+)-(T-\d+)")


def _int(v):
    return v if isinstance(v, int) and not isinstance(v, bool) else 0


def _parse_ts(s):
    """Parse an ISO timestamp (UTC 'Z' in logs, local offset in the DB); naive values count as local time."""
    try:
        t = dt.datetime.fromisoformat(s)
    except (TypeError, ValueError):
        return None
    return t.astimezone() if t.tzinfo is None else t


def _local_day(ts):
    t = _parse_ts(ts)
    return t.astimezone().date().isoformat() if t else "unknown"


class _FileState:
    __slots__ = ("session", "project", "sub", "inode", "offset", "mtime", "contrib", "keys",
                 "first_ts", "last_ts", "agent_name", "cwd", "cost_state", "skipping")

    def __init__(self, session, project, sub):
        self.session, self.project, self.sub = session, project, sub
        self.inode = None
        self.offset = 0
        self.mtime = 0.0
        self.reset()

    def reset(self):
        self.offset = 0
        self.skipping = False  # inside an over-long line already reported: drop bytes up to the next newline
        self.contrib = {}      # (model, day, fast) -> [input, output, cache_read, w5m, w1h, requests]
        self.keys = set()
        self.first_ts = self.last_ts = self.agent_name = self.cwd = self.cost_state = None


class TokenIndex:
    def __init__(self, root=None, extra_roots=(), workers_root=None, only_session=None):
        """`root` is the one projects dir (default ~/.claude/projects); `extra_roots` are more projects dirs scanned
        the same way. Sandboxed workers write to <workers_root>/<agent>/claude/projects; with no explicit `root`
        those are scanned too (workers_root defaults to scout.workerbox.WORKERS_ROOT), with one, only if
        `workers_root` is given. `only_session` indexes just that session's files (the tick's budget check), not every log."""
        self.only_session = only_session
        self.root = Path(root) if root else Path.home() / ".claude" / "projects"
        self.extra_roots = [Path(r) for r in extra_roots]
        if workers_root is None and root is None:
            workers_root = WORKERS_ROOT
        self.workers_root = Path(workers_root) if workers_root else None
        self._files = {}                 # path -> _FileState
        self._owner = {}                 # session -> {dedupe key: path}
        self._refresh_lock = threading.Lock()
        self._swap_lock = threading.Lock()
        self._snap = {"ready": False, "last_scan": None, "files": 0, "bytes_read": 0, "bytes_total": 0,
                      "errors": 0, "rows": [], "sessions": {}}
        self._errors = 0
        self._bytes_total = 0
        self._thread = None

    # ── scanning ──
    @staticmethod
    def _discover_root(root, confine=None, sid=None):
        """(path, session, project, is_subagent, confine) for every transcript under `root`. With `confine` (a worker controls
        its own config dir) a file is kept only if it is a regular file (one link) whose realpath is inside `confine`. With `sid`
        only that session's files are listed."""
        out = []
        try:
            for proj in root.iterdir():
                if not proj.is_dir():
                    continue
                found = [(f, f.stem, proj.name, False, confine) for f in proj.glob(f"{sid}.jsonl" if sid else "*.jsonl")]
                found += [(f, f.parent.parent.name, proj.name, True, confine) for f in proj.glob(f"{sid}/subagents/*.jsonl" if sid else "*/subagents/*.jsonl")]
                for item in found:
                    if confine is None or TokenIndex._inside(item[0], confine):
                        out.append(item)
        except OSError:
            pass
        return out

    @staticmethod
    def _inside(path, confine):
        try:
            real = os.path.realpath(path)
            st = os.stat(real)
            if not stat.S_ISREG(st.st_mode) or st.st_nlink > 1:
                return False
            return os.path.commonpath([real, confine]) == confine
        except (OSError, ValueError):
            return False

    @staticmethod
    def _open_confined(path, confine):
        """Open a worker file for reading without following a symlink at the last component, then re-check on the open fd
        that it is a regular file whose real location is still inside `confine`. Discover-time checks go stale if the
        worker swaps the file or a parent dir afterwards; this one cannot. Returns the file object, or None."""
        try:
            fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        except OSError:
            return None
        try:
            st = os.fstat(fd)
            if not stat.S_ISREG(st.st_mode):
                raise OSError("not a regular file")
            if st.st_nlink > 1:
                raise OSError("hardlinked file")   # a link to a file outside confine has an inside realpath
            real = os.path.realpath(f"/proc/self/fd/{fd}")
            if os.path.commonpath([real, confine]) != confine:
                raise OSError("outside confinement")
            return os.fdopen(fd, "rb")
        except (OSError, ValueError):
            os.close(fd)
            return None

    def _worker_roots(self):
        """[(projects dir, confine realpath)] for each <workers_root>/<agent>/claude/projects that exists. The agent dir
        itself must resolve inside the workers root, and files inside that agent's own dir."""
        out = []
        if self.workers_root is None:
            return out
        try:
            base = os.path.realpath(self.workers_root)
            for agent in sorted(self.workers_root.iterdir()):
                proj = agent / "claude" / "projects"
                if not proj.is_dir():
                    continue
                real_agent = os.path.realpath(agent)
                if os.path.commonpath([real_agent, base]) != base or real_agent == base:
                    continue
                out.append((proj, os.path.join(real_agent, "claude", "projects")))
        except (OSError, ValueError):
            pass
        return out

    def _discover(self):
        out = self._discover_root(self.root, sid=self.only_session)
        for r in self.extra_roots:
            out += self._discover_root(r, sid=self.only_session)
        for proj, confine in self._worker_roots():
            out += self._discover_root(proj, confine, self.only_session)
        return out

    def refresh(self):
        with self._refresh_lock:
            bytes_read = 0
            now = time.time()
            seen = set()
            for path, session, project, sub, confine in self._discover():
                seen.add(path)
                f = None
                try:
                    if confine is not None:
                        f = self._open_confined(path, confine)
                        if f is None:
                            self._errors += 1
                            seen.discard(path)
                            continue
                        st = os.fstat(f.fileno())
                    else:
                        st = path.stat()
                    fs = self._files.get(path)
                    if fs is None:
                        fs = self._files[path] = _FileState(session, project, sub)
                    if fs.inode is not None and (st.st_ino != fs.inode or st.st_size < fs.offset):
                        self._drop_file(path, fs)
                        fs.reset()
                    fs.inode = st.st_ino
                    if st.st_size > fs.offset:
                        bytes_read += self._read(path, fs, st.st_size, f)
                        fs.mtime = st.st_mtime
                    if fs.keys and now - st.st_mtime > IDLE_DROP_SECONDS:
                        self._forget_keys(path, fs)
                except OSError:
                    continue
                finally:
                    if f is not None:
                        f.close()
            for path in set(self._files) - seen:
                self._drop_file(path, self._files[path])
                del self._files[path]
            self._bytes_total += bytes_read
            snap = self._build(bytes_read)
            with self._swap_lock:
                self._snap = snap

    def _forget_keys(self, path, fs):
        owner = self._owner.get(fs.session)
        if owner:
            for k in fs.keys:
                if owner.get(k) == path:
                    del owner[k]
        fs.keys = set()

    def _drop_file(self, path, fs):
        self._forget_keys(path, fs)

    def _read(self, path, fs, size, f=None):
        """Read at most MAX_READ_BYTES of the unread tail (a bigger tail is consumed over several passes, never held
        whole in memory). `f` is an already-open fd (worker files); otherwise the path is opened here."""
        want = min(size - fs.offset, MAX_READ_BYTES)
        try:
            if f is None:
                with open(path, "rb") as fh:
                    fh.seek(fs.offset)
                    data = fh.read(want)
            else:
                f.seek(fs.offset)
                data = f.read(want)
        except OSError:
            self._errors += 1
            return 0
        if fs.skipping:
            nl = data.find(b"\n")
            if nl < 0:
                fs.offset += len(data)
                return len(data)
            fs.skipping = False
            fs.offset += nl + 1
            return nl + 1
        end = data.rfind(b"\n")
        if end < 0:
            if size - fs.offset <= MAX_READ_BYTES:
                return 0                 # only a partial line so far; re-read next pass
            print(f"web.tokens: {path}: line longer than {MAX_READ_BYTES} bytes at offset {fs.offset}, skipped",
                  file=sys.stderr)
            self._errors += 1
            fs.skipping = True
            fs.offset += len(data)
            return len(data)
        owner = self._owner.setdefault(fs.session, {})
        pos = 0
        while pos <= end:
            nl = data.find(b"\n", pos, end)
            if nl < 0:
                nl = end
            line = data[pos:nl]    # one line at a time; never a copy of the whole chunk
            pos = nl + 1
            if b'"usage"' in line:
                kind = "usage"
            elif b'"agent-name"' in line:
                kind = "agent-name"
            elif b'"cost-state"' in line:
                kind = "cost-state"
            else:
                continue
            try:
                d = json.loads(line)
            except ValueError:
                self._errors += 1
                continue
            if not isinstance(d, dict):
                continue
            t = d.get("type")
            if t == "assistant" and kind == "usage":
                self._usage(path, fs, owner, d)
            elif t == "agent-name" and isinstance(d.get("agentName"), str):
                if fs.agent_name is None:
                    fs.agent_name = d["agentName"]
            elif t == "cost-state":
                total = d.get("totalCostUSD")
                if isinstance(total, (int, float)):
                    fs.cost_state = total
        fs.offset += end + 1
        return end + 1

    def _usage(self, path, fs, owner, d):
        msg = d.get("message")
        u = msg.get("usage") if isinstance(msg, dict) else None
        if not isinstance(u, dict):
            return
        mid, rid = msg.get("id"), d.get("requestId")
        key = (mid, rid) if mid and rid else d.get("uuid")
        if key is not None:
            if key in owner:
                return
            owner[key] = path
            fs.keys.add(key)
        ts = d.get("timestamp")
        t = _parse_ts(ts)
        if t is not None:
            tu = t.astimezone(dt.timezone.utc).isoformat(timespec="seconds")
            if fs.first_ts is None or tu < fs.first_ts:
                fs.first_ts = tu
            if fs.last_ts is None or tu > fs.last_ts:
                fs.last_ts = tu
        if fs.cwd is None and isinstance(d.get("cwd"), str):
            fs.cwd = d["cwd"]
        cc = u.get("cache_creation")
        cc = cc if isinstance(cc, dict) else {}
        w1h = _int(cc.get("ephemeral_1h_input_tokens"))
        w5m = _int(cc.get("ephemeral_5m_input_tokens"))
        total_w = _int(u.get("cache_creation_input_tokens"))
        if w5m + w1h < total_w:          # no / partial split: the remainder is billed as 5m
            w5m = total_w - w1h
        model = msg.get("model") if isinstance(msg.get("model"), str) else "unknown"
        fast = u.get("speed") == "fast"
        row = fs.contrib.setdefault((model, _local_day(ts), fast), [0, 0, 0, 0, 0, 0])
        row[0] += _int(u.get("input_tokens"))
        row[1] += _int(u.get("output_tokens"))
        row[2] += _int(u.get("cache_read_input_tokens"))
        row[3] += w5m
        row[4] += w1h
        row[5] += 1                      # `iterations[]` is deliberately ignored: it repeats these totals

    def _build(self, bytes_read):
        agg = {}
        sessions = {}
        for fs in self._files.values():
            s = sessions.setdefault(fs.session, {"first_ts": None, "last_ts": None, "agent_name": None,
                                                 "project": fs.project, "cwd": None, "cost_state": None,
                                                 "_main_cost": False})
            if fs.first_ts and (s["first_ts"] is None or fs.first_ts < s["first_ts"]):
                s["first_ts"] = fs.first_ts
                if fs.cwd:
                    s["cwd"] = fs.cwd
            if fs.last_ts and (s["last_ts"] is None or fs.last_ts > s["last_ts"]):
                s["last_ts"] = fs.last_ts
            if s["cwd"] is None:
                s["cwd"] = fs.cwd
            if fs.agent_name and (not fs.sub or s["agent_name"] is None):
                s["agent_name"] = fs.agent_name
            if fs.cost_state is not None and (not fs.sub or not s["_main_cost"]):
                s["cost_state"] = fs.cost_state
                s["_main_cost"] = s["_main_cost"] or not fs.sub
            for (model, day, fast), c in fs.contrib.items():
                a = agg.setdefault((fs.session, model, day, fast), [0] * 6)
                for i, v in enumerate(c):
                    a[i] += v
        for s in sessions.values():
            del s["_main_cost"]
        rows = [dict(session=k[0], model=k[1], day=k[2], fast=k[3], **dict(zip(COUNTERS, v)))
                for k, v in sorted(agg.items())]
        return {"ready": True, "last_scan": dt.datetime.now().astimezone().isoformat(timespec="seconds"),
                "files": len(self._files), "bytes_read": bytes_read, "bytes_total": self._bytes_total,
                "errors": self._errors, "rows": rows, "sessions": sessions}

    # ── public ──
    def snapshot(self):
        """Independent copy of the last published aggregates; never waits for a running refresh."""
        with self._swap_lock:
            snap = self._snap
        return json.loads(json.dumps(snap))   # published dicts are never mutated; the copy is the caller's

    def start_background(self, interval=60):
        if self._thread and self._thread.is_alive():
            return

        def loop():
            while True:
                try:
                    self.refresh()
                except Exception:
                    self._errors += 1
                time.sleep(interval)

        self._thread = threading.Thread(target=loop, name="token-index", daemon=True)
        self._thread.start()


def session_tokens(session, root=None):
    """input + output + cache creation tokens of one session (subagent logs included), from its JSONL; 0 if none.
    Cache reads are not counted. Re-reads the session's files on every call (no index kept between calls)."""
    if not re.fullmatch(r"[A-Za-z0-9._-]+", session or ""):
        return 0                         # the id becomes part of a glob pattern: only plain ids
    idx = TokenIndex(root, only_session=session)
    idx.refresh()
    return sum(r["input"] + r["output"] + r["cache_write_5m"] + r["cache_write_1h"] for r in idx.snapshot()["rows"])


def session_tokens_known(session, root=None):
    """session_tokens(), but None when the session's log cannot be read: a bad id, no log file, or no usage line in it.
    0 then means a log that was read and shows no tokens used (scout_runs.tokens: NULL = unknown, T-0851)."""
    if not re.fullmatch(r"[A-Za-z0-9._-]+", session or ""):
        return None
    idx = TokenIndex(root, only_session=session)
    idx.refresh()
    rows = idx.snapshot()["rows"]
    if not rows:
        return None
    return sum(r["input"] + r["output"] + r["cache_write_5m"] + r["cache_write_1h"] for r in rows)


def latest_context(session, root=None, tail_bytes=1 << 20):
    """Context size of the session's latest main-thread request (input + cache read + cache creation tokens), or None
    when the session has no log or no usage line. Reads only the tail of the log; subagent logs are not looked at."""
    if not re.fullmatch(r"[A-Za-z0-9._-]+", session or ""):
        return None
    base = Path(root) if root else Path.home() / ".claude" / "projects"
    best = None
    try:
        files = [f for proj in base.iterdir() if proj.is_dir() for f in proj.glob(f"{session}.jsonl")]
    except OSError:
        return None
    for f in files:
        try:
            with open(f, "rb") as fh:
                size = fh.seek(0, os.SEEK_END)
                fh.seek(max(0, size - tail_bytes))
                lines = fh.read().split(b"\n")
            mtime = f.stat().st_mtime
        except OSError:
            continue
        for line in reversed(lines):
            if b'"usage"' not in line:
                continue
            try:
                d = json.loads(line)
            except ValueError:
                continue  # the first line of the tail may be cut
            if not isinstance(d, dict):
                continue
            msg = d.get("message")
            u = msg.get("usage") if isinstance(msg, dict) else None
            if d.get("type") != "assistant" or not isinstance(u, dict) or d.get("isSidechain"):
                continue
            n = _int(u.get("input_tokens")) + _int(u.get("cache_read_input_tokens")) + _int(u.get("cache_creation_input_tokens"))
            if best is None or mtime >= best[0]:
                best = (mtime, n)
            break
    return best[1] if best else None


def usage_totals(root):
    """Summed usage of every JSONL under `root` (a projects dir, subagent logs included), via the same parser as the
    index: {"input", "output", "cache_read", "cache_write"}. A missing dir gives zeros."""
    idx = TokenIndex(root)
    idx.refresh()
    rows = idx.snapshot()["rows"]
    return {"input": sum(r["input"] for r in rows), "output": sum(r["output"] for r in rows),
            "cache_read": sum(r["cache_read"] for r in rows),
            "cache_write": sum(r["cache_write_5m"] + r["cache_write_1h"] for r in rows)}


# ───────────────────────────── attribution ─────────────────────────────

def _agent_rows(con):
    return [r for r in con.execute(
        "SELECT name, role, goal_id, task_id, session_id, started, ended FROM agents ORDER BY started")]


def _event_sessions(con):
    """session id -> agent name from the tick's session-change events (detail JSON stored / herdr)."""
    out = {}
    try:
        rows = con.execute("SELECT entity_id, detail FROM events WHERE entity='agent' AND "
                           "(detail LIKE '%\"stored\"%' OR detail LIKE '%\"herdr\"%') ORDER BY id").fetchall()
    except sqlite3.Error:
        return out
    for r in rows:
        try:
            d = json.loads(r["detail"])
        except (TypeError, ValueError):
            continue
        if not isinstance(d, dict):
            continue
        for k in ("stored", "herdr"):
            if isinstance(d.get(k), str):
                out[d[k]] = r["entity_id"]
    return out


def _main_model(rows):
    tot = {}
    for r in rows:
        tot[r["model"]] = tot.get(r["model"], 0) + r["input"] + r["output"] + r["cache_read"] + r["cache_write_5m"] + r["cache_write_1h"]
    return max(tot, key=tot.get) if tot else None


def attribute(snapshot, con):
    """session id -> {agent, role, task, goal, model_main, via}; via is one of
    session | event | name | worktree | unattributed."""
    agents = _agent_rows(con)
    by_name = {}
    for a in agents:
        by_name.setdefault(a["name"], []).append(a)
    by_sess = {a["session_id"]: a for a in agents if a["session_id"]}
    ev = _event_sessions(con)
    now = dt.datetime.now().astimezone()
    models = {}
    for r in snapshot["rows"]:
        models.setdefault(r["session"], []).append(r)
    out = {}
    for sid, s in snapshot["sessions"].items():
        agent, via = by_sess.get(sid), "session"
        if agent is None and ev.get(sid) in by_name:
            agent, via = by_name[ev[sid]][-1], "event"
        if agent is None and s.get("agent_name") in by_name:
            first = _parse_ts(s.get("first_ts"))
            for a in by_name[s["agent_name"]]:
                start, end = _parse_ts(a["started"]), (_parse_ts(a["ended"]) if a["ended"] else now)
                if first and start and end and start <= first <= end:
                    agent, via = a, "name"
                    break
        info = {"agent": None, "role": None, "task": None, "goal": None,
                "model_main": _main_model(models.get(sid, [])), "via": "unattributed"}
        if agent is not None:
            info.update(agent=agent["name"], role=agent["role"], task=agent["task_id"], goal=agent["goal_id"], via=via)
        else:
            m = TASK_RE.search(s.get("project") or "") or TASK_RE.search(s.get("cwd") or "")
            if m:
                info.update(goal=m.group(1), task=m.group(2), via="worktree")
        out[sid] = info
    return out


def _add(into, key, r, c):
    e = into.setdefault(key, {k: 0 for k in COUNTERS} | {"cost": 0.0, "cost_unknown": False, "priced": 0})
    for k in COUNTERS:
        e[k] += r[k]
    if c is not None:
        e["cost"] += c
        e["priced"] += 1
    elif any(r[k] for k in COUNTERS[:5]):   # a zero-token row (e.g. model "<synthetic>") has no cost to be unknown
        e["cost_unknown"] = True


def _finish(res):
    """A bucket with unpriced tokens and nothing priced has no cost at all: None (the UI shows n/a), not 0.0."""
    for buckets in res.values():
        for e in (buckets.values() if "cost" not in buckets else [buckets]):
            if e["cost_unknown"] and not e["priced"]:
                e["cost"] = None


def rollup(snapshot, attribution, org_of=None):
    """Totals by agent / task / goal / model / day, each with tokens and estimated cost (a model without a price adds
    no cost and sets cost_unknown unless the row has no tokens; a bucket with only such rows has cost None).
    With `org_of` (session id -> org name) there is a by_org bucket too (G-028)."""
    res = {k: {} for k in ("by_agent", "by_task", "by_goal", "by_model", "by_day", "by_org", "total")}
    for r in snapshot["rows"]:
        a = attribution.get(r["session"]) or {"agent": None, "task": None, "goal": None}
        c = cost(r["model"], r, "fast" if r["fast"] else None)
        if org_of is not None:
            _add(res["by_org"], org_of(r["session"]), r, c)
        _add(res["by_agent"], a["agent"] or "unattributed", r, c)
        _add(res["by_task"], a["task"] or "unattributed", r, c)
        _add(res["by_goal"], a["goal"] or "unattributed", r, c)
        _add(res["by_model"], r["model"], r, c)
        _add(res["by_day"], r["day"], r, c)
        _add(res["total"], "all", r, c)
    res["total"] = res["total"].get("all") or {k: 0 for k in COUNTERS} | {"cost": 0.0, "cost_unknown": False, "priced": 0}
    _finish(res)
    return res


# ───────────────────────────── per-task / per-goal usage (C2) ─────────────────────────────

NOT_READY = "not_ready"
NOT_AVAILABLE = "not_available"
_TOKEN_KEYS = ("input", "output", "cache_read", "cache_write")


def _usage_agents(con):
    """name -> agent row (plus org and spawn_args when the table has them: the same inputs scout.orgs.of_agent uses)."""
    rows = {a["name"]: dict(a) for a in _agent_rows(con)}
    for cols in ("name, org, spawn_args", "name, spawn_args"):   # an older schema lacks org; the prefix still decides
        try:
            extra = con.execute(f"SELECT {cols} FROM agents").fetchall()
        except sqlite3.OperationalError:
            continue
        for r in extra:
            if r["name"] in rows:
                rows[r["name"]].update({k: r[k] for k in r.keys() if k != "name"})
        break
    return rows


def _is_cursor(agent_row):
    from scout import orgs as _orgs
    return _orgs.of_agent(agent_row) == "cursor"


def _start_key(agent):
    """Sort key by the parsed, timezone-aware start; a missing or unparseable start sorts last."""
    t = _parse_ts(agent["started"]) if agent and agent["started"] else None
    return (0, t.timestamp()) if t else (1, 0.0)


def _first_rejection(con, task_id):
    """Earliest fail/reject verdict or judge-reject event of the task, as a datetime (None when never rejected)."""
    stamps = []
    try:
        stamps += [r[0] for r in con.execute(
            "SELECT created FROM verdicts WHERE task_id=? AND verdict IN ('fail','reject')", (task_id,))]
        stamps += [r[0] for r in con.execute(
            "SELECT ts FROM events WHERE entity='task' AND entity_id=? AND action='judge-reject'", (task_id,))]
    except sqlite3.Error:
        return None
    parsed = [p for p in (_parse_ts(s) for s in stamps) if p]
    return min(parsed) if parsed else None


def _usage_buckets(snapshot, attribution, keep):
    """agent name -> bucket (rollup counters, cost, sessions set) for the rows whose attribution passes `keep`."""
    by, sess = {}, {}
    for r in snapshot["rows"]:
        a = attribution.get(r["session"]) or {"agent": None, "task": None, "goal": None}
        if not keep(a):
            continue
        name = a["agent"] or "unattributed"
        _add(by, name, r, cost(r["model"], r, "fast" if r["fast"] else None))
        sess.setdefault(name, set()).add(r["session"])
    _finish({"x": by})
    return by, sess


def _usage_buckets_multi(snapshot, attribution, task_ids, goal_id):
    """One pass over the snapshot: {task id: (by, sess)} for `task_ids`, and None -> (by, sess) for the goal's
    sessions without a task. Same buckets _usage_buckets gives per task."""
    out = {}
    for r in snapshot["rows"]:
        a = attribution.get(r["session"]) or {"agent": None, "task": None, "goal": None}
        if a["task"] in task_ids:
            key = a["task"]
        elif a["goal"] == goal_id and not a["task"]:
            key = None
        else:
            continue
        by, sess = out.setdefault(key, ({}, {}))
        name = a["agent"] or "unattributed"
        _add(by, name, r, cost(r["model"], r, "fast" if r["fast"] else None))
        sess.setdefault(name, set()).add(r["session"])
    for by, _ in out.values():
        _finish({"x": by})
    return out


def _tokens(e):
    return {"input": e["input"], "output": e["output"], "cache_read": e["cache_read"],
            "cache_write": e["cache_write_5m"] + e["cache_write_1h"]}


def _wall_seconds(a, now):
    start = _parse_ts(a["started"]) if a["started"] else None
    end = _parse_ts(a["ended"]) if a["ended"] else now
    return max(0.0, (end - start).total_seconds()) if start and end else None


def _cursor_row(a, now):
    return {"agent": a["name"], "role": a["role"], "runtime": "cursor", "sessions": 1, "wall_s": _wall_seconds(a, now),
            "input": None, "output": None, "cache_read": None, "cache_write": None, "cost": None,
            "rework": False, "state": NOT_AVAILABLE}


def _claude_row(name, e, sessions, agent):
    return {"agent": name, "role": agent["role"] if agent else None, "runtime": "claude", "sessions": len(sessions),
            "wall_s": None, **_tokens(e), "cost": None if e["cost_unknown"] else e["cost"], "rework": False, "state": "ok"}


def _totals(rows):
    """Totals over the Claude rows; Cursor rows have no tokens or cost and are counted in `cursor_rows` only."""
    real = [r for r in rows if r["state"] == "ok"]
    cursor = [r for r in rows if r["state"] == NOT_AVAILABLE]
    costs = [r["cost"] for r in real]
    unknown = any(c is None for c in costs)
    total = None if unknown or (not real and cursor) else sum(costs)
    rework = None if unknown or not real else sum(r["cost"] for r in real if r["rework"])
    share = rework / total if total and rework is not None else None
    tokens = None if not real else sum(r[k] for r in real for k in _TOKEN_KEYS)
    state = "ok" if real and not cursor else ("partial" if real else (NOT_AVAILABLE if cursor else "ok"))
    return {"state": state, "tokens": tokens, "cost": total, "rework_cost": rework, "rework_share": share,
            "sessions": sum(r["sessions"] for r in rows), "cursor_rows": len(cursor),
            "wall_s": sum(r["wall_s"] or 0 for r in cursor) if cursor else None,
            **{k: (sum(r[k] for r in real) if real else None) for k in _TOKEN_KEYS}}


def task_usage(snapshot, attribution, con, task_id, now=None):
    """Per-agent usage of one task: {"state", "task", "rows", "totals", "first_rejection"}. A row has agent, role,
    sessions, input, output, cache_read, cache_write (5m + 1h), cost (None when the model has no price) and `rework`
    (the agent started after the task's first rejection). Cursor agents get a row with sessions and wall_s but
    tokens and cost None and state "not_available", never 0. Lead/HQ cost is goal overhead (goal_usage), not here.
    A token index that is not ready gives state "not_ready" and no rows."""
    if not snapshot.get("ready"):
        return {"state": NOT_READY, "task": task_id, "rows": [], "totals": None, "first_rejection": None}
    now = now or dt.datetime.now().astimezone()
    agents = _usage_agents(con)
    by, sess = _usage_buckets(snapshot, attribution, lambda a: a["task"] == task_id)
    return _task_usage(task_id, agents, by, sess, con, now)


def _task_usage(task_id, agents, by, sess, con, now):
    rej = _first_rejection(con, task_id)
    rows = []
    for name, e in by.items():
        a = agents.get(name)
        if a is not None and _is_cursor(a):
            continue
        rows.append(_claude_row(name, e, sess[name], a))
    for a in agents.values():
        if a["task_id"] == task_id and _is_cursor(a):
            rows.append(_cursor_row(a, now))
    for r in rows:
        a = agents.get(r["agent"])
        start = _parse_ts(a["started"]) if a and a["started"] else None
        r["rework"] = bool(rej and start and start > rej and r["state"] == "ok")
    rows.sort(key=lambda r: _start_key(agents.get(r["agent"])))
    return {"state": "ok", "task": task_id, "rows": rows, "totals": _totals(rows),
            "first_rejection": rej.isoformat() if rej else None}


def goal_usage(snapshot, attribution, con, goal_id, now=None):
    """A goal's usage: its tasks (task_usage each), plus "goal overhead": sessions attributed to the goal without a
    task (lead, pm without a task, HQ), and Cursor agents of the goal without a task. {"state", "goal", "tasks",
    "overhead", "totals"}; totals = tasks + overhead."""
    if not snapshot.get("ready"):
        return {"state": NOT_READY, "goal": goal_id, "tasks": {}, "overhead": None, "totals": None}
    now = now or dt.datetime.now().astimezone()
    agents = _usage_agents(con)
    ids = {a["task"] for a in attribution.values() if a["goal"] == goal_id and a["task"]}
    ids |= {a["task_id"] for a in agents.values() if a["goal_id"] == goal_id and a["task_id"]}
    buckets = _usage_buckets_multi(snapshot, attribution, ids, goal_id)    # one scan of the snapshot for all tasks
    empty = ({}, {})
    tasks = {t: _task_usage(t, agents, *buckets.get(t, empty), con, now) for t in sorted(ids)}
    by, sess = buckets.get(None, empty)
    over = []
    for name, e in by.items():
        a = agents.get(name)
        if a is not None and _is_cursor(a):
            continue
        over.append(_claude_row(name, e, sess[name], a))
    over += [_cursor_row(a, now) for a in agents.values() if a["goal_id"] == goal_id and not a["task_id"] and _is_cursor(a)]
    allrows = over + [r for t in tasks.values() for r in t["rows"]]
    return {"state": "ok", "goal": goal_id, "tasks": tasks, "overhead": {"rows": over, "totals": _totals(over)},
            "totals": _totals(allrows)}
