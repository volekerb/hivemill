"""Quality page: four headline tiles, bouncing tasks, per-team table, rejections by source, rejections per week.

Formulas are the ones in the queries.quality docstring. Rejections per week use the same rule
(fail/reject verdict rows, plus judge-reject events that have no judge verdict row).
"""
import datetime as dt

from web import charts, queries
from web.components import data_table, disclosure, empty_state, href, list_row, section, tile
from web.html import el

TITLE = "Quality"
WEEKS = 12
DELTA_DAYS = 14
BOUNCING_MAX = 6
SORTS = {"team": "asc", "merged": "desc", "rejections": "desc", "rate": "desc"}   # sortable column -> first-click direction
GETTERS = {"team": lambda t: t["team_id"], "merged": lambda t: t["merged"], "rejections": lambda t: t["rejections"],
           "rate": lambda t: t["rejections_per_merged"]}
FORMULA = ("Rejections per merged PR = rejection events / merged PRs; retries = attempts beyond the first; "
           "CI pass rate = merged / (merged + tasks the judge blocked for red checks).")
NOTES = ("Rejections count failed QA/review verdicts, plus old judge rejections that left no verdict row. "
         "Merged counts tasks with a merge event. Tasks without a team are counted in every team of their goal. "
         "Rework time is 0 when a task was never sent back.")


def _pct(x):
    return "-" if x is None else f"{round(100 * x)}%"


def _rpm(x):
    return "-" if x is None else f"{x:.2f}"


def _week(iso):
    d = queries.parse_ts(iso)
    if d is None:
        return None
    day = d.astimezone().date()
    return (day - dt.timedelta(days=day.weekday())).isoformat()


def _weekly(con):
    counts = {}
    vcount = {}
    for r in con.execute("SELECT task_id, role, created FROM verdicts WHERE verdict IN ('fail','reject')"):
        w = _week(r["created"])
        if w:
            counts[w] = counts.get(w, 0) + 1
        if r["role"] == "judge":
            vcount[r["task_id"]] = vcount.get(r["task_id"], 0) + 1
    evs = {}
    for r in con.execute("SELECT entity_id, ts FROM events WHERE entity='task' AND action='judge-reject' ORDER BY id"):
        evs.setdefault(r["entity_id"], []).append(r["ts"])
    for tid, tss in evs.items():
        extra = max(0, len(tss) - vcount.get(tid, 0))
        for t in tss[len(tss) - extra:]:
            w = _week(t)
            if w:
                counts[w] = counts.get(w, 0) + 1
    return [{"week": w, "rejections": counts[w]} for w in sorted(counts)][-WEEKS:]


ADDITIVE = ("tasks", "merged", "rejections", "retries", "merged_first_try", "merged_after_rejection",
            "rej_reviewer", "rej_qa", "rej_judge", "gate_judge_blocked", "gate_merge_failed", "gate_refusals")


def _with_unassigned(con, q):
    """Add the team_id-NULL tasks of a team's goal to that team, the way bin/factory scorecard_rows attributes them.

    The per-team ratios are recomputed after the addition, so they never go stale.
    """
    extra = q["unassigned"]
    goal = {r["id"]: r["goal_id"] for r in con.execute("SELECT id, goal_id FROM teams")}
    out = []
    for t in q["teams"]:
        e = extra.get(goal.get(t["team_id"]))
        if e:
            t = dict(t)
            for k in ADDITIVE:
                t[k] += e[k]
            t["rejections_per_merged"] = round(t["rejections"] / t["merged"], 2) if t["merged"] else None
        out.append(t)
    return out


