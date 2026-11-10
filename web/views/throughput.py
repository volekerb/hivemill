"""Throughput page: merges per day, spawn->merge times, where the time goes, per goal, slowest tasks.

Numbers come from queries.throughput (stage definitions are in its docstring); a stage that could not be measured
is None there and is counted as "unknown" here, never guessed.
"""
import datetime as dt
import math
import statistics

from web import charts, queries, tokens as wtokens
from web.prices import PRICES_AS_OF
from web.components import ctx_org, data_table, empty_state, org_href, section, segmented, tile
from web.html import dur, el, link
from web.views import tokens as tokens_view

TITLE = "Throughput"

DAYS = (7, 14, 30)
DEFAULT_DAYS = 14
SLOWEST = 10
HQ_ROLES = ("boss", "director", "staff-engineer")
SORTS = {"total": "desc", "task": "asc", "merged": "desc"}   # sortable column of the slowest table -> first-click direction
GETTERS = {"total": lambda s: s["total_s"], "task": lambda s: s["task_id"], "merged": lambda s: s["merged"]}
LIMITS = "Counted from factory records only. It cannot see a push the owner makes by hand, or an owner wait set on a task that was already blocked."
TOKEN_KEYS = ("input", "output", "cache_read", "cache_write_5m", "cache_write_1h")
STAGES = (("coding_s", "Coding"), ("review_s", "Review / QA wait"), ("judge_s", "Judge"), ("rework_s", "Rework"))


def _days(ctx):
    try:
        n = int((getattr(ctx, "query", None) or {}).get("days", ""))
    except (TypeError, ValueError):
        return DEFAULT_DAYS
    return n if n in DAYS else DEFAULT_DAYS


def _median(xs):
    return round(statistics.median(xs)) if xs else None


def _p90(xs):
    """Nearest-rank 90th percentile."""
    if not xs:
        return None
    s = sorted(xs)
    return s[max(0, math.ceil(0.9 * len(s)) - 1)]


def _goal_filter(ctx):
    org = ctx_org(ctx)
    if not org:
        return None, org
    gorgs = queries.goal_orgs(ctx.db)
    goals = {r[0] for r in ctx.db.execute("SELECT id FROM goals") if queries.org_of_goal(gorgs, r[0]) == org}
    return goals, org


def _cost(ctx, items, org, days):
    """Cost per merged PR: the by_task cost of the Claude-org merged tasks of the period (each task's whole cost, not cut
    to the period) over those with a known cost. Tasks with no attributed tokens or a None cost are counted as left out,
    never as $0. Cursor-org tasks add nothing; their agent-hours are shown instead. Never waits for the token index."""
    gorgs = queries.goal_orgs(ctx.db)
    cursor_ids = [i["task_id"] for i in items if queries.org_of_goal(gorgs, i["goal_id"]) == "cursor"]
    claude_ids = [i["task_id"] for i in items if queries.org_of_goal(gorgs, i["goal_id"]) != "cursor"]
    out = {"state": "ready", "figure": None, "priced": 0, "left_out": 0, "merged": len(claude_ids),
           "cursor_tasks": len(cursor_ids), "cursor_hours": None, "prices_as_of": PRICES_AS_OF,
           "cursor_ids": sorted(cursor_ids), "left_out_ids": [], "cost_median": None, "cost_p90": None,
           "tokens_median": None, "tokens_p90": None}
    if org in (None, "cursor"):
        since = (queries._now(ctx.now).astimezone().date() - dt.timedelta(days=days - 1)).isoformat()
        ag = queries.agents(ctx.db, include_ended=True, now=ctx.now)
        out["cursor_hours"] = round(sum(r["hours"] for r in tokens_view.cursor_activity(ag, since, ctx.now, org)), 2)
    if org == "cursor":
        out["state"] = "cursor"
        return out
    snap = ctx.tokens() if getattr(ctx, "tokens", None) else None
    if not snap or not snap.get("ready"):
        out["state"] = "not_ready"
        return out
    if not claude_ids:
        return out
    by_task = wtokens.rollup(snap, wtokens.attribute(snap, ctx.db))["by_task"]
    priced = [t for t in claude_ids if t in by_task and by_task[t]["cost"] is not None
              and not by_task[t]["cost_unknown"]]     # a part-priced task has a partial cost: left out too
    costs = [by_task[t]["cost"] for t in priced]
    out["priced"], out["left_out"] = len(costs), len(claude_ids) - len(costs)
    out["left_out_ids"] = sorted(set(claude_ids) - set(priced))
    if costs:
        out["figure"] = round(sum(costs) / len(costs), 4)
        toks = [sum(by_task[t][k] for k in TOKEN_KEYS) for t in priced]    # tokens of the same priced set
        out["cost_median"], out["cost_p90"] = round(statistics.median(costs), 4), round(_p90(costs), 4)
        out["tokens_median"], out["tokens_p90"] = round(statistics.median(toks)), _p90(toks)
    return out


