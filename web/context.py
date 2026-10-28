"""Context baseline: how much context each factory agent carries, measured from Claude Code transcripts
(~/.claude/projects/**/*.jsonl) and attributed to agents through the factory DB. Read-only, stdlib only.

    python3 -m web.context baseline [--since YYYY-MM-DD] [--until YYYY-MM-DD] [--json]
    python3 -m web.context per-pr   [--since YYYY-MM-DD] [--until YYYY-MM-DD] [--last N] [--json]

Per request, context = input_tokens + cache_read_input_tokens + cache_creation_input_tokens (5m and 1h together).
Only usage numbers, ids, timestamps and the sidechain flag are read out of a line; message content, prompts and
tool output are never kept or printed (same rule as web/tokens.py). Nothing is written anywhere.

per-pr (the SC5 metric): one row per merged task = tasks.status='done', pr_url set and a judge 'merge' verdict;
its merge time is the (latest) judge merge verdict's created. --since/--until (local days, inclusive) and --last N
(most recent merges first) apply to that time. Sessions are attributed to a task by agents rows with
task_id = the task (every role: coder, qa, reviewer, judge, and pm/cleaner when task-scoped; resolved through
agents.session_id and the tick's session events) plus, for sessions no agent row claims, the tokens.py TASK_RE
match on the worktree cwd. HQ sessions (boss/director/lead) are never attributed to a task; their input tokens
inside the window are reported separately as "HQ input per merged PR". The HQ window is --since/--until, and with
--last and no --since it starts on the local day of the oldest selected merge. Input = input + cache_read +
cache_write; cost comes from web/prices.py (secondary, None when a model has no price). Rejects = verdicts with
verdict reject/fail for the task, split by role.

Sidechain (subagent) requests, isSidechain=true, are counted in sidechain_requests but excluded everywhere else:
from fixed_prefix, from input_per_task, from the persistent series, and from per-pr input, output and cost. Their
tokens are therefore not part of any agent's or task's total; a task that leans on subagents is under-counted.
Malformed lines (any line that is not a JSON object, with or without "usage", plus unreadable files and usage lines
with no timestamp) are counted in malformed_lines. persistent.first is the first request inside --since/--until
(fixed_prefix still uses the session's very first request).

per-pr medians are exact: an even count gives the mean of the two middle values, so median_input can be x.5.

Memory: lines are streamed; per attributed session only a few ints and an hourly max map are kept, plus one dedupe
key per unique request (~150 bytes).
"""
import argparse
import datetime as dt
import json
import math
import os
import sqlite3
import statistics
import sys
from pathlib import Path
from urllib.parse import quote

from web import dbpath, prices
from web.tokens import TASK_RE, _agent_rows, _event_sessions, _int, _parse_ts

HQ_ROLES = ("boss", "director", "lead")
BIG_CONTEXT = 250_000
SIDECHAIN_NOTE = ("Sidechain (subagent) requests are excluded from fixed_prefix, input_per_task, persistent and "
                  "per-pr input/output/cost; baseline only counts them (sidechain_requests).")


def db_path():
    """Same resolution as bin/factory (web/dbpath.py): $FACTORY_HOME/db/factory.db, else the legacy $FACTORY_HOME/factory.db."""
    return dbpath.resolve(os.environ.get("FACTORY_HOME") or Path.home() / "factory")


def projects_dir():
    return Path(os.environ.get("FACTORY_CLAUDE_PROJECTS") or Path.home() / ".claude" / "projects")


def connect_ro(path):
    path = os.path.abspath(str(path))
    if not os.path.isfile(path):
        raise FileNotFoundError(f"factory database not found: {path} (set FACTORY_HOME)")
    con = sqlite3.connect(f"file:{quote(path)}?mode=ro", uri=True, timeout=2, isolation_level=None)
    con.row_factory = sqlite3.Row
    con.execute("PRAGMA query_only=ON")
    return con


def is_hq(role, name):
    return role in HQ_ROLES or (name or "").startswith(("boss", "director", "lead-"))


def attribution(con):
    """session id -> {agent, role, task}: agents.session_id first, then the tick's session-change events."""
    agents = _agent_rows(con)
    by_name = {}
    for a in agents:
        by_name[a["name"]] = a
    out = {}
    for sid, name in _event_sessions(con).items():
        if name in by_name:
            a = by_name[name]
            out[sid] = {"agent": a["name"], "role": a["role"], "task": a["task_id"]}
    for a in agents:
        if a["session_id"]:
            out[a["session_id"]] = {"agent": a["name"], "role": a["role"], "task": a["task_id"]}
    return out