def data(ctx, **params):
    q = queries.quality(ctx.db)
    last = {}
    for r in ctx.db.execute("SELECT team_id, score, created FROM evaluations ORDER BY id"):
        last[r["team_id"]] = {"score": r["score"], "date": (r["created"] or "")[:10]}
    teams = [dict(t, eval_date=(last.get(t["team_id"]) or {}).get("date")) for t in _with_unassigned(ctx.db, q)]
    return {"overall": q["overall"], "teams": teams, "weeks": _weekly(ctx.db), "formula": FORMULA,
            "windows": queries.quality_windows(ctx.db, DELTA_DAYS, now=ctx.now),
            "bouncing": queries.bouncing_tasks(ctx.db, BOUNCING_MAX)}


def _delta(cur, prev, fmt, lower_is_better, days):
    """(text, tone) for "↓ 0.25 vs prev 14 d", or None when either period has nothing to compare."""
    if cur is None or prev is None:
        return None
    diff = cur - prev
    if not diff:
        return (f"no change vs prev {days} d", "neutral")
    better = (diff < 0) == lower_is_better
    return (f"{'↑' if diff > 0 else '↓'} {fmt(abs(diff))} vs prev {days} d", "success" if better else "warning")


def _num(x):
    return f"{x:g}"


def _headline(d):
    o, w = d["overall"], d["windows"]
    cur, prev, days = w["current"], w["previous"], w["days"]
    since = f" The change compares the last {days} days with the {days} days before."
    first = round(100 * o["merged_first_try"] / o["merged"]) if o["merged"] else None
    return el("div", tile("Rejections per merged PR", _rpm(o["rejections_per_merged"]),
                          delta=_delta(cur["rejections_per_merged"], prev["rejections_per_merged"], lambda x: f"{x:.2f}", True, days),
                          definition="Rejections divided by merged PRs, over all time. Lower is better." + since),
              tile("First-try merge rate", "-" if first is None else f"{first}%",
                   delta=_delta(cur["first_try_pct"], prev["first_try_pct"], lambda x: f"{x:g} pts", False, days),
                   definition="Merged tasks that were never rejected, as a share of merged PRs, over all time." + since),
              tile("CI-blocked merges", o["ci_blocked"],
                   delta=_delta(cur["ci_blocked"], prev["ci_blocked"], _num, True, days),
                   definition="Times the judge refused a merge because checks were red or still pending, over all time." + since),
              tile("Median attempts", _median_attempts(d),
                   delta=_delta(cur["median_attempts"], prev["median_attempts"], _num, True, days),
                   definition="Median number of attempts of the tasks that merged, over all time. 1 means most merged first time." + since),
              class_="tiles tiles--4")


def _median_attempts(d):
    m = d["windows"]["all"]["median_attempts"]
    return "-" if m is None else _num(m)


def _more_metrics(d):
    o = d["overall"]
    scored = [t for t in d["teams"] if t["score"] is not None]
    scores = ", ".join(f"{t['team_id']} {t['score']}/5" for t in scored) or "-"
    retries = (o["retries"] / o["merged"]) if o["merged"] else None
    # every definition string below is shown word for word inside the tile's (i) details (acceptance 3 of T-0478)
    tiles = [
        tile("Merged PRs", o["merged"], definition="Distinct tasks with a merge event."),
        tile("Merged first try", o["merged_first_try"], definition="Merged tasks that were never rejected."),
        tile("Merged after >= 1 rejection", o["merged_after_rejection"],
             definition="Merged tasks rejected at least once before they merged."),
        tile("Rejections", o["rejections"],
             definition="Reviewer, QA and judge fail or reject verdicts, plus judge rejections without a verdict row."),
        tile("Rejections per merged PR", _rpm(o["rejections_per_merged"]), definition="Rejections divided by merged PRs."),
        tile("Gate refusals", o["gate_refusals"],
             definition="Judge-blocked and merge-failed events: stale verdict, red or pending CI, conflicts."),
        tile("Rejections by cause", "Not measured yet", definition="No cause is stored yet (counts incidents, not events)."),
        tile("Retries per merged task", "-" if retries is None else f"{retries:.2f}",
             definition="Attempts beyond the first, per merged task."),
        tile("CI pass rate", _pct(o["ci_pass_rate"]), definition="Merged / (merged + tasks the judge blocked for red checks)."),
        tile("Latest team scores", scores, definition="Newest evaluation per team."),
    ]
    return disclosure("More metrics", el("div", tiles, class_="tiles"), el("p", d["formula"], class_="muted"),
                      el("p", NOTES, class_="muted"), key="more-metrics", count=len(tiles))


