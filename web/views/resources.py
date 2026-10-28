"""Resources page: derived gate card, machine card, caps/config, 7-day director timeline.

The gate card is derived from DB counts, /proc and the injected mem/usage helpers; it never calls herdr or gate().
Event detail text is agent-written, so only structured details are shown (queued reason, config key/value, eval score).
"""
import datetime as dt
import os
import re
import time
from pathlib import Path

from web import queries
from web.html import badge, bar, card, dur, el, empty, kpi, link, table, ts
from web.views import agents

TITLE = "Resources"

DAYS = 7
PAGE_SIZE = 40
REASON_MAX = 200
_STARTED = time.time()    # the views are imported when `factory serve` starts
_EVENTS = {("agent", a) for a in ("spawn", "queued", "done", "reaped", "stopped", "lost")} | {
    ("config", "set"), ("team", "new"), ("team", "close"), ("team", "eval"), ("task", "merged"),
    ("task", "judge-reject")}
_EVENT_SQL = "(%s OR entity='digest')" % " OR ".join("(entity=? AND action=?)" for _ in _EVENTS)
_EVENT_ARGS = [x for pair in sorted(_EVENTS) for x in pair]
_KIND = {"spawn": "info", "queued": "warn", "done": "ok", "reaped": "muted", "stopped": "muted", "lost": "bad",
         "set": "info", "new": "info", "close": "muted", "eval": "info", "merged": "ok", "judge-reject": "bad",
         "decision": "info", "escalation": "bad", "alert": "bad", "report": "muted"}


# ── gate (derived) ──

def _num(v, default):
    try:
        return float(v)
    except (TypeError, ValueError):
        return float(default)


def derive_gate(ctx):
    """{'open': bool, 'reason': str, 'checks': [...]} in the same order as gate() in bin/factory, minus herdr."""
    cfg = {r["key"]: r["value"] for r in queries.config(ctx.db)}
    dflt = ctx.defaults or {}

    def c(k, fallback):
        return _num(cfg.get(k, dflt.get(k)), fallback)
    cap, total_cap, floor = int(c("max_live_agents", 6)), int(c("max_total_claude", 10)), int(c("min_mem_available_mb", 1024))
    h5, d7 = c("max_five_hour_pct", 92), c("max_seven_day_pct", 92)
    live = ctx.db.execute("SELECT count(*) FROM agents WHERE status='live'").fetchone()[0]
    total = agents.claude_total(agents.proc_snapshot())
    workers, leads, parked = agents.lead_counts(ctx.db)
    leads_cap = int(c("max_live_leads", 4))
    mem = ctx.mem_fn() if ctx.mem_fn else None
    u = ctx.usage_fn() if ctx.usage_fn else None
    if u is None:
        usage_txt = "claude usage unknown"
    elif u.get("unreadable"):
        usage_txt = "claude usage unreadable"
    else:
        usage_txt = (f"claude usage 5h {u['five_hour']:.0f}% · week {u['seven_day']:.0f}%"
                     + ("" if u["fresh"] else f" (stale: {u['age_min']} min old)"))
    checks = [f"agents live {live}/{cap}",
              f"workers live {workers} · leads live {leads}/{leads_cap} (parked {parked})",
              "claude processes unknown" if total is None else f"claude processes {total}/{total_cap} on host",
              "mem available unknown" if mem is None else f"mem available {mem} MB (floor {floor})", usage_txt]
    reason = None
    if live >= cap:
        reason = f"{live}/{cap} agent panes live"
    elif total is not None and total >= total_cap:
        reason = f"{total}/{total_cap} claude processes on host"
    elif mem is not None and mem < floor:
        reason = f"MemAvailable {mem}MB < floor {floor}MB"
    elif u and u["fresh"] and not u.get("unreadable") and u["five_hour"] >= h5:
        reason = f"5h usage {u['five_hour']:.0f}% ≥ {h5:g}%"
    elif u and u["fresh"] and not u.get("unreadable") and u["seven_day"] >= d7:
        reason = f"weekly usage {u['seven_day']:.0f}% ≥ {d7:g}%"
    return {"open": reason is None, "reason": reason, "checks": checks,
            "text": "likely OPEN" if reason is None else f"likely CLOSED — {reason}"}


