"""Throughput page: merges per day, spawn->merge times, where the time goes, per goal, slowest tasks.

Numbers come from queries.throughput (stage definitions are in its docstring); a stage that could not be measured
is None there and is counted as "unknown" here, never guessed.
"""
import math
import statistics

from web import queries
from web.html import bar, card, el, empty, kpi, link, table, dur

TITLE = "Throughput"

DAYS = (7, 14, 30)
DEFAULT_DAYS = 14
SLOWEST = 10
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
    return {"days": days, "merges": len(items), "median_s": _median(totals), "p90_s": _p90(totals),
            "timed": len(totals), "open_tasks": open_tasks,
            "live_workers": live_workers, "merges_per_day": t["merges_per_day"],
            "stages": stages, "goals": goals, "slowest": slowest}


def _range_links(days):
    return el("p", [el("span", link(f"/throughput?days={n}", f"{n} days") if n != days else el("strong", f"{n} days"), " ")
                    for n in DAYS], class_="muted")


def _per_day(d):
    rows = d["merges_per_day"]
    top = max((r["merges"] for r in rows), default=0)
    if not top:
        return card("Merges per day", empty("No merges in this period."))
    return card("Merges per day", table(["Day", "Merges"], [
        (r["day"], bar(100 * r["merges"] / top, r["merges"], f"{r['merges']} of max {top}"))
        for r in rows], cls="table table--compact"))


def _stage_card(d):
    if not d["merges"]:
        return card("Where the time goes", empty("No merged tasks in this period."))
    total = sum(s["sum_s"] for s in d["stages"])
    rows = []
    for s in d["stages"]:
        unk = f"{s['unknown']} unknown" if s["unknown"] else "all known"
        share = round(100 * s["sum_s"] / total) if total else 0
        rows.append((s["label"], el("div", bar(share, f"{share}% of total", f"{share}%"), dur(s["sum_s"])),
                     (dur(s["median_s"]), "num"), el("span", unk, class_="muted")))
    return card("Where the time goes", table(["Stage", "Total", "Median", "Known"], rows, cls="table table--compact"),
                el("p", "Stages that could not be measured for a task are left out and counted as unknown.", class_="muted"))


def render(ctx, d):
    head = el("div",
              kpi(f"Merges ({d['days']} days)", d["merges"]), kpi("Median spawn to merge", dur(d["median_s"])),
              kpi("p90 spawn to merge", dur(d["p90_s"])), kpi("Open tasks now", d["open_tasks"]),
              kpi("Live workers now", d["live_workers"]), class_="grid")
    defs = el("p", "Open tasks now: in progress or in review. Live workers now: live agents working on a task.",
              class_="muted")
    if d["goals"]:
        goals = card("Per goal", table(["Goal", "Tasks done", "Median per task"], [
            (el("span", link(f"/goal/{g['goal_id']}", g["goal_id"]), " ", g["title"] or "") if g["goal_id"] else "-",
             (g["done"], "num"), (dur(g["median_s"]), "num")) for g in d["goals"]], cls="table table--compact"))
    else:
        goals = card("Per goal", empty("No merged tasks in this period."))
    if d["slowest"]:
        slow = card(f"Slowest {SLOWEST} tasks", table(["Task", "Spawn to merge", "Merged"], [
            (el("span", link(f"/task/{s['task_id']}", s["task_id"]), " ", s["title"]), (dur(s["total_s"]), "num"),
             queries.parse_ts(s["merged"]) and s["merged"][:10]) for s in d["slowest"]], cls="table table--compact"))
    else:
        slow = card(f"Slowest {SLOWEST} tasks", empty("No timed tasks in this period."))
    return el("div", _range_links(d["days"]), head, defs, _per_day(d), _stage_card(d), goals, slow)
