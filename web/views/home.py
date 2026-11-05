"""Overview: the owner's cockpit (G-019 B4, R2 F-27). Sections in a fixed order: needs-you (the strip, only when
something waits on the owner), goals (the goal, team, task tree), system, agents, shipped, quality. Desktop: two columns;
phone: one column in the same order. One query group per section, none per row. /api/home.json keeps its original keys and only adds new ones."""
import statistics

from scout import orgs as _orgs
from web import charts, queries
from web.db import has_column
from web.components import (AGENT_COLS, agent_row, ago, ctx_org, empty_state, feed, feed_row, list_row, needs_you_row, org_badge,
                            org_href, pr_label, row_status, tile)
from web.html import el, glyph, link, org_dot, status
from web.views import resources

TITLE = "Overview"

NEEDS_MAX = 3
TEAM_SCOPE_MAX = 120
TASK_TITLE_MAX = 90
SHOWN_TASK = ("blocked", "in_review", "in_progress")   # level 3 lists these; todo sits behind "N more"
COUNTED = ("todo", "in_progress", "in_review", "blocked")
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

def _needs(ctx, org=None, op=None):
    """Items waiting on the owner as needs_you_row dicts, most urgent first. Escalations carry the subject only.
    Each item has an `org` (its goal's org; a message without a goal: the boss it was sent to, else its sender's
    prefix, else the default org). With `org` only that org's items are returned."""
    op = op or queries.owner_pending(ctx.db)
    refs = queries.message_refs(ctx.db, [m["id"] for m in op["escalations"]])
    gorgs = queries.goal_orgs(ctx.db)
    task_goal = {r[0]: r[1] for r in ctx.db.execute("SELECT id, goal_id FROM tasks")} if gorgs else {}
    g_org = lambda gid: queries.org_of_goal(gorgs, gid)   # noqa: E731
    wait = lambda ts_: f"waiting {ago(ts_, ctx.now)}" if ago(ts_, ctx.now) else ""   # noqa: E731
    items = []
    for t in op["tasks_blocked"]:
        items.append({"kind": "Blocked task", "tone": "danger", "subject": queries._strip_tags(t["title"]), "ref": t["id"],
                      "href": f"/task/{t['id']}", "age": wait(t["updated"]), "org": g_org(t["goal_id"]), "goal": t["goal_id"]})
    for m in op["escalations"]:
        goal, task = refs.get(m["id"], (None, None))
        ref, href = (task, f"/task/{task}") if task else (goal, f"/goal/{goal}") if goal else (None, None)
        gid = goal or task_goal.get(task)
        o = (g_org(gid) if gid else _orgs.prefix_of(m.get("recipient")) or _orgs.prefix_of(m.get("sender")) or queries.DEFAULT_ORG)
        items.append({"kind": "Escalation" if m["kind"] == "escalation" else "Request", "tone": "warning", "subject": m["subject"],
                      "ref": ref, "href": href, "message": m["id"], "age": wait(m["created"]), "org": o, "goal": gid})
    for t in op["judge_blocked_auto_merge"]:
        items.append({"kind": "Merge needs OK", "tone": "warning", "subject": queries._strip_tags(t["title"]), "ref": t["task_id"],
                      "href": f"/task/{t['task_id']}", "age": wait(t["ts"]), "org": g_org(t["goal_id"]), "goal": t["goal_id"]})
    for g in op["goals"]:
        items.append({"kind": "Blocked goal" if g["status"] == "blocked" else "Proposed goal",
                      "tone": "danger" if g["status"] == "blocked" else "neutral", "subject": g["title"], "ref": g["id"],
                      "href": f"/goal/{g['id']}", "age": wait(g["updated"]), "org": g_org(g["id"]), "goal": g["id"]})
    return [i for i in items if org is None or i["org"] == org]