def data(ctx, **params):
    days = _days(ctx)
    goals, org = _goal_filter(ctx)
    t = queries.throughput(ctx.db, days=days, now=ctx.now, goals=goals)
    items = t["tasks"]
    totals = [i["total_s"] for i in items if i["total_s"] is not None]
    stages = []
    for key, label in STAGES:
        vals = [i[key] for i in items if i[key] is not None]
        stages.append({"key": key, "label": label, "sum_s": sum(vals), "median_s": _median(vals),
                       "known": len(vals), "unknown": len(items) - len(vals)})
    titles = {r["id"]: r["title"] for r in ctx.db.execute("SELECT id, title FROM tasks")}
    gtitles = {r["id"]: r["title"] for r in ctx.db.execute("SELECT id, title FROM goals")}
    per_goal = {}
    for i in items:
        per_goal.setdefault(i["goal_id"], []).append(i)
    goals_rows = [{"goal_id": g, "title": gtitles.get(g), "done": len(v),
              "median_s": _median([i["total_s"] for i in v if i["total_s"] is not None])}
             for g, v in sorted(per_goal.items(), key=lambda kv: (kv[0] is None, kv[0] or ""))]
    slow = sorted((i for i in items if i["total_s"] is not None), key=lambda i: -i["total_s"])[:SLOWEST]
    slowest = [{"task_id": i["task_id"], "title": queries._strip_tags(titles.get(i["task_id"])),
                "goal_id": i["goal_id"], "total_s": i["total_s"], "merged": i["merged"]} for i in slow]
    if goals is not None:
        if goals:
            ph = ",".join("?" * len(goals))
            open_tasks = ctx.db.execute(
                f"SELECT COUNT(*) FROM tasks WHERE status IN ('in_progress','in_review') AND goal_id IN ({ph})",
                tuple(goals)).fetchone()[0]
            live_workers = ctx.db.execute(
                f"SELECT COUNT(*) FROM agents a JOIN tasks t ON t.id=a.task_id WHERE a.status='live' "
                f"AND a.task_id IS NOT NULL AND t.goal_id IN ({ph})", tuple(goals)).fetchone()[0]
        else:
            open_tasks = live_workers = 0
    else:
        open_tasks = ctx.db.execute("SELECT COUNT(*) FROM tasks WHERE status IN ('in_progress','in_review')").fetchone()[0]
        live_workers = ctx.db.execute("SELECT COUNT(*) FROM agents WHERE status='live' AND task_id IS NOT NULL").fetchone()[0]
    live_agents = [a for a in queries.agents(ctx.db, now=ctx.now) if a["status"] == "live"]
    if org:
        live_agents = [a for a in live_agents if a["org"] == org]
    live = [a["role"] for a in live_agents]
    agents = {"running": len(live), "leads": sum(1 for r in live if r == "lead"),
              "hq": sum(1 for r in live if r in HQ_ROLES)}
    agents["workers"] = agents["running"] - agents["leads"] - agents["hq"]
    attended = queries.attended_tasks(ctx.db, [i["task_id"] for i in items])
    return {"org": org, "days": days, "merges": len(items), "unattended": len(items) - len(attended), "cost": _cost(ctx, items, org, days), "agents": agents, "median_s": _median(totals), "p90_s": _p90(totals),
            "timed": len(totals), "open_tasks": open_tasks,
            "live_workers": live_workers, "merges_per_day": t["merges_per_day"],
            "stages": stages, "goals": goals_rows, "slowest": slowest}


