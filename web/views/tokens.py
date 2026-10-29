"""Tokens page: tokens and estimated API-equivalent cost per day and, in one table, per goal / task / agent / model, plus
5h / weekly usage. The breakdown shows one of the four (?by=, whitelisted; anything else is "goal").

Numbers come from the TokenIndex snapshot (ctx.tokens), attributed to factory agents with web.tokens.attribute and
priced with web.prices. A model without a price shows "n/a" and is counted separately, never as $0. Only usage
counts, ids and names are shown: no message text, no full session ids (8-char prefixes on the agent rows only).
"""
import datetime as dt
import re

from web import charts, queries
from web.components import data_table, disclosure, empty_state, href, section, segmented, tile
from web.html import el, link, table
from web.prices import PRICES_AS_OF
from web.tokens import attribute, rollup
from web.views import resources

TITLE = "Tokens"

PERIODS = ("1", "7", "30", "all")
DEFAULT_PERIOD = "7"
TOP = 20
BY = ("goal", "task", "agent", "model")
BY_LABEL = {"goal": "By goal", "task": "By task", "agent": "By agent", "model": "By model"}
VIA = ("session", "event", "name", "worktree", "unattributed")
GOAL_RE = re.compile(r"^(G-\d+|unattributed)$")


def _fmt(n):
    n = n or 0
    if n >= 1_000_000:
        return f"{n / 1_000_000:.1f}M"
    if n >= 1_000:
        return f"{n / 1_000:.1f}k"
    return str(n)


def _money(c):
    return "n/a" if c is None else f"${c:.2f}"


def _query(ctx):
    q = getattr(ctx, "query", None)
    return q if isinstance(q, dict) else {}


def _usage(ctx):
    fn = getattr(ctx, "usage_fn", None)
    if fn is None:
        return None
    try:
        u = fn()
    except Exception:  # noqa: BLE001 — the usage file is outside our control
        return {"unreadable": True}
    return u if isinstance(u, dict) else {"unreadable": True}


def _reset(v):
    """usage() already returns epoch ints or None; keep only that."""
    return v if isinstance(v, int) and not isinstance(v, bool) else None


def _usage_data(u):
    if u is None:
        return None
    if u.get("unreadable"):
        return {"unreadable": True, "label": "unreadable"}
    out = {"five_hour": u.get("five_hour"), "seven_day": u.get("seven_day"), "fresh": bool(u.get("fresh")),
           "age_min": u.get("age_min"), "label": None if u.get("fresh") else f"stale: {u.get('age_min', -1)} min old"}
    for k in ("five_hour_resets_at", "seven_day_resets_at"):
        v = u.get(k)
        out[k] = _reset(v)
    return out


def _since(ctx, period):
    if period == "all":
        return None
    today = ctx.now.astimezone().date()
    return (today - dt.timedelta(days=int(period) - 1)).isoformat()


def _entry(key, e):
    return {"key": key, "input": e["input"], "output": e["output"], "cache_read": e["cache_read"],
            "cache_write": e["cache_write_5m"] + e["cache_write_1h"], "requests": e["requests"],
            "cost": e["cost"], "cost_unknown": e["cost_unknown"]}


def _top(buckets, extra=None):
    """Entries sorted by cost (unknown last), then tokens; the first TOP and the count of the rest."""
    items = []
    for key, e in buckets.items():
        row = _entry(key, e)
        row.update((extra or {}).get(key, {}))
        items.append(row)
    items.sort(key=lambda r: (-(r["cost"] or 0), -(r["input"] + r["output"] + r["cache_read"] + r["cache_write"]), str(r["key"])))
    return items[:TOP], max(0, len(items) - TOP)


