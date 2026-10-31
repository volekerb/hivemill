"""Agents page: teams, live/queued/recently ended agents, best-effort per-agent memory from /proc.

Never shows spawn_args (kickoff text); summaries are truncated to 200 characters. Never calls herdr or gate().
"""
import datetime as dt
import os
import re
import threading
import time
from pathlib import Path
from urllib.parse import quote

from web import queries
from web.db import has_column
from web.components import (AGENT_COLS, agent_row, ago, chips, data_table, disclosure, empty_state, row_status,
                            runtime_badge, section)
from web.html import badge, el, link, status

TITLE = "Agents"
SCOPE_MAX = 120   # teams.scope is a director-written summary; never return the full text

SUMMARY_MAX = 200
ENDED_WINDOW_S = 24 * 3600
ENDED_MAX = 40
PROC_TTL_S = 30
PROC_BUDGET_S = 0.08    # a scan gives up after this long; the partial result is cached and shown as is
# Filters (?status=, ?role=, ?goal=, ?runtime=). Values are comma-separated; unknown values are ignored. The server keeps
# only the first value of a repeated key, so repeating a key is not supported; use commas.
# The status set comes from the agents table (so 'starting' etc. work); "blocked" is derived from blocked_since.
BLOCKED = "blocked"
_STATUS_ORDER = ("live", "starting", "queued", "stopped", "exited", "done", "lost", "cancelled")   # display order only
ROLES = ("boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "scout",
         "evaluator")   # mirrors bin/factory ROLES (web/ never imports it)
RUNTIMES = ("claude", "cursor")  # factory default is Claude; Cursor via agent_runtimes
_GOAL_RE = re.compile(r"^G-\d+$")
MATCH_MAX = 200
# status chips: label -> the raw statuses it covers (Ended merges exited and stopped); the first four always show
CHIP_GROUPS = (("Running", ("live",)), ("Queued", ("queued",)), ("Ended", ("exited", "stopped")), ("Blocked", (BLOCKED,)),
               ("Starting", ("starting",)), ("Done", ("done",)), ("Lost", ("lost",)), ("Cancelled", ("cancelled",)))
CHIP_ALWAYS = ("Running", "Queued", "Ended", "Blocked")
GOAL_CHIPS = 6
TITLE_MAX = 120

# ── /proc snapshot (shared with the resources view), cached for PROC_TTL_S ──
_lock = threading.Lock()
_cache = {"at": None, "value": None}
_clock = time.monotonic


def _proc_dir():
    return Path(os.environ.get("FACTORY_PROC_DIR") or "/proc")


def _scan():
    """{pid: {"argv": [...], "ppid": int|None, "rss_kb": int|None}} of claude processes; None if /proc is unreadable."""
    root = _proc_dir()
    try:
        entries = [d for d in root.iterdir() if d.name.isdigit()]
    except OSError:
        return None
    deadline = time.monotonic() + PROC_BUDGET_S
    found = {}
    for d in entries:
        if time.monotonic() > deadline:
            break
        try:
            argv = [x.decode(errors="replace") for x in (d / "cmdline").read_bytes().split(b"\0") if x]
        except OSError:
            continue
        if not any(os.path.basename(x) == "claude" for x in argv[:3]):
            continue
        ppid = rss = None
        try:
            stat = (d / "stat").read_text(errors="replace")
            ppid = int(stat[stat.rindex(")") + 1:].split()[1])
        except (OSError, ValueError, IndexError):
            pass
        try:
            for line in (d / "status").read_text(errors="replace").splitlines():
                if line.startswith("VmRSS:"):
                    rss = int(line.split()[1])
                    break
        except (OSError, ValueError, IndexError):
            pass
        found[int(d.name)] = {"argv": argv, "ppid": ppid, "rss_kb": rss}
    return found


def proc_snapshot():
    """Cached /proc scan (30 s), shared by /agents and /resources."""
    with _lock:
        now = _clock()
        if _cache["at"] is None or now - _cache["at"] >= PROC_TTL_S:
            _cache["value"] = _scan()
            _cache["at"] = now
        return _cache["value"]


def claude_total(snap):
    """Top-level claude processes (a claude whose parent is itself a claude is a subagent/MCP launcher)."""
    if snap is None:
        return None
    return sum(1 for p in snap.values() if p["ppid"] not in snap)


