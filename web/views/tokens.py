"""Spend page (G-069, replaces Tokens; /tokens redirects here): where the budget goes.

Every figure comes from `hm-obs api spend` (ctx.obs), which reads state/obs.db built by the tick from the Claude Code
session logs. Dollars are at API list prices. The page shows a strip of five headline figures with deltas against the
previous window, a daily work / polling / spawn chart and one group-by table (role, model, goal, project, agent,
session) whose rows drill down. Only numbers, ids and names are shown: no message text.
"""
import datetime as dt

from web import charts, queries
from web.components import banners, data_table, empty_state, howto, href, section, segmented, spark_cell, strip
from web.html import el, link

TITLE = "Spend"
PATH = "/spend"
DOCS = "docs/obs.md"   # the one "How this is counted" link
GROUP_LABEL = {"role": "Role", "model": "Model", "goal": "Goal", "project": "Project", "agent": "Agent", "session": "Session"}
SERIES = (("work_usd", "Work", "b-work"), ("polling_usd", "Polling", "b-polling"), ("spawn_usd", "Spawn", "b-spawn"))
CURSOR_NOTE = ("Token usage is not reported by Cursor CLI: it leaves no token or usage counts on this machine, so Cursor "
               "runtime agents show sessions and agent-hours per model from the agents table, and no cost.")


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


def _hours(a, since_e, now_e):
    """Hours of agent `a` inside [since_e, now_e]: started -> ended (live: now). 0 when it has no start."""
    start = queries._epoch(a.get("started"))
    if start is None:
        return 0.0
    end = queries._epoch(a.get("ended")) if a["status"] in queries.ENDED_AGENT else now_e
    end = now_e if end is None else min(end, now_e)
    lo = max(start, since_e) if since_e is not None else start
    return max(0.0, end - lo) / 3600


def cursor_activity(ag, since, now, org=None):
    """Cursor runtime agents in the range, per (org, model): {org, model, sessions, live, hours}. Cursor CLI reports no
    token counts, so this is the honest measure of what the Cursor runtime did. `since` is a local ISO date or None."""
    since_e = None
    if since:
        since_e = dt.datetime.fromisoformat(since).replace(tzinfo=now.astimezone().tzinfo).timestamp()
    now_e = now.timestamp()
    out = {}
    for a in ag:
        if a.get("runtime") != "cursor" or (org and a["org"] != org) or a["status"] == "queued":
            continue
        end = queries._epoch(a.get("ended")) if a["status"] in queries.ENDED_AGENT else now_e
        if since_e is not None and end is not None and end < since_e:
            continue
        key = (a["org"], a.get("model") or "unknown")
        e = out.setdefault(key, {"org": key[0], "model": key[1], "sessions": 0, "live": 0, "hours": 0.0})
        e["sessions"] += 1
        e["live"] += a["status"] == "live"
        e["hours"] += _hours(a, since_e, now_e)
    rows = sorted(out.values(), key=lambda e: (-e["hours"], e["org"], e["model"]))
    for e in rows:
        e["hours"] = round(e["hours"], 2)
    return rows


def data(ctx, **params):
    q = _query(ctx)
    rng = queries.range_param(q)
    obs, err = ctx.obs("spend") if getattr(ctx, "obs", None) else (None, "hm-obs is not available in this server")
    return {"range": rng[0], "range_label": rng[2], "obs": obs, "error": err, "usage": _usage_data(_usage(ctx)),
            "query": _page_state(q)}


def _page_state(q):
    """The query keys Spend links carry: a valid range, org, group and drill values. Nothing else is echoed."""
    out = {"range": queries.range_param(q)[0] if q.get("range") else None, "org": queries.org_param(q),
           "group": q.get("group") if q.get("group") in queries.OBS_GROUPS else None}
    for g in queries.OBS_GROUPS:
        out[g] = queries.obs_value(q.get(g))
    return {k: v for k, v in out.items() if v}


# ───────────────────────────── rendering ─────────────────────────────

def _money(v):
    if v is None:
        return "–"
    return f"${v:,.0f}" if abs(v) >= 100 else f"${v:,.2f}"


def _pct(v):
    return "–" if v is None else f"{v:.0f}%" if abs(v) >= 10 else f"{v:.1f}%"


def _delta(cur, prev, fmt, worse_up=True):
    """'+$3.10 vs prev 24 h' with a tone: up is worse for cost figures. None when there is nothing to compare."""
    if cur is None or prev is None:
        return None, "neutral"
    d = cur - prev
    if abs(d) < 1e-9:
        return "no change vs previous", "neutral"
    sign = "+" if d > 0 else "−"
    tone = ("warn" if worse_up else "good") if d > 0 else ("good" if worse_up else "neutral")
    return f"{sign}{fmt(abs(d))} vs previous", tone


def _link(d, **change):
    """/spend with the page state (range, project, org, group, drill filters) plus `change` (None drops a key)."""
    params = dict(d["query"])
    params.update(change)
    return href(PATH, {k: v for k, v in params.items() if v not in (None, "")})


def _usage_pct(u, key):
    try:
        return float(u.get(key))
    except (TypeError, ValueError):
        return None