def data(ctx, **params):
    q = _query(ctx)
    period = q.get("days") if q.get("days") in PERIODS else DEFAULT_PERIOD
    snap = ctx.tokens() if getattr(ctx, "tokens", None) else None
    snap = snap or {}
    usage = _usage_data(_usage(ctx))
    by = q.get("by") if q.get("by") in BY else "goal"
    base = {"period": period, "by": by, "prices_as_of": PRICES_AS_OF, "usage": usage, "ready": bool(snap.get("ready")),
            "files": snap.get("files", 0), "mb_read": round(snap.get("bytes_total", 0) / 1048576, 1)}
    if not base["ready"]:
        return base | {"goal": None, "model": None, "models": [], "totals": None, "by_goal": ([], 0), "by_task": ([], 0),
                       "by_agent": ([], 0), "by_model": ([], 0), "by_day": ([], 0), "via": {}, "unpriced": []}
    rows = snap.get("rows", [])
    since = _since(ctx, period)
    if since:
        rows = [r for r in rows if r["day"] >= since]       # an "unknown" day sorts after digits: only in "all"
    models = sorted({r["model"] for r in rows})
    model = q.get("model") if q.get("model") in models else None
    att = attribute(snap, ctx.db)
    goal = q.get("goal") if GOAL_RE.match(q.get("goal") or "") else None
    if model:
        rows = [r for r in rows if r["model"] == model]
    if goal:
        rows = [r for r in rows if (att.get(r["session"], {}).get("goal") or "unattributed") == goal]
    view = {"rows": rows, "sessions": snap.get("sessions", {})}
    roll = rollup(view, att)
    used = {r["session"] for r in rows}
    via = {v: 0 for v in VIA}
    agent_info = {}
    for sid in sorted(used):
        a = att.get(sid) or {"via": "unattributed", "agent": None, "role": None}
        via[a["via"]] = via.get(a["via"], 0) + 1
        if a["agent"]:
            info = agent_info.setdefault(a["agent"], {"role": a["role"], "sessions": []})
            info["sessions"].append(sid[:8])
    gt = {r["id"]: r["title"] for r in ctx.db.execute("SELECT id, title FROM goals")}
    tt = {r["id"]: r["title"] for r in ctx.db.execute("SELECT id, title FROM tasks")}
    extra_goal = {g: {"title": queries._strip_tags(t)} for g, t in gt.items()}
    extra_task = {t: {"title": queries._strip_tags(n)} for t, n in tt.items()}
    days = sorted(roll["by_day"], reverse=True)
    by_day = ([_entry(d, roll["by_day"][d]) for d in days[:TOP]], max(0, len(days) - TOP))
    unpriced = sorted(m for m, e in roll["by_model"].items() if e["cost_unknown"])
    unpriced_requests = sum(r["requests"] for r in rows if r["model"] in unpriced)
    totals = _entry("all", roll["total"]) | {"unpriced_requests": unpriced_requests}
    return base | {"goal": goal, "model": model, "models": models, "totals": totals,
                   "by_goal": _top(roll["by_goal"], extra_goal), "by_task": _top(roll["by_task"], extra_task),
                   "by_agent": _top(roll["by_agent"], {k: {"role": v["role"], "sessions": v["sessions"][:3],
                                                           "more_sessions": max(0, len(v["sessions"]) - 3)}
                                                      for k, v in agent_info.items()}),
                   "by_model": _top(roll["by_model"]), "by_day": by_day, "via": via, "unpriced": unpriced}


# ───────────────────────────── rendering ─────────────────────────────

def _href(period, goal=None, model=None, by=None):
    return href("/tokens", {"days": period, "goal": goal, "model": model, "by": by if by in BY[1:] else None})


def _model_href(period, goal, model, by=None):
    return _href(period, goal, model, by) if re.match(r"^[\w.-]+$", model) else None


def _controls(d):
    labels = {"1": "Today", "7": "7 days", "30": "30 days", "all": "All time"}
    seg = segmented("Range", [(labels[p], _href(p, d["goal"], d["model"], d["by"]), p == d["period"]) for p in PERIODS])
    active = []
    if d["goal"]:
        active.append(f"goal {d['goal']}")
    if d["model"]:
        active.append(f"model {d['model']}")
    if not active:
        return seg
    return el("div", seg, el("p", "Filtered by " + ", ".join(active) + " · ", link(_href(d["period"], by=d["by"]), "clear"),
                             class_="muted"))


def _pct(v):
    """A percentage as a float; anything else (the usage file is outside our control) is unknown."""
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


def _usage_section(ctx, d):
    u = d["usage"]
    if u is None:
        return section("Claude usage", empty_state("No usage data."))
    if u.get("unreadable"):
        return section("Claude usage", el("p", "unreadable", class_="stale"))
    view = {"five_hour": _pct(u["five_hour"]), "seven_day": _pct(u["seven_day"]), "stale": not u["fresh"],
            "cap_five_hour": resources._cfg(ctx, "max_five_hour_pct", 92), "cap_seven_day": resources._cfg(ctx, "max_seven_day_pct", 92)}
    note = {k: u.get(k) for k in ("five_hour_resets_at", "seven_day_resets_at")}
    if u["fresh"]:
        note["age_min"] = u.get("age_min")
    return section("Claude usage", el("div", resources.usage_row(view), class_="ins-gauges"), resources.usage_note(note),
                   el("p", u["label"], class_="stale") if u["label"] else None)


def _cost_cell(e):
    if e["cost"] is None:
        return "n/a"
    return _money(e["cost"]) + ("+" if e["cost_unknown"] else "")


def _all(e):
    return e["input"] + e["output"] + e["cache_read"] + e["cache_write"]