def _sessions(argv):
    out = []
    for flag in ("--resume", "--session-id"):
        out += [argv[i + 1] for i, x in enumerate(argv[:-1]) if x == flag]
        out += [x.split("=", 1)[1] for x in argv if x.startswith(flag + "=")]
    return out


def rss_by_session(snap):
    """{session_id: rss_mb} for claude processes naming the session in argv (top-level process wins)."""
    out = {}
    for pid, p in sorted((snap or {}).items()):
        if p["rss_kb"] is None:
            continue
        for sid in _sessions(p["argv"]):
            out.setdefault(sid, round(p["rss_kb"] / 1024))
    return out


# ── data ──

def is_blocked(blocked_since):
    """The one definition of blocked, used by the badge, the count and ?status=blocked."""
    return blocked_since is not None and blocked_since != ""


def parse_filters(query, statuses=()):
    """{"status": [...], "role": [...], "goal": [...], "runtime": [...]} from ctx.query; invalid dropped, order kept.

    `statuses` are the status values present in the agents table; "blocked" is always accepted.
    """
    known = {*statuses, BLOCKED}
    out = {}
    for key, ok in (("status", known.__contains__), ("role", ROLES.__contains__), ("goal", _GOAL_RE.match),
                    ("runtime", RUNTIMES.__contains__)):
        raw = (query or {}).get(key)
        vals = []
        for v in (raw.split(",") if isinstance(raw, str) else []):
            v = v.strip()
            if v and ok(v) and v not in vals:
                vals.append(v)
        if vals:
            out[key] = vals
    return out


def _matches(a, f):
    if "status" in f and not (a["status"] in f["status"] or (BLOCKED in f["status"] and is_blocked(a.get("blocked_since")))):
        return False
    if "role" in f and a["role"] not in f["role"]:
        return False
    if "runtime" in f and (a.get("runtime") or "claude") not in f["runtime"]:
        return False
    return "goal" not in f or a["goal_id"] in f["goal"]


def _href(f, **change):
    """/agents URL for filters `f` with the given keys replaced (empty value removes the key)."""
    g = dict(f)
    for k, v in change.items():
        if v:
            g[k] = v
        else:
            g.pop(k, None)
    qs = "&".join(f"{k}={'%2C'.join(quote(v, safe='') for v in g[k])}"
                  for k in ("status", "role", "goal", "runtime") if g.get(k))
    return "/agents?" + qs if qs else "/agents"


def _agent_rows(con, ctx_now):
    base = queries.agents(con, include_ended=True, now=ctx_now)
    blocked = {}
    if has_column(con, "agents", "blocked_since"):
        blocked = {r["name"]: r["blocked_since"] for r in con.execute(
            "SELECT name, blocked_since FROM agents WHERE blocked_since IS NOT NULL")}
    for a in base:
        a["blocked_since"] = blocked.get(a["name"])
    summ = {}
    if has_column(con, "agents", "summary"):
        summ = {r["name"]: r["s"] for r in con.execute(
            "SELECT name, substr(summary,1,?) s FROM agents WHERE status IN ('done','exited','lost','stopped')", (SUMMARY_MAX + 1,))}
    for a in base:
        a["summary"] = queries._t(summ.get(a["name"]), SUMMARY_MAX)
    return base


def _last_events(con, names):
    """{agent name: ts of its newest event}; events name the acting agent in `actor`."""
    names = sorted(names)
    out = {}
    for i in range(0, len(names), 500):
        part = names[i:i + 500]
        out.update(con.execute(f"SELECT actor, MAX(ts) FROM events WHERE actor IN ({','.join('?' * len(part))}) GROUP BY actor", part).fetchall())
    return out


def _task_titles(con, ids):
    ids = sorted(ids)
    out = {}
    for i in range(0, len(ids), 500):
        part = ids[i:i + 500]
        out.update({r[0]: queries._t(r[1], TITLE_MAX) for r in con.execute(
            f"SELECT id, title FROM tasks WHERE id IN ({','.join('?' * len(part))})", part)})
    return out