def _system(ctx, ag):
    live = sum(1 for a in ag if a["status"] == "live")
    queued = sum(1 for a in ag if a["status"] == "queued")
    cap = int(_cfg(ctx, "max_live_agents", 6))
    floor = int(_cfg(ctx, "min_mem_available_mb", 1024))
    mem_fn = getattr(ctx, "mem_fn", None)
    mem = mem_fn() if mem_fn else None
    m = machine()
    load, cpus = (m.get("load") or [None])[0], m.get("cpus")
    u = ctx.usage_fn() if ctx.usage_fn else None
    fresh = bool(u) and not u.get("unreadable") and (u.get("five_hour") is not None or u.get("seven_day") is not None)
    cap5, cap7 = _cfg(ctx, "max_five_hour_pct", 92), _cfg(ctx, "max_seven_day_pct", 92)
    return {"agents": {"live": live, "queued": queued, "cap": cap},
            "memory": {"available_mb": mem, "floor_mb": floor},
            "load": {"load1": load, "cpus": cpus},
            "usage": {"five_hour": u["five_hour"] if fresh else None, "seven_day": u.get("seven_day") if fresh else None,
                      "cap_five_hour": cap5, "cap_seven_day": cap7,
                      "stale": bool(fresh and (not u.get("fresh") or (u.get("age_min") or 0) > 30))},
            "cursor": resources.cursor_usage(ctx)}


def _goals(ctx, all_goals):
    gl = [g for g in all_goals if g["status"] in OPEN_GOAL]
    last = queries.last_activity_by_goal(ctx.db)
    gl.sort(key=lambda g: (99 if g["priority"] is None else g["priority"], g["id"]))
    return [{"id": g["id"], "title": g["title"], "status": g["status"], "priority": g["priority"], "tasks_total": g["tasks_total"],
             "tasks_done": g["tasks_done"], "open_prs": g["open_prs"], "last_activity": last.get(g["id"]), "org": g["org"]} for g in gl]


def needs_by_goal(needs):
    """{goal id: number of owner waits} from _needs() items; an item with no goal counts for no goal."""
    out = {}
    for i in needs:
        if i.get("goal"):
            out[i["goal"]] = out.get(i["goal"], 0) + 1
    return out


def _task_row(t, rej):
    return {"id": t["id"], "title": queries._t(queries._strip_tags(t["title"]), TASK_TITLE_MAX), "status": t["status"],
            "assignee": t["assignee"], "pr_url": t["pr_url"], "rejections": rej.get(t["id"], 0), "attempts": t["attempts"] or 0,
            "waits": t["waits"]}


def _team_node(team, tasks, agents, rej):
    """One level-2 node. `team` is a goal_teams row or None (the goal's tasks that have no team). Level 3 holds the
    in review / blocked / in progress tasks (blocked first); `more` holds the open rest (todo). Done and cancelled
    tasks are history: they count in the goal's progress, not here."""
    by = {st: [t for t in tasks if t["status"] == st] for st in (*COUNTED, "done", "cancelled")}
    shown = [_task_row(t, rej) for st in SHOWN_TASK for t in by[st]]
    lead = team["lead_agent"] if team else None
    lead_status = next((a["status"] for a in agents if a["name"] == lead), None)
    return {"id": team["id"] if team else None, "scope": queries._t(team["scope"], TEAM_SCOPE_MAX) if team and team["scope"] else None,
            "lead": lead, "lead_status": lead_status, "status": team["status"] if team else None,
            "running": sum(1 for a in agents if a["status"] == "live"),
            "counts": {st: len(by[st]) for st in COUNTED}, "tasks": shown,
            "more": [_task_row(t, rej) for t in by["todo"]],
            "owner_wait": any(t["waits"] == "owner" for t in by["blocked"])}


def _tree(ctx, gl, needs):
    """Level 1 to 3 of the overview tree for the open goals of `gl`: goals with an owner wait first, then by priority.
    Reads queries.goal_teams (teams + agents + tasks), criteria_by_goal, last_activity_by_goal, bouncing_tasks: a
    fixed number of queries, none per goal, team or task."""
    grouped = queries.goal_teams(ctx.db)
    crit = queries.criteria_by_goal(ctx.db)
    last = queries.last_activity_by_goal(ctx.db)
    rej = {b["task_id"]: b["rejections"] for b in queries.bouncing_tasks(ctx.db, limit=10 ** 9)}
    wait = needs_by_goal(needs)
    out = []
    for g in (g for g in gl if g["status"] in OPEN_GOAL):
        grp = grouped.get(g["id"]) or {"teams": [], "loose": []}
        nodes = [_team_node(t, t["tasks"], t["agents"], rej) for t in grp["teams"]]
        loose = [t for t in grp["loose"] if t["status"] not in ("done", "cancelled")]
        if loose:
            nodes.append(_team_node(None, loose, [], rej))
        nodes.sort(key=lambda n: (not n["owner_wait"], n["id"] is None, n["id"] or ""))
        out.append({"id": g["id"], "title": g["title"], "status": g["status"], "priority": g["priority"], "org": g["org"],
                    "tasks_total": g["tasks_total"], "tasks_done": g["tasks_done"], "open_prs": g["open_prs"],
                    "criteria": list(crit[g["id"]]) if g["id"] in crit else None, "last_activity": last.get(g["id"]) or g["updated"],
                    "needs": wait.get(g["id"], 0), "teams": nodes})
    out.sort(key=lambda g: (g["needs"] == 0, 99 if g["priority"] is None else g["priority"], g["id"]))
    return out


