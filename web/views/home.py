"""Overview: the owner's cockpit (G-019 B4). Six sections in a fixed order: needs-you, system, goals, agents, shipped,
quality. Desktop: two columns (rows needs-you | system, goals | agents, shipped | quality); phone: one column in the
same order. One query group per section, none per row. /api/home.json keeps its original keys and only adds new ones."""
import statistics

from web import charts, queries
from web.components import AGENT_COLS, agent_row, ago, empty_state, feed, feed_row, list_row, needs_you_row, row_status, tile
from web.html import el
from web.views import resources

TITLE = "Overview"

NEEDS_MAX = 4
GOALS_MAX = 4
AGENTS_MAX = 4
MERGES_MAX = 5
BOUNCING_MAX = 3
DAYS = 14
HQ_ROLES = ("boss", "director", "staff-engineer")
OPEN_GOAL = ("active", "planning", "proposed", "blocked")
machine = resources.machine      # replaced in tests: the live /proc is not deterministic


def _cfg(ctx, key, fallback):
    cfg = {r["key"]: r["value"] for r in queries.config(ctx.db)}
    return queries._cfg_num(cfg, ctx.defaults or {}, key, fallback)


# ── data ──

def _needs(ctx):
    """Items waiting on the owner as needs_you_row dicts, most urgent first. Escalations carry the subject only."""
    op = queries.owner_pending(ctx.db)
    refs = queries.message_refs(ctx.db, [m["id"] for m in op["escalations"]])
    wait = lambda ts_: f"waiting {ago(ts_, ctx.now)}" if ago(ts_, ctx.now) else ""   # noqa: E731
    items = []
    for t in op["tasks_blocked"]:
        items.append({"kind": "Blocked task", "tone": "danger", "subject": queries._strip_tags(t["title"]), "ref": t["id"],
                      "href": f"/task/{t['id']}", "age": wait(t["updated"])})
    for m in op["escalations"]:
        goal, task = refs.get(m["id"], (None, None))
        ref, href = (task, f"/task/{task}") if task else (goal, f"/goal/{goal}") if goal else (None, None)
        items.append({"kind": "Escalation" if m["kind"] == "escalation" else "Request", "tone": "warning", "subject": m["subject"],
                      "ref": ref, "href": href, "message": m["id"], "age": wait(m["created"])})
    for t in op["judge_blocked_auto_merge"]:
        items.append({"kind": "Merge needs OK", "tone": "warning", "subject": queries._strip_tags(t["title"]), "ref": t["task_id"],
                      "href": f"/task/{t['task_id']}", "age": wait(t["ts"])})
    for g in op["goals"]:
        items.append({"kind": "Blocked goal" if g["status"] == "blocked" else "Proposed goal",
                      "tone": "danger" if g["status"] == "blocked" else "neutral", "subject": g["title"], "ref": g["id"],
                      "href": f"/goal/{g['id']}", "age": wait(g["updated"])})
    return items


def _system(ctx, ag):
    live = sum(1 for a in ag if a["status"] == "live")
    queued = sum(1 for a in ag if a["status"] == "queued")
    cap = int(_cfg(ctx, "max_live_agents", 6))
    floor = int(_cfg(ctx, "min_mem_available_mb", 1024))
    mem = ctx.mem_fn() if ctx.mem_fn else None
    m = machine()
    load, cpus = (m.get("load") or [None])[0], m.get("cpus")
    u = ctx.usage_fn() if ctx.usage_fn else None
    fresh = bool(u) and not u.get("unreadable") and u.get("five_hour") is not None
    cap5, cap7 = _cfg(ctx, "max_five_hour_pct", 92), _cfg(ctx, "max_seven_day_pct", 92)
    return {"agents": {"live": live, "queued": queued, "cap": cap}, "memory": {"available_mb": mem, "floor_mb": floor},
            "load": {"load1": load, "cpus": cpus},
            "usage": {"five_hour": u["five_hour"] if fresh else None, "seven_day": u.get("seven_day") if fresh else None,
                      "cap_five_hour": cap5, "cap_seven_day": cap7, "stale": bool(fresh and (not u.get("fresh") or (u.get("age_min") or 0) > 30))}}


def _goals(ctx):
    gl = [g for g in queries.goals(ctx.db) if g["status"] in OPEN_GOAL]
    last = queries.last_activity_by_goal(ctx.db)
    gl.sort(key=lambda g: (99 if g["priority"] is None else g["priority"], g["id"]))
    return [{"id": g["id"], "title": g["title"], "status": g["status"], "priority": g["priority"], "tasks_total": g["tasks_total"],
             "tasks_done": g["tasks_done"], "open_prs": g["open_prs"], "last_activity": last.get(g["id"])} for g in gl]