# ── machine ──

def machine():
    mi = {}
    try:
        for line in Path("/proc/meminfo").read_text().splitlines():
            k, _, v = line.partition(":")
            if k in ("MemAvailable", "MemTotal", "SwapTotal", "SwapFree"):
                mi[k] = int(v.split()[0]) // 1024
    except (OSError, ValueError, IndexError):
        pass
    try:
        load = [round(x, 2) for x in os.getloadavg()]
    except OSError:
        load = None
    swap_used = mi["SwapTotal"] - mi["SwapFree"] if "SwapTotal" in mi and "SwapFree" in mi else None
    return {"mem_available_mb": mi.get("MemAvailable"), "mem_total_mb": mi.get("MemTotal"), "swap_used_mb": swap_used,
            "swap_total_mb": mi.get("SwapTotal"), "load": load, "cpus": os.cpu_count(),
            "serve_uptime_s": round(time.time() - _STARTED)}


# ── timeline ──

def _before(ctx):
    """Timeline cursor "<epoch>-<src>-<id>" (ASCII digits only); anything else is ignored."""
    v = (ctx.query or {}).get("before", "")
    m = re.fullmatch(r"([0-9]{1,12})-([01])-([0-9]{1,12})", v) if v.isascii() else None
    return tuple(int(x) for x in m.groups()) if m else None


def _event_text(e):
    a, d = e["action"], e["detail"]
    if e["entity"] == "agent":
        base = f"{e['entity_id']} {a}"
        return f"{base}: {queries._t(d, REASON_MAX)}" if a == "queued" and d else base
    if e["entity"] == "config":
        return f"config {e['entity_id']} = {queries._t(d, 60)}"
    if e["entity"] == "team":
        return f"team {e['entity_id']} {a}" + (f" {queries._t(d, 10)}" if a == "eval" and d else "")
    if e["entity"] == "task":
        return f"task {e['entity_id']} {a}"
    return f"{e['entity']} {a}"


def timeline(con, now, before=None):
    """Events (allowlisted actions) and message metadata of the last 7 days, newest first, one page.

    Both sources form one stream ordered by (epoch, src, id); `before` is the key of the last item of the
    previous page, so paging continues exactly where it stopped and reaches every item once."""
    cut = now.timestamp() - DAYS * 86400
    # timestamps are ISO strings with a local offset: bound the scan one day early, the exact cut is applied below
    low = dt.datetime.fromtimestamp(cut - 86400).astimezone().isoformat(timespec="seconds")
    items = []
    for r in con.execute(f"SELECT id,ts,actor,entity,entity_id,action,detail FROM events WHERE ts >= ? AND {_EVENT_SQL} "
                         "ORDER BY id DESC", [low, *_EVENT_ARGS]):
        e = queries._epoch(r["ts"])
        if e is not None and e < cut:
            continue
        items.append({"id": r["id"], "src": 1, "ts": r["ts"], "epoch": int(e or 0),
                      "kind": r["action"] if r["entity"] != "digest" else "digest", "text": _event_text(r)})
    for m in con.execute("SELECT id, sender, recipient, kind, substr(subject,1,?) subject, created FROM messages "
                         "WHERE created >= ? AND kind IN ('decision','escalation','alert','report') ORDER BY id DESC",
                         (queries.SUBJECT_MAX, low)):
        e = queries._epoch(m["created"])
        if e is None or e < cut:
            continue
        items.append({"id": m["id"], "src": 0, "ts": m["created"], "epoch": int(e), "kind": m["kind"],
                      "text": f"{m['sender']} → {m['recipient']}: {m['subject']}"})
    items.sort(key=lambda i: (i["epoch"], i["src"], i["id"]), reverse=True)
    if before is not None:
        items = [i for i in items if (i["epoch"], i["src"], i["id"]) < before]
    more = len(items) > PAGE_SIZE
    items = items[:PAGE_SIZE]
    last = items[-1] if more else None
    nxt = f"{last['epoch']}-{last['src']}-{last['id']}" if last else None
    for i in items:
        del i["src"]
        del i["id"]
    return {"items": items, "next_before": nxt}