def data(ctx, **params):
    con = ctx.db
    counts = {r[0]: r[1] for r in con.execute("SELECT status, count(*) FROM agents GROUP BY status")}   # unfiltered
    f = parse_filters(getattr(ctx, "query", None), [s for s in counts if isinstance(s, str)])
    rows = _agent_rows(con, ctx.now)
    all_rows = rows
    if f:
        rows = [a for a in rows if _matches(a, f)]
    snap = proc_snapshot()
    rss = rss_by_session(snap)
    live = [a for a in rows if a["status"] == "live"]
    for a in live:
        a["rss_mb"] = rss.get(a["session_id"]) if a["session_id"] else None
        a["stop_requested"] = bool(a["stop_requested_at"])
    queued = [a for a in rows if a["status"] == "queued"]
    reasons = {q["name"]: q["reason"] for q in queries.resources_db(con)["queued"]}
    for a in queued:
        a["reason"] = reasons.get(a["name"])
    cut = ctx.now.timestamp() - ENDED_WINDOW_S
    ended = [a for a in rows if a["status"] in queries.ENDED_AGENT and (queries._epoch(a["ended"]) or 0) >= cut]
    ended.sort(key=lambda a: queries._epoch(a["ended"]) or 0, reverse=True)
    n_live = {}
    for a in live:
        n_live[a["team_id"]] = n_live.get(a["team_id"], 0) + 1
    lead_status = {r["name"]: r["status"] for r in con.execute("SELECT name, status FROM agents WHERE role='lead'")}
    parked = _parked(con)
    teams = [dict(t, scope=queries._t(t["scope"], SCOPE_MAX), live_workers=n_live.get(t["id"], 0),
                  lead_state=_lead_state(t, lead_status, parked)) for t in queries.teams(con)]
    by_role, by_goal, by_runtime = {}, {}, {}
    for a in all_rows:
        by_role[a["role"]] = by_role.get(a["role"], 0) + 1
        rt = a.get("runtime") or "claude"
        by_runtime[rt] = by_runtime.get(rt, 0) + 1
        if a["goal_id"] and _GOAL_RE.match(a["goal_id"]):
            by_goal[a["goal_id"]] = by_goal.get(a["goal_id"], 0) + 1
    n_blocked = sum(1 for a in all_rows if is_blocked(a["blocked_since"]))

    cap = dict(con.execute("SELECT key, value FROM config")).get("max_live_agents", (ctx.defaults or {}).get("max_live_agents"))
    shown = [a for a in live] + (sorted(rows, key=lambda a: a["name"])[:MATCH_MAX] if f else [])
    last, titles = _last_events(con, {a["name"] for a in shown}), _task_titles(con, {a["task_id"] for a in shown if a["task_id"]})
    for a in shown:
        a["last_event"], a["task_title"] = last.get(a["name"]), titles.get(a["task_id"])

    def keep(a, extra=()):
        keys = ("name", "role", "goal_id", "team_id", "task_id", "status", "model", "runtime", "started", "ended",
                "queued_at", "age_s", "blocked_since", *extra)
        return {k: a.get(k) for k in keys}   # explicit allowlist: spawn_args / session_id / pane never leave this module
    matches = [keep(a, ("last_event", "task_title")) for a in sorted(rows, key=lambda a: a["name"])[:MATCH_MAX]] if f else []
    return {"counts": counts, "blocked": n_blocked, "roles": {r: by_role[r] for r in ROLES if r in by_role},
            "runtimes": {r: by_runtime[r] for r in RUNTIMES if r in by_runtime},
            "goals": dict(sorted(by_goal.items())), "filters": f, "matches": matches,
            "matches_total": len(rows) if f else None, "teams": teams,
            "live": [keep(a, ("rss_mb", "stop_requested", "last_event", "task_title")) for a in live], "cap": cap,
            "queued": [keep(a, ("reason",)) for a in queued],
            "ended": [keep(a, ("summary",)) for a in ended[:ENDED_MAX]],
            "ended_total": len(ended), "proc_scan": snap is not None}


# ── render ──

