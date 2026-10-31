"""Resources page: spawn gate, four gauges, gate checks, per-day table, 7-day events and caps/config (both collapsed).

The gate card is derived from DB counts, /proc and the injected mem/usage helpers; it never calls herdr or gate().
Event detail text is agent-written, so only structured details are shown (queued reason, config key/value, eval score).
"""
import datetime as dt
import os
import re
import time
from pathlib import Path

from web import charts, queries
from web.components import disclosure, empty_state, section
from web.html import badge, el, glyph, link, table, ts
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
    # Runtime mix from DB (spawn_args.runtime); Claude process cap below stays host /proc of `claude` only.
    try:
        cursor_live = ctx.db.execute(
            "SELECT count(*) FROM agents WHERE status='live' AND json_extract(spawn_args,'$.runtime')='cursor'"
        ).fetchone()[0]
    except Exception:  # noqa: BLE001 — old sqlite / missing column: treat as all Claude
        cursor_live = 0
    claude_live = max(0, live - cursor_live)
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
    mix = f"claude {claude_live} · cursor {cursor_live}" if cursor_live else f"claude {claude_live}"
    checks = [f"agents live {live}/{cap} ({mix})",
              f"workers live {workers} · leads live {leads}/{leads_cap} (parked {parked})",
              "claude processes unknown" if total is None else f"claude processes {total}/{total_cap} on host",
              "mem available unknown" if mem is None else f"mem available {mem} MB (floor {floor})", usage_txt]
    rows = _check_rows(live, cap, workers, leads, leads_cap, total, total_cap, mem, floor, u, h5, d7)
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
    queued = ctx.db.execute("SELECT count(*) FROM agents WHERE status='queued'").fetchone()[0]
    return {"open": reason is None, "reason": reason, "checks": checks, "rows": rows,
            "live": live, "cap": cap, "queued": queued,
            "text": "likely OPEN" if reason is None else f"likely CLOSED — {reason}"}


def _check_rows(live, cap, workers, leads, leads_cap, total, total_cap, mem, floor, u, h5, d7):
    """One row per gate input: {label, value, limit, state, word}; state is ok | over | unknown (glyph and word on the page)."""
    def row(label, value, limit, state, word):
        return {"label": label, "value": value, "limit": limit, "state": state, "word": word}

    def vs(label, value, cap_, over_word):
        return row(label, f"{value}", f"cap {cap_}", "over" if value >= cap_ else "ok",
                   over_word if value > cap_ else "At cap" if value == cap_ else "OK")
    lead = vs("Leads", leads, leads_cap, "Over cap")
    lead["state"] = "over" if leads > leads_cap else "ok"      # at the lead cap is a limit reached, not a breach
    out = [vs("Agents", live, cap, "Over cap"), row("Workers", f"{workers}", "-", "ok", "OK"), lead]
    out.append(row("Claude processes", "unknown", f"cap {total_cap}", "unknown", "Unknown") if total is None
               else vs("Claude processes", total, total_cap, "Over cap"))
    out.append(row("Memory available", "unknown", f"floor {floor} MB", "unknown", "Unknown") if mem is None
               else row("Memory available", f"{mem} MB", f"floor {floor} MB", "over" if mem < floor else "ok",
                        "Low" if mem < floor else "OK"))
    if u is None or u.get("unreadable"):
        out.append(row("Claude usage", "unreadable" if u else "unknown", f"cap {h5:g}% · {d7:g}%", "unknown", "Unknown"))
    else:
        hot = u["five_hour"] >= h5 or u["seven_day"] >= d7
        fresh = bool(u["fresh"]) and (u.get("age_min") or 0) <= 30        # the gauge's stale rule
        word = ("Over cap" if hot else "OK") if fresh else "Stale"
        out.append(row("Claude usage", f"{u['five_hour']:.0f}% 5-hour · {u['seven_day']:.0f}% weekly", f"cap {h5:g}% · {d7:g}%",
                       "over" if hot and fresh else "ok" if fresh else "unknown", word))
    return out


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
    pinned = os.environ.get("FACTORY_LOADAVG")   # the screenshot/demo server pins it so the shots do not depend on the host
    try:
        load = [float(pinned)] * 3 if pinned else [round(x, 2) for x in os.getloadavg()]
    except (OSError, ValueError):
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


def _cfg(ctx, key, fallback):
    cfg = {r["key"]: r["value"] for r in queries.config(ctx.db)}
    return queries._cfg_num(cfg, ctx.defaults or {}, key, fallback)