def context_of(usage):
    return (_int(usage.get("input_tokens")) + _int(usage.get("cache_read_input_tokens"))
            + _int(usage.get("cache_creation_input_tokens")))


class _Session:
    __slots__ = ("first", "wfirst", "n", "last", "max", "total", "hours", "big")

    def __init__(self):
        self.first = None          # (timestamp utc iso, ctx) of the earliest request seen, window or not
        self.wfirst = None         # same, but only requests inside the window
        self.n = 0                 # in-window counters from here on
        self.last = None
        self.max = 0
        self.total = 0
        self.hours = {}
        self.big = 0


def scan(root, attr, since, until):
    """Stream every transcript once. Returns (sessions dict, counters)."""
    sess = {}
    unattributed = set()
    c = {"files": 0, "malformed_lines": 0, "duplicate_requests": 0, "sidechain_requests": 0,
         "empty_sessions": 0, "requests": 0}
    seen = set()
    for path in sorted(Path(root).rglob("*.jsonl")) if Path(root).is_dir() else []:
        c["files"] += 1
        usage_lines = 0
        try:
            f = open(path, "rb")
        except OSError:
            c["malformed_lines"] += 1
            continue
        with f:
            for raw in f:
                raw = raw.strip()
                if not raw:
                    continue
                if not (raw.startswith(b"{") and raw.endswith(b"}")):
                    c["malformed_lines"] += 1
                    continue
                try:
                    d = json.loads(raw)
                except ValueError:
                    c["malformed_lines"] += 1
                    continue
                if b'"usage"' not in raw:
                    continue
                msg = d.get("message") if isinstance(d, dict) else None
                u = msg.get("usage") if isinstance(msg, dict) else None
                if d.get("type") != "assistant" or not isinstance(u, dict):
                    continue
                usage_lines += 1
                mid, rid = msg.get("id"), d.get("requestId")
                key = (mid, rid) if mid and rid else d.get("uuid")
                if key is not None:
                    if key in seen:
                        c["duplicate_requests"] += 1
                        continue
                    seen.add(key)
                t = _parse_ts(d.get("timestamp"))
                if t is None:
                    c["malformed_lines"] += 1
                    continue
                if d.get("isSidechain") is True:
                    c["sidechain_requests"] += 1
                    continue
                sid = d.get("sessionId") if isinstance(d.get("sessionId"), str) else path.stem
                a = attr.get(sid)
                local = t.astimezone()
                day = local.date().isoformat()
                ctx = context_of(u)
                if a is None:
                    if (not since or day >= since) and (not until or day <= until):
                        unattributed.add(sid)
                    continue
                s = sess.get(sid)
                if s is None:
                    s = sess[sid] = _Session()
                tu = t.astimezone(dt.timezone.utc).isoformat(timespec="seconds")
                if s.first is None or tu < s.first[0]:
                    s.first = (tu, ctx)
                if (since and day < since) or (until and day > until):
                    continue
                c["requests"] += 1
                s.n += 1
                if s.wfirst is None or tu < s.wfirst[0]:
                    s.wfirst = (tu, ctx)
                s.total += ctx
                if s.last is None or tu >= s.last[0]:
                    s.last = (tu, ctx)
                s.max = max(s.max, ctx)
                s.big += ctx > BIG_CONTEXT
                hour = local.strftime("%Y-%m-%dT%H:00")
                if ctx > s.hours.get(hour, -1):
                    s.hours[hour] = ctx
        if usage_lines == 0:
            c["empty_sessions"] += 1
    c["unattributed_sessions"] = len(unattributed)
    return sess, c


def _pct(vals, p):
    vals = sorted(vals)
    return vals[max(0, math.ceil(p * len(vals)) - 1)]