def per_day(con, now):
    days = [(now - dt.timedelta(days=i)).date() for i in range(DAYS)]
    cut = now.timestamp() - DAYS * 86400
    rows = {d.isoformat(): {"spawns": 0, "queued": 0, "merges": 0, "escalations": 0} for d in days}

    def bump(iso, key):
        d = queries.parse_ts(iso)
        if d is not None and d.timestamp() >= cut:
            day = d.astimezone().date().isoformat()
            if day in rows:
                rows[day][key] += 1
    key = {"spawn": "spawns", "queued": "queued", "merged": "merges"}
    for r in con.execute("SELECT ts,action FROM events WHERE (entity='agent' AND action IN ('spawn','queued')) OR "
                         "(entity='task' AND action='merged') ORDER BY id DESC LIMIT 5000"):
        bump(r["ts"], key[r["action"]])
    for r in con.execute("SELECT created FROM messages WHERE kind='escalation' ORDER BY id DESC LIMIT 2000"):
        bump(r["created"], "escalations")
    return [dict(day=k, **v) for k, v in rows.items()]


def data(ctx, **params):
    before = _before(ctx)
    cfg = queries.config(ctx.db)
    have = {r["key"] for r in cfg}
    caps = [dict(r, source="config") for r in cfg] + [
        {"key": k, "value": v, "source": "default"} for k, v in sorted((ctx.defaults or {}).items()) if k not in have]
    return {"gate": derive_gate(ctx), "machine": machine(), "config": caps,
            "timeline": timeline(ctx.db, ctx.now, before), "per_day": per_day(ctx.db, ctx.now),
            "before": (ctx.query or {}).get("before") if before else None}


# ── render ──

def _gate_card(g):
    kind = "ok" if g["open"] else "bad"
    return card("Spawn gate", el("p", badge(g["text"], kind), " ", el("span", "derived (no herdr check)", class_="muted")),
                el("p", " · ".join(g["checks"]), class_="muted"))


def _machine_card(m):
    def mb(v):
        return "-" if v is None else f"{v} MB"
    load = "-" if m["load"] is None else f"{m['load'][0]:.1f} / {m['cpus']} cpus"
    return card("Machine", el("div", kpi("MemAvailable", mb(m["mem_available_mb"])), kpi("Swap used", mb(m["swap_used_mb"])),
                              kpi("Load", load), kpi("Serve uptime", dur(m["serve_uptime_s"])), class_="grid"))


def _config_card(rows):
    if not rows:
        return card("Caps and config", empty("No data yet."))
    return card("Caps and config", table(["Key", "Value", "Source"], [
        (el("span", r["key"], class_="mono"), el("span", r["value"], class_="mono"), r["source"]) for r in rows],
        cls="table table--compact"))


def _days_card(days):
    if not any(v for d in days for k, v in d.items() if k != "day"):
        return card("Per day", empty("No data yet."))
    mx = {k: max(d[k] for d in days) or 1 for k in ("spawns", "queued", "merges", "escalations")}
    rows = [(d["day"][5:], *[bar(100 * d[k] / mx[k], d[k], f"{d[k]} of max {mx[k]}")
                              for k in ("spawns", "queued", "merges", "escalations")]) for d in days]
    return card("Per day", table(["Day", "Spawns", "Queued", "Merges", "Escalations"], rows, cls="table table--compact"))


def _timeline_card(t, before):
    if not t["items"]:
        return card("Timeline (7 days)", empty("No data yet."))
    items = [el("div", badge(i["kind"], _KIND.get(i["kind"], "muted")), " ", ts(i["ts"]), " ", i["text"], class_="timeline-item")
             for i in t["items"]]
    nav = []
    if t["next_before"]:
        nav.append(link(f"/resources?before={t['next_before']}", "More"))
    if before is not None:
        nav += [" ", link("/resources", "Newest")]
    return card("Timeline (7 days)", el("div", *items, class_="timeline"), el("p", *nav) if nav else "")


def render(ctx, d):
    return el("div", _gate_card(d["gate"]), _machine_card(d["machine"]), _days_card(d["per_day"]),
              _timeline_card(d["timeline"], d["before"]), _config_card(d["config"]))