def system_inputs(ctx, gate):
    """Inputs of the four gauges, the same numbers as the overview's System section (web/views/home.py _system)."""
    mem = ctx.mem_fn() if ctx.mem_fn else None
    m = machine()
    load, cpus = (m.get("load") or [None])[0], m.get("cpus")
    u = ctx.usage_fn() if ctx.usage_fn else None
    fresh = bool(u) and not u.get("unreadable") and u.get("five_hour") is not None
    return {"agents": {"live": gate["live"], "queued": gate["queued"], "cap": gate["cap"]},
            "memory": {"available_mb": mem, "floor_mb": int(_cfg(ctx, "min_mem_available_mb", 1024))},
            "load": {"load1": load, "cpus": cpus},
            "usage": {"five_hour": u["five_hour"] if fresh else None, "seven_day": u.get("seven_day") if fresh else None,
                      "cap_five_hour": _cfg(ctx, "max_five_hour_pct", 92), "cap_seven_day": _cfg(ctx, "max_seven_day_pct", 92),
                      "stale": bool(fresh and (not u.get("fresh") or (u.get("age_min") or 0) > 30)),
                      "age_min": u.get("age_min") if fresh else None,
                      "five_hour_resets_at": u.get("five_hour_resets_at") if fresh else None,
                      "seven_day_resets_at": u.get("seven_day_resets_at") if fresh else None}}


def data(ctx, **params):
    before = _before(ctx)
    cfg = queries.config(ctx.db)
    have = {r["key"] for r in cfg}
    caps = [dict(r, source="config") for r in cfg] + [
        {"key": k, "value": v, "source": "default"} for k, v in sorted((ctx.defaults or {}).items()) if k not in have]
    gate = derive_gate(ctx)
    return {"gate": gate, "machine": machine(), "config": caps, "system": system_inputs(ctx, gate),
            "timeline": timeline(ctx.db, ctx.now, before), "per_day": per_day(ctx.db, ctx.now),
            "before": (ctx.query or {}).get("before") if before else None}


# ── render ──

def _tone(v, cap):
    return ("danger", "Over cap") if v >= cap else ("warning", "Near cap") if v >= 0.8 * cap else ("success", "OK")


def _mb(mb):
    return f"{mb / 1024:.1f} GB" if mb >= 1024 else f"{mb:.0f} MB"


def gauge_rows(s):
    """The Agents, Memory and Load gauges (C9), with the labels, zones and thresholds of the overview's System section."""
    a, mem, ld = s["agents"], s["memory"], s["load"]
    full = max(a["cap"], a["live"]) * 1.25 or 1
    zone = ("danger", "Over cap") if a["live"] > a["cap"] else ("warning", "At cap") if a["live"] >= a["cap"] else ("success", "OK")
    rows = [charts.gauge("Agents", f"{a['live']} / {a['cap']}", 100 * a["live"] / full, [100 * a["cap"] / full], zone,
                         f"{a['queued']} queued" if a["queued"] else "none queued")]
    if mem["available_mb"] is None:
        rows.append(charts.gauge("Memory", "unknown", 0, [], ("neutral", "Unknown"), f"floor {_mb(mem['floor_mb'])}"))
    else:
        full = max(mem["available_mb"], 4 * mem["floor_mb"]) or 1
        zone = (("danger", "Low") if mem["available_mb"] < mem["floor_mb"]
                else ("warning", "Tight") if mem["available_mb"] < 2 * mem["floor_mb"] else ("success", "OK"))
        rows.append(charts.gauge("Memory", f"{_mb(mem['available_mb'])} free", 100 * mem["available_mb"] / full,
                                 [100 * mem["floor_mb"] / full], zone, f"floor {_mb(mem['floor_mb'])}"))
    if ld["load1"] is None or not ld["cpus"]:
        rows.append(charts.gauge("Load", "unknown", 0, [], ("neutral", "Unknown"), None))
    else:
        full = max(ld["load1"], ld["cpus"]) * 1.25
        zone = ("danger", "Overloaded") if ld["load1"] > ld["cpus"] else ("warning", "Busy") if ld["load1"] > 0.75 * ld["cpus"] else ("success", "OK")
        rows.append(charts.gauge("Load", f"{ld['load1']:.1f} / {ld['cpus']} CPUs", 100 * ld["load1"] / full, [100 * ld["cpus"] / full],
                                 zone, "1-min average"))
    return rows