def _agents(ctx, ag):
    last = queries.last_event_by_agent(ctx.db)
    rows = sorted(ag, key=lambda a: (a["status"] != "live", -(queries._epoch(last.get(a["name"])) or 0), a["name"]))
    shown = rows[:AGENTS_MAX]
    titles = queries.task_titles(ctx.db, {a["task_id"] for a in shown if a["task_id"]})
    live = [a for a in ag if a["status"] == "live"]
    return {"running": len(live), "queued": sum(1 for a in ag if a["status"] == "queued"), "total": len(ag),
            "leads": sum(1 for a in live if a["role"] == "lead"), "hq": sum(1 for a in live if a["role"] in HQ_ROLES),
            "rows": [{"name": a["name"], "role": a["role"], "status": a["status"], "runtime": a.get("runtime") or "claude",
                      "org": a.get("org"), "task_id": a["task_id"], "task_title": titles.get(a["task_id"]),
                      "last_event": last.get(a["name"])} for a in shown]}


def _quality(ctx, goals=None):
    q = queries.quality(ctx.db)["overall"]
    spawn = [t["total_s"] for t in queries.throughput(ctx.db, days=DAYS, now=ctx.now)["tasks"] if t["total_s"] is not None]
    return {"merged": q["merged"], "rejections_per_merged": q["rejections_per_merged"],
            "first_try_pct": round(100 * q["merged_first_try"] / q["merged"]) if q["merged"] else None,
            "median_spawn_to_merge_s": round(statistics.median(spawn)) if spawn else None,
            "bouncing": queries.bouncing_tasks(ctx.db, BOUNCING_MAX + 1, goals=goals)}


ACTIVE_GOAL = ("active", "planning")


def _hq(ag, org, role):
    """The org's boss/director among the non-ended agents: live first, then queued/starting; None when not running."""
    rows = [a for a in ag if a["org"] == org and a["role"] == role]
    rows.sort(key=lambda a: (a["status"] != "live", a["status"] != "starting", a["name"] != f"{org}-{role}", a["name"]))
    return {"name": rows[0]["name"], "status": rows[0]["status"]} if rows else None


def _parked_team_ids(con):
    if not has_column(con, "teams", "parked_at"):
        return set()
    return {r[0] for r in con.execute("SELECT id FROM teams WHERE status='active' AND parked_at IS NOT NULL")}


def _lane(org, gl, ag, teams, needs, parked_ids):
    """One org lane of the overview: boss/director state, active teams, workers on those teams, goals, queued, health."""
    live = [a for a in ag if a["org"] == org and a["status"] == "live"]
    status_by = {a["name"]: a["status"] for a in ag}
    org_active = [t for t in teams if t["org"] == org and t["status"] == "active"]
    active_ids = {t["id"] for t in org_active}
    leads_awake = leads_parked = leads_waking = 0
    for t in org_active:
        st = status_by.get(t["lead_agent"]) if t["lead_agent"] else None
        if st == "live":
            leads_awake += 1
        elif st in ("queued", "starting"):
            leads_waking += 1
        elif t["id"] in parked_ids:
            leads_parked += 1
    workers_live = sum(1 for a in ag if a["status"] == "live" and a["team_id"] in active_ids
                       and a["role"] not in ("boss", "director", "lead"))
    goals = [g for g in gl if g["org"] == org]
    open_goals = [g for g in goals if g["status"] in OPEN_GOAL]
    boss, director = _hq(ag, org, "boss"), _hq(ag, org, "director")
    queued = sum(1 for a in ag if a["org"] == org and a["status"] == "queued")
    lane = {"org": org, "label": _orgs.label(org), "boss": boss, "director": director,
            "teams_active": len(org_active), "leads_awake": leads_awake, "leads_parked": leads_parked,
            "leads_waking": leads_waking, "workers_live": workers_live,
            "agents_live": len(live), "queued": queued,
            "goals_active": sum(1 for g in goals if g["status"] in ACTIVE_GOAL), "goals_open": len(open_goals),
            "needs": sum(1 for i in needs if i["org"] == org)}
    busy = bool(open_goals or live or queued)
    up = lambda x: bool(x) and x["status"] == "live"   # noqa: E731
    if busy and not up(boss):
        lane["health"] = {"tone": "danger", "text": "Boss not running"}
    elif lane["goals_active"] and not up(director):
        lane["health"] = {"tone": "warning", "text": "Director not running"}
    elif lane["needs"]:
        lane["health"] = {"tone": "warning", "text": f"{lane['needs']} need{'s' if lane['needs'] == 1 else ''} you"}
    elif queued:
        lane["health"] = {"tone": "warning", "text": f"{queued} queued"}
    elif not busy:
        lane["health"] = {"tone": "neutral", "text": "Idle"}
    else:
        lane["health"] = {"tone": "success", "text": "All normal"}
    return lane