def _tiles(d):
    t = d["totals"]
    total = _all(t)
    share = f"{round(100 * t['cache_read'] / total)}%" if total else "-"
    note = ""
    if d["unpriced"]:
        note = el("p", f"{t['unpriced_requests']} requests on models without a price are not in the cost: ",
                  ", ".join(d["unpriced"]), class_="muted")
    return el("div", el("div",
                        tile("Total tokens", _fmt(total),
                             definition="Input, output and cache tokens (read and write) of every request in the range."),
                        tile("Cost estimate", _money(t["cost"]),
                             definition=f"Estimated API-equivalent cost at list prices as of {d['prices_as_of']}. "
                                        "Requests on a model without a price are left out and listed below."),
                        tile("Cache read share", share, definition="Cache-read tokens as a share of all tokens. A high share means "
                             "most context was served from the cache."),
                        tile("Requests", _fmt(t["requests"]), definition="Model requests that carried usage counts."),
                        class_="tiles tiles--4"), note)


def _day_section(d):
    items, more = d["by_day"]
    points = [(e["key"], _all(e)) for e in reversed(items)]
    return section("By day", charts.columns(points, "tokens"),
                   el("p", f"+{more} earlier days not shown", class_="muted") if more else None)


def _pin_last(items):
    """The "unattributed" row goes last whatever the order was."""
    return [e for e in items if e["key"] != "unattributed"] + [e for e in items if e["key"] == "unattributed"]


def _unattributed():
    return el("span", "Not attributed", " ",
              el("details", el("summary", "i", aria_label="What is Not attributed?"),
                 el("p", "Usage from sessions that could not be matched to a factory goal, task or agent. "
                         "See Attribution quality below.", class_="tile-def"), class_="disc--i"))


def _name(d, by, e):
    k = e["key"]
    if k == "unattributed":
        return _unattributed()
    if by == "goal":
        return el("span", link(_href(d["period"], k, d["model"], d["by"]), k), " ", e.get("title") or "")
    if by == "task":
        return el("span", link(f"/task/{k}", k), " ", e.get("title") or "")
    if by == "agent":
        sess = ", ".join(e.get("sessions", [])) + (f" +{e['more_sessions']}" if e.get("more_sessions") else "")
        return el("span", k, " ", el("span", e.get("role") or "", class_="muted"), title=f"sessions: {sess}" if sess else None)
    href_ = _model_href(d["period"], d["goal"], k, d["by"])
    return link(href_, k) if href_ else k


def _breakdown(d):
    by = d["by"]
    seg = segmented("Breakdown", [(BY_LABEL[b], _href(d["period"], d["goal"], d["model"], b), b == by) for b in BY])
    items, more = d[f"by_{by}"]
    grand = _all(d["totals"])
    if not items:
        return section("Breakdown", seg, empty_state("no data yet"))
    cols = [{"key": "name", "label": BY_LABEL[by].removeprefix("By ").capitalize(), "cls": "c-title"},
            {"key": "tokens", "label": "Tokens", "cls": "num"}, {"key": "share", "label": "Share", "cls": "num"},
            {"key": "io", "label": "In / out", "cls": "num ins-wide"}, {"key": "req", "label": "Requests", "cls": "num ins-wide"},
            {"key": "cost", "label": "Cost", "cls": "num ins-wide"}]
    rows = []
    for e in _pin_last(items):
        tot = _all(e)
        pct = 100 * tot / grand if grand else 0
        rows.append([_name(d, by, e), _fmt(tot), f"{pct:.1f}%" if pct < 10 else f"{pct:.0f}%",
                     f"{_fmt(e['input'])} / {_fmt(e['output'])}", e["requests"], _cost_cell(e)])
    return section("Breakdown", seg, data_table(cols, [{"key": "all", "label": None, "count": len(rows), "rows": rows}], key="breakdown"),
                   el("p", f"+{more} more", class_="muted") if more else None)


def render(ctx, d):
    parts = [_controls(d), _usage_section(ctx, d)]
    if not d["ready"]:
        parts.append(section("Token index", el("p", f"indexing… {d['files']} files / {d['mb_read']} MB read", class_="stale")))
        return el("div", parts)
    parts += [_tiles(d), _day_section(d), _breakdown(d),
              disclosure("Attribution quality", table(["Matched by", "Sessions"], [(v, (d["via"].get(v, 0), "num")) for v in VIA],
                                                       cls="table table--compact"), key="attribution"),
              el("p", f"Costs are estimates at list prices as of {d['prices_as_of']}; a trailing + means some tokens were on "
                      "models without a price.", class_="muted")]
    return el("div", parts)