def usage_row(u):
    """"Claude usage" (the same label on every page): 5-hour and weekly bars stacked, one zone word for the worse one."""
    p5, p7 = u["five_hour"], u["seven_day"]
    if p5 is None:
        return charts.gauge("Claude usage", "unknown", 0, [], ("neutral", "Unknown"), "5 h · week")
    zones = [_tone(p5, u["cap_five_hour"])] + ([_tone(p7, u["cap_seven_day"])] if p7 is not None else [])
    tone, word = max(zones, key=lambda z: ("success", "warning", "danger").index(z[0]))
    if u["stale"]:
        word = "Stale" if word == "OK" else f"{word}, stale"
    bars = [charts.gauge_bar(p5, [u["cap_five_hour"]], zones[0][0], f"Claude usage 5-hour {p5:.0f}%, cap {u['cap_five_hour']:.0f}%")]
    if p7 is not None:
        bars.append(charts.gauge_bar(p7, [u["cap_seven_day"]], zones[1][0], f"Claude usage weekly {p7:.0f}%, cap {u['cap_seven_day']:.0f}%"))
    text = f"{p5:.0f}%" + (f" · {p7:.0f}%" if p7 is not None else "")
    return el("div", el("span", "Claude usage", class_="gl"), el("div", bars, class_="gauge-stack"),
              el("span", text, el("span", el("b", word, class_=f"gz fg-{tone}"), " · 5 h · week"), class_="gv"), class_="gauge-row")


def _when(epoch):
    """"10-28 14:00" in local time for an epoch int; "" for anything else."""
    if not isinstance(epoch, int) or isinstance(epoch, bool):
        return ""
    try:
        return dt.datetime.fromtimestamp(epoch).astimezone().strftime("%m-%d %H:%M")
    except (OverflowError, OSError, ValueError):
        return ""


def usage_note(u):
    """Reset times and data age under the Claude usage gauge; "" when the usage data is missing."""
    bits = []
    for label, key in (("5-hour", "five_hour_resets_at"), ("weekly", "seven_day_resets_at")):
        w = _when(u.get(key))
        if w:
            bits.append(f"{label} resets {w}")
    if u.get("age_min") is not None:
        bits.append(f"data {u['age_min']} min old")
    return el("p", " · ".join(bits), class_="muted") if bits else ""


def _gate_section(g):
    key, word = ("done", "Gate open") if g["open"] else ("blocked", "Gate closed")
    tone = "success" if g["open"] else "danger"
    sentence = f"{g['live']} agents running, cap {g['cap']}; {g['queued']} queued"
    if g["reason"]:
        sentence += f". Closed because: {g['reason']}"
    return section("Spawn gate", el("p", el("span", glyph(key), word, class_=f"st st--pill tone-{tone}"), " ", sentence),
                   el("p", "Derived from the database and /proc (no live pane check), so it can differ from the real gate for a moment.",
                      class_="muted"))


def _gauges_section(s):
    return section("Capacity", el("div", [*gauge_rows(s), usage_row(s["usage"])], class_="ins-gauges"), usage_note(s["usage"]))


def _checks_section(g):
    glyphs = {"ok": "done", "over": "blocked", "unknown": "queued"}
    rows = [el("li", glyph(glyphs[r["state"]]), el("span", r["label"], class_="ins-name"), el("span", r["value"], class_="mono"),
               el("span", r["limit"], class_="muted"), el("b", r["word"], class_="gz"), class_="ins-check") for r in g["rows"]]
    return section("Checks", el("ul", rows, class_="lrow-list"))


def _days_section(days):
    if not any(v for d in days for k, v in d.items() if k != "day"):
        return section("Per day", empty_state("No data yet."))
    rows = [(d["day"][5:], (d["spawns"], "num"), (d["queued"], "num"), (d["merges"], "num"), (d["escalations"], "num"))
            for d in days]
    return section("Per day", table(["Day", "Spawns", "Queued", "Merges", "Escalations"], rows, cls="table table--compact"))


def _config_disclosure(rows):
    if not rows:
        return disclosure("Caps and config", empty_state("No data yet."), key="config")
    body = table(["Setting", "Value", "Source"], [
        (el("span", r["key"].replace("_", " ").capitalize(), " ", el("span", r["key"], class_="mono ins-key")),
         (el("span", r["value"], class_="mono"), "num"), r["source"]) for r in rows], cls="table table--compact")
    return disclosure("Caps and config", body, key="config", count=len(rows))


def _events_disclosure(t, before):
    if not t["items"]:
        return disclosure("7-day events", empty_state("No data yet."), key="events", open=before is not None)
    items = [el("div", badge(i["kind"], _KIND.get(i["kind"], "muted")), " ", ts(i["ts"]), " ", i["text"], class_="timeline-item")
             for i in t["items"]]
    nav = []
    if t["next_before"]:
        nav.append(link(f"/resources?before={t['next_before']}", "More"))
    if before is not None:
        nav += [" ", link("/resources", "Newest")]
    # a "More" page is reached from inside the disclosure, so it opens there; the first page stays collapsed
    return disclosure("7-day events", el("div", *items, class_="timeline"), el("p", *nav) if nav else "", key="events",
                      count=len(t["items"]), open=before is not None)


def render(ctx, d):
    return el("div", _gate_section(d["gate"]), _gauges_section(d["system"]), _checks_section(d["gate"]),
              _days_section(d["per_day"]), _events_disclosure(d["timeline"], d["before"]), _config_disclosure(d["config"]))