def _lanes(ctx, gl, ag, needs, org):
    """Org lanes: both orgs when both have goals or agents (or the one picked with ?org=); [] otherwise."""
    present = [o for o in _orgs.NAMES if any(g["org"] == o for g in gl) or any(a["org"] == o for a in ag)]
    shown = [org] if org else (present if len(present) > 1 else [])
    if not shown:
        return []
    teams = queries.teams(ctx.db)
    parked_ids = _parked_team_ids(ctx.db)
    return [_lane(o, gl, ag, teams, needs, parked_ids) for o in shown]


def data(ctx, **params):
    org = ctx_org(ctx)
    all_goals = queries.goals(ctx.db)
    all_agents = queries.agents(ctx.db, now=ctx.now)
    op = queries.owner_pending(ctx.db)
    all_needs = _needs(ctx, op=op)
    lanes = _lanes(ctx, all_goals, all_agents, all_needs, org)
    gl = [g for g in all_goals if org is None or g["org"] == org]
    ag = [a for a in all_agents if org is None or a["org"] == org]
    goal_ids = {g["id"] for g in gl} if org else None
    by_status = {}
    for g in gl:
        by_status[g["status"]] = by_status.get(g["status"], 0) + 1
    u = (ctx.usage_fn() if ctx.usage_fn else None) or {}
    per_day = queries.merges_per_day(ctx.db, DAYS, now=ctx.now, goals=goal_ids)
    needs = [i for i in all_needs if org is None or i["org"] == org]
    return {"org": org, "lanes": lanes, "utilization": queries.utilization_sample(ctx.db, now=ctx.now),
            "live_agents": sum(1 for a in ag if a["status"] == "live"),
            "queued_agents": sum(1 for a in ag if a["status"] == "queued"),
            "open_prs": sum(g["open_prs"] for g in gl),
            "goals_by_status": by_status,
            "owner_pending": sum(len(v) for v in op.values()) if org is None else len(needs),
            "usage": {"five_hour": u.get("five_hour"), "seven_day": u.get("seven_day"),
                      "fresh": u.get("fresh"), "age_min": u.get("age_min")},
            "needs_you": needs, "tree": _tree(ctx, gl, needs), "system": _system(ctx, all_agents), "goals": _goals(ctx, gl), "agents": _agents(ctx, ag),
            "shipped": {"recent": queries.recent_merges(ctx.db, MERGES_MAX, goals=goal_ids), "per_day": per_day,
                        "total": sum(d["merges"] for d in per_day)},
            "quality": _quality(ctx, goal_ids)}


# ── render ──

def _sect(sid, title, *body, count=None, more=None):
    head = el("div", el("h2", title), el("span", count, class_="sect-n") if count is not None else None,
              el("a", more[1], href=more[0], class_="sect-link") if more else None, class_="sect-head")
    return el("section", head, el("div", *body, class_="sect-box"), id=sid, class_="sect ov-sect")


_HEALTH_GLYPH = {"success": "done", "warning": "queued", "danger": "lost", "neutral": "exited"}


def _hq_cell(role, a):
    if not a:
        return el("span", el("span", f"{role}:", class_="lane-l"), " ", el("span", glyph("exited"), "Not running", class_="st fg-neutral"))
    return el("span", el("span", f"{role}:", class_="lane-l"), " ", status(a["status"]), " ", el("span", a["name"], class_="mono"))


