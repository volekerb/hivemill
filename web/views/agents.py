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
from web.html import badge, card, dur, el, empty, link, table, ts

TITLE = "Agents"
SCOPE_MAX = 120   # teams.scope is a director-written summary; never return the full text

SUMMARY_MAX = 200
ENDED_WINDOW_S = 24 * 3600
ENDED_MAX = 40
PROC_TTL_S = 30
PROC_BUDGET_S = 0.08    # a scan gives up after this long; the partial result is cached and shown as is
# Filters (?status=, ?role=, ?goal=). Values are comma-separated; unknown values are ignored. The server keeps only the
# first value of a repeated key, so repeating a key is not supported; use commas.
# The status set comes from the agents table (so 'starting' etc. work); "blocked" is derived from blocked_since.
BLOCKED = "blocked"
_STATUS_ORDER = ("live", "starting", "queued", "stopped", "exited", "done", "lost", "cancelled")   # display order only
ROLES = ("boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "scout",
         "evaluator")   # mirrors bin/factory ROLES (web/ never imports it)
_GOAL_RE = re.compile(r"^G-\d+$")
MATCH_MAX = 200
_KIND = {"live": "ok", "queued": "warn", "parked": "muted", "done": "ok", "exited": "muted", "lost": "bad", "stopped": "muted"}

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
    """{"status": [...], "role": [...], "goal": [...]} from ctx.query; invalid values are dropped, order kept.

    `statuses` are the status values present in the agents table; "blocked" is always accepted.
    """
    known = {*statuses, BLOCKED}
    out = {}
    for key, ok in (("status", known.__contains__), ("role", ROLES.__contains__), ("goal", _GOAL_RE.match)):
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
    return "goal" not in f or a["goal_id"] in f["goal"]