def _range(d):
    return segmented("Range", [(f"{n} days", org_href("/throughput", {"days": n}, d.get("org")), n == d["days"]) for n in DAYS])


def _tiles(d):
    """One grid of six tiles (3 x 2 on desktop, 2 x 3 on a phone): no orphan tile. The note on the limits follows it."""
    a = d["agents"]
    unattended = f"{d['unattended']} of {d['merges']}" if d["merges"] else "-"
    grid = el("div",
              tile("Merges", d["merges"], unit=f"in {d['days']} days", unit_nl=True,
                   definition="Tasks merged in the selected period. A task counts once, on the day of its last merge."),
              tile("Median spawn to merge", dur(d["median_s"]),
                   definition="Median time from a task's first agent spawn to its merge. Tasks without a recorded spawn are left out."),
              tile("p90 spawn to merge", dur(d["p90_s"]),
                   definition="90th percentile (nearest rank) of the same times: nine in ten tasks merged faster."),
              tile("Agents running now", a["running"],
                   delta=f"{a['workers']} workers · {a['leads']} leads · {a['hq']} HQ",
                   definition="Agents with a live pane right now: workers, team leads and HQ (boss, director, staff engineer)."),
              tile("Unattended share", unattended,
                   definition="Merged tasks in the selected period with no owner involvement: no owner wait, "
                              "no auto-merge block, no boss decision about the task."),
              _cost_tile(d),
              class_="tiles tiles--6")
    return el("div", grid, el("p", LIMITS, class_="muted"))


def _cost_tile(d):
    """The Cost per merged PR tile. The delta line is short (the figure's basis); the rest of the facts (price date, tasks
    left out, Cursor hours) sit in the (i) definition, so none is lost."""
    c = d["cost"]
    definition = (f"Average estimated API-equivalent cost of a merged PR at list prices as of {c['prices_as_of']}: the whole cost of each "
                  "merged Claude-org task in the period (not cut to the period), divided by the tasks with a known cost. "
                  "Tasks with no attributed tokens or no price are left out and counted. Cursor reports no tokens, so it has no cost.")
    if c["state"] == "not_ready":
        value, delta = "not ready", "The token index is still warming up."
    elif c["state"] == "cursor":
        value, delta = "-", f"Cursor: {c['cursor_hours']} agent-hours in {d['days']} days, no cost or tokens reported."
    else:
        value = f"${c['figure']:.2f}" if c["figure"] is not None else "-"
        delta = f"{c['priced']} of {c['merged']} tasks priced"
        more = [f"The figure is a list-price estimate (as of {c['prices_as_of']})."]
        if c["left_out"]:
            more.append(f"{c['left_out']} left out: no tokens or no price.")
        if c["cursor_hours"] is not None:
            more.append(f"Cursor: {c['cursor_hours']} agent-hours, no cost.")
        definition += " " + " ".join(more)
    return tile("Cost per merged PR", value, delta=delta, definition=definition)


def _ids(ids, cap=8):
    return ", ".join(ids[:cap]) + (f" and {len(ids) - cap} more" if len(ids) > cap else "")


def _money(x):
    return f"${x:,.2f}" if x is not None else "-"


def _count(x):
    return f"{x:,}" if x is not None else "-"


