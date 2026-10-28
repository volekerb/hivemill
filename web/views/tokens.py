"""Tokens page: tokens and estimated API-equivalent cost per goal / task / agent / model / day, plus 5h / weekly usage.

Numbers come from the TokenIndex snapshot (ctx.tokens), attributed to factory agents with web.tokens.attribute and
priced with web.prices. A model without a price shows "n/a" and is counted separately, never as $0. Only usage
counts, ids and names are shown: no message text, no full session ids (8-char prefixes on the agent rows only).
"""
import datetime as dt
import re

from web import queries
from web.html import bar, card, el, empty, kpi, link, table
from web.prices import PRICES_AS_OF
from web.tokens import attribute, rollup

TITLE = "Tokens"

PERIODS = ("1", "7", "30", "all")
DEFAULT_PERIOD = "7"
TOP = 20
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
           "label": None if u.get("fresh") else f"stale: {u.get('age_min', -1)} min old"}
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
    base = {"period": period, "prices_as_of": PRICES_AS_OF, "usage": usage, "ready": bool(snap.get("ready")),
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

def _href(period, goal=None, model=None):
    parts = [f"days={period}"]
    if goal:
        parts.append(f"goal={goal}")
    if model:
        parts.append(f"model={model}")
    return "/tokens?" + "&".join(parts)


def _model_href(period, goal, model):
    return _href(period, goal, model) if re.match(r"^[\w.-]+$", model) else None


def _filters(d):
    labels = {"1": "today", "7": "7 days", "30": "30 days", "all": "all time"}
    items = [el("span", el("strong", labels[p]) if p == d["period"] else link(_href(p, d["goal"], d["model"]), labels[p]), " ")
             for p in PERIODS]
    active = []
    if d["goal"]:
        active.append(f"goal {d['goal']}")
    if d["model"]:
        active.append(f"model {d['model']}")
    if active:
        items.append(el("span", " · filtered by " + ", ".join(active) + " · ", link(_href(d["period"]), "clear")))
    return el("p", items, class_="muted")


def _usage_card(d):
    u = d["usage"]
    if u is None:
        return card("Claude usage", empty("No usage data."))
    if u.get("unreadable"):
        return card("Claude usage", el("p", "unreadable", class_="stale"))
    out = []
    for label, pct, reset in (("5-hour", u["five_hour"], u["five_hour_resets_at"]),
                              ("Weekly", u["seven_day"], u["seven_day_resets_at"])):
        when = ""
        if reset is not None:
            try:
                when = "resets " + dt.datetime.fromtimestamp(reset).astimezone().strftime("%m-%d %H:%M")
            except (OverflowError, OSError, ValueError):
                when = ""
        out.append(el("div", el("div", label, class_="kpi-label"), bar(pct), el("span", when, class_="muted"), class_="kpi"))
    if u["label"]:
        out.append(el("p", u["label"], class_="stale"))
    return card("Claude usage", el("div", out, class_="grid"))


def _cost_cell(e):
    if e["cost"] is None:
        return "n/a"
    return _money(e["cost"]) + ("+" if e["cost_unknown"] else "")


def _more(n):
    return el("p", f"+{n} more", class_="muted") if n else ""


def _tokens_cell(e):
    return f"{_fmt(e['input'])} / {_fmt(e['output'])}"


def _cache_cell(e):
    return f"{_fmt(e['cache_read'])} / {_fmt(e['cache_write'])}"


def _section(title, pair, cols, make_row):
    items, more = pair
    if not items:
        return card(title, empty("no data yet"))
    return card(title, table(cols, [make_row(e) for e in items], cls="table table--compact"), _more(more))


def _kpis(d):
    t = d["totals"]
    if t["cost"] is None:
        cost = "n/a"
    else:
        cost = _money(t["cost"])
    label = f"Estimated API-equivalent cost (list prices as of {d['prices_as_of']})"
    note = ""
    if d["unpriced"]:
        note = el("p", f"{t['unpriced_requests']} requests on models without a price are not in the cost: ",
                  ", ".join(d["unpriced"]), class_="muted")
    return el("div", el("div", kpi("Input", _fmt(t["input"])), kpi("Output", _fmt(t["output"])),
                        kpi("Cache read", _fmt(t["cache_read"])), kpi("Cache write (5m+1h)", _fmt(t["cache_write"])),
                        kpi("Requests", _fmt(t["requests"])), kpi(label, cost), class_="grid"), note)


def render(ctx, d):
    parts = [_filters(d), _usage_card(d)]
    if not d["ready"]:
        parts.append(card("Token index", el("p", f"indexing… {d['files']} files / {d['mb_read']} MB read", class_="stale")))
        parts += [card(t, empty("no data yet")) for t in ("By goal", "By task", "By agent", "By model", "By day")]
        return el("div", parts)
    parts.append(_kpis(d))

    def goal_row(e):
        k = e["key"]
        name = k if k == "unattributed" else el("span", link(_href(d["period"], k, d["model"]), k), " ", e.get("title") or "")
        return (name, (_tokens_cell(e), "num"), (_cache_cell(e), "num"), (_cost_cell(e), "num"))

    def task_row(e):
        k = e["key"]
        name = k if k == "unattributed" else el("span", link(f"/task/{k}", k), " ", e.get("title") or "")
        return (name, (_tokens_cell(e), "num"), (e["requests"], "num"), (_cost_cell(e), "num"))

    def agent_row(e):
        k = e["key"]
        if k == "unattributed":
            return ("unattributed", (_tokens_cell(e), "num"), (e["requests"], "num"), (_cost_cell(e), "num"))
        sess = ", ".join(e.get("sessions", [])) + (f" +{e['more_sessions']}" if e.get("more_sessions") else "")
        return (el("span", k, " ", el("span", e.get("role") or "", class_="muted"), " ", el("span", sess, class_="mono")),
                (_tokens_cell(e), "num"), (e["requests"], "num"), (_cost_cell(e), "num"))

    def model_row(e):
        k = e["key"]
        href = _model_href(d["period"], d["goal"], k)
        return (link(href, k) if href else k, (_tokens_cell(e), "num"), (_cache_cell(e), "num"), (_cost_cell(e), "num"))

    top_day = max((e["input"] + e["output"] + e["cache_read"] + e["cache_write"] for e in d["by_day"][0]), default=0)

    def day_row(e):
        tot = e["input"] + e["output"] + e["cache_read"] + e["cache_write"]
        pct = 100 * tot / top_day if top_day else 0
        return (e["key"], bar(pct, _fmt(tot), f"{_fmt(tot)} of max {_fmt(top_day)}"), (_cost_cell(e), "num"))

    parts += [
        _section("By goal", d["by_goal"], ["Goal", "In / out", "Cache r / w", "Cost"], goal_row),
        _section("By task", d["by_task"], ["Task", "In / out", "Requests", "Cost"], task_row),
        _section("By agent", d["by_agent"], ["Agent", "In / out", "Requests", "Cost"], agent_row),
        _section("By model", d["by_model"], ["Model", "In / out", "Cache r / w", "Cost"], model_row),
        _section("By day", d["by_day"], ["Day", "Usage", "Cost"], day_row),
        card("Attribution quality", table(["Matched by", "Sessions"], [(v, (d["via"].get(v, 0), "num")) for v in VIA],
                                          cls="table table--compact")),
        el("p", f"Costs are estimates at list prices as of {d['prices_as_of']}; a trailing + means some tokens were on "
                "models without a price.", class_="muted"),
    ]
    return el("div", parts)