def _agents(ctx, ag):
    last = queries.last_event_by_agent(ctx.db)
    rows = sorted(ag, key=lambda a: (a["status"] != "live", -(queries._epoch(last.get(a["name"])) or 0), a["name"]))
    shown = rows[:AGENTS_MAX]
    titles = queries.task_titles(ctx.db, {a["task_id"] for a in shown if a["task_id"]})
    live = [a for a in ag if a["status"] == "live"]
    return {"running": len(live), "queued": sum(1 for a in ag if a["status"] == "queued"), "total": len(ag),
            "leads": sum(1 for a in live if a["role"] == "lead"), "hq": sum(1 for a in live if a["role"] in HQ_ROLES),
            "rows": [{"name": a["name"], "role": a["role"], "status": a["status"], "task_id": a["task_id"],
                      "task_title": titles.get(a["task_id"]), "last_event": last.get(a["name"])} for a in shown]}


def _quality(ctx):
    q = queries.quality(ctx.db)["overall"]
    spawn = [t["total_s"] for t in queries.throughput(ctx.db, days=DAYS, now=ctx.now)["tasks"] if t["total_s"] is not None]
    return {"merged": q["merged"], "rejections_per_merged": q["rejections_per_merged"],
            "first_try_pct": round(100 * q["merged_first_try"] / q["merged"]) if q["merged"] else None,
            "median_spawn_to_merge_s": round(statistics.median(spawn)) if spawn else None,
            "bouncing": queries.bouncing_tasks(ctx.db, BOUNCING_MAX + 1)}


def data(ctx, **params):
    gl = queries.goals(ctx.db)
    by_status = {}
    for g in gl:
        by_status[g["status"]] = by_status.get(g["status"], 0) + 1
    ag = queries.agents(ctx.db, now=ctx.now)
    u = (ctx.usage_fn() if ctx.usage_fn else None) or {}
    per_day = queries.merges_per_day(ctx.db, DAYS, now=ctx.now)
    needs = _needs(ctx)
    return {"utilization": queries.utilization_sample(ctx.db, now=ctx.now),
            "live_agents": sum(1 for a in ag if a["status"] == "live"),
            "queued_agents": sum(1 for a in ag if a["status"] == "queued"),
            "open_prs": sum(g["open_prs"] for g in gl),
            "goals_by_status": by_status,
            "owner_pending": sum(len(v) for v in queries.owner_pending(ctx.db).values()),
            "usage": {"five_hour": u.get("five_hour"), "seven_day": u.get("seven_day"),
                      "fresh": u.get("fresh"), "age_min": u.get("age_min")},
            "needs_you": needs, "system": _system(ctx, ag), "goals": _goals(ctx), "agents": _agents(ctx, ag),
            "shipped": {"recent": queries.recent_merges(ctx.db, MERGES_MAX), "per_day": per_day,
                        "total": sum(d["merges"] for d in per_day)},
            "quality": _quality(ctx)}


# ── render ──

def _sect(sid, title, *body, count=None, more=None):
    head = el("div", el("h2", title), el("span", count, class_="sect-n") if count is not None else None,
              el("a", more[1], href=more[0], class_="sect-link") if more else None, class_="sect-head")
    return el("section", head, el("div", *body, class_="sect-box"), id=sid, class_="sect ov-sect")


def _needs_section(d, now):
    items = d["needs_you"]
    if not items:
        return _sect("needs-you", "Needs you", empty_state("Nothing needs you"))
    more = len(items) - NEEDS_MAX
    return _sect("needs-you", "Needs you", el("ul", [needs_you_row(i) for i in items[:NEEDS_MAX]], class_="lrow-list"),
                 el("a", f"{more} more", href="/goals", class_="more-link") if more > 0 else None,
                 count=len(items), more=("/goals", "All"))


def _tone(v, cap):
    return ("danger", "Over cap") if v >= cap else ("warning", "Near cap") if v >= 0.8 * cap else ("success", "OK")


def _mb(mb):
    return f"{mb / 1024:.1f} GB" if mb >= 1024 else f"{mb:.0f} MB"


def _system_section(d):
    s = d["system"]
    a, mem, ld, u = s["agents"], s["memory"], s["load"], s["usage"]
    rows = []
    full = max(a["cap"], a["live"]) * 1.25 or 1
    zone = ("danger", "Over cap") if a["live"] > a["cap"] else ("warning", "At cap") if a["live"] >= a["cap"] else ("success", "OK")
    rows.append(charts.gauge("Agents", f"{a['live']} / {a['cap']}", 100 * a["live"] / full, [100 * a["cap"] / full], zone,
                             f"{a['queued']} queued" if a["queued"] else "none queued"))
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
    rows.append(_usage_row(u))
    ut = d["utilization"]
    ut_text = ("utilization: no fresh tick sample" if ut is None
               else ut["line"] + (" (UNDER TARGET)" if ut["under"] else ""))
    return _sect("system", "System", el("div", rows, class_="system"), el("p", ut_text, class_="summary-line"),
                 more=("/resources", "Resources"))


def _usage_row(u):
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