def _strip(d):
    o, cur, prev = d["obs"], d["obs"]["current"], d["obs"].get("previous") or {}
    items = []
    for label, key, fmt, defn in (
            ("Spend", "cost_usd", _money, "Sum of every request in the window at API list prices (not the plan price)."),
            ("Per merged PR", "per_merged_pr_usd", _money, "Spend divided by the PRs merged in the window. – when none merged."),
            ("Polling", "polling_pct", _pct, "Share of spend on turns that only waited or checked status (inbox, sleep, "
                                             "CI wait) and changed nothing."),
            ("Cache writes", "cache_write_pct", _pct, "Share of spend on writing the prompt cache. High when sessions "
                                                      "restart or their context churns.")):
        delta, tone = _delta(cur.get(key), prev.get(key), fmt)
        items.append({"label": label, "value": fmt(cur.get(key)), "delta": delta, "tone": tone, "definition": defn})
    u = d.get("usage") or {}
    week = _usage_pct(u, "seven_day") if not u.get("unreadable") else None
    near = week is not None and week >= 80
    items.append({"label": "Weekly limit", "value": _pct(week), "tone": "warn" if near else "neutral",
                  # amber is never the only signal: a near-limit week says so in words (the strip adds "!")
                  "delta": u.get("label") if not u.get("fresh", True) else ("near the limit" if near else None),
                  "definition": "Claude plan weekly usage from the usage file; the plan limit, not dollars."})
    if o.get("unattributed_pct") is not None:
        items[0]["delta"] = (items[0]["delta"] or "") + f" · {_pct(o['unattributed_pct'])} not attributed"
    return strip(items, key="spend-strip")


def _bin_label(start, bin_h):
    try:
        t = dt.datetime.fromisoformat(str(start).replace("Z", "+00:00")).astimezone()
    except ValueError:
        return str(start)
    return t.strftime("%H:%M") if bin_h < 24 else t.strftime("%m-%d")


def _chart(d):
    o = d["obs"]
    bins = [(_bin_label(b.get("start"), o.get("bin_h") or 24), b) for b in o.get("bins") or []]
    unit = "hour" if (o.get("bin_h") or 24) < 24 else "day"
    return section("Spend by " + unit,
                   charts.axis_chart(bins, SERIES, "spend", unit="$", x_label=unit))


def _row_name(d, group, r):
    if r.get("key") in ("", None):
        return el("span", "Not attributed", class_="muted")
    lab = r.get("label") or r["key"]
    mono = group in ("model", "session", "agent", "goal")
    return el("span", lab, class_="mono" if mono else None)


def _drill_href(d, group, r):
    o = d["obs"]
    nxt = o.get("drill")
    if not nxt:
        return None
    return _link(d, **{group: r.get("key") or queries.NOT_ATTRIBUTED, "group": nxt})


def _warn(text, flag):
    return el("span", "! ", text, class_="warn-cell") if flag else text


def _table(d):
    o = d["obs"]
    group = o.get("group") or "role"
    seg = segmented("Group by", [(GROUP_LABEL[g], _link(d, group=g), g == group) for g in queries.OBS_GROUPS], scroll_strip=True)
    filt = [(k, v) for k, v in (o.get("filters") or {}).items() if v is not None]
    crumbs = None
    if filt:
        crumbs = el("p", "Filtered by ", ", ".join(f"{GROUP_LABEL.get(k, k).lower()} {v or 'Not attributed'}" for k, v in filt),
                    # project is also the header selector: clearing the drill keeps it
                    " · ", link(_link(d, **{g: None for g in queries.OBS_GROUPS if g != "project"}), "clear"), class_="muted drill")
    rows = o.get("rows") or []
    if not rows:
        return section("Where it goes", seg, crumbs, empty_state("No spend in this range"))
    cols = [{"key": "name", "label": GROUP_LABEL[group], "cls": "c-title"},
            {"key": "sess", "label": "Sessions", "cls": "num", "phone_label": True},
            {"key": "req", "label": "Requests", "cls": "num", "phone_label": True},
            {"key": "usd", "label": "$", "cls": "num", "phone_label": True},
            {"key": "share", "label": "Share", "cls": "num", "phone_label": True},
            {"key": "spawn", "label": "Spawn", "cls": "num", "phone_label": True},
            {"key": "poll", "label": "Polling", "cls": "num", "phone_label": True},
            {"key": "work", "label": "Work", "cls": "num", "phone_label": True},
            {"key": "pr", "label": "$/PR", "cls": "num", "phone_label": True},
            {"key": "trend", "label": "Trend", "cls": "spark-cell"}]
    body = []
    for r in rows:
        name = _row_name(d, group, r)
        dh = _drill_href(d, group, r)
        body.append([link(dh, name) if dh else name, r.get("sessions"), r.get("requests"), _money(r.get("cost_usd")),
                     _pct(r.get("share_pct")), _warn(_money(r.get("spawn_usd")), r.get("spawn_warn")),
                     _warn(_money(r.get("polling_usd")), r.get("polling_warn")), _money(r.get("work_usd")),
                     _money(r.get("per_merged_pr_usd")), spark_cell(r.get("spark_usd"), f"{r.get('label') or 'Not attributed'} spend")])
    return section("Where it goes", seg, crumbs,
                   data_table(cols, [{"key": "all", "label": None, "count": len(body), "rows": body}], key="spend-table"),
                   el("p", "! marks polling over 20% or spawn over 15% of a row's spend. Click a row to drill down.", class_="muted"))


def render(ctx, d):
    if d.get("obs") is None:
        return el("div", banners([f"! Spend data unavailable: {d.get('error') or 'unknown error'}"]),
                  empty_state("Spend appears once the tick has run hm-obs ingest."), howto(DOCS))
    o = d["obs"]
    return el("div", banners(o.get("banners")), _strip(d), _chart(d), _table(d),
              el("p", f"Data as of {o.get('ingested_at') or '–'}. Dollars at API list prices.", class_="muted"), howto(DOCS))
