"""Throughput page: merges per day, spawn->merge times, where the time goes, per goal, slowest tasks.

Numbers come from queries.throughput (stage definitions are in its docstring); a stage that could not be measured
is None there and is counted as "unknown" here, never guessed.
"""
import math
import statistics

from web import charts, queries
from web.components import data_table, empty_state, href, section, segmented, tile
from web.html import dur, el, link

TITLE = "Throughput"

DAYS = (7, 14, 30)
DEFAULT_DAYS = 14
SLOWEST = 10
HQ_ROLES = ("boss", "director", "staff-engineer")
SORTS = {"total": "desc", "task": "asc", "merged": "desc"}   # sortable column of the slowest table -> first-click direction
GETTERS = {"total": lambda s: s["total_s"], "task": lambda s: s["task_id"], "merged": lambda s: s["merged"]}
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


def data(ctx, **params):
    days = _days(ctx)
    t = queries.throughput(ctx.db, days=days, now=ctx.now)
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
    goals = [{"goal_id": g, "title": gtitles.get(g), "done": len(v),
              "median_s": _median([i["total_s"] for i in v if i["total_s"] is not None])}
             for g, v in sorted(per_goal.items(), key=lambda kv: (kv[0] is None, kv[0] or ""))]
    slow = sorted((i for i in items if i["total_s"] is not None), key=lambda i: -i["total_s"])[:SLOWEST]
    slowest = [{"task_id": i["task_id"], "title": queries._strip_tags(titles.get(i["task_id"])),
                "goal_id": i["goal_id"], "total_s": i["total_s"], "merged": i["merged"]} for i in slow]
    open_tasks = ctx.db.execute("SELECT COUNT(*) FROM tasks WHERE status IN ('in_progress','in_review')").fetchone()[0]
    live_workers = ctx.db.execute("SELECT COUNT(*) FROM agents WHERE status='live' AND task_id IS NOT NULL").fetchone()[0]
    live = [r["role"] for r in ctx.db.execute("SELECT role FROM agents WHERE status='live'")]
    agents = {"running": len(live), "leads": sum(1 for r in live if r == "lead"),
              "hq": sum(1 for r in live if r in HQ_ROLES)}
    agents["workers"] = agents["running"] - agents["leads"] - agents["hq"]
    return {"days": days, "merges": len(items), "agents": agents, "median_s": _median(totals), "p90_s": _p90(totals),
            "timed": len(totals), "open_tasks": open_tasks,
            "live_workers": live_workers, "merges_per_day": t["merges_per_day"],
            "stages": stages, "goals": goals, "slowest": slowest}


def _range(d):
    return segmented("Range", [(f"{n} days", href("/throughput", {"days": n}), n == d["days"]) for n in DAYS])


def _tiles(d):
    a = d["agents"]
    return el("div",
              tile("Merges", d["merges"], unit=f"in {d['days']} days",
                   definition="Tasks merged in the selected period. A task counts once, on the day of its last merge."),
              tile("Median spawn to merge", dur(d["median_s"]),
                   definition="Median time from a task's first agent spawn to its merge. Tasks without a recorded spawn are left out."),
              tile("p90 spawn to merge", dur(d["p90_s"]),
                   definition="90th percentile (nearest rank) of the same times: nine in ten tasks merged faster."),
              tile("Agents running now", a["running"],
                   delta=f"{a['workers']} workers · {a['leads']} leads · {a['hq']} HQ",
                   definition="Agents with a live pane right now: workers, team leads and HQ (boss, director, staff engineer)."),
              class_="tiles tiles--4")


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
        return href("/throughput", {"days": d["days"], "sort": col, "dir": flip})
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
    return el("div", _range(d), _tiles(d), open_note, _per_day(d), _stage_card(d), _goals(d), _slowest(ctx, d))