_WORKER_ROLES = ("pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "scout", "evaluator")


def _parked(con):
    if not has_column(con, "teams", "parked_at"):
        return set()
    return {r[0] for r in con.execute("SELECT id FROM teams WHERE status='active' AND parked_at IS NOT NULL")}


def lead_counts(con):
    """(workers live, leads live, parked teams): the same rules as lead_counts() in bin/factory."""
    w = con.execute("SELECT count(*) FROM agents WHERE status='live' AND role IN (%s)" % ",".join("?" * len(_WORKER_ROLES)),
                    _WORKER_ROLES).fetchone()[0]
    lv = con.execute("SELECT count(*) FROM agents WHERE status='live' AND role='lead'").fetchone()[0]
    p = len(_parked(con))
    return w, lv, p


def _lead_state(t, status, parked):
    s = status.get(t["lead_agent"]) if t["lead_agent"] else None
    if s in ("live", "starting"):
        return "live"
    if s == "queued":
        return "queued"
    return "parked" if t["id"] in parked else "-"


def _blocked(a):
    if not is_blocked(a.get("blocked_since")):
        return ""
    t = _hhmm(a["blocked_since"])
    return badge(f"blocked since {t}" if t else "blocked (time unknown)", "bad")


def _hhmm(epoch):
    """HH:MM of an epoch, or None when it cannot be parsed."""
    try:
        return dt.datetime.fromtimestamp(float(epoch)).strftime("%H:%M")
    except (TypeError, ValueError, OverflowError, OSError):
        return None


def _stat(s):
    return status(s) if s and s != "-" else ""


def _teams(d):
    cols = [{"key": "team", "label": "Team", "cls": "c-id mono"}, {"key": "goal", "label": "Goal", "cls": "c-who"},
            {"key": "lead", "label": "Lead", "cls": "c-title"}, {"key": "status", "label": "Status", "cls": "c-who"},
            {"key": "state", "label": "Lead state", "cls": "c-who"}, {"key": "live", "label": "Live", "cls": "c-age num"}]
    rows = [[t["id"], link(f"/goal/{t['goal_id']}", t["goal_id"]) if t["goal_id"] else "", t["lead_agent"] or "",
             _stat(t["status"]), _stat(t["lead_state"]), t["live_workers"]] for t in d["teams"]]
    return section("Teams", data_table(cols, [{"key": "t", "rows": rows, "count": len(rows)}], key="teams", empty="No data yet."),
                   count=len(rows) or None)


COLS = (AGENT_COLS[0], AGENT_COLS[1], AGENT_COLS[2],
        {"key": "runtime", "label": "Runtime", "cls": "c-who"},
        {"key": "model", "label": "Model", "cls": "c-who"}, AGENT_COLS[3])


def _cells(a, now):
    """agent_row cells plus runtime + model; blocked and stop-requested badges sit next to the name."""
    c = agent_row(dict(a, blocked=is_blocked(a.get("blocked_since"))), now)
    extra = [" ", _blocked(a)] + ([" ", badge("stop requested", "warn")] if a.get("stop_requested") else [])
    # agent_row already embeds runtime_badge in the who cell; column repeats a plain label for sorting/scan
    return [c[0], el("span", c[1], *extra), c[2], runtime_badge(a.get("runtime")), a.get("model") or "", c[3]]


def _urgent_first(a):
    return (0 if is_blocked(a.get("blocked_since")) or a["status"] == "lost" else 1, a["name"])


def _running(d, now):
    teams = {t["id"]: t for t in d["teams"]}
    by = {}
    for a in sorted(d["live"], key=_urgent_first):
        by.setdefault(a["team_id"], []).append(a)
    groups = []
    for tid, members in by.items():
        t = teams.get(tid)
        label = f"{tid} · {t['goal_id']}" if t and t["goal_id"] else (tid or "No team")
        groups.append({"key": tid or "none", "label": label, "count": len(members), "rows": [_cells(a, now) for a in members]})
    return section("Running", data_table(COLS, groups, key="agents-run", empty="No live agents."), count=len(d["live"]))


def _queued(d, now):
    cols = [AGENT_COLS[0], AGENT_COLS[1], {"key": "rt", "label": "Runtime", "cls": "c-who"}, AGENT_COLS[2],
            {"key": "since", "label": "Queued", "cls": "c-age num"}, {"key": "why", "label": "Reason", "cls": "c-who"}]
    rows = [[row_status("queued"),
             el("span", a["name"], " ", el("span", a["role"], class_="agent-role") if a["role"] != a["name"] else None),
             runtime_badge(a.get("runtime")),
             link(f"/task/{a['task_id']}", a["task_id"]) if a["task_id"] else "", ago(a["queued_at"], now),
             f"Waiting: {a['reason']}" if a.get("reason") else ""] for a in d["queued"]]
    return section("Queued", data_table(cols, [{"key": "q", "rows": rows, "count": len(rows)}], key="agents-queued",
                                        empty="Nothing is queued."), count=len(d["queued"]))


def _ended(d, now):
    cols = [AGENT_COLS[0], AGENT_COLS[1], {"key": "rt", "label": "Runtime", "cls": "c-who"}, AGENT_COLS[2],
            {"key": "ran", "label": "Ran", "cls": "c-age num"}, {"key": "sum", "label": "Summary", "cls": "c-who"}]
    rows = [[row_status(a["status"]),
             el("span", a["name"], " ", el("span", a["role"], class_="agent-role") if a["role"] != a["name"] else None),
             runtime_badge(a.get("runtime")),
             link(f"/task/{a['task_id']}", a["task_id"]) if a["task_id"] else "", ago(a["ended"], now), a.get("summary") or ""]
            for a in d["ended"]]
    n_all = sum(d["counts"].get(s, 0) for s in ("exited", "stopped"))   # same number as the Ended chip
    note = el("p", f"Showing the {d['ended_total']} that ended in the last 24 h.", class_="muted")
    body = (disclosure("Ended", note, data_table(cols, [{"key": "e", "rows": rows, "count": len(rows)}], key="agents-ended-t"),
                       key="agents-ended", count=n_all) if rows else empty_state("No agents ended in the last 24 h."))
    return el("section", body, class_="sect")


def _chips(d):
    f = d["filters"]
    cur = set(f.get("status", []))
    items = []
    for label, sts in CHIP_GROUPS:
        n = d["blocked"] if sts == (BLOCKED,) else sum(d["counts"].get(s, 0) for s in sts)
        if n or label in CHIP_ALWAYS:
            on = cur == set(sts)
            items.append((label, n, _href(f, status=None if on else list(sts)), on))
    out = [chips("Status", items)]
    roles = [(r, n, _href(f, role=None if f.get("role") == [r] else [r]), f.get("role") == [r]) for r, n in d["roles"].items()]
    out.append(chips("Role", roles))
    rts = [(r, n, _href(f, runtime=None if f.get("runtime") == [r] else [r]), f.get("runtime") == [r])
           for r, n in d.get("runtimes", {}).items()]
    if rts:
        out.append(chips("Runtime", rts))
    goals = list(d["goals"].items())
    first, rest = goals[:GOAL_CHIPS], goals[GOAL_CHIPS:]
    row = lambda gs: [(g, n, _href(f, goal=None if f.get("goal") == [g] else [g]), f.get("goal") == [g]) for g, n in gs]
    out.append(chips("Goal", row(first)))
    if rest:
        out.append(disclosure(f"{len(rest)} more goals", chips("More goals", row(rest)), key="agents-goals"))
    if f:
        shown = ", ".join(f"{k}={','.join(v)}" for k, v in f.items())
        out.append(el("p", f"Showing: {shown} · ", link("/agents", "clear"), class_="muted"))
    return out


def _summary(d):
    n = d["counts"]
    parts = [f"{n.get('live', 0)} running"]
    if d.get("cap"):
        parts.append(f"cap {d['cap']}")
    parts.append(f"{n.get('queued', 0)} queued")
    if d["blocked"]:
        parts.append(f"{d['blocked']} blocked")
    rts = d.get("runtimes") or {}
    if rts:
        parts.append(" · ".join(f"{n} {r}" for r, n in rts.items()))
    over = str(d.get("cap") or "").isdigit() and n.get("live", 0) >= int(d["cap"])
    info = el("details", el("summary", "(i)", aria_label="About these states"),
              el("p", "Busy/idle is not tracked; last event age is shown. Runtime is Claude Code or Cursor Agent "
                      "(docs/runtimes.md).", class_="muted"),
              class_="disc disc--i", data_key="agents-info")
    return el("div", el("p", " · ".join(parts), class_="summary-line summary-line--warn" if over else "summary-line"), info, class_="summary")


def _matching(d, now):
    rows = [_cells(a, now) for a in d["matches"]]
    return section("Matching agents", data_table(COLS, [{"key": "m", "rows": rows, "count": len(rows)}], key="agents-match",
                                                 empty="No agents match these filters."), count=d["matches_total"])


def render(ctx, d):
    now = ctx.now
    head = [_summary(d), *_chips(d)]
    if d["filters"]:
        return el("div", head, _matching(d, now), _teams(d))
    return el("div", head, _running(d, now), _queued(d, now), _teams(d), _ended(d, now))