def _stats(vals):
    vals = sorted(vals)
    n = len(vals)
    mid = vals[n // 2] if n % 2 else (vals[n // 2 - 1] + vals[n // 2]) // 2
    return {"n": n, "median": mid, "p90": _pct(vals, 0.9), "min": vals[0], "max": vals[-1]}


def baseline(root, con, since=None, until=None):
    attr = attribution(con)
    sess, c = scan(root, attr, since, until)
    first_by_role, per_agent, persistent = {}, {}, []
    for sid, s in sess.items():
        a = attr[sid]
        role = a["role"] or "unknown"
        if s.first is not None and s.n and _in_window(s.first[0], since, until):
            first_by_role.setdefault(role, []).append(s.first[1])
        if a["task"] and s.n:
            e = per_agent.setdefault(a["agent"], [role, 0])
            e[1] += s.total
        if s.n and is_hq(a["role"], a["agent"]):
            persistent.append({
                "agent": a["agent"], "role": role, "session": sid, "requests": s.n,
                "first": s.wfirst[1], "last": s.last[1], "max": s.max, "over_250k": s.big,
                "hourly_max": dict(sorted(s.hours.items()))})
    by_task = {}
    for role, total in per_agent.values():
        by_task.setdefault(role, []).append(total)
    persistent.sort(key=lambda p: (p["agent"], p["session"]))
    return {
        "window": {"since": since, "until": until, **c},
        "fixed_prefix": {r: _stats(v) for r, v in sorted(first_by_role.items())},
        "input_per_task": {r: {k: v for k, v in _stats(t).items() if k in ("n", "median", "p90")}
                           for r, t in sorted(by_task.items())},
        "persistent": persistent,
    }


def _in_window(tu, since, until):
    day = _parse_ts(tu).astimezone().date().isoformat()
    return (not since or day >= since) and (not until or day <= until)


def render(rep):
    w = rep["window"]
    out = [f"window: {w['since'] or 'start'} .. {w['until'] or 'now'}",
           f"  transcript files read: {w['files']}, requests counted: {w['requests']}",
           f"  unattributed sessions skipped: {w['unattributed_sessions']}, empty sessions: {w['empty_sessions']}",
           f"  sidechain requests (excluded): {w['sidechain_requests']}, duplicate requests: {w['duplicate_requests']}, "
           f"malformed lines: {w['malformed_lines']}",
           "", "1. fixed_prefix (context of a session's first request), tokens per role"]
    out.append(f"  {'role':<10}{'n':>5}{'median':>10}{'p90':>10}{'min':>10}{'max':>10}")
    for r, s in rep["fixed_prefix"].items():
        out.append(f"  {r:<10}{s['n']:>5}{s['median']:>10}{s['p90']:>10}{s['min']:>10}{s['max']:>10}")
    out += ["", "2. input_per_task (sum of context over all requests of a task agent), per role"]
    out.append(f"  {'role':<10}{'n':>5}{'median':>12}{'p90':>12}")
    for r, s in rep["input_per_task"].items():
        out.append(f"  {r:<10}{s['n']:>5}{s['median']:>12}{s['p90']:>12}")
    out += ["", "3. persistent (HQ sessions)"]
    for p in rep["persistent"]:
        out.append(f"  {p['agent']} session {p['session']}: requests={p['requests']} first={p['first']} "
                   f"last={p['last']} max={p['max']} over_250k={p['over_250k']}")
        out.append("    hourly max: " + ", ".join(f"{h}={v}" for h, v in p["hourly_max"].items()))
    return "\n".join(out) + "\n"


def _local_day(ts):
    t = _parse_ts(ts)
    return t.astimezone().date().isoformat() if t else None


def merged_tasks(con, since=None, until=None, last=None):
    """Merged tasks (done + pr_url + judge merge verdict), most recent merge first, window and --last applied."""
    # latest merge verdict by parsed instant: created carries local offsets (+01:00/+02:00), so SQL MAX on the text is wrong
    latest = {}
    for r in con.execute(
            "SELECT t.id, t.goal_id, t.pr_url, v.created FROM tasks t JOIN verdicts v ON v.task_id=t.id "
            "WHERE t.status='done' AND t.pr_url IS NOT NULL AND t.pr_url<>'' AND v.role='judge' "
            "AND lower(v.verdict)='merge'").fetchall():
        t = _parse_ts(r["created"])
        if t is not None and (r["id"] not in latest or t > latest[r["id"]][0]):
            latest[r["id"]] = (t, r)
    out = []
    for t, r in latest.values():
        day = t.astimezone().date().isoformat()
        if (since and day < since) or (until and day > until):
            continue
        t = t.astimezone(dt.timezone.utc)
        out.append({"task": r["id"], "goal": r["goal_id"], "pr_url": r["pr_url"],
                    "merged": t.isoformat(timespec="seconds"), "sessions": 0, "input": 0, "output": 0,
                    "cost": 0.0, "rejects": 0, "rejects_by_role": {}})
    out.sort(key=lambda m: (m["merged"], m["task"]), reverse=True)
    return out[:last] if last is not None else out


def scan_usage(root, attr, task_ids, hq_since=None, hq_until=None):
    """Stream every transcript once: per task-attributed session its input, output and cost; plus the HQ input
    inside the local-day window. Returns (task id -> {session id -> totals}, hq_input)."""
    per_task, hq_input, seen, cwd_task = {}, 0, set(), {}
    for path in sorted(Path(root).rglob("*.jsonl")) if Path(root).is_dir() else []:
        try:
            f = open(path, "rb")
        except OSError:
            continue
        with f:
            for raw in f:
                raw = raw.strip()
                if not (raw.startswith(b"{") and raw.endswith(b"}")) or b'"usage"' not in raw:
                    continue
                try:
                    d = json.loads(raw)
                except ValueError:
                    continue
                msg = d.get("message") if isinstance(d, dict) else None
                u = msg.get("usage") if isinstance(msg, dict) else None
                if d.get("type") != "assistant" or not isinstance(u, dict) or d.get("isSidechain") is True:
                    continue
                mid, rid = msg.get("id"), d.get("requestId")
                key = (mid, rid) if mid and rid else d.get("uuid")
                if key is not None:
                    if key in seen:
                        continue
                    seen.add(key)
                t = _parse_ts(d.get("timestamp"))
                if t is None:
                    continue
                sid = d.get("sessionId") if isinstance(d.get("sessionId"), str) else path.stem
                a = attr.get(sid)
                if a is None:
                    if sid not in cwd_task:
                        m = TASK_RE.search(d.get("cwd") or "")
                        cwd_task[sid] = m.group(2) if m else None
                    task = cwd_task[sid]
                    hq = False
                else:
                    task, hq = a["task"], is_hq(a["role"], a["agent"])
                    if hq:
                        task = None
                ctx = context_of(u)
                if hq:
                    day = t.astimezone().date().isoformat()
                    if (not hq_since or day >= hq_since) and (not hq_until or day <= hq_until):
                        hq_input += ctx
                    continue
                if task not in task_ids:
                    continue
                cc = u.get("cache_creation")
                cc = cc if isinstance(cc, dict) else {}
                w1h = _int(cc.get("ephemeral_1h_input_tokens"))
                w5m = _int(cc.get("ephemeral_5m_input_tokens"))
                total_w = _int(u.get("cache_creation_input_tokens"))
                if w5m + w1h < total_w:
                    w5m = total_w - w1h
                c = prices.cost(msg.get("model"), {
                    "input": _int(u.get("input_tokens")), "output": _int(u.get("output_tokens")),
                    "cache_read": _int(u.get("cache_read_input_tokens")), "cache_write_5m": w5m,
                    "cache_write_1h": w1h}, u.get("speed"))
                s = per_task.setdefault(task, {}).setdefault(sid, {"input": 0, "output": 0, "cost": 0.0, "unpriced": False})
                s["input"] += ctx
                s["output"] += _int(u.get("output_tokens"))
                if c is None:
                    s["unpriced"] = s["unpriced"] or ctx > 0 or _int(u.get("output_tokens")) > 0
                else:
                    s["cost"] += c
    return per_task, hq_input


def _median(vals):
    return statistics.median(vals) if vals else None


def per_pr(root, con, since=None, until=None, last=None):
    merges = merged_tasks(con, since, until, last)
    hq_since = since
    if last is not None and not since and merges:
        hq_since = _local_day(merges[-1]["merged"])
    per_task, hq_input = scan_usage(root, attribution(con), {m["task"] for m in merges}, hq_since, until)
    for m in merges:
        sess = per_task.get(m["task"], {})
        m["sessions"] = len(sess)
        m["input"] = sum(s["input"] for s in sess.values())
        m["output"] = sum(s["output"] for s in sess.values())
        m["cost"] = None if any(s["unpriced"] for s in sess.values()) else round(sum(s["cost"] for s in sess.values()), 4)
        by_role = {}
        for r in con.execute("SELECT role, COUNT(*) n FROM verdicts WHERE task_id=? AND lower(verdict) IN "
                             "('reject','fail') GROUP BY role ORDER BY role", (m["task"],)):
            by_role[r["role"]] = r["n"]
        m["rejects_by_role"] = by_role
        m["rejects"] = sum(by_role.values())
    n = len(merges)
    inputs = [m["input"] for m in merges]
    costs = [m["cost"] for m in merges if m["cost"] is not None]
    return {
        "window": {"since": since, "until": until, "last": last, "hq_since": hq_since},
        "summary": {
            "n": n,
            "median_input": _median(inputs) if n else None,
            "p75_input": _pct(inputs, 0.75) if n else None,
            "median_sessions": _median([m["sessions"] for m in merges]),
            "median_cost": round(_median(costs), 4) if costs else None,
            "rejects": sum(m["rejects"] for m in merges),
            "hq_input": hq_input,
            "hq_input_per_pr": round(hq_input / n) if n else None,
        },
        "tasks": merges,
    }


def render_per_pr(rep):
    w, s = rep["window"], rep["summary"]
    out = [f"merged tasks: {w['since'] or 'start'} .. {w['until'] or 'now'}" + (f", last {w['last']}" if w["last"] else ""),
           f"  {'task':<8}{'goal':<7}{'merged (UTC)':<22}{'sess':>5}{'input':>13}{'output':>10}{'$':>9}{'rej':>5}  pr"]
    for m in rep["tasks"]:
        cost = "n/a" if m["cost"] is None else f"{m['cost']:.2f}"
        rej = str(m["rejects"]) + ("".join(f" {r}={n}" for r, n in m["rejects_by_role"].items()))
        out.append(f"  {m['task']:<8}{m['goal']:<7}{m['merged']:<22}{m['sessions']:>5}{m['input']:>13}"
                   f"{m['output']:>10}{cost:>9}{rej:>5}  {m['pr_url']}")
    out += ["", "summary",
            f"  n merged tasks: {s['n']}",
            f"  input tokens per merged PR: median {s['median_input']}, p75 {s['p75_input']}",
            f"  sessions per merged PR: median {s['median_sessions']}",
            f"  $ per merged PR: median {s['median_cost']}",
            f"  rejects: {s['rejects']}",
            f"  HQ input per merged PR: {s['hq_input_per_pr']} (HQ input in window {s['hq_input']} / {s['n']} merged tasks)"]
    return "\n".join(out) + "\n"


def _day(s):
    try:
        return dt.date.fromisoformat(s).isoformat()
    except ValueError:
        raise argparse.ArgumentTypeError(f"{s!r} is not a YYYY-MM-DD date")


def _positive(s):
    try:
        n = int(s)
    except ValueError:
        n = 0
    if n < 1:
        raise argparse.ArgumentTypeError(f"{s!r} is not a positive integer")
    return n


def main(argv=None):
    ap = argparse.ArgumentParser(prog="python3 -m web.context", description=__doc__.split("\n\n")[0],
                                 epilog=SIDECHAIN_NOTE, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    b = sub.add_parser("baseline", help="fixed prefix per role, input per task, HQ context over time", epilog=SIDECHAIN_NOTE)
    b.add_argument("--since", type=_day, metavar="YYYY-MM-DD", help="first local day to include")
    b.add_argument("--until", type=_day, metavar="YYYY-MM-DD", help="last local day to include")
    b.add_argument("--json", action="store_true", help="one JSON object instead of text")
    p = sub.add_parser("per-pr", help="tokens per merged PR (the SC5 metric) and HQ input per merged PR", epilog=SIDECHAIN_NOTE)
    p.add_argument("--since", type=_day, metavar="YYYY-MM-DD", help="first local day of merge time to include")
    p.add_argument("--until", type=_day, metavar="YYYY-MM-DD", help="last local day of merge time to include")
    p.add_argument("--last", type=_positive, metavar="N", help="only the N most recent merges")
    p.add_argument("--json", action="store_true", help="one JSON object instead of text")
    args = ap.parse_args(argv)
    try:
        con = connect_ro(db_path())
    except (FileNotFoundError, sqlite3.Error) as e:
        print(f"web.context: {e}", file=sys.stderr)
        return 2
    try:
        if args.cmd == "per-pr":
            rep, text = per_pr(projects_dir(), con, args.since, args.until, args.last), render_per_pr
        else:
            rep, text = baseline(projects_dir(), con, args.since, args.until), render
    finally:
        con.close()
    sys.stdout.write(json.dumps(rep, indent=1) + "\n" if args.json else text(rep))
    return 0


if __name__ == "__main__":
    sys.exit(main())