def _lane_el(ln):
    o = ln["org"]
    h_ = ln["health"]
    head = el("div", el("h2", org_dot(o), el("a", f"{ln['label']} org", href=f"/?org={o}"), class_="lane-title"),
              el("span", glyph(_HEALTH_GLYPH[h_["tone"]]), h_["text"], class_=f"st st--pill tone-{h_['tone']} lane-health"),
              class_="lane-head")
    hq = el("div", _hq_cell("Boss", ln["boss"]), _hq_cell("Director", ln["director"]), class_="lane-hq")

    def stat(n, label, href_, title=None):
        return el("a", el("span", n, class_="lane-n"), el("span", label, class_="lane-l"), href=href_, class_="lane-stat", title=title)
    n, w = ln["teams_active"], ln["workers_live"]
    team_line = (f" teams · {ln['leads_awake']} lead awake · {ln['leads_parked']} parked · "
                 f"{ln['leads_waking']} waking · {w} workers")
    stats = el("div",
               stat(n, team_line, f"/agents?org={o}", f"{n}{team_line}"),
               stat(ln["workers_live"], "Live workers", f"/agents?org={o}&status=live", f"{ln['agents_live']} agents live in total"),
               stat(ln["goals_active"], "Active goals", f"/goals?org={o}", f"{ln['goals_open']} open goals"),
               stat(ln["queued"], "Queued", f"/agents?org={o}&status=queued"), class_="lane-stats")
    return el("section", head, hq, stats, class_=f"lane lane--{o}", aria_label=f"{ln['label']} org")


def _lanes_section(d):
    if not d.get("lanes"):
        return None
    note = (el("p", f"Showing the {_orgs.label(d['org'])} org only. System capacity is shared by both orgs; ",
               el("a", "show all", href="/"), ".", class_="org-note") if d.get("org") else None)
    return el("div", [_lane_el(ln) for ln in d["lanes"]], note, class_="lanes", id="orgs")


def _needs_section(d, now):
    """Level 0: the count, the top NEEDS_MAX items and "All N". No section at all when nothing waits on the owner."""
    items = d["needs_you"]
    if not items:
        return None
    more = len(items) - NEEDS_MAX
    return _sect("needs-you", "Needs you", el("ul", [needs_you_row(i) for i in items[:NEEDS_MAX]], class_="lrow-list"),
                 el("a", f"{more} more", href=_o(d, "/goals"), class_="more-link") if more > 0 else None,
                 count=len(items), more=(_o(d, "/goals"), f"All {len(items)}"))


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
    rows.append(resources.cursor_row(s["cursor"]))
    ut = d["utilization"]
    ut_text = ("Utilization appears after the next tick sample" if ut is None
               else ut["line"] + (" (UNDER TARGET)" if ut["under"] else ""))
    return _sect("system", "System", el("div", rows, class_="system"), el("p", ut_text, class_="summary-line"),
                 more=("/resources", "Resources"))


def _usage_row(u):
    """Same row as the Resources page (one definition: web/views/resources.py usage_row)."""
    return resources.usage_row(u)


def goal_row(g, now, *, crit=None, last=None, needs=0, extra=None, body=None, opened=False):
    """The level-1 goal row, shared by the overview tree and /goals: state glyph, id, title, priority chip, org tag,
    progress (done/total tasks), criteria n/m, open PRs, `extra`, a red "needs you N" when the goal has an owner wait,
    last activity. With `body` (the level-2 list) the row is a <details> that is open when `opened`."""
    done, total = g["tasks_done"], g["tasks_total"]
    prs = f"{g['open_prs']} open PR" + ("s" if g["open_prs"] != 1 else "") if g["open_prs"] else None
    act = ago(last, now)
    title = el("span", el("a", g["title"], href=f"/goal/{g['id']}"),
               el("span", f"P{g['priority']}", class_="chip-p") if g["priority"] is not None else None, " ", org_badge(g.get("org")),
               class_="gtitle")
    meta = el("div", charts.meter(done, total, f"{done}/{total} tasks"),
              el("span", f"criteria {crit[0]}/{crit[1]}", class_="gprs") if crit else None,
              el("span", prs, class_="gprs") if prs else None, extra,
              el("span", f"needs you {needs}", class_="gneeds fg-danger") if needs else None,
              el("span", f"last activity {act}", class_="gact") if act else None, class_="gm")
    head = [row_status(g["status"]), el("span", g["id"], class_="mono lrow-id"), title, meta]
    if body is None:
        return el("li", *head, class_="goalrow")
    return el("li", el("details", el("summary", el("div", *head, class_="gsum")), body, class_="disc gnode",
                       data_key=f"ov-goal-{g['id']}", open=opened or None), class_="goalrow goalrow--tree")