def _goals_section(d, now):
    gl = d["goals"]
    if not gl:
        return _sect("goals", "Open goals", empty_state("No open goals"), more=("/goals", "All goals"))
    rows = []
    for g in gl[:GOALS_MAX]:
        prs = f"{g['open_prs']} open PR" + ("s" if g["open_prs"] != 1 else "") if g["open_prs"] else "no open PR"
        act = ago(g["last_activity"], now)
        rows.append(el("li", row_status(g["status"]), el("span", g["id"], class_="mono lrow-id"),
                       el("span", el("a", g["title"], href=f"/goal/{g['id']}"), el("span", f"P{g['priority']}", class_="chip-p")
                          if g["priority"] is not None else None, class_="gtitle"),
                       el("div", charts.meter(g["tasks_done"], g["tasks_total"], f"{g['tasks_done']}/{g['tasks_total']} tasks"),
                          el("span", prs, class_="gprs"), el("span", f"last activity {act}" if act else "no activity yet", class_="gact"),
                          class_="gm"), class_="goalrow"))
    more = len(gl) - GOALS_MAX
    return _sect("goals", "Open goals", el("ul", rows, class_="lrow-list"),
                 el("a", f"{more} more", href="/goals", class_="more-link") if more > 0 else None,
                 count=len(gl), more=("/goals", "All goals"))


def _n(n, word):
    return f"{n} {word}" + ("" if n == 1 else "s")


def _agents_section(d, now):
    a = d["agents"]
    if not a["total"]:
        return _sect("agents", "Agents running", empty_state("No agents running"), more=("/agents", "All agents"))
    workers = a["running"] - a["leads"] - a["hq"]
    summary = f"{a['running']} running · {_n(workers, 'worker')} · {_n(a['leads'], 'lead')} · {a['hq']} HQ · {a['queued']} queued"
    rows = []
    for r in a["rows"]:
        cells = agent_row(r, now)
        rows.append(el("li", [el("span", c, class_=k["cls"]) for k, c in zip(AGENT_COLS, cells)], class_="agentrow"))
    more = a["total"] - len(a["rows"])
    return _sect("agents", "Agents running", el("p", summary, class_="summary-line"), el("ul", rows, class_="lrow-list"),
                 el("a", f"{more} more · All {a['total']} agents", href="/agents", class_="more-link") if more > 0 else None,
                 count=a["running"], more=("/agents", "All agents"))


def _shipped_section(d, now):
    s = d["shipped"]
    pts = [(p["day"], p["merges"]) for p in s["per_day"]]
    head = el("div", el("span", f"{s['total']} in {DAYS} days", class_="sect-n"), charts.sparkline(pts, "merges"), class_="ov-trend")
    if not s["recent"]:
        return _sect("shipped", "Shipped", head, empty_state(f"No merges in the last {DAYS} days"), more=("/changelog", "Changelog"))
    rows = [feed_row(m["title"], f"/task/{m['task_id']}", goal=m["goal_id"], pr_url=m["pr_url"], sha=m["sha"],
                     when=ago(m["merged"], now, " ago"), when_title=m["merged"]) for m in s["recent"]]
    return _sect("shipped", "Shipped", head, feed(rows), more=("/changelog", "Changelog"))


def _median(sec):
    if sec is None:
        return "n/a", None
    return (f"{sec / 3600:.1f}", " h") if sec >= 3600 else (f"{sec / 60:.0f}", " min")


def _quality_section(d):
    q = d["quality"]
    rej = "n/a" if q["rejections_per_merged"] is None else f"{q['rejections_per_merged']:.2f}"
    first = "n/a" if q["first_try_pct"] is None else str(q["first_try_pct"])
    med, med_unit = _median(q["median_spawn_to_merge_s"])
    tiles = el("div",
               tile("Rejections per merged PR", rej, definition="Rejections (failed review, QA or judge) divided by merged PRs, all time. Lower is better."),
               tile("First-try merge rate", first, unit="%" if q["first_try_pct"] is not None else None,
                    definition="Share of merged PRs that were never rejected. Higher is better."),
               tile("Median spawn to merge", med, unit=med_unit,
                    definition=f"Median time from a task's first agent start to its merge, last {DAYS} days."),
               class_="tiles")
    bounce = q["bouncing"]
    if bounce:
        rows = [list_row("in_review", b["task_id"], b["title"], f"/task/{b['task_id']}",
                         (f"{b['rejections']} rejection{'s' if b['rejections'] != 1 else ''}", f"{b['attempts']} tries"))
                for b in bounce[:BOUNCING_MAX]]
        more = len(bounce) - BOUNCING_MAX
        bounce_el = el("div", el("h3", "Bouncing now ", el("span", len(bounce) if more <= 0 else f"{BOUNCING_MAX}+", class_="sect-n")),
                       el("ul", rows, class_="lrow-list"), class_="ov-bounce")
    else:
        bounce_el = el("div", el("h3", "Bouncing now"), empty_state("Nothing is bouncing"), class_="ov-bounce")
    return _sect("quality", "Quality", tiles, bounce_el, more=("/quality", "Details"))


def render(ctx, d):
    return el("div", _needs_section(d, ctx.now), _system_section(d), _goals_section(d, ctx.now), _agents_section(d, ctx.now),
              _shipped_section(d, ctx.now), _quality_section(d), class_="ov")