def _href(f, **change):
    """/agents URL for filters `f` with the given keys replaced (empty value removes the key)."""
    g = dict(f)
    for k, v in change.items():
        if v:
            g[k] = v
        else:
            g.pop(k, None)
    qs = "&".join(f"{k}={'%2C'.join(quote(v, safe='') for v in g[k])}" for k in ("status", "role", "goal") if g.get(k))
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
    by_role, by_goal = {}, {}
    for a in all_rows:
        by_role[a["role"]] = by_role.get(a["role"], 0) + 1
        if a["goal_id"] and _GOAL_RE.match(a["goal_id"]):
            by_goal[a["goal_id"]] = by_goal.get(a["goal_id"], 0) + 1
    n_blocked = sum(1 for a in all_rows if is_blocked(a["blocked_since"]))

    def keep(a, extra=()):
        keys = ("name", "role", "goal_id", "team_id", "task_id", "status", "model", "started", "ended", "queued_at",
                "age_s", "blocked_since", *extra)
        return {k: a.get(k) for k in keys}   # explicit allowlist: spawn_args / session_id / pane never leave this module
    matches = [keep(a) for a in sorted(rows, key=lambda a: a["name"])[:MATCH_MAX]] if f else []
    return {"counts": counts, "blocked": n_blocked, "roles": {r: by_role[r] for r in ROLES if r in by_role},
            "goals": dict(sorted(by_goal.items())), "filters": f, "matches": matches,
            "matches_total": len(rows) if f else None, "teams": teams,
            "live": [keep(a, ("rss_mb", "stop_requested")) for a in live],
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


def _status(s):
    return badge(s, _KIND.get(s, "muted"))


def _who(a):
    return el("span", a["name"], " ", el("span", a["role"], class_="muted"))


def _where(a):
    out = []
    if a.get("goal_id"):
        out.append(link(f"/goal/{a['goal_id']}", a["goal_id"]))
    if a.get("task_id"):
        out += [" ", link(f"/task/{a['task_id']}", a["task_id"])]
    return el("span", *out) if out else "-"


def _teams(d):
    rows = [(t["id"], link(f"/goal/{t['goal_id']}", t["goal_id"]) if t["goal_id"] else "-", t["lead_agent"] or "-",
             _status(t["status"]) if t["status"] else "-", _status(t["lead_state"]) if t["lead_state"] != "-" else "-",
             (t["live_workers"], "num")) for t in d["teams"]]
    return card("Teams", table(["Team", "Goal", "Lead", "Status", "Lead state", "Live"], rows, cls="table table--compact")
                if rows else empty("No data yet."))


def _live(d):
    rows = []
    for a in d["live"]:
        state = [_status("live"), " ", el("span", dur(a["age_s"]), class_="muted")]
        if a["stop_requested"]:
            state += [" ", badge("stop requested", "warn")]
        if is_blocked(a.get("blocked_since")):
            state += [" ", _blocked(a)]
        rows.append((_who(a), _where(a), a["model"] or "-", el("span", *state),
                     (f"{a['rss_mb']} MB" if a["rss_mb"] is not None else "—", "num")))
    return card("Live agents", table(["Agent", "Goal / task", "Model", "Status", "Mem"], rows, cls="table table--compact")
                if rows else empty("No live agents."))


def _queued(d):
    rows = [(_who(a), ts(a["queued_at"]), a["reason"] or "-") for a in d["queued"]]
    return card("Queued agents", table(["Agent", "Queued", "Last reason"], rows, cls="table table--compact")
                if rows else empty("Nothing is queued."))


def _ended(d):
    rows = []
    for a in d["ended"]:
        who = el("div", _who(a), el("div", a["summary"], class_="muted") if a.get("summary") else "")
        rows.append((who, _where(a), el("span", ts(a["started"]), " → ", ts(a["ended"]), " ", dur(a["age_s"])),
                     _status(a["status"])))
    body = (el("details", el("summary", f"{d['ended_total']} ended in the last 24 h"),
               table(["Agent", "Goal / task", "Ran", "Status"], rows, cls="table table--compact"))
            if rows else empty("No agents ended in the last 24 h."))
    return card("Recently ended", body)


def _hhmm(epoch):
    """HH:MM of an epoch, or None when it cannot be parsed."""
    try:
        return dt.datetime.fromtimestamp(float(epoch)).strftime("%H:%M")
    except (TypeError, ValueError, OverflowError, OSError):
        return None


def _blocked(a):
    if not is_blocked(a.get("blocked_since")):
        return ""
    t = _hhmm(a["blocked_since"])
    return badge(f"blocked since {t}" if t else "blocked (time unknown)", "bad")


def _facet(label, f, key, items):
    """A row of links, one per value; a link sets this facet to that value and keeps the other filters."""
    out = []
    for name, n in items:
        text = f"{name} {n}"
        out += [link(_href(f, **{key: [name]}), badge(text, "info") if name in f.get(key, []) else text), " "]
    return el("p", el("span", label + ": ", class_="muted"), *out)


def _filters(d):
    f = d["filters"]
    present = [s for s in d["counts"] if isinstance(s, str) and s != BLOCKED]
    present.sort(key=lambda s: (_STATUS_ORDER.index(s) if s in _STATUS_ORDER else len(_STATUS_ORDER), s))
    status = [(s, d["counts"][s]) for s in present]
    status.append((BLOCKED, d["blocked"]))
    body = [_facet("Status", f, "status", status), _facet("Role", f, "role", d["roles"].items()),
            _facet("Goal", f, "goal", d["goals"].items())]
    if f:
        shown = ", ".join(f"{k}={','.join(v)}" for k, v in f.items())
        body.append(el("p", f"Showing: {shown} · ", link("/agents", "clear"), class_="muted"))
    body.append(el("p", "herdr working/idle is not shown: this page is read-only and never calls herdr. "
                        "“blocked” comes from the tick (blocked_since).", class_="muted"))
    return card("Filters", *body)


def _matching(d):
    rows = [(_who(a), _where(a), a["model"] or "-", el("span", _status(a["status"]), " ", _blocked(a)),
             ts(a["started"] or a["queued_at"])) for a in d["matches"]]
    return card(f"Matching agents ({d['matches_total']})",
                table(["Agent", "Goal / task", "Model", "Status", "Since"], rows, cls="table table--compact")
                if rows else empty("No agents match these filters."))


def render(ctx, d):
    head = _filters(d)
    if d["filters"]:
        return el("div", head, _matching(d), _teams(d))
    return el("div", head, _teams(d), _live(d), _queued(d), _ended(d))