def _spread(d):
    """Median and p90 of cost and of tokens per merged PR, over the same priced set as the average cost tile. A figure
    that cannot be computed shows "-", never zero; tasks left out and Cursor tasks are named."""
    c = d["cost"]
    title = "Cost and tokens per merged PR"
    if c["state"] == "cursor":
        return section(title, empty_state(f"Not available for Cursor: {c['cursor_hours']} agent-hours, no cost or tokens reported."))
    if c["state"] == "not_ready":
        return section(title, empty_state("Not ready: the token index is still warming up."))
    grid = el("div",
              tile("Median cost per merged PR", _money(c["cost_median"]),
                   definition="Median estimated cost of a merged Claude-org PR, over the same priced tasks as the average."),
              tile("p90 cost per merged PR", _money(c["cost_p90"]),
                   definition="90th percentile (nearest rank) of the same costs."),
              tile("Median tokens per merged PR", _count(c["tokens_median"]),
                   definition="Median of input, output and cache tokens of the same priced tasks."),
              tile("p90 tokens per merged PR", _count(c["tokens_p90"]),
                   definition="90th percentile (nearest rank) of the same token counts."),
              class_="tiles")
    notes = [f"{c['priced']} of {c['merged']} Claude tasks priced; estimated at list prices as of {c['prices_as_of']} "
             "(API-equivalent, not a bill)."]
    if c["left_out_ids"]:
        notes.append(f"Left out (no tokens or no price): {_ids(c['left_out_ids'])}.")
    if c["cursor_ids"]:
        notes.append(f"Cursor tasks, no cost or tokens reported: {_ids(c['cursor_ids'])}.")
    return section(title, grid, el("p", " ".join(notes), class_="muted"))


def _per_day(d):
    points = [(r["day"], r["merges"]) for r in d["merges_per_day"]]
    return section("Merges per day", charts.columns(points, "merges", height=96))


def _stage_card(d):
    if not d["merges"]:
        return section("Where the time goes", empty_state("No merged tasks in this period."))
    total = sum(s["sum_s"] for s in d["stages"])
    rows = []
    for s in d["stages"]:
        unk = f"{s['unknown']} unknown" if s["unknown"] else "all known"
        share = round(100 * s["sum_s"] / total) if total else 0
        rows.append(el("div", el("span", s["label"], class_="ins-name"),
                       charts.meter(s["sum_s"], total, f"{dur(s['sum_s'])} · {share}% of total", noun="seconds"),
                       el("span", f"median {dur(s['median_s'])} · {unk}", class_="muted"), class_="ins-row"))
    return section("Where the time goes", el("div", rows, class_="ins-list"),
                   el("p", "Stages that could not be measured for a task are left out and counted as unknown.", class_="muted"))


def _goals(d):
    if not d["goals"]:
        return section("Per goal", empty_state("No merged tasks in this period."))
    cols = [{"key": "goal", "label": "Goal", "cls": "c-title"}, {"key": "done", "label": "Tasks done", "cls": "num"},
            {"key": "median", "label": "Median per task", "cls": "num"}]
    rows = [[el("span", link(f"/goal/{g['goal_id']}", g["goal_id"]), " ", g["title"] or "") if g["goal_id"] else "-",
             g["done"], dur(g["median_s"])] for g in d["goals"]]
    return section("Per goal", data_table(cols, [{"key": "all", "label": None, "count": len(rows), "rows": rows}], key="goals"))


def _slowest(ctx, d):
    title = f"Slowest {SLOWEST} tasks"
    if not d["slowest"]:
        return section(title, empty_state("No timed tasks in this period."))
    key, direction = queries.table_sort(getattr(ctx, "query", None), SORTS, ("total", "desc"))
    items = queries.sort_rows(d["slowest"], key, direction, GETTERS)

    def sort_href(col):
        flip = "asc" if key == col and direction == "desc" else "desc" if key == col else SORTS[col]
        return org_href("/throughput", {"days": d["days"], "sort": col, "dir": flip}, d.get("org"))
    cols = [{"key": "task", "label": "Task", "cls": "c-title", "sortable": True},
            {"key": "total", "label": "Spawn to merge", "cls": "num", "sortable": True},
            {"key": "merged", "label": "Merged", "cls": "num", "sortable": True}]
    rows = [[el("span", link(f"/task/{s['task_id']}", s["task_id"]), " ", s["title"]), dur(s["total_s"]),
             queries.parse_ts(s["merged"]) and s["merged"][:10]] for s in items]
    return section(title, data_table(cols, [{"key": "all", "label": None, "count": len(rows), "rows": rows}],
                                     sort=(key, direction), sort_href=sort_href, key="slowest"))


def render(ctx, d):
    open_note = el("p", f"{d['open_tasks']} tasks open now (in progress or in review), {d['live_workers']} agents working on a task.",
                   class_="muted")
    return el("div", _range(d), _tiles(d), _spread(d), open_note, _per_day(d), _stage_card(d), _goals(d), _slowest(ctx, d))