def _bouncing(d):
    rows = [list_row(b["status"], b["task_id"], b["title"], url=f"/task/{b['task_id']}",
                     meta=(f"{b['rejections']} rejection" + ("s" if b["rejections"] != 1 else ""), f"attempt {b['attempts']}"))
            for b in d["bouncing"]]
    body = el("ul", rows, class_="lrow-list") if rows else empty_state("Nothing is bouncing")
    return section("Bouncing now", body, count=len(rows) or None)


def _teams(ctx, d):
    key, direction = queries.table_sort(getattr(ctx, "query", None), SORTS, ("team", "asc"))
    if not d["teams"]:
        return section("Per team", empty_state("No teams yet."))
    rows = queries.sort_rows(d["teams"], key, direction, GETTERS)

    def sort_href(col):
        flip = "asc" if key == col and direction == "desc" else "desc" if key == col else SORTS[col]
        return href("/quality", {"sort": col, "dir": flip})
    cols = [{"key": "team", "label": "Team", "cls": "c-title", "sortable": True},
            {"key": "merged", "label": "Merged", "cls": "num", "sortable": True},
            {"key": "rejections", "label": "Rejections", "cls": "num", "sortable": True},
            {"key": "rate", "label": "Rejections per merged PR", "cls": "num", "sortable": True}]
    table = [[t["team_id"], t["merged"], t["rejections"], _rpm(t["rejections_per_merged"])] for t in rows]
    return section("Per team", data_table(cols, [{"key": "all", "label": None, "count": len(table), "rows": table}],
                                          sort=(key, direction), sort_href=sort_href, key="teams"))


def _source_row(name, value, total):
    return el("div", el("span", name, class_="ins-name"), charts.meter(value, total, str(value), noun="rejections"), class_="ins-row")


def _sources(d):
    o = d["overall"]
    parts = [("Reviewer", o["rej_reviewer"]), ("QA", o["rej_qa"]), ("Judge", o["rej_judge"])]
    total = sum(v for _, v in parts)
    gate = [("CI", o["ci_blocked"]), ("Gate: judge-blocked", o["gate_judge_blocked"]), ("Gate: merge-failed", o["gate_merge_failed"])]
    gtotal = o["gate_refusals"]
    return section("Rejections by source",
                   el("div", [_source_row(n, v, total) for n, v in parts], class_="ins-list"),
                   el("p", "CI counts merges the judge refused for red or pending checks (part of judge-blocked); they are not rejections.",
                      class_="muted"),
                   el("div", [_source_row(n, v, gtotal) for n, v in gate], class_="ins-list"))


def _week_points(ctx, d):
    """The last WEEKS Mondays up to this week, oldest first, with zero for a week without rejections."""
    counts = {w["week"]: w["rejections"] for w in d["weeks"]}
    today = ctx.now.astimezone().date()
    this = today - dt.timedelta(days=today.weekday())
    return [((w := (this - dt.timedelta(weeks=i)).isoformat()), counts.get(w, 0)) for i in range(WEEKS - 1, -1, -1)]


def _weeks(ctx, d):
    if not d["weeks"]:
        return section("Per week", empty_state("No rejections recorded."))
    return section("Per week", charts.columns(_week_points(ctx, d), "rejections", unit="week"))


def render(ctx, d):
    return el("div", _headline(d), _bouncing(d), _teams(ctx, d), _sources(d), _weeks(ctx, d), _more_metrics(d))