def _task_li(t):
    meta = [t["assignee"], link(t["pr_url"], pr_label(t["pr_url"])) if t["pr_url"] else None,
            _n(t["rejections"], "rejection") if t["rejections"] else None, f"{t['attempts']} {'try' if t['attempts'] == 1 else 'tries'}"
            if t["attempts"] else None]
    return el("li", row_status(t["status"]), el("a", t["id"], href=f"/task/{t['id']}", class_="mono lrow-id"),
              el("span", t["title"], class_="gtitle"),
              el("span", [el("span", m) for m in meta if m], class_="tkmeta"), class_="tkrow")


_COUNT_LABEL = {"todo": "to do", "in_progress": "in progress", "in_review": "in review", "blocked": "blocked"}


def _team_el(goal_id, t):
    """Level 2: team id, scope, lead and its status, running agents, tasks by status. A <details> only when it has
    level-3 rows; a team with none is a plain row (no empty box)."""
    lead = None
    if t["lead"]:
        lead = el("span", "lead ", el("span", t["lead"], class_="mono"), " ",
                  status(t["lead_status"]) if t["lead_status"] else el("span", glyph("exited"), "not running", class_="st fg-neutral"))
    counts = [f"{t['counts'][st]} {_COUNT_LABEL[st]}" for st in COUNTED if t["counts"][st]]
    meta = el("div", lead, el("span", f"{t['running']} running") if t["running"] else None,
              el("span", " · ".join(counts) if counts else "no open tasks"), class_="tm")
    ident = el("span", t["id"] or "no team", class_="mono lrow-id")
    head = el("div", ident, el("span", t["scope"], class_="tscope") if t["scope"] else None, meta, class_="tsum")
    if not (t["tasks"] or t["more"]):
        return el("li", head, class_="team")
    key = f"ov-team-{goal_id}-{t['id'] or 'none'}"
    body = [el("ul", [_task_li(x) for x in t["tasks"]], class_="tk") if t["tasks"] else None,
            el("details", el("summary", f"{len(t['more'])} more"), el("ul", [_task_li(x) for x in t["more"]], class_="tk"),
               class_="disc", data_key=key + "-more") if t["more"] else None]
    return el("li", el("details", el("summary", head), *body, class_="disc", data_key=key, open=t["owner_wait"] or None), class_="team")


def _goals_section(d, now):
    gl = d["tree"]
    if not gl:
        return _sect("goals", "Goals", empty_state("No open goals"), more=(_o(d, "/goals"), "All goals"))
    rows = []
    for g in gl:
        body = el("ul", [_team_el(g["id"], t) for t in g["teams"]], class_="teams") if g["teams"] else None
        rows.append(goal_row(g, now, crit=g["criteria"], last=g["last_activity"], needs=g["needs"], body=body, opened=bool(g["needs"])))
    return _sect("goals", "Goals", el("ul", rows, class_="lrow-list"), count=len(gl), more=(_o(d, "/goals"), "All goals"))


def _o(d, path):
    """`path` keeping the overview's org filter."""
    return org_href(path, {}, d.get("org"))


def _n(n, word):
    return f"{n} {word}" + ("" if n == 1 else "s")


def _agents_section(d, now):
    a = d["agents"]
    if not a["total"]:
        return _sect("agents", "Agents running", empty_state("No agents running"), more=(_o(d, "/agents"), "All agents"))
    workers = a["running"] - a["leads"] - a["hq"]
    summary = f"{a['running']} running · {_n(workers, 'worker')} · {_n(a['leads'], 'lead')} · {a['hq']} HQ · {a['queued']} queued"
    rows = []
    for r in a["rows"]:
        cells = agent_row(r, now)
        rows.append(el("li", [el("span", c, class_=k["cls"]) for k, c in zip(AGENT_COLS, cells)], class_="agentrow"))
    more = a["total"] - len(a["rows"])
    return _sect("agents", "Agents running", el("p", summary, class_="summary-line"), el("ul", rows, class_="lrow-list"),
                 el("a", f"{more} more · All {a['total']} agents", href=_o(d, "/agents"), class_="more-link") if more > 0 else None,
                 count=a["running"], more=(_o(d, "/agents"), "All agents"))


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
    return el("div", _lanes_section(d), _needs_section(d, ctx.now), _goals_section(d, ctx.now), _system_section(d), _agents_section(d, ctx.now),
              _shipped_section(d, ctx.now), _quality_section(d), class_="ov")
